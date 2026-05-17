package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/asset"
	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/journal"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/toolruntime"
)

// Session API 请求/响应类型

// CreateSessionRequest 创建会话请求
type CreateSessionRequest struct {
	Name string   `json:"name"`
	Tags []string `json:"tags,omitempty"`
}

// CreateSessionResponse 创建会话响应
type CreateSessionResponse struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
}

// SessionListItem 会话列表项
type SessionListItem struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	MessageCount  int       `json:"message_count"`
	LastAccessed  time.Time `json:"last_accessed"`
	SelectedModel string    `json:"selected_model,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	IsActive      bool      `json:"is_active"`
	IsStarred     bool      `json:"is_starred"`
}

// SessionListResponse 会话列表响应
type SessionListResponse struct {
	Sessions []SessionListItem `json:"sessions"`
}

// SessionDetailResponse 会话详情响应
type SessionDetailResponse struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	CreatedAt      string   `json:"created"`
	UpdatedAt      string   `json:"updated"`
	LastAccessedAt string   `json:"last_accessed"`
	SelectedModel  string   `json:"selected_model,omitempty"`
	KBDomainID     string   `json:"kb_domain_id,omitempty"`
	MessageCount   int      `json:"message_count"`
	TotalTokens    int      `json:"total_tokens"`
	Tags           []string `json:"tags,omitempty"`
	IsActive       bool     `json:"is_active"`
}

// UpdateSessionRequest 更新会话请求
type UpdateSessionRequest struct {
	Name       string   `json:"name,omitempty"`
	KBDomainID string   `json:"kb_domain_id,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

// SendMessageRequest 发送消息请求
type SendMessageRequest struct {
	Content         string                  `json:"content"`
	Attachments     []master.FileAttachment `json:"attachments,omitempty"`
	ReasoningEffort string                  `json:"reasoning_effort,omitempty"`
	KBDomainID      string                  `json:"kb_domain_id,omitempty"`
}

// SendMessageResponse 发送消息响应
type SendMessageResponse struct {
	Content   string `json:"content"`
	Completed bool   `json:"completed"`
}

// handleCreateSession 处理 POST /api/v1/sessions
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "无效的请求体",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	if req.Name == "" {
		req.Name = "新会话"
	}

	// 使用 ProcessCommand 避免 ResponseCh 竞态
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resp, err := s.master.ProcessCommand(ctx, master.SessionRequest{
		Command: master.SessionCommandNew,
		Args:    []string{req.Name},
	})
	if err != nil {
		writeJSON(w, http.StatusRequestTimeout, ErrorResponse{
			Error: "请求超时",
			Code:  errs.CodeTimeout,
		})
		return
	}

	if resp.Error != "" {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: resp.Error,
			Code:  errs.CodeInternal,
		})
		return
	}

	sessionID, _ := s.master.GetCurrentSessionInfo()
	writeJSON(w, http.StatusCreated, CreateSessionResponse{
		SessionID: sessionID,
		Name:      req.Name,
	})
}

// handleListSessions 处理 GET /api/v1/sessions
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	// 直接从 Master 获取会话列表
	sessions, err := s.master.ListAllSessions(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: err.Error(),
			Code:  errs.CodeInternal,
		})
		return
	}

	// 获取当前活跃会话 ID
	activeSessionID, _ := s.master.GetCurrentSessionInfo()

	// 转换为 API 响应格式
	items := make([]SessionListItem, 0, len(sessions))
	for _, session := range sessions {
		if isHiddenIMSessionForWeb(session.ID) {
			continue
		}

		// 解析时间
		lastAccessed, _ := time.Parse(time.RFC3339, session.LastAccessedAt)

		item := SessionListItem{
			ID:            session.ID,
			Name:          session.Name,
			MessageCount:  session.MessageCount,
			LastAccessed:  lastAccessed,
			SelectedModel: session.SelectedModel,
			Tags:          session.Tags,
			IsActive:      session.ID == activeSessionID,
			IsStarred:     session.IsStarred,
		}

		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, SessionListResponse{
		Sessions: items,
	})
}

func isHiddenIMSessionForWeb(sessionID string) bool {
	return strings.HasPrefix(sessionID, imctx.SessionIDPrefix+"-")
}

