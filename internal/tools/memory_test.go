package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/memory"
	"go.uber.org/zap"
)

func TestMemorySaveWritesGovernanceMetadata(t *testing.T) {
	store := &fakeMemoryToolStore{}
	ctx := auth.WithUser(context.Background(), &auth.User{ID: "user-1", Role: "user", Status: "active"})

	result, err := memorySave(ctx, store, memoryInput{
		Operation: "save",
		Type:      string(memory.MemoryTypeFeedback),
		Content:   "go test 失败时先用 rg 定位失败测试名",
		Tags:      []string{"go", "test", "debug"},
	})
	if err != nil {
		t.Fatalf("memorySave error = %v", err)
	}
	if result.IsError {
		t.Fatalf("memorySave result error: %s", result.DecodeContent())
	}
	if store.saved == nil {
		t.Fatal("memorySave did not call Save")
	}
	gov := memory.DecodeGovernance(store.saved.Metadata)
	if gov.Source != "memory_tool" {
		t.Fatalf("governance source = %q, want memory_tool", gov.Source)
	}
	if gov.Confidence != 0.9 {
		t.Fatalf("governance confidence = %v, want 0.9", gov.Confidence)
	}
	if gov.SourceUserID != "user-1" {
		t.Fatalf("governance source_user_id = %q, want user-1", gov.SourceUserID)
	}
	if kind := memory.DecodeMemoryKind(store.saved.Metadata, store.saved.Type); kind != memory.MemoryKind("feedback") {
		t.Fatalf("memory kind = %q, want feedback", kind)
	}

	var body struct {
		Status     string         `json:"status"`
		ID         int64          `json:"id"`
		Governance map[string]any `json:"governance"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &body); err != nil {
		t.Fatalf("decode result: %v; content=%s", err, result.DecodeContent())
	}
	if body.Status != "saved" || body.ID != 42 {
		t.Fatalf("result = %+v, want saved id 42", body)
	}
	if body.Governance["source"] != "memory_tool" {
		t.Fatalf("result governance = %+v", body.Governance)
	}
}

func TestMemoryToolDescriptionSeparatesKnowledgeBase(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	registerMemory(host, logger, nil)

	def, err := host.GetTool("memory")
	if err != nil {
		t.Fatalf("GetTool(memory): %v", err)
	}
	for _, want := range []string{"不是项目知识库", "kb.doc.meta", "kb.doc.structure", "kb.section.text"} {
		if !strings.Contains(def.Description, want) {
			t.Fatalf("memory description should contain %q, got %q", want, def.Description)
		}
	}
}

type fakeMemoryToolStore struct {
	saved *memory.MemoryRecord
}

func (s *fakeMemoryToolStore) Save(_ context.Context, record *memory.MemoryRecord) (int64, error) {
	copy := *record
	copy.Tags = append([]string(nil), record.Tags...)
	copy.Metadata = append(json.RawMessage(nil), record.Metadata...)
	s.saved = &copy
	return 42, nil
}

func (s *fakeMemoryToolStore) Get(context.Context, int64) (*memory.MemoryRecord, error) {
	return nil, nil
}

func (s *fakeMemoryToolStore) BatchGet(context.Context, []int64) ([]memory.MemoryRecord, error) {
	return nil, nil
}

func (s *fakeMemoryToolStore) Update(context.Context, *memory.MemoryRecord) error {
	return nil
}

func (s *fakeMemoryToolStore) Delete(context.Context, int64) error {
	return nil
}

func (s *fakeMemoryToolStore) Search(context.Context, memory.SearchOptions) (*memory.SearchResult, error) {
	return &memory.SearchResult{}, nil
}

func (s *fakeMemoryToolStore) List(context.Context, memory.SearchOptions) (*memory.SearchResult, error) {
	return &memory.SearchResult{}, nil
}

func (s *fakeMemoryToolStore) Stats(context.Context) (*memory.MemoryStats, error) {
	return &memory.MemoryStats{}, nil
}

func (s *fakeMemoryToolStore) SetEmbedding(memory.EmbeddingProvider, memory.VectorStore) {}

func (s *fakeMemoryToolStore) Close() error {
	return nil
}
