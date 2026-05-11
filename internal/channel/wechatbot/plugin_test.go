package wechatbot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/chef-guo/agents-hive/internal/channel"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/store"
)

type fakeBackend struct {
	loginErr error
	sendErr  error
	runErr   error
	panicRun bool
	sentTo   string
	sentText string
	handler  func(*SDKMessage)
	runCh    chan struct{}
	runCount int32
}

func (b *fakeBackend) Login(context.Context, bool) (*Credentials, error) {
	if b.loginErr != nil {
		return nil, b.loginErr
	}
	return &Credentials{AccountID: "wx-owner", UserID: "wx-sdk-user"}, nil
}

func (b *fakeBackend) OnMessage(handler func(*SDKMessage)) {
	b.handler = handler
}

func (b *fakeBackend) Run(ctx context.Context) error {
	atomic.AddInt32(&b.runCount, 1)
	if b.runCh != nil {
		close(b.runCh)
	}
	if b.panicRun {
		b.panicRun = false
		panic("sdk panic")
	}
	if b.runErr != nil {
		err := b.runErr
		b.runErr = nil
		return err
	}
	<-ctx.Done()
	return nil
}

func (b *fakeBackend) Stop() {}

func (b *fakeBackend) Send(_ context.Context, userID, text string) error {
	b.sentTo = userID
	b.sentText = text
	return b.sendErr
}

func TestPluginSendRequiresOwnerScope(t *testing.T) {
	reg := NewRegistry(Config{Enabled: true, CredRoot: t.TempDir()}, nil, store.NewMemoryStore(), zap.NewNop())
	plugin := NewPlugin(reg, zap.NewNop())

	if err := plugin.Send(context.Background(), channel.OutboundMessage{
		TenantKey: "owner-1",
		ChatID:    "wx-peer",
		Content:   "hello",
	}); err == nil {
		t.Fatal("expected owner_user_id error")
	}

	if err := plugin.Send(context.Background(), channel.OutboundMessage{
		OwnerUserID: "owner-1",
		TenantKey:   "owner-2",
		ChatID:      "wx-peer",
		Content:     "hello",
	}); err == nil {
		t.Fatal("expected tenant mismatch error")
	}
}

func TestRegistryLoginAndPluginSend(t *testing.T) {
	st := store.NewMemoryStore()
	backend := &fakeBackend{runCh: make(chan struct{})}
	writer := &wechatMetricCaptureWriter{ch: make(chan observability.Metric, 8)}
	reg := NewRegistry(Config{Enabled: true, CredRoot: t.TempDir()}, nil, st, zap.NewNop())
	reg.SetBackendFactory(func(string, string, BackendOptions) Backend { return backend })
	reg.SetMetricsWriter(writer)
	plugin := NewPlugin(reg, zap.NewNop())

	if _, err := reg.Ensure(context.Background(), "owner-1", false); err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}
	select {
	case <-backend.runCh:
	case <-time.After(time.Second):
		t.Fatal("backend Run was not started")
	}

	err := st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "owner-1",
		OwnerAccountID: "wx-owner",
		PeerWxid:       "wx-peer",
		SessionID:      "im-wechatbot-owner-1-wx-peer",
		CanSend:        true,
		SendState:      "ready",
	})
	if err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}

	if err := plugin.Send(context.Background(), channel.OutboundMessage{
		OwnerUserID: "owner-1",
		TenantKey:   "owner-1",
		ChatID:      "wx-peer",
		Content:     "hello",
	}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if backend.sentTo != "wx-peer" || backend.sentText != "hello" {
		t.Fatalf("unexpected send target/content: %q %q", backend.sentTo, backend.sentText)
	}
	if metric := writer.find(MetricActiveBots, "", nil); metric == nil || metric.Value != 1 {
		t.Fatalf("missing active bots metric: %+v", writer.items)
	}
	if metric := writer.find(MetricLoginTotal, "status", "success"); metric == nil {
		t.Fatalf("missing login success metric: %+v", writer.items)
	}
}