// handleGetSession 处理 GET /api/v1/sessions/:id
func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "需要会话 ID",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	// 从 Master 获取会话详情
	session, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{
			Error: err.Error(),
			Code:  errs.CodeTaskNotFound,
		})
		return
	}
	if !s.checkSessionOwnership(w, r, session) {
		return
	}

	// 获取当前活跃会话 ID
	activeSessionID, _ := s.master.GetCurrentSessionInfo()

	// 返回结构化响应
	writeJSON(w, http.StatusOK, SessionDetailResponse{
		ID:             session.ID,
		Name:           session.Name,
		CreatedAt:      session.CreatedAt,
		UpdatedAt:      session.UpdatedAt,
		LastAccessedAt: session.LastAccessedAt,
		SelectedModel:  session.SelectedModel,
		KBDomainID:     session.KBDomainID,
		MessageCount:   session.MessageCount,
		TotalTokens:    session.TotalTokens,
		Tags:           session.Tags,
		IsActive:       session.ID == activeSessionID || (len(activeSessionID) >= 8 && session.ID[:8] == activeSessionID),
	})
}

// handleUpdateSession 处理 PATCH /api/v1/sessions/:id
func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "需要会话 ID",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	sess, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return
	}
	if !s.checkSessionOwnership(w, r, sess) {
		return
	}

	var req UpdateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "无效的请求体",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	if req.Name != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		resp, err := s.master.ProcessCommand(ctx, master.SessionRequest{
			SessionID: sessionID,
			Command:   master.SessionCommandRename,
			Args:      []string{req.Name},
		})
		if err != nil {
			writeJSON(w, http.StatusRequestTimeout, ErrorResponse{
				Error: "请求超时",
				Code:  errs.CodeTimeout,
			})
			return
		}

		if resp.Error != "" {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{
				Error: resp.Error,
				Code:  errs.CodeInternal,
			})
			return
		}
	}
	if strings.TrimSpace(req.KBDomainID) != "" {
		if err := s.master.SetSessionKBDomain(r.Context(), sessionID, strings.TrimSpace(req.KBDomainID), true); err != nil {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{
				Error: err.Error(),
				Code:  errs.CodeInternal,
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "会话已更新",
	})
}

