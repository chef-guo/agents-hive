package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/chef-guo/agents-hive/internal/errs"
)

// MemoryStore 内存存储实现，仅用于测试
type MemoryStore struct {
	mu           sync.RWMutex
	sessions     map[string]*SessionRecord
	messages     map[string][]MessageRecord // sessionID -> messages
	msgID        int64
	llmProviders map[string]*LLMProviderRecord
	llmModels    map[string]*LLMModelRecord
	schedules    map[string]*ScheduledPushRecord
	tasks        map[string]*ScheduledTask
	taskRuns     map[string]*ScheduledTaskRun
	externalIDs  map[string]*UserExternalIDRecord
	wechatConvs  map[string]*WechatConversationRecord
}

var _ SessionStore = (*MemoryStore)(nil)

// NewMemoryStore 创建内存存储实例（仅用于测试）
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions:    make(map[string]*SessionRecord),
		messages:    make(map[string][]MessageRecord),
		schedules:   make(map[string]*ScheduledPushRecord),
		tasks:       make(map[string]*ScheduledTask),
		taskRuns:    make(map[string]*ScheduledTaskRun),
		externalIDs: make(map[string]*UserExternalIDRecord),
		wechatConvs: make(map[string]*WechatConversationRecord),
	}
}

func (m *MemoryStore) CreateSession(_ context.Context, record *SessionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sessions[record.ID]; exists {
		return fmt.Errorf("session %s already exists", record.ID)
	}
	cp := *record
	m.sessions[record.ID] = &cp
	return nil
}

func (m *MemoryStore) SaveSession(_ context.Context, record *SessionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *record
	m.sessions[record.ID] = &cp
	return nil
}

func (m *MemoryStore) LoadSession(_ context.Context, sessionID string) (*SessionRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (m *MemoryStore) DeleteSession(_ context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
	delete(m.messages, sessionID)
	return nil
}

func (m *MemoryStore) ListSessions(_ context.Context) ([]*SessionRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*SessionRecord, 0, len(m.sessions))
	for _, s := range m.sessions {
		cp := *s
		result = append(result, &cp)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastAccessedAt > result[j].LastAccessedAt
	})
	return result, nil
}

func (m *MemoryStore) GetLastActiveSession(_ context.Context) (*SessionRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var latest *SessionRecord
	for _, s := range m.sessions {
		if latest == nil || s.LastAccessedAt > latest.LastAccessedAt {
			latest = s
		}
	}
	if latest == nil {
		return nil, ErrNotFound
	}
	cp := *latest
	return &cp, nil
}

func (m *MemoryStore) AddMessage(_ context.Context, sessionID, role, content string, metadata map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgID++
	msg := MessageRecord{
		ID:        m.msgID,
		SessionID: sessionID,
		Role:      role,
		Content:   content,
		CreatedAt: time.Now(),
	}
	if len(metadata) > 0 {
		if data, err := json.Marshal(metadata); err == nil {
			msg.Metadata = data
		}
	}
	m.messages[sessionID] = append(m.messages[sessionID], msg)
	if s, ok := m.sessions[sessionID]; ok {
		s.MessageCount = len(m.messages[sessionID])
	}
	return nil
}

func (m *MemoryStore) GetMessages(_ context.Context, sessionID string, limit int) ([]MessageRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	msgs := m.messages[sessionID]
	if limit > 0 && limit < len(msgs) {
		msgs = msgs[len(msgs)-limit:]
	}
	result := make([]MessageRecord, len(msgs))
	copy(result, msgs)
	return result, nil
}

func (m *MemoryStore) ForkSession(_ context.Context, parentID string, forkPoint int, newSessionID, newName, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	parent, ok := m.sessions[parentID]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().Format(time.RFC3339)
	m.sessions[newSessionID] = &SessionRecord{
		ID:        newSessionID,
		Name:      newName,
		CreatedAt: now,
		UpdatedAt: now,
		ParentID:  parentID,
		ForkPoint: forkPoint,
		UserID:    userID,
	}
	// Copy messages up to fork point
	if msgs, ok := m.messages[parentID]; ok && forkPoint <= len(msgs) {
		forked := make([]MessageRecord, forkPoint)
		copy(forked, msgs[:forkPoint])
		m.messages[newSessionID] = forked
	}
	parent.Children = append(parent.Children, newSessionID)
	return nil
}

