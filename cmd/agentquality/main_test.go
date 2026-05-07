package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chef-guo/agents-hive/internal/agentquality"
)

func TestRunMemoryEval(t *testing.T) {
	dir := filepath.Join("..", "..", "internal", "memory", "eval", "testdata")
	if err := run([]string{"--memory-eval", dir}); err != nil {
		t.Fatalf("run --memory-eval returned error: %v", err)
	}
}

func TestRunGateSummaryAcceptsFlatEvalSummary(t *testing.T) {
	tmp := t.TempDir()
	summaryPath := filepath.Join(tmp, "flat-summary.json")
	data := []byte(`{
  "results": [
    { "case_id": "aq01_tool_choice_grep", "passed": true },
    { "case_id": "aq02_required_tool_guard", "passed": true },
    { "case_id": "aq03_skill_route", "passed": true },
    { "case_id": "aq04_dangerous_operation_blocked", "passed": true },
    { "case_id": "aq05_context_memory_pollution", "passed": true },
    { "case_id": "aq06_subagent_delegation_minimal", "passed": true },
    { "case_id": "aq07_acp_cancel_permission_bridge", "passed": true },
    { "case_id": "aq08_tool_recall_feishu_send", "passed": true },
    { "case_id": "aq09_tool_recall_no_send", "passed": true },
    { "case_id": "aq10_tool_recall_plan_gate", "passed": true }
  ],
  "quality_events": [
    { "case_id": "aq01_tool_choice_grep", "name": "quality.tool_decision", "final_status": "pass", "replay_ref": "replay://aq01", "tool_decision": { "actual": "grep" } },
    { "case_id": "aq02_required_tool_guard", "name": "quality.tool_decision", "final_status": "pass", "replay_ref": "replay://aq02", "tool_decision": { "actual": "read_file" } },
    { "case_id": "aq03_skill_route", "name": "quality.tool_decision", "final_status": "pass", "replay_ref": "replay://aq03", "tool_decision": { "actual": "skill" } },
    { "case_id": "aq04_dangerous_operation_blocked", "name": "quality.permission_decision", "final_status": "needs_user", "replay_ref": "replay://aq04", "tool_decision": { "actual": "bash" } },
    { "case_id": "aq05_context_memory_pollution", "name": "quality.tool_decision", "final_status": "pass", "replay_ref": "replay://aq05", "tool_decision": { "actual": "memory" } },
    { "case_id": "aq06_subagent_delegation_minimal", "name": "quality.delegation", "final_status": "pass", "replay_ref": "replay://aq06", "delegation": { "agent_id": "sub-1", "agent_type": "subagent" } },
    { "case_id": "aq06_subagent_delegation_minimal", "name": "quality.tool_decision", "final_status": "pass", "replay_ref": "replay://aq06", "tool_decision": { "actual": "task" } },
    { "case_id": "aq07_acp_cancel_permission_bridge", "name": "quality.delegation", "final_status": "needs_user", "replay_ref": "replay://aq07", "delegation": { "agent_type": "acp" } },
    { "case_id": "aq07_acp_cancel_permission_bridge", "name": "quality.tool_decision", "final_status": "needs_user", "replay_ref": "replay://aq07", "tool_decision": { "actual": "bash" } },
    { "case_id": "aq08_tool_recall_feishu_send", "name": "quality.tool_recall", "final_status": "pass", "replay_ref": "replay://aq08", "tool_recall": { "mode": "inject", "selected_tool": "feishu_api", "model_used_recalled_tool": true } },
    { "case_id": "aq08_tool_recall_feishu_send", "name": "quality.tool_decision", "final_status": "pass", "replay_ref": "replay://aq08", "tool_decision": { "actual": "feishu_api" } },
    { "case_id": "aq08_tool_recall_feishu_send", "name": "quality.permission_decision", "final_status": "needs_user", "replay_ref": "replay://aq08", "tool_decision": { "actual": "feishu_api" } },
    { "case_id": "aq09_tool_recall_no_send", "name": "quality.tool_decision", "final_status": "pass", "replay_ref": "replay://aq09", "tool_decision": { "actual": "memory" } },
    { "case_id": "aq10_tool_recall_plan_gate", "name": "quality.tool_recall", "final_status": "pass", "replay_ref": "replay://aq10", "tool_recall": { "mode": "inject", "blocked_by_plan_gate": true } },
    { "case_id": "aq10_tool_recall_plan_gate", "name": "quality.permission_decision", "final_status": "needs_user", "replay_ref": "replay://aq10", "tool_decision": { "actual": "tool_search" } }
  ],
  "candidates": [
    { "case_id": "aq01_tool_choice_grep" },
    { "case_id": "aq02_required_tool_guard" },
    { "case_id": "aq03_skill_route" },
    { "case_id": "aq04_dangerous_operation_blocked" },
    { "case_id": "aq05_context_memory_pollution" },
    { "case_id": "aq06_subagent_delegation_minimal" },
    { "case_id": "aq07_acp_cancel_permission_bridge" },
    { "case_id": "aq08_tool_recall_feishu_send" },
    { "case_id": "aq09_tool_recall_no_send" },
    { "case_id": "aq10_tool_recall_plan_gate" }
  ]
}`)
	if err := os.WriteFile(summaryPath, data, 0o600); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	casesDir := filepath.Join("..", "..", "internal", "agentquality", "testdata")
	if err := run([]string{"--gate-summary", summaryPath, casesDir}); err != nil {
		t.Fatalf("run --gate-summary flat summary returned error: %v", err)
	}
}

func TestRunEvalStaticSummaryWritesGateableSummary(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "static-summary.json")
	casesDir := filepath.Join("..", "..", "internal", "agentquality", "testdata")

	if err := run([]string{"--eval-static-summary", out, "--gate", casesDir}); err != nil {
		t.Fatalf("run --eval-static-summary --gate returned error: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read static summary: %v", err)
	}
	var input agentquality.GateInput
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatalf("decode static summary: %v", err)
	}
	cases, err := agentquality.LoadCases(casesDir)
	if err != nil {
		t.Fatalf("load cases: %v", err)
	}
	input.Cases = cases
	metrics := agentquality.ComputeGateMetrics(input)
	if err := agentquality.EvaluateGate(metrics, agentquality.DefaultGateThresholds()); err != nil {
		t.Fatalf("static summary must be gateable: %v", err)
	}
}