// handleDeleteSession 处理 DELETE /api/v1/sessions/:id
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "需要会话 ID",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	sess, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return
	}
	if !s.checkSessionOwnership(w, r, sess) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resp, err := s.master.ProcessCommand(ctx, master.SessionRequest{
		Command: master.SessionCommandDelete,
		Args:    []string{sessionID},
	})
	if err != nil {
		writeJSON(w, http.StatusRequestTimeout, ErrorResponse{
			Error: "请求超时",
			Code:  errs.CodeTimeout,
		})
		return
	}

	if resp.Error != "" {
		status := http.StatusInternalServerError
		code := errs.CodeInternal
		if strings.Contains(resp.Error, "not found") {
			status = http.StatusNotFound
			code = errs.CodeTaskNotFound
		} else if strings.Contains(resp.Error, "cannot delete") {
			status = http.StatusConflict
			code = errs.CodeInvalidInput
		}
		writeJSON(w, status, ErrorResponse{
			Error: resp.Error,
			Code:  code,
		})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleSendMessage 处理 POST /api/v1/sessions/:id/messages
func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "需要会话 ID",
			Code:  errs.CodeBadRequest,
		})
		return
	}
	if isHiddenIMSessionForWeb(sessionID) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return
	}

	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "无效的请求体",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	if req.Content == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "需要 content 字段",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	sess, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return
	}
	if !s.checkSessionOwnership(w, r, sess) {
		return
	}

	decodedAttachments := make([][]byte, len(req.Attachments))
	if len(req.Attachments) > 10 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "附件数量不能超过 10 个",
			Code:  errs.CodeBadRequest,
		})
		return
	}
	for i, att := range req.Attachments {
		if att.Filename == "" || att.MimeType == "" {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{
				Error: fmt.Sprintf("附件 #%d: filename 和 mime_type 不能为空", i+1),
				Code:  errs.CodeBadRequest,
			})
			return
		}
		if !isAllowedMimeType(att.MimeType) {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{
				Error: fmt.Sprintf("附件 #%d: 不支持的 MIME 类型 %q", i+1, att.MimeType),
				Code:  errs.CodeBadRequest,
			})
			return
		}
		decoded, err := master.DecodeAttachmentData(att.Data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{
				Error: fmt.Sprintf("附件 #%d: 无效的 base64 数据", i+1),
				Code:  errs.CodeBadRequest,
			})
			return
		}
		req.Attachments[i].Size = int64(len(decoded))
		decodedAttachments[i] = decoded
		if len(decoded) > 25*1024*1024 {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{
				Error: fmt.Sprintf("附件 #%d: 文件大小超过 25MB 限制", i+1),
				Code:  errs.CodeBadRequest,
			})
			return
		}
	}
	if err := s.persistChatAttachments(r.Context(), sessionID, sess, req.Attachments, decodedAttachments); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: "附件持久化失败: " + err.Error(),
			Code:  errs.CodeInternal,
		})
		return
	}
	if s.assetService != nil {
		for i := range req.Attachments {
			req.Attachments[i].Data = ""
		}
	}

	// 验证推理努力级别
	if req.ReasoningEffort != "" {
		switch req.ReasoningEffort {
		case "low", "medium", "high":
			// 合法值
		default:
			writeJSON(w, http.StatusBadRequest, ErrorResponse{
				Error: "reasoning_effort 必须为 \"low\"、\"medium\" 或 \"high\"",
				Code:  errs.CodeBadRequest,
			})
			return
		}
	}

	// 构建可选参数
	var opts []master.MessageOption
	if len(req.Attachments) > 0 {
		opts = append(opts, master.WithAttachments(req.Attachments))
		s.logger.Info("API 收到用户附件",
			zap.String("session_id", sessionID),
			zap.Int("attachment_count", len(req.Attachments)),
		)
	}
	if req.ReasoningEffort != "" {
		opts = append(opts, master.WithReasoningEffort(req.ReasoningEffort))
	}
	if strings.TrimSpace(req.KBDomainID) != "" {
		opts = append(opts, master.WithKBDomainID(strings.TrimSpace(req.KBDomainID)))
	}

	// 使用 ProcessMessageWithOptions 支持附件和推理努力级别
	resp, err := s.master.ProcessMessageWithOptions(r.Context(), sessionID, req.Content, opts...)
	if err != nil {
		writeJSON(w, http.StatusRequestTimeout, ErrorResponse{
			Error: "请求超时: " + err.Error(),
			Code:  errs.CodeTimeout,
		})
		return
	}

	if resp.Error != "" {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: resp.Error,
			Code:  errs.CodeInternal,
		})
		return
	}

	writeJSON(w, http.StatusOK, SendMessageResponse{
		Content:   resp.Content,
		Completed: resp.Completed,
	})
}

func (s *Server) persistChatAttachments(ctx context.Context, sessionID string, sess *store.SessionRecord, attachments []master.FileAttachment, decoded [][]byte) error {
	if s == nil || s.assetService == nil || len(attachments) == 0 {
		return nil
	}
	ownerID := chatAttachmentOwnerID(ctx, sess)
	if ownerID == "" {
		return nil
	}
	namespace := "chat/user/" + ownerID + "/session/" + sessionID
	for i := range attachments {
		if attachments[i].AssetURI != "" || i >= len(decoded) || len(decoded[i]) == 0 {
			continue
		}
		contentHash := attachmentContentHash(decoded[i])
		uri, err := s.assetService.Upload(ctx, decoded[i], asset.UploadOpts{
			Namespace:  namespace,
			Filename:   attachments[i].Filename,
			MimeType:   attachments[i].MimeType,
			OwnerScope: "user",
			OwnerID:    ownerID,
			Tags: map[string]string{
				"source_kind": "chat_attachment",
				"session_id":  sessionID,
				"platform":    "web",
			},
		})
		if err != nil {
			return err
		}
		attachments[i].AssetURI = uri.String()
		attachments[i].ContentHash = contentHash
		attachments[i].Size = int64(len(decoded[i]))
	}
	return nil
}