func (m *MemoryStore) RevertSession(_ context.Context, sessionID string, revertTo int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs, ok := m.messages[sessionID]
	if !ok {
		return nil
	}
	if revertTo < len(msgs) {
		m.messages[sessionID] = msgs[:revertTo]
	}
	if s, ok := m.sessions[sessionID]; ok {
		s.MessageCount = len(m.messages[sessionID])
	}
	return nil
}

func (m *MemoryStore) ListSessionsByUser(_ context.Context, userID string, _ bool) ([]*SessionRecord, error) {
	if userID == "" {
		return []*SessionRecord{}, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var records []*SessionRecord
	for _, r := range m.sessions {
		if r.Deleted {
			continue
		}
		if r.UserID != userID {
			continue
		}
		cp := *r
		records = append(records, &cp)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].IsStarred != records[j].IsStarred {
			return records[i].IsStarred
		}
		return records[i].LastAccessedAt > records[j].LastAccessedAt
	})
	return records, nil
}

func (m *MemoryStore) UpsertSessionPref(_ context.Context, _, _ string, _ bool) error { return nil }
func (m *MemoryStore) GetSessionStarred(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}
func (m *MemoryStore) UpdateSessionTags(_ context.Context, sessionID string, tags []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[sessionID]; ok {
		s.Tags = tags
	}
	return nil
}

func externalIDKey(userID, providerType string) string {
	return userID + "\x00" + providerType
}

func wechatConvOwnerPeerKey(ownerUserID, peerWxid string) string {
	return ownerUserID + "\x00" + peerWxid
}

