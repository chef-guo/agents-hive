package tools

import "context"

// DelegationObserver 接收工具委派事件，用于质量闭环和回放定位。
type DelegationObserver interface {
	RecordDelegation(ctx context.Context, ev DelegationEvent)
}

// DelegationEvent 描述一次子代理或远程代理委派的结果。
type DelegationEvent struct {
	SessionID     string
	ParentTraceID string
	ChildTraceID  string
	AgentID       string
	AgentType     string
	GroupID       string
	ToolWhitelist []string
	SpawnDepth    int
	MaxTurns      int
	Status        string
	FailureType   string
	StopReason    string
	Error         string
}

// DeriveChildTraceID 稳定派生子 agent trace，避免各委派入口拼接规则漂移。
func DeriveChildTraceID(parentTraceID, agentID string) string {
	if parentTraceID == "" {
		return agentID
	}
	if agentID == "" {
		return parentTraceID + ":unknown-agent"
	}
	return parentTraceID + ":" + agentID
}