func chatAttachmentOwnerID(ctx context.Context, sess *store.SessionRecord) string {
	if user := auth.UserFrom(ctx); user != nil && strings.TrimSpace(user.ID) != "" {
		return strings.TrimSpace(user.ID)
	}
	if sess != nil && strings.TrimSpace(sess.UserID) != "" {
		return strings.TrimSpace(sess.UserID)
	}
	return "local"
}

func attachmentContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// handleClearSession 处理 POST /api/v1/sessions/:id/clear
func (s *Server) handleClearSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "需要会话 ID",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	sess, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return
	}
	if !s.checkSessionOwnership(w, r, sess) {
		return
	}

	// 使用 ProcessMessage 发送 clear 命令
	resp, err := s.master.ProcessMessage(r.Context(), sessionID, "clear")
	if err != nil {
		writeJSON(w, http.StatusRequestTimeout, ErrorResponse{
			Error: "请求超时: " + err.Error(),
			Code:  errs.CodeTimeout,
		})
		return
	}

	if resp.Error != "" {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: resp.Error,
			Code:  errs.CodeInternal,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "会话已清空",
	})
}

// MessageResponse 表示单条消息的响应格式
type MessageResponse struct {
	ID               int64                   `json:"id,omitempty"`
	Role             string                  `json:"role"`
	Content          string                  `json:"content"`
	ReasoningContent string                  `json:"reasoning_content,omitempty"` // 推理内容（仅 reasoning 模型有值）
	Metadata         map[string]any          `json:"metadata,omitempty"`
	CreatedAt        string                  `json:"created_at"`
	Timestamp        string                  `json:"timestamp"`
	ToolCalls        []ToolCallInfo          `json:"tool_calls,omitempty"`
	ToolCallID       string                  `json:"tool_call_id,omitempty"`
	IsError          bool                    `json:"is_error,omitempty"`  // 错误标记（tool 消息）
	ToolName         string                  `json:"tool_name,omitempty"` // 工具名称（tool 消息）
	Recoverable      bool                    `json:"recoverable,omitempty"`
	Terminal         bool                    `json:"terminal,omitempty"`
	ErrorKind        string                  `json:"error_kind,omitempty"`
	Citations        []any                   `json:"citations,omitempty"`   // assistant KB citations
	Artifacts        []any                   `json:"artifacts,omitempty"`   // assistant artifact manifest
	Attachments      []master.FileAttachment `json:"attachments,omitempty"` // user attachment manifest
	Usage            *UsageInfo              `json:"usage,omitempty"`       // token 用量（assistant 消息）
}

// UsageInfo token 用量信息
type UsageInfo struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// ToolCallInfo 工具调用信息
type ToolCallInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// MessagesListResponse 表示消息列表响应
type MessagesListResponse struct {
	SessionID string            `json:"session_id"`
	Messages  []MessageResponse `json:"messages"`
	Total     int               `json:"total"`
}