func (m *MemoryStore) UpsertUserExternalID(_ context.Context, rec *UserExternalIDRecord) error {
	if rec == nil {
		return errs.New(errs.CodeInvalidInput, "external id record is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.externalIDs == nil {
		m.externalIDs = make(map[string]*UserExternalIDRecord)
	}
	now := time.Now()
	cp := *rec
	key := externalIDKey(rec.UserID, rec.ProviderType)
	if old, ok := m.externalIDs[key]; ok {
		cp.ID = old.ID
		cp.CreatedAt = old.CreatedAt
	} else {
		cp.ID = int64(len(m.externalIDs) + 1)
		cp.CreatedAt = now
	}
	cp.UpdatedAt = now
	m.externalIDs[key] = &cp
	return nil
}

func (m *MemoryStore) GetUserExternalID(_ context.Context, userID, providerType string) (*UserExternalIDRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.externalIDs[externalIDKey(userID, providerType)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *rec
	return &cp, nil
}

func (m *MemoryStore) DeleteUserExternalID(_ context.Context, userID, providerType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.externalIDs, externalIDKey(userID, providerType))
	return nil
}

func (m *MemoryStore) UpsertWechatConversation(_ context.Context, rec *WechatConversationRecord) error {
	if rec == nil {
		return errs.New(errs.CodeInvalidInput, "wechat conversation record is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wechatConvs == nil {
		m.wechatConvs = make(map[string]*WechatConversationRecord)
	}
	now := time.Now()
	cp := *rec
	key := wechatConvOwnerPeerKey(rec.OwnerUserID, rec.PeerWxid)
	if old, ok := m.wechatConvs[key]; ok {
		cp.ID = old.ID
		cp.CreatedAt = old.CreatedAt
	} else {
		cp.ID = int64(len(m.wechatConvs) + 1)
		cp.CreatedAt = now
	}
	cp.UpdatedAt = now
	if cp.ChatType == "" {
		cp.ChatType = "direct"
	}
	if cp.SendState == "" {
		cp.SendState = "unknown"
	}
	m.wechatConvs[key] = &cp
	return nil
}

func (m *MemoryStore) GetWechatConversationBySessionID(_ context.Context, sessionID string) (*WechatConversationRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, rec := range m.wechatConvs {
		if rec.SessionID == sessionID {
			cp := *rec
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemoryStore) GetWechatConversationByOwnerPeer(_ context.Context, ownerUserID, peerWxid string) (*WechatConversationRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.wechatConvs[wechatConvOwnerPeerKey(ownerUserID, peerWxid)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *rec
	return &cp, nil
}

func (m *MemoryStore) ListWechatConversationsByOwner(_ context.Context, ownerUserID string) ([]*WechatConversationRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var records []*WechatConversationRecord
	for _, rec := range m.wechatConvs {
		if rec.OwnerUserID != ownerUserID {
			continue
		}
		cp := *rec
		records = append(records, &cp)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].LastMessageAt == nil {
			return false
		}
		if records[j].LastMessageAt == nil {
			return true
		}
		return records[i].LastMessageAt.After(*records[j].LastMessageAt)
	})
	return records, nil
}

func (m *MemoryStore) UpdateWechatConversationSendState(_ context.Context, ownerUserID, peerWxid string, canSend bool, sendState string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.wechatConvs[wechatConvOwnerPeerKey(ownerUserID, peerWxid)]
	if !ok {
		return ErrNotFound
	}
	rec.CanSend = canSend
	rec.SendState = sendState
	rec.UpdatedAt = time.Now()
	return nil
}

func (m *MemoryStore) UpdateWechatConversationContextToken(_ context.Context, ownerUserID, peerWxid, contextToken string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.wechatConvs[wechatConvOwnerPeerKey(ownerUserID, peerWxid)]
	if !ok {
		return ErrNotFound
	}
	rec.ContextToken = contextToken
	rec.CanSend = true
	rec.SendState = "ready"
	rec.UpdatedAt = time.Now()
	return nil
}

func (m *MemoryStore) GetWechatConversationContextToken(_ context.Context, ownerUserID, peerWxid string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.wechatConvs[wechatConvOwnerPeerKey(ownerUserID, peerWxid)]
	if !ok || rec.ContextToken == "" {
		return "", ErrNotFound
	}
	return rec.ContextToken, nil
}

func (m *MemoryStore) ClearWechatConversationContextTokens(_ context.Context, ownerUserID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rec := range m.wechatConvs {
		if rec.OwnerUserID != ownerUserID {
			continue
		}
		rec.ContextToken = ""
		rec.CanSend = false
		rec.SendState = "expired"
		rec.UpdatedAt = time.Now()
	}
	return nil
}

// LLM Provider/Model — MemoryStore 提供内存实现，供测试使用
func (m *MemoryStore) GetLLMProvider(_ context.Context, name string) (*LLMProviderRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.llmProviders {
		if p.Name == name {
			cp := *p
			return &cp, nil
		}
	}
	return nil, errs.New(errs.CodeNotFound, "llm provider not found: "+name)
}
func (m *MemoryStore) SaveLLMProvider(_ context.Context, rec *LLMProviderRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.llmProviders == nil {
		m.llmProviders = map[string]*LLMProviderRecord{}
	}
	cp := *rec
	m.llmProviders[rec.Name] = &cp
	return nil
}
func (m *MemoryStore) DeleteLLMProvider(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mod := range m.llmModels {
		if mod.ProviderName == name {
			delete(m.llmModels, mod.Name)
		}
	}
	delete(m.llmProviders, name)
	return nil
}
func (m *MemoryStore) ListLLMProviders(_ context.Context) ([]*LLMProviderRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*LLMProviderRecord, 0, len(m.llmProviders))
	for _, p := range m.llmProviders {
		cp := *p
		out = append(out, &cp)
	}
	return out, nil
}
func (m *MemoryStore) SetDefaultLLMProvider(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.llmProviders {
		if p.Name == name {
			p.IsDefault = true
		} else {
			p.IsDefault = false
		}
	}
	return nil
}
func (m *MemoryStore) GetLLMModel(_ context.Context, name string) (*LLMModelRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, mod := range m.llmModels {
		if mod.Name == name {
			cp := *mod
			return &cp, nil
		}
	}
	return nil, errs.New(errs.CodeNotFound, "llm model not found: "+name)
}
func (m *MemoryStore) SaveLLMModel(_ context.Context, rec *LLMModelRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.llmModels == nil {
		m.llmModels = map[string]*LLMModelRecord{}
	}
	cp := *rec
	m.llmModels[rec.Name] = &cp
	return nil
}
func (m *MemoryStore) UpdateLLMModel(_ context.Context, oldName string, rec *LLMModelRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.llmModels == nil {
		m.llmModels = map[string]*LLMModelRecord{}
	}
	if _, ok := m.llmModels[oldName]; !ok {
		return errs.New(errs.CodeNotFound, "llm model not found: "+oldName)
	}
	if oldName != rec.Name {
		if _, ok := m.llmModels[rec.Name]; ok {
			return errs.New(errs.CodeInvalidInput, "llm model already exists: "+rec.Name)
		}
		delete(m.llmModels, oldName)
	}
	if rec.IsDefault {
		for _, mod := range m.llmModels {
			mod.IsDefault = false
		}
	}
	cp := *rec
	m.llmModels[rec.Name] = &cp
	return nil
}
func (m *MemoryStore) DeleteLLMModel(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.llmModels, name)
	return nil
}
func (m *MemoryStore) ListLLMModels(_ context.Context) ([]*LLMModelRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*LLMModelRecord, 0, len(m.llmModels))
	for _, mod := range m.llmModels {
		cp := *mod
		out = append(out, &cp)
	}
	return out, nil
}
func (m *MemoryStore) SetDefaultLLMModel(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mod := range m.llmModels {
		if mod.Name == name {
			mod.IsDefault = true
		} else {
			mod.IsDefault = false
		}
	}
	return nil
}

