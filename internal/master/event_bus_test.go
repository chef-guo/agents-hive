package master

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// newTestBus 创建用于测试的 EventBus，并在测试结束时自动 Close。
//
// 关键：t.Cleanup 注册 eb.Close()，确保：
//  1. 所有后台 retryCriticalSend goroutine 在测试结束前完成（通过 closeCh 信号立即退出）。
//  2. goroutine 不会在 testing.T 销毁后仍尝试写入 zaptest.Logger（消除数据竞态）。
//  3. 所有订阅者通道被关闭，无资源泄漏。
func newTestBus(t *testing.T) *EventBus {
	t.Helper()
	eb := NewEventBus(zaptest.NewLogger(t))
	t.Cleanup(func() { eb.Close() })
	return eb
}

// ---------- 基础功能 ----------

func TestSubscribeUnsubscribe(t *testing.T) {
	eb := newTestBus(t)

	id1, ch1 := eb.Subscribe()
	id2, ch2 := eb.Subscribe()

	if id1 == id2 {
		t.Fatal("两次订阅应产生不同的 ID")
	}
	if ch1 == nil || ch2 == nil {
		t.Fatal("订阅通道不应为 nil")
	}

	eb.Unsubscribe(id1)
	// 通道应已关闭
	select {
	case _, ok := <-ch1:
		if ok {
			t.Fatal("取消订阅后通道应已关闭")
		}
	default:
		t.Fatal("取消订阅后通道应可立即读取（已关闭）")
	}

	// id2 仍应存活
	eb.Broadcast(BroadcastMessage{Type: EventTypeMessage, Payload: "ping"})
	select {
	case msg := <-ch2:
		if msg.Type != EventTypeMessage {
			t.Fatalf("期望消息类型 %q，实际 %q", EventTypeMessage, msg.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("应能收到广播消息")
	}

	eb.Unsubscribe(id2)
}

// ---------- 关键问题修复验证：Broadcast 不再阻塞 ----------

// TestBroadcastCriticalNonBlocking 验证：即使订阅者通道已满，
// Broadcast 对关键事件也能在极短时间内返回（不再有 5s 阻塞）。
func TestBroadcastCriticalNonBlocking(t *testing.T) {
	eb := newTestBus(t)

	// 创建一个故意不消费的订阅者（使其通道立即填满）
	_, _ = eb.Subscribe()

	// 先填满通道（容量 256）
	for i := 0; i < subscriberBufferSize; i++ {
		eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
	}

	// 现在通道已满，向其广播关键事件，计时
	start := time.Now()
	eb.Broadcast(BroadcastMessage{Type: EventTypeInputRequest})
	elapsed := time.Since(start)

	// 修复后：Broadcast 应在毫秒级返回，绝不会等待 5 秒
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Broadcast 阻塞了 %v，应在 500ms 内返回（修复前会阻塞 5s）", elapsed)
	}
}

// TestBroadcastMultipleDeadSubscribersNonBlocking 验证 N 个死订阅者不会导致 N×5s 阻塞。
func TestBroadcastMultipleDeadSubscribersNonBlocking(t *testing.T) {
	const numDead = 5
	eb := newTestBus(t)

	// 创建 numDead 个满通道的死订阅者
	for i := 0; i < numDead; i++ {
		_, _ = eb.Subscribe()
	}
	for i := 0; i < subscriberBufferSize; i++ {
		eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
	}

	start := time.Now()
	eb.Broadcast(BroadcastMessage{Type: EventTypeInputRequest})
	elapsed := time.Since(start)

	// 修复前：最坏 numDead * 5s = 25s；修复后应在 1s 内返回
	if elapsed > time.Second {
		t.Fatalf("对 %d 个死订阅者广播关键事件阻塞了 %v，修复后应立即返回", numDead, elapsed)
	}
}

// ---------- 关键事件异步重试最终送达 ----------

// TestCriticalEventRetryDelivery 验证：通道从满变为可用时，
// 后台重试 goroutine 能在 criticalEventTimeout 内成功送达。
func TestCriticalEventRetryDelivery(t *testing.T) {
	eb := newTestBus(t)
	_, ch := eb.Subscribe()

	// 填满通道（仅留 1 个空位，用来放非关键消息作为哨兵）
	for i := 0; i < subscriberBufferSize; i++ {
		ch <- BroadcastMessage{Type: EventTypeMessage}
	}

	// 广播关键事件：通道已满，后台 goroutine 开始重试
	eb.Broadcast(BroadcastMessage{Type: EventTypeInputRequest, Payload: "重要"})

	// 稍等 goroutine 启动后，持续消费通道直到收到关键事件
	// 使用独立 goroutine 消费非关键消息，留出空间供重试 goroutine 写入
	received := make(chan BroadcastMessage, 1)
	go func() {
		for msg := range ch {
			if msg.Type == EventTypeInputRequest {
				received <- msg
				return
			}
			// 继续消费非关键消息，释放空间
		}
	}()

	select {
	case msg := <-received:
		if msg.Type != EventTypeInputRequest {
			t.Fatalf("期望收到关键事件 %q，实际 %q", EventTypeInputRequest, msg.Type)
		}
	case <-time.After(criticalEventTimeout + time.Second):
		t.Fatal("关键事件重试应在 criticalEventTimeout 内送达")
	}
}

// ---------- 非关键事件丢弃统计 ----------

func TestNonCriticalDropCounting(t *testing.T) {
	eb := newTestBus(t)
	_, _ = eb.Subscribe()

	// 填满通道
	for i := 0; i < subscriberBufferSize; i++ {
		eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
	}

	// 再发 3 条非关键事件，应全部被丢弃
	for i := 0; i < 3; i++ {
		eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
	}

	if got := eb.DroppedTotal(); got < 3 {
		t.Fatalf("期望至少丢弃 3 条，实际 %d", got)
	}
}

// ---------- 死订阅者清理 ----------

// TestPruneDeadSubscribers 验证连续丢弃超阈值的订阅者会被 Prune 清除。
func TestPruneDeadSubscribers(t *testing.T) {
	eb := newTestBus(t)
	deadID, _ := eb.Subscribe()

	// 填满通道
	for i := 0; i < subscriberBufferSize; i++ {
		eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
	}

	// 触发足够多的连续丢弃
	for i := 0; i < deadSubscriberThreshold+2; i++ {
		eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
	}

	pruned := eb.PruneDeadSubscribers()
	found := false
	for _, id := range pruned {
		if id == deadID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("死订阅者 %d 应被 PruneDeadSubscribers 清理，实际 pruned=%v", deadID, pruned)
	}

	// 清理后再广播不应 panic，也不应再看到该订阅者
	eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
}

func TestTodoSnapshotSlowConsumer(t *testing.T) {
	eb := newTestBus(t)
	deadID, _ := eb.Subscribe()
	m := &Master{eventBus: eb, obsCh: make(chan observabilityEntry, 512)}

	for i := int64(0); i < subscriberBufferSize+deadSubscriberThreshold; i++ {
		err := m.BroadcastTodoSnapshot(context.Background(), sessiontodo.Snapshot{
			SessionID:   "session-slow",
			PlanStatus:  sessiontodo.PlanStatusExecuting,
			PlanVersion: i + 1,
		})
		if err != nil {
			t.Fatalf("broadcast todo_snapshot: %v", err)
		}
	}

	eb.mu.RLock()
	_, exists := eb.subs[deadID]
	eb.mu.RUnlock()
	if exists {
		t.Fatalf("todo_snapshot 慢消费者 %d 达到连续丢弃阈值后应被自动剔除", deadID)
	}
}

// TestPruneResetOnSuccess 验证发送成功时连续丢弃计数器归零，不误杀活跃订阅者。
func TestPruneResetOnSuccess(t *testing.T) {
	eb := newTestBus(t)
	activeID, ch := eb.Subscribe()

	// 制造一些丢弃
	for i := 0; i < subscriberBufferSize; i++ {
		eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
	}
	for i := 0; i < deadSubscriberThreshold-2; i++ {
		eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
	}

	// 消费通道，恢复空间
	for len(ch) > 0 {
		<-ch
	}

	// 成功发送一条，应重置计数
	eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})

	// 此时连续丢弃应归零，不应被 Prune
	pruned := eb.PruneDeadSubscribers()
	for _, id := range pruned {
		if id == activeID {
			t.Fatalf("活跃订阅者 %d 不应被 PruneDeadSubscribers 清理", activeID)
		}
	}
}