// handleGetMessages 处理 GET /api/v1/sessions/:id/messages
func (s *Server) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "需要会话 ID",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	// 先验证 session 存在且当前用户有权访问
	sess, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return
	}
	if !s.checkSessionOwnership(w, r, sess) {
		return
	}

	// 从查询参数获取 limit（可选）
	limitStr := r.URL.Query().Get("limit")
	limit := 0
	if limitStr != "" {
		fmt.Sscanf(limitStr, "%d", &limit)
	}

	// 从 Master 获取消息
	messages, err := s.master.GetSessionMessages(r.Context(), sessionID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: "获取消息失败: " + err.Error(),
			Code:  errs.CodeInternal,
		})
		return
	}

	// 转换为 API 响应格式
	respMessages := make([]MessageResponse, 0, len(messages))
	for _, msg := range messages {
		var metadata map[string]any
		if len(msg.Metadata) > 0 {
			json.Unmarshal(msg.Metadata, &metadata)
		}

		ts := msg.CreatedAt.Format(time.RFC3339)
		// 优先使用 metadata 中的原始 created_at（消息实际产生时间），
		// DB created_at 在批量保存时几乎相同，会导致前端按时间排序失效
		if metadata != nil {
			if origTS, ok := metadata["created_at"].(string); ok && origTS != "" {
				ts = origTS
			}
		}

		// 从 metadata 中提取 tool_calls、tool_call_id 和 reasoning_content
		var toolCalls []ToolCallInfo
		var toolCallID string
		var reasoningContent string
		var isError bool
		var toolName string
		var recoverable bool
		var terminal bool
		var errorKind string
		var citations []any
		var artifacts []any
		var attachments []master.FileAttachment
		if metadata != nil {
			if tc, ok := metadata["tool_calls"]; ok {
				// tool_calls 可能是 JSON 数组（直接解析）或 JSON 字符串（需要二次解析）
				var tcList []any
				switch v := tc.(type) {
				case []any:
					tcList = v
				case string:
					// 兼容：存储时用了 string(jsonBytes)，需要再解析一次
					json.Unmarshal([]byte(v), &tcList)
				}
				for _, item := range tcList {
					if m, ok := item.(map[string]any); ok {
						// arguments 可能是 string 或 map，需要确保输出为 JSON 字符串
						var argsStr string
						switch v := m["arguments"].(type) {
						case string:
							argsStr = v
						default:
							if b, err := json.Marshal(v); err == nil {
								argsStr = string(b)
							}
						}
						toolCalls = append(toolCalls, ToolCallInfo{
							ID:        fmt.Sprintf("%v", m["id"]),
							Name:      fmt.Sprintf("%v", m["name"]),
							Arguments: argsStr,
						})
					}
				}
			}
			if tcID, ok := metadata["tool_call_id"]; ok {
				toolCallID = fmt.Sprintf("%v", tcID)
			}
			// 提取推理内容字段（reasoning 模型如 o1/o3/DeepSeek-R1 会在此存储思考过程）
			if rc, ok := metadata["reasoning_content"]; ok {
				if rcStr, ok := rc.(string); ok && rcStr != "" {
					reasoningContent = rcStr
				}
			}
			if ie, ok := metadata["is_error"].(bool); ok {
				isError = ie
			}
			if tn, ok := metadata["tool_name"].(string); ok {
				toolName = tn
			}
			if rv, ok := metadata["recoverable"].(bool); ok {
				recoverable = rv
			}
			if tv, ok := metadata["terminal"].(bool); ok {
				terminal = tv
			}
			if ek, ok := metadata["error_kind"].(string); ok {
				errorKind = ek
			}
			if c, ok := metadata["citations"]; ok {
				switch v := c.(type) {
				case []any:
					citations = v
				case string:
					_ = json.Unmarshal([]byte(v), &citations)
				}
			}
			if a, ok := metadata["artifacts"]; ok {
				switch v := a.(type) {
				case []any:
					artifacts = v
				case string:
					_ = json.Unmarshal([]byte(v), &artifacts)
				}
			}
			attachments = master.AttachmentsFromMetadataForAPI(metadata)
		}
		if isError && toolruntime.IsRecoverableToolCallError(msg.Content) {
			recoverable = true
			terminal = false
			if errorKind == "" {
				errorKind = toolruntime.RecoverableToolCallErrorKind(msg.Content)
			}
		}

		// 从 metadata 提取 token 用量（持久化在 metadata JSONB 中）
		var usage *UsageInfo
		if metadata != nil {
			var inTok, outTok int64
			if v, ok := metadata["input_tokens"]; ok {
				fmt.Sscanf(fmt.Sprintf("%v", v), "%d", &inTok)
			}
			if v, ok := metadata["output_tokens"]; ok {
				fmt.Sscanf(fmt.Sprintf("%v", v), "%d", &outTok)
			}
			if inTok > 0 || outTok > 0 {
				usage = &UsageInfo{InputTokens: inTok, OutputTokens: outTok}
			}
		}

		respMessages = append(respMessages, MessageResponse{
			ID:               msg.ID,
			Role:             msg.Role,
			Content:          msg.Content,
			ReasoningContent: reasoningContent,
			Metadata:         metadata,
			CreatedAt:        ts,
			Timestamp:        ts,
			ToolCalls:        toolCalls,
			ToolCallID:       toolCallID,
			IsError:          isError,
			ToolName:         toolName,
			Recoverable:      recoverable,
			Terminal:         terminal,
			ErrorKind:        errorKind,
			Citations:        citations,
			Artifacts:        artifacts,
			Attachments:      attachments,
			Usage:            usage,
		})
	}

	writeJSON(w, http.StatusOK, MessagesListResponse{
		SessionID: sessionID,
		Messages:  respMessages,
		Total:     len(respMessages),
	})
}