func (m *MemoryStore) Close() error {
	return nil
}

// Store interface stubs — 仅用于测试，不提供实际功能
func (m *MemoryStore) GetConfig(_ context.Context, _ string) (string, error)     { return "", nil }
func (m *MemoryStore) SetConfig(_ context.Context, _, _ string) error            { return nil }
func (m *MemoryStore) GetAllConfig(_ context.Context) (map[string]string, error) { return nil, nil }
func (m *MemoryStore) GetChannelConfig(_ context.Context, _ string) (*ChannelConfigRecord, error) {
	return nil, ErrNotFound
}
func (m *MemoryStore) SaveChannelConfig(_ context.Context, _ *ChannelConfigRecord) error { return nil }
func (m *MemoryStore) ListChannelConfigs(_ context.Context) ([]*ChannelConfigRecord, error) {
	return nil, nil
}
func (m *MemoryStore) SaveScheduledPush(_ context.Context, rec *ScheduledPushRecord) error {
	task := memoryScheduledPushToTask(rec)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveScheduledTaskLocked(task)
	return nil
}
func (m *MemoryStore) GetScheduledPush(_ context.Context, id string) (*ScheduledPushRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	task, ok := m.tasks[id]
	if !ok || task.TargetType != "im_push" {
		return nil, ErrNotFound
	}
	return memoryScheduledTaskToPush(task), nil
}
func (m *MemoryStore) DeleteScheduledPush(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[id]
	if !ok || task.TargetType != "im_push" {
		return ErrNotFound
	}
	delete(m.schedules, id)
	delete(m.tasks, id)
	return nil
}
func (m *MemoryStore) ListScheduledPushes(_ context.Context, platform string) ([]*ScheduledPushRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*ScheduledPushRecord, 0, len(m.tasks))
	for _, rec := range m.tasks {
		if rec.TargetType != "im_push" {
			continue
		}
		if platform != "" && rec.Platform != platform {
			continue
		}
		out = append(out, memoryScheduledTaskToPush(rec))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}