func TestBotRegistryConcurrentLoginIdempotent(t *testing.T) {
	backend := &blockingLoginBackend{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	reg := NewRegistry(Config{Enabled: true, CredRoot: t.TempDir()}, nil, store.NewMemoryStore(), zap.NewNop())
	var factoryCalls int32
	reg.SetBackendFactory(func(string, string, BackendOptions) Backend {
		atomic.AddInt32(&factoryCalls, 1)
		return backend
	})

	const workers = 20
	results := make(chan *BotInstance, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for idx := 0; idx < workers; idx++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst, err := reg.Ensure(context.Background(), "owner-1", false)
			if err != nil {
				errs <- err
				return
			}
			results <- inst
		}()
	}

	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("login was not started")
	}
	if got := atomic.LoadInt32(&factoryCalls); got != 1 {
		t.Fatalf("factory calls while first login is blocked = %d, want 1", got)
	}
	close(backend.release)
	wg.Wait()
	close(results)
	close(errs)
	defer reg.Stop()

	for err := range errs {
		t.Fatalf("Ensure returned error: %v", err)
	}
	var first *BotInstance
	for inst := range results {
		if first == nil {
			first = inst
			continue
		}
		if inst != first {
			t.Fatal("concurrent Ensure returned different instances for the same owner")
		}
	}
	if first == nil {
		t.Fatal("no instance returned")
	}
	if got := atomic.LoadInt32(&backend.loginCount); got != 1 {
		t.Fatalf("Login count = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&factoryCalls); got != 1 {
		t.Fatalf("factory calls = %d, want 1", got)
	}
	if first.Status() != StatusOnline {
		t.Fatalf("status = %s, want online", first.Status())
	}
}

func TestBotRegistry_ConcurrentAccess_NoDeadlock(t *testing.T) {
	reg := NewRegistry(Config{Enabled: true, CredRoot: t.TempDir()}, nil, store.NewMemoryStore(), zap.NewNop())
	reg.SetBackendFactory(func(string, string, BackendOptions) Backend {
		release := make(chan struct{})
		close(release)
		return &blockingLoginBackend{
			started: make(chan struct{}),
			release: release,
		}
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for idx := 0; idx < 100; idx++ {
			idx := idx
			wg.Add(1)
			go func() {
				defer wg.Done()
				owner := "owner-" + string(rune('a'+idx%5))
				_, _ = reg.Ensure(context.Background(), owner, false)
				_, _ = reg.Get(owner)
				_, _ = reg.Status(context.Background(), owner)
				if idx%17 == 0 {
					_ = reg.Logout(context.Background(), owner)
				}
			}()
		}
		wg.Wait()
		_ = reg.Stop()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent registry access deadlocked")
	}
}

func TestInstanceSendMarksNoContext(t *testing.T) {
	st := store.NewMemoryStore()
	backend := &fakeBackend{sendErr: errors.New("no context_token for user wx-peer")}
	writer := &wechatMetricCaptureWriter{ch: make(chan observability.Metric, 8)}
	inst := NewInstance(InstanceOptions{
		OwnerUserID:   "owner-1",
		Backend:       backend,
		Store:         st,
		Logger:        zap.NewNop(),
		MetricsWriter: writer,
	})
	inst.setStatus(StatusOnline, "")
	err := st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "owner-1",
		OwnerAccountID: "wx-owner",
		PeerWxid:       "wx-peer",
		SessionID:      "im-wechatbot-owner-1-wx-peer",
		CanSend:        true,
		SendState:      "ready",
	})
	if err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}

	if err := inst.Send(context.Background(), "wx-peer", "hello"); !errors.Is(err, ErrNoContextToken) {
		t.Fatalf("expected ErrNoContextToken, got %v", err)
	}
	conv, err := st.GetWechatConversationByOwnerPeer(context.Background(), "owner-1", "wx-peer")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv.CanSend || conv.SendState != "no_context" {
		t.Fatalf("send state not updated: can_send=%v send_state=%q", conv.CanSend, conv.SendState)
	}
	if metric := writer.find(MetricUnavailableTotal, "reason", "no_context"); metric == nil {
		t.Fatalf("missing unavailable no_context metric: %+v", writer.items)
	}
	if metric := writer.find(MetricOutboundTotal, "status", "no_context"); metric == nil {
		t.Fatalf("missing outbound no_context metric: %+v", writer.items)
	}
}