// TestPruneIdempotent 验证 PruneDeadSubscribers 无死订阅者时返回 nil 且不 panic。
func TestPruneIdempotent(t *testing.T) {
	eb := newTestBus(t)
	id, ch := eb.Subscribe()

	// 消费通道确保发送成功
	go func() {
		for range ch {
		}
	}()
	eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
	time.Sleep(10 * time.Millisecond)

	result := eb.PruneDeadSubscribers()
	if result != nil {
		t.Fatalf("无死订阅者时应返回 nil，实际 %v", result)
	}
	eb.Unsubscribe(id)
}

// ---------- 并发安全 ----------

// TestConcurrentSubscribeUnsubscribeBroadcast 验证高并发下不发生 data race。
// 使用 go test -race 运行可完整检测。
func TestConcurrentSubscribeUnsubscribeBroadcast(t *testing.T) {
	eb := newTestBus(t)
	var wg sync.WaitGroup
	var broadcastCount atomic.Int64

	// 10 个并发广播者
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				eb.Broadcast(BroadcastMessage{Type: EventTypeMessage, Payload: j})
				broadcastCount.Add(1)
			}
		}()
	}

	// 5 个并发订阅/取消者
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				id, ch := eb.Subscribe()
				// 简单消费
				go func(c chan BroadcastMessage) {
					for range c {
					}
				}(ch)
				time.Sleep(time.Millisecond)
				eb.Unsubscribe(id)
			}
		}()
	}

	wg.Wait()
}