func (m *MemoryStore) UpdateScheduledPushRun(_ context.Context, id string, lastRunAt, nextRunAt time.Time, lastError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.tasks[id]
	if !ok || rec.TargetType != "im_push" {
		return ErrNotFound
	}
	rec.LastRunAt = &lastRunAt
	rec.NextRunAt = &nextRunAt
	rec.LastError = lastError
	rec.UpdatedAt = time.Now().UTC()
	m.schedules[id] = memoryScheduledTaskToPush(rec)
	return nil
}
func memoryScheduledPushToTask(rec *ScheduledPushRecord) *ScheduledTask {
	task := scheduledPushToTask(rec)
	return task
}
func memoryScheduledTaskToPush(rec *ScheduledTask) *ScheduledPushRecord {
	return scheduledTaskToPush(rec)
}
func (m *MemoryStore) SaveScheduledTask(_ context.Context, rec *ScheduledTask) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveScheduledTaskLocked(rec)
	return nil
}
func (m *MemoryStore) saveScheduledTaskLocked(rec *ScheduledTask) {
	if m.tasks == nil {
		m.tasks = make(map[string]*ScheduledTask)
	}
	if m.schedules == nil {
		m.schedules = make(map[string]*ScheduledPushRecord)
	}
	cp := cloneScheduledTask(rec)
	now := time.Now().UTC()
	existing := m.tasks[cp.ID]
	createdAtWasZero := cp.CreatedAt.IsZero()
	if cp.TargetType == "" {
		cp.TargetType = "im_push"
	}
	if cp.TargetConfig == nil {
		cp.TargetConfig = map[string]any{}
	}
	if cp.Timezone == "" {
		cp.Timezone = "UTC"
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = now
	}
	if existing != nil {
		cp.ActiveRunID = existing.ActiveRunID
		if existing.LeaseExpiresAt != nil {
			leaseExpiresAt := *existing.LeaseExpiresAt
			cp.LeaseExpiresAt = &leaseExpiresAt
		} else {
			cp.LeaseExpiresAt = nil
		}
		if existing.LastRunAt != nil {
			lastRunAt := *existing.LastRunAt
			cp.LastRunAt = &lastRunAt
		} else {
			cp.LastRunAt = nil
		}
		cp.LastError = existing.LastError
		if createdAtWasZero {
			cp.CreatedAt = existing.CreatedAt
		}
	}
	cp.UpdatedAt = now
	m.tasks[cp.ID] = cp
	m.schedules[cp.ID] = memoryScheduledTaskToPush(cp)
}
func (m *MemoryStore) GetScheduledTask(_ context.Context, id string) (*ScheduledTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneScheduledTask(rec), nil
}
func (m *MemoryStore) DeleteScheduledTask(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tasks[id]; !ok {
		return ErrNotFound
	}
	delete(m.schedules, id)
	delete(m.tasks, id)
	return nil
}
func (m *MemoryStore) ListScheduledTasksByUser(_ context.Context, createdBy string) ([]*ScheduledTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*ScheduledTask, 0, len(m.tasks))
	for _, rec := range m.tasks {
		if rec.CreatedBy != createdBy {
			continue
		}
		out = append(out, cloneScheduledTask(rec))
	}
	sortScheduledTasks(out)
	return out, nil
}

func (m *MemoryStore) ListAllScheduledTasks(_ context.Context) ([]*ScheduledTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*ScheduledTask, 0, len(m.tasks))
	for _, rec := range m.tasks {
		out = append(out, cloneScheduledTask(rec))
	}
	sortScheduledTasks(out)
	return out, nil
}

