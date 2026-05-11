package wechatbot

import (
	"sync"
	"time"
)

// Status 表示当前用户微信连接状态。
type Status string

const (
	StatusDisabled        Status = "disabled"
	StatusNotConnected    Status = "not_connected"
	StatusWaitingQRScan   Status = "waiting_qr_scan"
	StatusScanned         Status = "scanned"
	StatusOnline          Status = "online"
	StatusRecovering      Status = "recovering"
	StatusReloginRequired Status = "relogin_required"
	StatusOffline         Status = "offline"
	StatusError           Status = "error"
)

// Event 是登录二维码/扫码/在线状态事件。
type Event struct {
	Type      string    `json:"type"`
	Status    Status    `json:"status,omitempty"`
	QRURL     string    `json:"qr_url,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type eventHub struct {
	mu   sync.RWMutex
	subs map[string]map[chan Event]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{subs: make(map[string]map[chan Event]struct{})}
}

func (h *eventHub) Subscribe(ownerUserID string) (<-chan Event, func()) {
	ch := make(chan Event, 16)
	h.mu.Lock()
	if h.subs[ownerUserID] == nil {
		h.subs[ownerUserID] = make(map[chan Event]struct{})
	}
	h.subs[ownerUserID][ch] = struct{}{}
	h.mu.Unlock()

	unsubscribe := func() {
		h.mu.Lock()
		if subs := h.subs[ownerUserID]; subs != nil {
			if _, ok := subs[ch]; ok {
				delete(subs, ch)
				close(ch)
			}
			if len(subs) == 0 {
				delete(h.subs, ownerUserID)
			}
		}
		h.mu.Unlock()
	}
	return ch, unsubscribe
}

func (h *eventHub) Publish(ownerUserID string, ev Event) {
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}
	h.mu.RLock()
	subs := h.subs[ownerUserID]
	targets := make([]chan Event, 0, len(subs))
	for ch := range subs {
		targets = append(targets, ch)
	}
	h.mu.RUnlock()

	for _, ch := range targets {
		select {
		case ch <- ev:
		default:
		}
	}
}