// ForkSessionRequest Fork 会话请求
type ForkSessionRequest struct {
	ForkName  string `json:"fork_name,omitempty"`  // Fork 会话名称（可选）
	ForkPoint int    `json:"fork_point,omitempty"` // Fork 起点消息索引（可选，默认为当前所有消息）
}

// ForkSessionResponse Fork 会话响应
type ForkSessionResponse struct {
	ForkID    string `json:"fork_id"`
	ForkName  string `json:"fork_name"`
	ForkPoint int    `json:"fork_point"`
	Message   string `json:"message"`
}

// handleForkSession 处理 POST /api/v1/sessions/:id/fork
func (s *Server) handleForkSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "需要会话 ID",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	sess, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return
	}
	if !s.checkSessionOwnership(w, r, sess) {
		return
	}

	var req ForkSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "无效的请求体",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	// 准备参数
	args := []string{}
	if req.ForkName != "" {
		args = append(args, req.ForkName)
	} else {
		args = append(args, "") // 占位符
	}
	if req.ForkPoint > 0 {
		args = append(args, fmt.Sprintf("%d", req.ForkPoint))
	}

	// 使用 ProcessCommand 发送 Fork 命令
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resp, err := s.master.ProcessCommand(ctx, master.SessionRequest{
		SessionID: sessionID,
		Command:   master.SessionCommandFork,
		Args:      args,
	})
	if err != nil {
		writeJSON(w, http.StatusRequestTimeout, ErrorResponse{
			Error: "请求超时",
			Code:  errs.CodeTimeout,
		})
		return
	}

	if resp.Error != "" {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: resp.Error,
			Code:  errs.CodeInternal,
		})
		return
	}

	// 获取新创建的 fork 会话信息
	currentID, currentName := s.master.GetCurrentSessionInfo()

	writeJSON(w, http.StatusCreated, ForkSessionResponse{
		ForkID:    currentID,
		ForkName:  currentName,
		ForkPoint: req.ForkPoint,
		Message:   resp.Message,
	})
}

// RevertSessionRequest 回滚会话请求
type RevertSessionRequest struct {
	RevertTo int `json:"revert_to"` // 目标消息索引
}

// handleRevertSession 处理 POST /api/v1/sessions/:id/revert
func (s *Server) handleRevertSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "需要会话 ID",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	sess, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return
	}
	if !s.checkSessionOwnership(w, r, sess) {
		return
	}

	var req RevertSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "无效的请求体",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	if req.RevertTo < 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "revert_to 必须大于等于 0",
			Code:  errs.CodeInvalidInput,
		})
		return
	}

	// 使用 ProcessCommand 发送 Revert 命令
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resp, err := s.master.ProcessCommand(ctx, master.SessionRequest{
		SessionID: sessionID,
		Command:   master.SessionCommandRevert,
		Args:      []string{fmt.Sprintf("%d", req.RevertTo)},
	})
	if err != nil {
		writeJSON(w, http.StatusRequestTimeout, ErrorResponse{
			Error: "请求超时",
			Code:  errs.CodeTimeout,
		})
		return
	}

	if resp.Error != "" {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: resp.Error,
			Code:  errs.CodeInternal,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": resp.Message,
	})
}