func (m *MemoryStore) ListEnabledScheduledTasks(_ context.Context) ([]*ScheduledTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*ScheduledTask, 0, len(m.tasks))
	for _, rec := range m.tasks {
		if !rec.Enabled {
			continue
		}
		out = append(out, cloneScheduledTask(rec))
	}
	sortScheduledTasks(out)
	return out, nil
}
func (m *MemoryStore) EnsureScheduledTaskRunPartition(_ context.Context, _ time.Time) error {
	return nil
}
func (m *MemoryStore) MaintainScheduledTaskRunPartitions(_ context.Context, _ time.Time, _ int) error {
	return nil
}
func (m *MemoryStore) ClaimDueScheduledTaskRun(_ context.Context, taskID string, now time.Time, runID string, leaseUntil time.Time, nextRunAt time.Time, claimedBy string) (*ScheduledTaskRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.tasks[taskID]
	if !ok || !rec.Enabled || rec.NextRunAt == nil || rec.NextRunAt.After(now) {
		return nil, ErrNotFound
	}
	if rec.ActiveRunID != "" && rec.LeaseExpiresAt != nil && rec.LeaseExpiresAt.After(now) {
		return nil, ErrNotFound
	}
	runKey := scheduledTaskRunKey(*rec.NextRunAt, runID)
	taskAtKey := scheduledTaskTaskAtKey(taskID, *rec.NextRunAt)
	for _, run := range m.taskRuns {
		if scheduledTaskTaskAtKey(run.TaskID, run.ScheduledAt) == taskAtKey {
			return nil, ErrNotFound
		}
	}
	run := &ScheduledTaskRun{
		ScheduledAt:    rec.NextRunAt.UTC(),
		ID:             runID,
		TaskID:         taskID,
		StartedAt:      time.Now().UTC(),
		Status:         "running",
		ClaimedBy:      claimedBy,
		ClaimExpiresAt: &leaseUntil,
	}
	if m.taskRuns == nil {
		m.taskRuns = make(map[string]*ScheduledTaskRun)
	}
	m.taskRuns[runKey] = cloneScheduledTaskRun(run)
	rec.ActiveRunID = runID
	rec.LeaseExpiresAt = &leaseUntil
	lastRunAt := now.UTC()
	rec.LastRunAt = &lastRunAt
	if nextRunAt.IsZero() {
		rec.NextRunAt = nil
	} else {
		next := nextRunAt.UTC()
		rec.NextRunAt = &next
	}
	rec.UpdatedAt = time.Now().UTC()
	m.schedules[taskID] = memoryScheduledTaskToPush(rec)
	return cloneScheduledTaskRun(run), nil
}
func (m *MemoryStore) ClaimManualScheduledTaskRun(_ context.Context, taskID string, now time.Time, runID string, leaseUntil time.Time, claimedBy string) (*ScheduledTaskRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.tasks[taskID]
	if !ok || !rec.Enabled {
		return nil, ErrNotFound
	}
	if rec.ActiveRunID != "" && rec.LeaseExpiresAt != nil && rec.LeaseExpiresAt.After(now) {
		return nil, ErrNotFound
	}
	scheduledAt := now.UTC()
	run := &ScheduledTaskRun{
		ScheduledAt:    scheduledAt,
		ID:             runID,
		TaskID:         taskID,
		StartedAt:      time.Now().UTC(),
		Status:         "running",
		ClaimedBy:      claimedBy,
		ClaimExpiresAt: &leaseUntil,
	}
	if m.taskRuns == nil {
		m.taskRuns = make(map[string]*ScheduledTaskRun)
	}
	m.taskRuns[scheduledTaskRunKey(scheduledAt, runID)] = cloneScheduledTaskRun(run)
	rec.ActiveRunID = runID
	rec.LeaseExpiresAt = &leaseUntil
	lastRunAt := scheduledAt
	rec.LastRunAt = &lastRunAt
	rec.UpdatedAt = time.Now().UTC()
	m.schedules[taskID] = memoryScheduledTaskToPush(rec)
	return cloneScheduledTaskRun(run), nil
}
func (m *MemoryStore) FinishScheduledTaskRun(_ context.Context, run *ScheduledTaskRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := scheduledTaskRunKey(run.ScheduledAt, run.ID)
	stored, ok := m.taskRuns[key]
	if !ok {
		return ErrNotFound
	}
	cp := cloneScheduledTaskRun(stored)
	cp.Status = run.Status
	cp.AttemptCount = run.AttemptCount
	cp.Output = run.Output
	cp.Error = run.Error
	cp.SessionID = run.SessionID
	if run.FinishedAt != nil {
		cp.FinishedAt = run.FinishedAt
	} else {
		now := time.Now().UTC()
		cp.FinishedAt = &now
	}
	m.taskRuns[key] = cp
	if task, ok := m.tasks[run.TaskID]; ok && task.ActiveRunID == run.ID {
		task.ActiveRunID = ""
		task.LeaseExpiresAt = nil
		task.LastError = run.Error
		task.UpdatedAt = time.Now().UTC()
		m.schedules[run.TaskID] = memoryScheduledTaskToPush(task)
	}
	if run.Status == "failed" || run.Status == "timeout" {
		total, failures := m.countRecentScheduledTaskFailuresLocked(run.TaskID, 5)
		if total == 5 && failures == 5 {
			if task, ok := m.tasks[run.TaskID]; ok {
				task.Enabled = false
				task.LastError = "最近 5 次执行均失败,已自动停用"
				task.UpdatedAt = time.Now().UTC()
				m.schedules[run.TaskID] = memoryScheduledTaskToPush(task)
			}
		}
	}
	return nil
}

func (m *MemoryStore) CountRecentScheduledTaskFailures(_ context.Context, taskID string, limit int) (int, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	total, failures := m.countRecentScheduledTaskFailuresLocked(taskID, limit)
	return total, failures, nil
}

func (m *MemoryStore) BulkMarkScheduledTaskReloadFailures(_ context.Context, failures map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, msg := range failures {
		task, ok := m.tasks[id]
		if !ok {
			continue
		}
		if msg == "" {
			msg = "定时任务恢复失败"
		}
		task.Enabled = false
		task.LastError = msg
		task.UpdatedAt = time.Now().UTC()
		m.schedules[id] = memoryScheduledTaskToPush(task)
	}
	return nil
}

