package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/errs"
)

type adminQualityPromptSmokeRequest struct {
	Key      string `json:"key"`
	Language string `json:"language"`
	Content  string `json:"content"`
}

const promptSmokeMaxContentRunes = 20000

var promptSmokeKnownKeys = map[string]struct{}{
	"system/main":          {},
	"system/base":          {},
	"system/execution":     {},
	"system/plan_runtime":  {},
	"system/business":      {},
	"system/code_editing":  {},
	"system/safety":        {},
	"system/reply":         {},
	"tools/wenyan":         {},
	"tools/spawn_agent":    {},
	"tools/dynamic_tools":  {},
	"subagents/title":      {},
	"subagents/summary":    {},
	"subagents/compaction": {},
	"subagents/explore":    {},
	"subagents/codereview": {},
}

func (s *Server) handleAdminQualityListCases(w http.ResponseWriter, r *http.Request) {
	cases, required, err := loadValidatedQualityCases()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cases":    cases,
		"total":    len(cases),
		"required": required,
	})
}

func (s *Server) handleAdminQualityPromptSmoke(w http.ResponseWriter, r *http.Request) {
	var req adminQualityPromptSmokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}

	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "content 不能为空", Code: errs.CodeInvalidInput})
		return
	}

	_, required, err := loadValidatedQualityCases()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}

	blockingWarnings := promptSmokeBlockingWarnings(req.Key, content)
	warnings := append(blockingWarnings, promptSmokeWarnings(req.Key, content)...)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            len(blockingWarnings) == 0,
		"checked_cases": required,
		"warnings":      warnings,
	})
}

func loadValidatedQualityCases() ([]agentquality.Case, int, error) {
	loaded, err := agentquality.LoadCases(qualityCasesDir())
	if err != nil {
		return nil, 0, err
	}

	cases := make([]agentquality.Case, 0, len(loaded))
	required := 0
	for _, lc := range loaded {
		if err := agentquality.ValidateCase(lc.Case); err != nil {
			return nil, 0, fmt.Errorf("%s: %w", lc.Path, err)
		}
		if lc.Case.Required {
			required++
		}
		cases = append(cases, lc.Case)
	}
	return cases, required, nil
}

func qualityCasesDir() string {
	_, file, _, ok := runtime.Caller(0)
	if ok {
		return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "agentquality", "testdata"))
	}
	return filepath.Join("internal", "agentquality", "testdata")
}

func promptSmokeWarnings(key, content string) []string {
	lowerContent := strings.ToLower(content)
	warnings := []string{}

	if strings.HasPrefix(key, "system/") && !strings.Contains(content, "工具") && !strings.Contains(lowerContent, "tool") {
		warnings = append(warnings, "system prompt should mention 工具 or tool")
	}
	if key == "system/safety" && !strings.Contains(content, "安全") && !strings.Contains(lowerContent, "permission") {
		warnings = append(warnings, "system/safety prompt should mention 安全 or permission")
	}
	return warnings
}

func promptSmokeBlockingWarnings(key, content string) []string {
	key = strings.TrimSpace(key)
	warnings := []string{}

	if key == "" {
		warnings = append(warnings, "prompt key 不能为空")
	} else if _, ok := promptSmokeKnownKeys[key]; !ok {
		warnings = append(warnings, "未知 prompt key，禁止保存")
	}
	if len([]rune(content)) > promptSmokeMaxContentRunes {
		warnings = append(warnings, "prompt content 超过 20000 字符，禁止保存")
	}
	return warnings
}
