package acpserver

import (
	"context"
	"fmt"

	acp "github.com/coder/acp-go-sdk"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/skills"
)

type acpPermissionRequester interface {
	RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)
}

type acpPermissionRecorder interface {
	RecordACPPermissionDecision(ctx context.Context, sessionID string, req skills.PermissionRequest, decision string, granted bool, remember bool, errText string)
}

// createACPPermissionFn 创建一个桥接 ACP 权限请求的 promptFn
// 当 Master 需要请求工具执行权限时，通过 ACP 协议转发给 IDE 客户端
func createACPPermissionFn(
	conn *acp.AgentSideConnection,
	sessionID string,
	logger *zap.Logger,
	recorders ...acpPermissionRecorder,
) func(ctx context.Context, req skills.PermissionRequest) (skills.PermissionResponse, error) {
	var recorder acpPermissionRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return createACPPermissionFnWithRequester(conn, sessionID, logger, recorder)
}

func createACPPermissionFnWithRequester(
	requester acpPermissionRequester,
	sessionID string,
	logger *zap.Logger,
	recorder acpPermissionRecorder,
) func(ctx context.Context, req skills.PermissionRequest) (skills.PermissionResponse, error) {
	return func(ctx context.Context, req skills.PermissionRequest) (skills.PermissionResponse, error) {
		logger.Debug("ACP 权限请求",
			zap.String("session_id", sessionID),
			zap.String("tool", req.ToolName))

		if requester == nil {
			recordACPPermissionDecision(ctx, recorder, sessionID, req, "error", false, false, "acp requester is nil")
			return skills.PermissionResponse{Granted: false}, nil
		}

		// 构建 ACP 权限请求
		permResp, err := requester.RequestPermission(ctx, acp.RequestPermissionRequest{
			SessionId: acp.SessionId(sessionID),
			ToolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId(fmt.Sprintf("perm_%s", req.ToolName)),
				Title:      acp.Ptr(req.Description),
				Kind:       acp.Ptr(toolKindFromName(req.ToolName)),
				Status:     acp.Ptr(acp.ToolCallStatusPending),
			},
			Options: []acp.PermissionOption{
				{
					Kind:     acp.PermissionOptionKindAllowOnce,
					Name:     "允许此次操作",
					OptionId: acp.PermissionOptionId("allow_once"),
				},
				{
					Kind:     acp.PermissionOptionKindAllowAlways,
					Name:     "本次会话始终允许",
					OptionId: acp.PermissionOptionId("allow_session"),
				},
				{
					Kind:     acp.PermissionOptionKindRejectOnce,
					Name:     "拒绝此次操作",
					OptionId: acp.PermissionOptionId("reject"),
				},
			},
		})
		if err != nil {
			logger.Warn("ACP 权限请求失败，默认拒绝",
				zap.String("session_id", sessionID),
				zap.Error(err))
			recordACPPermissionDecision(ctx, recorder, sessionID, req, "error", false, false, err.Error())
			return skills.PermissionResponse{Granted: false}, nil
		}

		// 处理权限结果
		if permResp.Outcome.Cancelled != nil {
			recordACPPermissionDecision(ctx, recorder, sessionID, req, "cancelled", false, false, "")
			return skills.PermissionResponse{Granted: false}, nil
		}
		if permResp.Outcome.Selected == nil {
			recordACPPermissionDecision(ctx, recorder, sessionID, req, "error", false, false, "permission outcome missing selected option")
			return skills.PermissionResponse{Granted: false}, nil
		}

		switch string(permResp.Outcome.Selected.OptionId) {
		case "allow_once":
			recordACPPermissionDecision(ctx, recorder, sessionID, req, "allow_once", true, false, "")
			return skills.PermissionResponse{Granted: true, Remember: false}, nil
		case "allow_session":
			recordACPPermissionDecision(ctx, recorder, sessionID, req, "allow_session", true, true, "")
			return skills.PermissionResponse{Granted: true, Remember: true}, nil
		default: // reject
			recordACPPermissionDecision(ctx, recorder, sessionID, req, "reject", false, false, "")
			return skills.PermissionResponse{Granted: false}, nil
		}
	}
}

func recordACPPermissionDecision(ctx context.Context, recorder acpPermissionRecorder, sessionID string, req skills.PermissionRequest, decision string, granted bool, remember bool, errText string) {
	if recorder == nil {
		return
	}
	recorder.RecordACPPermissionDecision(ctx, sessionID, req, decision, granted, remember, errText)
}