func (m *MemoryStore) countRecentScheduledTaskFailuresLocked(taskID string, limit int) (int, int) {
	if limit <= 0 || limit > 100 {
		limit = 5
	}
	runs := make([]*ScheduledTaskRun, 0, len(m.taskRuns))
	for _, rec := range m.taskRuns {
		if rec.TaskID != taskID || rec.Status == "running" {
			continue
		}
		runs = append(runs, rec)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].ScheduledAt.After(runs[j].ScheduledAt)
	})
	if len(runs) > limit {
		runs = runs[:limit]
	}
	failures := 0
	for _, run := range runs {
		if run.Status == "failed" || run.Status == "timeout" {
			failures++
		}
	}
	return len(runs), failures
}

func (m *MemoryStore) ListScheduledTaskRuns(_ context.Context, taskID string, limit int) ([]*ScheduledTaskRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	out := make([]*ScheduledTaskRun, 0, len(m.taskRuns))
	for _, rec := range m.taskRuns {
		if rec.TaskID != taskID {
			continue
		}
		out = append(out, cloneScheduledTaskRun(rec))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ScheduledAt.After(out[j].ScheduledAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func cloneScheduledTask(rec *ScheduledTask) *ScheduledTask {
	if rec == nil {
		return nil
	}
	cp := *rec
	if rec.TargetConfig != nil {
		cp.TargetConfig = make(map[string]any, len(rec.TargetConfig))
		for k, v := range rec.TargetConfig {
			cp.TargetConfig[k] = v
		}
	}
	if rec.LastRunAt != nil {
		t := *rec.LastRunAt
		cp.LastRunAt = &t
	}
	if rec.NextRunAt != nil {
		t := *rec.NextRunAt
		cp.NextRunAt = &t
	}
	if rec.LeaseExpiresAt != nil {
		t := *rec.LeaseExpiresAt
		cp.LeaseExpiresAt = &t
	}
	return &cp
}
func cloneScheduledTaskRun(rec *ScheduledTaskRun) *ScheduledTaskRun {
	if rec == nil {
		return nil
	}
	cp := *rec
	if rec.FinishedAt != nil {
		t := *rec.FinishedAt
		cp.FinishedAt = &t
	}
	if rec.ClaimExpiresAt != nil {
		t := *rec.ClaimExpiresAt
		cp.ClaimExpiresAt = &t
	}
	return &cp
}
func sortScheduledTasks(records []*ScheduledTask) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
}
func scheduledTaskRunKey(scheduledAt time.Time, runID string) string {
	return scheduledAt.UTC().Format(time.RFC3339Nano) + "/" + runID
}
func scheduledTaskTaskAtKey(taskID string, scheduledAt time.Time) string {
	return taskID + "/" + scheduledAt.UTC().Format(time.RFC3339Nano)
}
func (m *MemoryStore) GetMCPServer(_ context.Context, _ string) (*MCPServerRecord, error) {
	return nil, ErrNotFound
}
func (m *MemoryStore) SaveMCPServer(_ context.Context, _ *MCPServerRecord) error { return nil }
func (m *MemoryStore) DeleteMCPServer(_ context.Context, _ string) error         { return nil }
func (m *MemoryStore) ListMCPServers(_ context.Context) ([]*MCPServerRecord, error) {
	return nil, nil
}
func (m *MemoryStore) GetExternalResource(_ context.Context, _ string) (*ExternalResourceRecord, error) {
	return nil, ErrNotFound
}
func (m *MemoryStore) SaveExternalResource(_ context.Context, _ *ExternalResourceRecord) error {
	return nil
}
func (m *MemoryStore) DeleteExternalResource(_ context.Context, _ string) error { return nil }
func (m *MemoryStore) ListExternalResources(_ context.Context) ([]*ExternalResourceRecord, error) {
	return nil, nil
}
func (m *MemoryStore) SaveGrant(_ context.Context, _ *PermissionGrantRecord) error { return nil }
func (m *MemoryStore) LoadGrants(_ context.Context) ([]PermissionGrantRecord, error) {
	return nil, nil
}
func (m *MemoryStore) DeleteGrant(_ context.Context, _ int64) error                { return nil }
func (m *MemoryStore) DeleteAllGrants(_ context.Context) error                     { return nil }
func (m *MemoryStore) SaveOAuthToken(_ context.Context, _ *OAuthTokenRecord) error { return nil }
func (m *MemoryStore) LoadOAuthToken(_ context.Context, _ string) (*OAuthTokenRecord, error) {
	return nil, ErrNotFound
}
func (m *MemoryStore) DeleteOAuthToken(_ context.Context, _ string) error { return nil }
func (m *MemoryStore) OnConfigChange(_ func(key string))                  {}
