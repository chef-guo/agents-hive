package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadedCase 保留 fixture 来源路径，便于失败时定位。
type LoadedCase struct {
	Path string
	Case Case
}

// LoadCases 从目录加载所有 JSON fixture。
func LoadCases(dir string) ([]LoadedCase, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]LoadedCase, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var c Case
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, LoadedCase{Path: path, Case: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ValidateCase 校验 fixture schema 和期望 ID 一致性。
func ValidateCase(c Case) error {
	if c.ID == "" {
		return fmt.Errorf("id missing")
	}
	if c.Name == "" {
		return fmt.Errorf("%s: name missing", c.ID)
	}
	if c.Query == "" {
		return fmt.Errorf("%s: query missing", c.ID)
	}
	if c.UserID == "" {
		return fmt.Errorf("%s: user_id missing", c.ID)
	}
	if len(c.Memories) == 0 {
		return fmt.Errorf("%s: memories missing", c.ID)
	}

	seen := map[int64]bool{}
	for _, mem := range c.Memories {
		if mem.ID == 0 {
			return fmt.Errorf("%s: memory id missing", c.ID)
		}
		if seen[mem.ID] {
			return fmt.Errorf("%s: duplicate memory id %d", c.ID, mem.ID)
		}
		seen[mem.ID] = true
		if mem.UserID == "" {
			return fmt.Errorf("%s: memory %d user_id missing", c.ID, mem.ID)
		}
		if mem.Type == "" {
			return fmt.Errorf("%s: memory %d type missing", c.ID, mem.ID)
		}
		if mem.Content == "" {
			return fmt.Errorf("%s: memory %d content missing", c.ID, mem.ID)
		}
		if mem.Score != nil && (*mem.Score < -1 || *mem.Score > 1) {
			return fmt.Errorf("%s: memory %d score out of range", c.ID, mem.ID)
		}
	}
	if c.MinScore != nil && (*c.MinScore < 0 || *c.MinScore > 1) {
		return fmt.Errorf("%s: min_score out of range", c.ID)
	}
	if c.Hybrid != nil {
		for _, result := range c.Hybrid.VectorResults {
			if result.ID <= 0 {
				return fmt.Errorf("%s: hybrid vector result id missing", c.ID)
			}
			if result.Score < -1 || result.Score > 1 {
				return fmt.Errorf("%s: hybrid vector result %d score out of range", c.ID, result.ID)
			}
		}
	}

	expectedIDs := append([]int64{}, c.WantInjectedIDs...)
	expectedIDs = append(expectedIDs, c.WantSkippedIDs...)
	expectedIDs = append(expectedIDs, c.WantOrderIDs...)
	expectedIDs = append(expectedIDs, c.WantBatchGetIDs...)
	for _, want := range c.WantMetadata {
		expectedIDs = append(expectedIDs, want.MemoryID)
	}
	for _, want := range c.ScopeAssertions {
		expectedIDs = append(expectedIDs, want.MemoryID)
	}
	for _, id := range expectedIDs {
		if !seen[id] {
			return fmt.Errorf("%s: expected memory id %d not in fixtures", c.ID, id)
		}
	}
	return nil
}