func TestInstanceSendEmitsOutboundSuccessMetric(t *testing.T) {
	st := store.NewMemoryStore()
	backend := &fakeBackend{}
	writer := &wechatMetricCaptureWriter{ch: make(chan observability.Metric, 8)}
	inst := NewInstance(InstanceOptions{
		OwnerUserID:   "owner-1",
		Backend:       backend,
		Store:         st,
		Logger:        zap.NewNop(),
		MetricsWriter: writer,
	})
	inst.setStatus(StatusOnline, "")
	if err := st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "owner-1",
		OwnerAccountID: "wx-owner",
		PeerWxid:       "wx-peer",
		SessionID:      "im-wechatbot-owner-1-wx-peer",
		CanSend:        false,
		SendState:      "unknown",
	}); err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}

	if err := inst.Send(context.Background(), "wx-peer", "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if metric := writer.find(MetricOutboundTotal, "status", "success"); metric == nil {
		t.Fatalf("missing outbound success metric: %+v", writer.items)
	}
	conv, err := st.GetWechatConversationByOwnerPeer(context.Background(), "owner-1", "wx-peer")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if !conv.CanSend || conv.SendState != "ready" {
		t.Fatalf("send state = can_send=%v state=%q, want true/ready", conv.CanSend, conv.SendState)
	}
}

func TestBuildSessionRecordMasksPeerID(t *testing.T) {
	rec := buildSessionRecord("im-wechatbot-owner-1-wxid_real_peer_123456", "owner-1", "wxid_real_peer_123456", time.Now())
	if strings.Contains(rec.Name, "wxid_real_peer_123456") {
		t.Fatalf("session name leaked raw peer wxid: %q", rec.Name)
	}
	if rec.Name == "微信会话 " {
		t.Fatalf("session name missing safe peer suffix: %q", rec.Name)
	}
}