// handleRegenerateMessage 处理 POST /api/v1/sessions/:id/regenerate
// 找到最后一条用户消息，回滚 session，然后重新生成 AI 回复
func (s *Server) handleRegenerateMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "需要会话 ID",
			Code:  errs.CodeBadRequest,
		})
		return
	}
	s.logger.Info("重新生成请求进入",
		zap.String("session_id", sessionID))

	sess, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		s.logger.Warn("重新生成失败：会话不存在或无权访问",
			zap.String("session_id", sessionID),
			zap.Error(err))
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return
	}
	if !s.checkSessionOwnership(w, r, sess) {
		return
	}

	// 获取 session 消息列表，找到最后一条用户消息
	messages, err := s.master.GetSessionMessages(r.Context(), sessionID, 0)
	if err != nil {
		s.logger.Warn("重新生成失败：获取消息失败",
			zap.String("session_id", sessionID),
			zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: "获取消息失败: " + err.Error(),
			Code:  errs.CodeInternal,
		})
		return
	}

	lastUserIdx := -1
	var lastUserContent string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserIdx = i
			lastUserContent = messages[i].Content
			break
		}
	}

	if lastUserIdx < 0 {
		s.logger.Warn("重新生成失败：未找到用户消息",
			zap.String("session_id", sessionID),
			zap.Int("message_count", len(messages)))
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "未找到用户消息",
			Code:  errs.CodeBadRequest,
		})
		return
	}
	s.logger.Info("重新生成定位到最后一条用户消息",
		zap.String("session_id", sessionID),
		zap.Int("message_count", len(messages)),
		zap.Int("last_user_index", lastUserIdx))

	// 回滚 session 到最后一条用户消息之后（保留用户消息，只删除 AI 回复）
	revertCtx, revertCancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer revertCancel()

	revertResp, err := s.master.ProcessCommand(revertCtx, master.SessionRequest{
		SessionID: sessionID,
		Command:   master.SessionCommandRevert,
		Args:      []string{fmt.Sprintf("%d", lastUserIdx+1)},
	})
	if err != nil {
		s.logger.Warn("重新生成失败：内存会话回滚超时",
			zap.String("session_id", sessionID),
			zap.Error(err))
		writeJSON(w, http.StatusRequestTimeout, ErrorResponse{
			Error: "回滚超时",
			Code:  errs.CodeTimeout,
		})
		return
	}
	if revertResp.Error != "" {
		s.logger.Warn("重新生成失败：内存会话回滚失败",
			zap.String("session_id", sessionID),
			zap.String("error", revertResp.Error))
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: revertResp.Error,
			Code:  errs.CodeInternal,
		})
		return
	}

	// 同步回滚 DB：保留用户消息（revertTo = lastUserIdx+1），只删除其后的 AI 回复
	if err := s.master.RevertSessionDB(r.Context(), sessionID, lastUserIdx+1); err != nil {
		s.logger.Warn("DB revert failed during regenerate, continuing anyway",
			zap.String("session_id", sessionID),
			zap.Error(err))
	}
	s.logger.Info("重新生成回滚完成",
		zap.String("session_id", sessionID),
		zap.Int("revert_to", lastUserIdx+1),
		zap.Int("removed", len(messages)-lastUserIdx-1))

	// 重新处理该用户消息（流式响应通过 WebSocket 发送）
	// WithSkipUserMessage：用户消息已保留在 DB/内存，跳过重复写入，避免 timestamp 不一致
	s.logger.Info("重新生成开始调用 Agent",
		zap.String("session_id", sessionID),
		zap.Int("content_len", len(lastUserContent)))
	resp, err := s.master.ProcessMessageWithOptions(r.Context(), sessionID, lastUserContent, master.WithSkipUserMessage())
	if err != nil {
		s.logger.Warn("重新生成失败：Agent 调用超时或中断",
			zap.String("session_id", sessionID),
			zap.Error(err))
		writeJSON(w, http.StatusRequestTimeout, ErrorResponse{
			Error: "请求超时: " + err.Error(),
			Code:  errs.CodeTimeout,
		})
		return
	}

	if resp.Error != "" {
		s.logger.Warn("重新生成失败：Agent 返回错误",
			zap.String("session_id", sessionID),
			zap.String("error", resp.Error))
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: resp.Error,
			Code:  errs.CodeInternal,
		})
		return
	}
	s.logger.Info("重新生成完成",
		zap.String("session_id", sessionID),
		zap.Bool("completed", resp.Completed),
		zap.Int("content_len", len(resp.Content)))

	writeJSON(w, http.StatusOK, SendMessageResponse{
		Content:   resp.Content,
		Completed: resp.Completed,
	})
}

// allowedMimePrefixes 允许的 MIME 类型前缀白名单
var allowedMimePrefixes = []string{
	"image/",
	"audio/",
	"text/",
}

// allowedMimeTypes 允许的完整 MIME 类型白名单
var allowedMimeTypes = map[string]bool{
	"video/mp4":                true,
	"video/mpeg":               true,
	"application/pdf":          true,
	"application/octet-stream": true,
	"application/msword":       true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
	"application/vnd.ms-excel": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
	"application/vnd.ms-powerpoint":                                             true,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
}

