package agentquality

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type CandidateStatus string

const (
	CandidateNew               CandidateStatus = "new"
	CandidateReviewing         CandidateStatus = "reviewing"
	CandidateApproved          CandidateStatus = "approved"
	CandidateRejected          CandidateStatus = "rejected"
	CandidatePromoted          CandidateStatus = "promoted"
	CandidatePromotedVerified  CandidateStatus = "promoted_verified"
	CandidatePromotedRegressed CandidateStatus = "promoted_regressed"
)

type Candidate struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Route          string      `json:"route"`
	Input          string      `json:"input"`
	ExpectedTools  []string    `json:"expected_tools,omitempty"`
	AllowedTools   []string    `json:"allowed_tools,omitempty"`
	ExpectedSkills []string    `json:"expected_skills,omitempty"`
	ExpectedAgents []string    `json:"expected_agents,omitempty"`
	Scenario       string      `json:"scenario,omitempty"`
	ExpectedStatus FinalStatus `json:"expected_status"`
	FailureType    FailureType `json:"failure_type,omitempty"`
	Risk           string      `json:"risk,omitempty"`
	Required       bool        `json:"required"`
	Notes          string      `json:"notes,omitempty"`
}

type CandidateRecord struct {
	ID             string                   `json:"id"`
	Status         CandidateStatus          `json:"status"`
	Route          string                   `json:"route"`
	SessionID      string                   `json:"session_id"`
	ReplayRef      string                   `json:"replay_ref"`
	Input          string                   `json:"input"`
	Case           Candidate                `json:"case"`
	FailureType    FailureType              `json:"failure_type"`
	Risk           string                   `json:"risk"`
	Fingerprint    string                   `json:"fingerprint"`
	SourceEvent    Event                    `json:"source_event"`
	ReviewNote     string                   `json:"review_note,omitempty"`
	CreatedBy      string                   `json:"created_by,omitempty"`
	ReviewedBy     string                   `json:"reviewed_by,omitempty"`
	PromotedCaseID string                   `json:"promoted_case_id,omitempty"`
	ClusterID      string                   `json:"cluster_id,omitempty"`
	VerifyResult   string                   `json:"verify_result,omitempty"`
	Suggestions    []OptimizationSuggestion `json:"optimization_suggestions,omitempty"`
	GoldenCase     *Case                    `json:"golden_case,omitempty"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
	ReviewedAt     *time.Time               `json:"reviewed_at,omitempty"`
	LastVerifiedAt *time.Time               `json:"last_verified_at,omitempty"`
}

func CandidateFromFailure(sessionID, input, replayRef string, ev Event) CandidateRecord {
	fingerprint := CandidateFingerprint(input, ev)
	id := strings.TrimPrefix(fingerprint, "sha256:")
	if id == "" {
		id = "manual"
	}
	status := ev.FinalStatus
	if status == "" {
		status = StatusFail
	}
	risk := "safe"
	if ev.FailureType == FailurePermission || status == StatusNeedsUser {
		risk = "dangerous"
	}
	c := Candidate{
		ID:             "candidate_" + id,
		Name:           "失败回归候选 " + id,
		Route:          ev.Route,
		Input:          input,
		ExpectedStatus: status,
		FailureType:    ev.FailureType,
		Risk:           risk,
		Required:       false,
		Notes:          "由失败轨迹生成，必须人工 review 后才能加入 required golden task",
	}
	if len(ev.ToolDecision.Expected) > 0 {
		c.ExpectedTools = append([]string(nil), ev.ToolDecision.Expected...)
	} else if ev.ToolDecision.Actual != "" {
		c.AllowedTools = []string{ev.ToolDecision.Actual}
	}
	now := time.Now()
	rec := CandidateRecord{
		ID:          c.ID,
		Status:      CandidateNew,
		Route:       ev.Route,
		SessionID:   sessionID,
		ReplayRef:   replayRef,
		Input:       input,
		Case:        c,
		FailureType: ev.FailureType,
		Risk:        risk,
		Fingerprint: fingerprint,
		SourceEvent: ev,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	rec.Suggestions = BuildOptimizationSuggestions(rec)
	return rec
}

func CandidateFromReflection(sessionID, input, replayRef string, ev Event) CandidateRecord {
	if ev.Name == "" {
		ev.Name = EventReflection
	}
	rec := CandidateFromFailure(sessionID, input, replayRef, ev)
	rec.FailureType = ev.FailureType
	rec.SourceEvent = ev
	rec.Case.FailureType = ev.FailureType
	rec.Case.Notes = strings.TrimSpace(rec.Case.Notes + "\n由 quality.reflection 事件生成，必须人工 review 后才能进入优化或 golden case。")
	rec.Suggestions = BuildOptimizationSuggestions(rec)
	return rec
}

func CandidateFingerprint(input string, ev Event) string {
	payload := map[string]any{
		"route":        ev.Route,
		"input":        strings.TrimSpace(input),
		"failure_type": ev.FailureType,
		"status":       ev.FinalStatus,
		"tool":         ev.ToolDecision.Actual,
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:8])
}