func TestBotInstanceAutoRecoverAfterRunError(t *testing.T) {
	backend := &fakeBackend{runErr: errors.New("stream closed")}
	writer := &wechatMetricCaptureWriter{ch: make(chan observability.Metric, 8)}
	inst := NewInstance(InstanceOptions{
		OwnerUserID:        "owner-1",
		Backend:            backend,
		Store:              store.NewMemoryStore(),
		Logger:             zap.NewNop(),
		RecoverDelay:       time.Millisecond,
		MaxRecoverAttempts: 1,
		MetricsWriter:      writer,
	})
	defer inst.Stop()

	if err := inst.Login(context.Background(), false); err != nil {
		t.Fatalf("login: %v", err)
	}
	deadline := time.After(time.Second)
	for inst.Status() != StatusOnline || atomic.LoadInt32(&backend.runCount) < 2 {
		select {
		case <-deadline:
			t.Fatalf("auto recover did not return online, status=%s run_count=%d err=%s", inst.Status(), atomic.LoadInt32(&backend.runCount), inst.Error())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if metric := writer.find(MetricAutoRecoverTotal, "result", "success"); metric == nil {
		t.Fatalf("missing auto recover success metric: %+v", writer.items)
	}
}

func TestBotInstanceAutoRecoverRequiresManualAfterFailures(t *testing.T) {
	backend := &fakeBackend{loginErr: errors.New("login failed"), runErr: errors.New("stream closed")}
	writer := &wechatMetricCaptureWriter{ch: make(chan observability.Metric, 8)}
	inst := NewInstance(InstanceOptions{
		OwnerUserID:        "owner-1",
		Backend:            backend,
		Logger:             zap.NewNop(),
		RecoverDelay:       time.Millisecond,
		MaxRecoverAttempts: 2,
		MetricsWriter:      writer,
	})
	inst.setStatus(StatusOnline, "")
	go inst.runLoop(context.Background())
	defer inst.Stop()

	deadline := time.After(time.Second)
	for inst.Status() != StatusReloginRequired {
		select {
		case <-deadline:
			t.Fatalf("status=%s, want relogin_required; err=%s", inst.Status(), inst.Error())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if metric := writer.find(MetricAutoRecoverTotal, "result", "manual_required"); metric == nil {
		t.Fatalf("missing manual_required metric: %+v", writer.items)
	}
}

func TestBotInstanceAutoRecoverAfterPanic(t *testing.T) {
	backend := &fakeBackend{panicRun: true}
	inst := NewInstance(InstanceOptions{
		OwnerUserID:        "owner-1",
		Backend:            backend,
		Logger:             zap.NewNop(),
		RecoverDelay:       time.Millisecond,
		MaxRecoverAttempts: 1,
	})
	defer inst.Stop()

	if err := inst.Login(context.Background(), false); err != nil {
		t.Fatalf("login: %v", err)
	}
	deadline := time.After(time.Second)
	for inst.Status() != StatusOnline || atomic.LoadInt32(&backend.runCount) < 2 {
		select {
		case <-deadline:
			t.Fatalf("panic auto recover did not return online, status=%s run_count=%d", inst.Status(), atomic.LoadInt32(&backend.runCount))
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestBotInstanceInboundEmitsMetric(t *testing.T) {
	writer := &wechatMetricCaptureWriter{ch: make(chan observability.Metric, 8)}
	inst := NewInstance(InstanceOptions{
		OwnerUserID:   "owner-1",
		Backend:       &fakeBackend{},
		Store:         store.NewMemoryStore(),
		Logger:        zap.NewNop(),
		MetricsWriter: writer,
	})

	inst.handleIncoming(context.Background(), &SDKMessage{
		UserID:       "wx-peer",
		Text:         "hello",
		Type:         "text",
		ContextToken: "ctx-token",
		Timestamp:    time.Now(),
	})

	if metric := writer.find(MetricInboundTotal, "msg_type", "text"); metric == nil {
		t.Fatalf("missing inbound text metric: %+v", writer.items)
	}
}

func TestBotInstanceInboundLogsMalformedMessages(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	inst := NewInstance(InstanceOptions{
		OwnerUserID: "owner-1",
		Backend:     &fakeBackend{},
		Store:       store.NewMemoryStore(),
		Logger:      zap.New(core),
	})

	inst.handleIncoming(context.Background(), nil)
	inst.handleIncoming(context.Background(), &SDKMessage{
		Text:         "hello",
		Type:         "text",
		ContextToken: "ctx-token",
		Timestamp:    time.Now(),
	})

	if logs.FilterMessage("wechatbot 入站消息为空，已丢弃").Len() != 1 {
		t.Fatalf("missing nil inbound diagnostic log: %+v", logs.All())
	}
	if logs.FilterMessage("wechatbot 入站消息缺少 peer wxid，已丢弃").Len() != 1 {
		t.Fatalf("missing empty user_id diagnostic log: %+v", logs.All())
	}
}

func TestWeChatEventHubUserIsolation(t *testing.T) {
	reg := NewRegistry(Config{Enabled: true, CredRoot: t.TempDir()}, nil, store.NewMemoryStore(), zap.NewNop())
	ownerAEvents, unsubscribeA := reg.Subscribe("owner-a")
	defer unsubscribeA()
	ownerBEvents, unsubscribeB := reg.Subscribe("owner-b")
	defer unsubscribeB()

	reg.events.Publish("owner-a", Event{Type: "qr", Status: StatusWaitingQRScan, QRURL: "https://example.test/qr"})

	select {
	case ev := <-ownerAEvents:
		if ev.Type != "qr" || ev.QRURL == "" {
			t.Fatalf("unexpected owner-a event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("owner-a did not receive event")
	}

	select {
	case ev := <-ownerBEvents:
		t.Fatalf("owner-b received leaked event: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestWeChatBotCredentialsDirPermission(t *testing.T) {
	credRoot := t.TempDir()
	backend := &fakeBackend{}
	reg := NewRegistry(Config{Enabled: true, CredRoot: credRoot}, nil, store.NewMemoryStore(), zap.NewNop())
	reg.SetBackendFactory(func(_ string, credPath string, _ BackendOptions) Backend {
		if err := os.MkdirAll(filepath.Dir(credPath), 0777); err != nil {
			t.Fatalf("mkdir cred dir: %v", err)
		}
		if err := os.WriteFile(credPath, []byte("{}"), 0666); err != nil {
			t.Fatalf("write cred file: %v", err)
		}
		return backend
	})

	inst, err := reg.Ensure(context.Background(), "owner-1", false)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	defer inst.Stop()

	dirInfo, err := os.Stat(filepath.Join(credRoot, "users", "owner-1"))
	if err != nil {
		t.Fatalf("stat cred dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Fatalf("dir mode = %o, want 0700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(credRoot, "users", "owner-1", "credentials.json"))
	if err != nil {
		t.Fatalf("stat cred file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Fatalf("file mode = %o, want 0600", got)
	}
}

func TestPluginWebhookIsNotSupported(t *testing.T) {
	plugin := NewPlugin(nil, zap.NewNop())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/channel/wechatbot/webhook", nil)
	plugin.WebhookHandler()(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

type wechatMetricCaptureWriter struct {
	mu    sync.Mutex
	items []observability.Metric
	ch    chan observability.Metric
}

func (w *wechatMetricCaptureWriter) Record(_ context.Context, metric observability.Metric) error {
	w.mu.Lock()
	w.items = append(w.items, metric)
	w.mu.Unlock()
	if w.ch != nil {
		select {
		case w.ch <- metric:
		default:
		}
	}
	return nil
}

func (w *wechatMetricCaptureWriter) find(name, key string, value any) *observability.Metric {
	deadline := time.After(time.Second)
	for {
		w.mu.Lock()
		for _, metric := range w.items {
			if metric.Name == name {
				if key == "" {
					cp := metric
					w.mu.Unlock()
					return &cp
				}
				if metric.Labels != nil && metric.Labels[key] == value {
					cp := metric
					w.mu.Unlock()
					return &cp
				}
			}
		}
		w.mu.Unlock()
		select {
		case metric := <-w.ch:
			if metric.Name != name {
				continue
			}
			if key == "" {
				return &metric
			}
			if metric.Labels != nil && metric.Labels[key] == value {
				return &metric
			}
		case <-deadline:
			return nil
		}
	}
}

var _ observability.MetricsWriter = (*wechatMetricCaptureWriter)(nil)

type blockingLoginBackend struct {
	started    chan struct{}
	release    chan struct{}
	loginCount int32
	runCount   int32
	startOnce  sync.Once
}

func (b *blockingLoginBackend) Login(ctx context.Context, _ bool) (*Credentials, error) {
	atomic.AddInt32(&b.loginCount, 1)
	b.startOnce.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return &Credentials{AccountID: "wx-owner", UserID: "wx-sdk-user"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *blockingLoginBackend) OnMessage(func(*SDKMessage)) {}

func (b *blockingLoginBackend) Run(ctx context.Context) error {
	atomic.AddInt32(&b.runCount, 1)
	<-ctx.Done()
	return nil
}

func (b *blockingLoginBackend) Stop() {}

func (b *blockingLoginBackend) Send(context.Context, string, string) error {
	return nil
}

var _ Backend = (*blockingLoginBackend)(nil)