// handleStopSession 处理 POST /api/v1/sessions/:id/stop
// 停止当前正在运行的任务
func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要会话 ID", Code: errs.CodeBadRequest})
		return
	}
	sess, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return
	}
	if !s.checkSessionOwnership(w, r, sess) {
		return
	}
	stopped := s.master.StopSessionTask(sessionID)
	msg := "当前无运行中的任务"
	if stopped {
		msg = "任务已停止"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stopped": stopped,
		"message": msg,
	})
}

// checkSessionOwnership 检查当前用户是否有权访问指定 session。
// 所有用户（包括 admin）只能访问自己的 session，遗留无主 session 也不可见。
// 返回 true 表示允许继续，false 表示已向 w 写入错误响应。
func (s *Server) checkSessionOwnership(w http.ResponseWriter, r *http.Request, session *store.SessionRecord) bool {
	if session == nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return false
	}
	if isHiddenIMSessionForWeb(session.ID) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return false
	}
	if !auth.IsAuthEnabled(r.Context()) {
		return true
	}
	user := auth.UserFrom(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "未授权", Code: errs.CodePermissionDenied})
		return false
	}
	if user.ID != session.UserID {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "无权访问此会话", Code: errs.CodePermissionDenied})
		return false
	}
	return true
}

// isAllowedMimeType 检查 MIME 类型是否在白名单中
func isAllowedMimeType(mimeType string) bool {
	mt := strings.ToLower(mimeType)
	for _, prefix := range allowedMimePrefixes {
		if strings.HasPrefix(mt, prefix) {
			return true
		}
	}
	return allowedMimeTypes[mt]
}

// handleGetSessionJournal 处理 GET /api/v1/sessions/{id}/journal（回放剧场事件流）
func (s *Server) handleGetSessionJournal(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "需要会话 ID",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	// 1. 先检查 session 是否存在
	sess, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{
			Error: err.Error(),
			Code:  errs.CodeTaskNotFound,
		})
		return
	}
	if !s.checkSessionOwnership(w, r, sess) {
		return
	}

	// 2. 解析 limit（默认不限制，最大 2000）
	limit := 0
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
			if limit > 2000 {
				limit = 2000
			}
		}
	}

	// 2b. 解析 after（增量查询：仅返回 timestamp > after 的事件）
	// 支持 RFC3339 格式，如 ?after=2024-01-01T12:00:00Z
	var after time.Time
	if afterStr := r.URL.Query().Get("after"); afterStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, afterStr); err == nil {
			after = t
		} else if t, err := time.Parse(time.RFC3339, afterStr); err == nil {
			after = t
		}
		// 无法解析时忽略，返回全量
	}

	// 3. 调用 journal
	events, err := s.master.GetSessionJournal(r.Context(), sessionID, limit, after)
	if errors.Is(err, journal.ErrJournalNotAvailable) {
		writeJSON(w, http.StatusNotImplemented, ErrorResponse{
			Error: "journal 功能未启用",
			Code:  errs.CodeUnavailable,
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: "查询 journal 失败",
			Code:  errs.CodeInternal,
		})
		return
	}

	// 4. events 为 nil/空 → 200 + 空数组（旧 session，非 404）
	if events == nil {
		events = []journal.JournalEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"events":     events,
	})
}

// handleGetJournalStats 处理 GET /api/v1/journal/stats?session_ids=id1,id2,...（画廊页批量统计）
func (s *Server) handleGetJournalStats(w http.ResponseWriter, r *http.Request) {
	idsParam := r.URL.Query().Get("session_ids")
	if idsParam == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "需要 session_ids 参数",
			Code:  errs.CodeBadRequest,
		})
		return
	}

	ids := strings.Split(idsParam, ",")
	if len(ids) > 200 {
		ids = ids[:200]
	}

	stats, err := s.master.GetJournalStats(r.Context(), ids)
	if errors.Is(err, journal.ErrJournalNotAvailable) {
		writeJSON(w, http.StatusNotImplemented, ErrorResponse{
			Error: "journal 功能未启用",
			Code:  errs.CodeUnavailable,
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: "查询 journal 统计失败",
			Code:  errs.CodeInternal,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"stats": stats,
	})
}