// TestConcurrentBroadcastAndPrune 验证 Broadcast 与 PruneDeadSubscribers 并发时不 panic。
func TestConcurrentBroadcastAndPrune(t *testing.T) {
	eb := newTestBus(t)

	// 创建若干不消费的死订阅者
	for i := 0; i < 5; i++ {
		_, _ = eb.Subscribe()
	}
	// 制造足够丢弃使其成为死订阅者
	for i := 0; i < subscriberBufferSize+deadSubscriberThreshold+5; i++ {
		eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
	}

	var wg sync.WaitGroup
	// 并发广播
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
			}
		}()
	}
	// 并发 Prune
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				eb.PruneDeadSubscribers()
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
}

// ---------- RLock 释放速度回归验证 ----------

// TestBroadcastRLockReleaseSpeed 验证 Broadcast 结束后 Subscribe（需要 Lock）能立即获取锁。
// 若 Broadcast 仍在持有 RLock 阻塞，Subscribe 会超时失败。
func TestBroadcastRLockReleaseSpeed(t *testing.T) {
	eb := newTestBus(t)

	// 创建满通道订阅者
	_, _ = eb.Subscribe()
	for i := 0; i < subscriberBufferSize; i++ {
		eb.Broadcast(BroadcastMessage{Type: EventTypeMessage})
	}

	done := make(chan struct{})
	go func() {
		// 等 Broadcast 完成后，Subscribe 应该很快拿到锁
		time.Sleep(50 * time.Millisecond)
		id, ch := eb.Subscribe()
		close(done)
		eb.Unsubscribe(id)
		_ = ch
	}()

	// 先发一个关键事件（以前会阻塞 5s）
	eb.Broadcast(BroadcastMessage{Type: EventTypeInputRequest})

	select {
	case <-done:
		// Subscribe 顺利完成，RLock 已释放
	case <-time.After(time.Second):
		t.Fatal("Broadcast 后 Subscribe 未能在 1s 内获取锁，RLock 可能仍被占用")
	}
}

// ---------- 辅助：确保 zap.Logger 不是 nil（避免空指针） ----------

func TestNewEventBusLogger(t *testing.T) {
	eb := NewEventBus(zap.NewNop())
	if eb == nil {
		t.Fatal("NewEventBus 不应返回 nil")
	}
	if eb.logger == nil {
		t.Fatal("EventBus.logger 不应为 nil")
	}
}
