package master

import (
	"testing"

	"github.com/chef-guo/agents-hive/internal/router"
)

func TestFastPathTurnIntentResultUsesRuleFastPathWithoutLLM(t *testing.T) {
	got := fastPathTurnIntentResult(&SessionState{ID: "s1"}, "给郭松发一下今天的天气信息")
	if got.Source != "rule_fast_path" {
		t.Fatalf("Source = %q, want rule_fast_path; result=%+v", got.Source, got)
	}
	if got.LLMAttempt {
		t.Fatalf("LLMAttempt = true, want false; result=%+v", got)
	}
	if got.Degraded {
		t.Fatalf("Degraded = true, want false; result=%+v", got)
	}
	if !got.BudgetOK {
		t.Fatalf("BudgetOK = false, want true; result=%+v", got)
	}
	if got.Intent.Kind != router.IntentExternalWrite || !got.Intent.AllowsSideEffects || !got.Intent.RequiresExternal {
		t.Fatalf("intent = %+v, want external write with side effects", got.Intent)
	}
	if !hasString(got.Intent.Signals, "first_token_fast_path") {
		t.Fatalf("signals = %#v, want first_token_fast_path", got.Intent.Signals)
	}
}

func TestFastPathTurnIntentRecoverExternalSend(t *testing.T) {
	intent := fastPathTurnIntent(&SessionState{ID: "s1"}, "给郭松发一下今天的天气信息")
	if intent.Kind != router.IntentExternalWrite || !intent.AllowsSideEffects || !intent.RequiresExternal {
		t.Fatalf("intent = %+v, want external write with side effects", intent)
	}
}

func TestFastPathTurnIntentRecoverCreateSkill(t *testing.T) {
	intent := fastPathTurnIntent(&SessionState{ID: "s1"}, "创建一个跟我打招呼的技能")
	if intent.Kind != router.IntentCreateSkill || !intent.AllowsSideEffects {
		t.Fatalf("intent = %+v, want create skill with side effects", intent)
	}
}

func TestFastPathTurnIntentKeepsReadOnlyQuery(t *testing.T) {
	intent := fastPathTurnIntent(&SessionState{ID: "s1"}, "读取配置文件")
	if intent.Kind != router.IntentRead || intent.AllowsSideEffects {
		t.Fatalf("intent = %+v, want read without side effects", intent)
	}
}

func TestFastPathTurnIntentUsesPendingExternalSendContinuation(t *testing.T) {
	session := &SessionState{ID: "s1"}
	session.RememberPendingExternalSendIntent(router.IntentFrame{
		Kind:              router.IntentExternalWrite,
		AllowsSideEffects: true,
		RequiresExternal:  true,
		Subject:           "发给郭松",
	})

	intent := fastPathTurnIntent(session, "现在能不能发")
	if intent.Kind != router.IntentExternalWrite || !intent.AllowsSideEffects || !intent.RequiresExternal {
		t.Fatalf("intent = %+v, want pending external write continuation", intent)
	}
}
