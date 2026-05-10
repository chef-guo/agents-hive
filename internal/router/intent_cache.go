package router

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const DefaultIntentCacheTTL = 10 * time.Minute

type IntentCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]intentCacheEntry
}

type intentCacheEntry struct {
	Intent    IntentFrame
	ExpiresAt time.Time
}

func NewIntentCache(ttl time.Duration) *IntentCache {
	if ttl <= 0 {
		ttl = DefaultIntentCacheTTL
	}
	return &IntentCache{
		ttl:     ttl,
		now:     time.Now,
		entries: map[string]intentCacheEntry{},
	}
}

func (c *IntentCache) SetNowForTest(now func() time.Time) {
	if c == nil || now == nil {
		return
	}
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func (c *IntentCache) Get(sessionID, message string) (IntentFrame, bool) {
	if c == nil {
		return IntentFrame{}, false
	}
	key := IntentCacheKey(sessionID, message)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return IntentFrame{}, false
	}
	if !c.now().Before(entry.ExpiresAt) {
		delete(c.entries, key)
		return IntentFrame{}, false
	}
	return entry.Intent, true
}

func (c *IntentCache) Set(sessionID, message string, intent IntentFrame) {
	if c == nil || strings.TrimSpace(message) == "" {
		return
	}
	key := IntentCacheKey(sessionID, message)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = intentCacheEntry{
		Intent:    intent,
		ExpiresAt: c.now().Add(c.ttl),
	}
}

func IntentCacheKey(sessionID, message string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(message)))
	return hex.EncodeToString(sum[:])
}
