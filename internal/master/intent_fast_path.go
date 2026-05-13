package master

import (
	"time"

	"github.com/chef-guo/agents-hive/internal/router"
)

// fastPathTurnIntent 使用本地规则计算本轮 intent，避免普通请求在首 token 前等待 LLM 分类。
// RuleClassifyIntent 只给出保守基线；resolveTurnIntent 负责恢复明确外部发送、skill/tool 管理
// 以及跨回合待发送意图。
func fastPathTurnIntent(session *SessionState, latestQuery string) router.IntentFrame {
	return resolveTurnIntent(session, latestQuery, router.RuleClassifyIntent(latestQuery))
}

func fastPathTurnIntentResult(session *SessionState, latestQuery string) router.IntentClassificationResult {
	start := time.Now()
	intent := fastPathTurnIntent(session, latestQuery)
	intent.Signals = appendSignalForToolVisibility(intent.Signals, "first_token_fast_path")
	return router.IntentClassificationResult{
		Intent:     intent,
		Source:     "rule_fast_path",
		BudgetOK:   true,
		Duration:   time.Since(start),
		LLMAttempt: false,
	}
}
