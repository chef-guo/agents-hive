package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/imcore"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/mcphost"
)

// FeishuToolProvider 飞书 API 提供者接口（避免直接依赖 feishu 包）
type FeishuToolProvider interface {
	// 文档
	SearchDocs(ctx context.Context, query string, count int) (json.RawMessage, error)
	GetDocContent(ctx context.Context, documentID string) (string, error)
	CreateDoc(ctx context.Context, title string, folderToken string) (string, string, error)
	AppendDocContent(ctx context.Context, documentID string, content string) error
	// Wiki
	ResolveWikiNode(ctx context.Context, nodeToken string) (json.RawMessage, error)
	GetWikiNode(ctx context.Context, spaceID, nodeToken string) (json.RawMessage, error)
	ListWikiNodes(ctx context.Context, spaceID, parentNodeToken string, count int) (json.RawMessage, error)
	// 通讯录
	SearchContacts(ctx context.Context, query string, pageSize int) (json.RawMessage, error)
	GetUserInfo(ctx context.Context, userID string) (json.RawMessage, error)
	// 日历
	ListCalendarEvents(ctx context.Context, calendarID string, startTime, endTime time.Time) (json.RawMessage, error)
	GetPrimaryCalendarID(ctx context.Context) (string, error)
	// 消息
	SendMessage(ctx context.Context, chatID, content string) error
	UploadImage(ctx context.Context, imageBase64 string) (json.RawMessage, error)
	UploadFile(ctx context.Context, fileBase64, fileName string) (json.RawMessage, error)
	SendImage(ctx context.Context, chatID, imageKey string) error
	SendFile(ctx context.Context, chatID, fileKey string) error
	// 审批
	ListApprovalInstances(ctx context.Context, approvalCode string, startTime, endTime int64, pageSize int) (json.RawMessage, error)
	GetApprovalInstance(ctx context.Context, instanceID string) (json.RawMessage, error)
	CreateApprovalInstance(ctx context.Context, approvalCode, openID, form string) (string, error)
	// 多维表格
	ListBitableRecords(ctx context.Context, appToken, tableID string, pageSize int, filter string) (json.RawMessage, error)
	CreateBitableRecord(ctx context.Context, appToken, tableID string, fields map[string]interface{}) (json.RawMessage, error)
	UpdateBitableRecord(ctx context.Context, appToken, tableID, recordID string, fields map[string]interface{}) error
	ListBitableTables(ctx context.Context, appToken string) (json.RawMessage, error)
	// 任务
	CreateTask(ctx context.Context, summary, dueTimestamp string) (json.RawMessage, error)
	ListTasks(ctx context.Context, pageSize int) (json.RawMessage, error)
	CompleteTask(ctx context.Context, taskID string) error
	// 电子表格
	ReadSheetRange(ctx context.Context, spreadsheetToken, sheetRange string) (json.RawMessage, error)
	WriteSheetRange(ctx context.Context, spreadsheetToken, sheetRange string, values [][]interface{}) error
	// 群管理
	GetChatInfo(ctx context.Context, chatID string) (json.RawMessage, error)
	ListChatMembers(ctx context.Context, chatID string, pageSize int) (json.RawMessage, error)
	// 资源下载
	DownloadMessageResource(ctx context.Context, messageID, fileKey, resourceType string) (json.RawMessage, error)
}

// feishuAPIInput 统一输入参数
type feishuAPIInput struct {
	Action          string `json:"action"`
	Query           string `json:"query,omitempty"`
	DocumentID      string `json:"document_id,omitempty"`
	SpaceID         string `json:"space_id,omitempty"`
	NodeToken       string `json:"node_token,omitempty"`
	ParentNodeToken string `json:"parent_node_token,omitempty"`
	UserID          string `json:"user_id,omitempty"`
	ReceiveID       string `json:"receive_id,omitempty"`
	Email           string `json:"email,omitempty"`
	CalendarID      string `json:"calendar_id,omitempty"`
	StartTime       string `json:"start_time,omitempty"`
	EndTime         string `json:"end_time,omitempty"`
	ChatID          string `json:"chat_id,omitempty"`
	Content         string `json:"content,omitempty"`
	Data            string `json:"data,omitempty"`
	Count           int    `json:"count,omitempty"`
	Title           string `json:"title,omitempty"`
	FileName        string `json:"filename,omitempty"`
	FolderToken     string `json:"folder_token,omitempty"`
	// 审批
	ApprovalCode string `json:"approval_code,omitempty"`
	InstanceID   string `json:"instance_id,omitempty"`
	OpenID       string `json:"open_id,omitempty"`
	Form         string `json:"form,omitempty"`
	// 多维表格
	AppToken string                 `json:"app_token,omitempty"`
	TableID  string                 `json:"table_id,omitempty"`
	RecordID string                 `json:"record_id,omitempty"`
	Filter   string                 `json:"filter,omitempty"`
	Fields   map[string]interface{} `json:"fields,omitempty"`
	// 任务
	TaskID  string `json:"task_id,omitempty"`
	Summary string `json:"summary,omitempty"`
	DueTime string `json:"due_time,omitempty"`
	// 电子表格
	SpreadsheetToken string          `json:"spreadsheet_token,omitempty"`
	SheetRange       string          `json:"range,omitempty"`
	Values           [][]interface{} `json:"values,omitempty"`
	// 资源下载
	MessageID    string `json:"message_id,omitempty"`
	FileKey      string `json:"file_key,omitempty"`
	ImageKey     string `json:"image_key,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
}

type FeishuToolOptions struct {
	EnableBinaryTransfer bool
	AuditSink            toolAuditSink
}

type toolAuditSink interface {
	Write(context.Context, any) error
}

// RegisterFeishuTools 注册单个统一的 feishu_api 工具
// Agent 自主决定调用哪个 action，无需预设多个工具
func RegisterFeishuTools(host *mcphost.Host, logger *zap.Logger, provider FeishuToolProvider, formatter ResultFormatter) {
	RegisterFeishuToolsWithOptions(host, logger, provider, formatter, FeishuToolOptions{EnableBinaryTransfer: true})
}

func RegisterFeishuToolsWithOptions(host *mcphost.Host, logger *zap.Logger, provider FeishuToolProvider, formatter ResultFormatter, options FeishuToolOptions) {
	feishuIMAdapter := imcore.NewFeishuAdapter(provider)
	actions := []string{
		// 文档
		"search_docs", "get_doc_content", "create_doc", "edit_doc", "wiki_get_node", "wiki_list_nodes",
		// 通讯录
		"search_contacts", "get_user_info",
		// 日历
		"get_calendar_events",
		// 消息&群
		"send_message", "get_chat_info", "get_chat_admins", "list_chat_members",
		// 审批
		"list_approvals", "get_approval", "create_approval",
		// 多维表格
		"list_bitable_tables", "list_bitable_records", "create_bitable_record", "update_bitable_record",
		// 任务
		"create_task", "list_tasks", "complete_task",
		// 电子表格
		"read_sheet", "write_sheet",
		// 资源下载
		"download_message_resource",
	}
	if options.EnableBinaryTransfer {
		actions = append(actions, "upload_image", "upload_file", "send_image", "send_file")
	}

	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        actions,
				"description": "要执行的飞书 API 操作",
			},
			"query":             map[string]any{"type": "string", "description": "搜索关键词（search_docs、search_contacts）"},
			"document_id":       map[string]any{"type": "string", "description": "文档 ID（get_doc_content、edit_doc）"},
			"space_id":          map[string]any{"type": "string", "description": "Wiki space ID（wiki_list_nodes 必填；wiki_get_node 可选）"},
			"node_token":        map[string]any{"type": "string", "description": "Wiki 节点 token（wiki_get_node，只有 wiki URL/token 时直接传此字段即可）"},
			"parent_node_token": map[string]any{"type": "string", "description": "父 Wiki 节点 token（wiki_list_nodes，可选）"},
			"user_id":           map[string]any{"type": "string", "description": "用户 ID（get_user_info；send_message 时会自动解析为 open_id）"},
			"open_id":           map[string]any{"type": "string", "description": "飞书用户 open_id（ou_ 开头；send_message/send_image/send_file 单聊优先使用；create_approval 为发起人 open_id）"},
			"receive_id":        map[string]any{"type": "string", "description": "飞书接收 ID（支持 chat_id/open_id/user_id/email，send_message/send_image/send_file）"},
			"email":             map[string]any{"type": "string", "description": "飞书用户邮箱（send_message/send_image/send_file 可用）"},
			"calendar_id":       map[string]any{"type": "string", "description": "日历 ID（get_calendar_events，留空=主日历）"},
			"start_time":        map[string]any{"type": "string", "description": "起始时间 RFC3339（get_calendar_events、list_approvals）"},
			"end_time":          map[string]any{"type": "string", "description": "结束时间 RFC3339（get_calendar_events、list_approvals）"},
			"chat_id":           map[string]any{"type": "string", "description": "会话 chat_id（oc_ 开头；send_message/get_chat_info/get_chat_admins/list_chat_members）"},
			"content":           map[string]any{"type": "string", "description": "内容（send_message、edit_doc、create_doc）"},
			"data":              map[string]any{"type": "string", "description": "Base64 内容（upload_image、upload_file）"},
			"count":             map[string]any{"type": "integer", "description": "结果数量（默认 10/20）"},
			"title":             map[string]any{"type": "string", "description": "标题（create_doc）"},
			"filename":          map[string]any{"type": "string", "description": "文件名（upload_file）"},
			"folder_token":      map[string]any{"type": "string", "description": "文件夹 token（create_doc）"},
			// 审批参数
			"approval_code": map[string]any{"type": "string", "description": "审批定义 code（list_approvals、create_approval）"},
			"instance_id":   map[string]any{"type": "string", "description": "审批实例 ID（get_approval）"},
			"form":          map[string]any{"type": "string", "description": "审批表单 JSON（create_approval）"},
			// 多维表格参数
			"app_token": map[string]any{"type": "string", "description": "多维表格 app_token"},
			"table_id":  map[string]any{"type": "string", "description": "数据表 ID"},
			"record_id": map[string]any{"type": "string", "description": "记录 ID（update_bitable_record）"},
			"filter":    map[string]any{"type": "string", "description": "过滤条件（list_bitable_records）"},
			"fields":    map[string]any{"type": "object", "description": "字段键值对（create/update_bitable_record）"},
			// 任务参数
			"task_id":  map[string]any{"type": "string", "description": "任务 ID（complete_task）"},
			"summary":  map[string]any{"type": "string", "description": "任务摘要（create_task）"},
			"due_time": map[string]any{"type": "string", "description": "截止时间戳（create_task）"},
			// 电子表格参数
			"spreadsheet_token": map[string]any{"type": "string", "description": "电子表格 token"},
			"range":             map[string]any{"type": "string", "description": "范围如 Sheet1!A1:C10（read_sheet、write_sheet）"},
			"values":            map[string]any{"type": "array", "description": "二维数组（write_sheet）", "items": map[string]any{"type": "array", "items": map[string]any{}}},
			// 资源下载参数
			"message_id":    map[string]any{"type": "string", "description": "消息 ID（download_message_resource）"},
			"file_key":      map[string]any{"type": "string", "description": "文件 key（download_message_resource，来自消息解析的 image_key/file_key）"},
			"image_key":     map[string]any{"type": "string", "description": "图片 key（send_image）"},
			"resource_type": map[string]any{"type": "string", "description": "资源类型: image/file（download_message_resource）"},
		},
		"required": []string{"action"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name: "feishu_api",
			Description: `飞书应用 API 工具。当你判断用户的任务需要访问飞书上的资源时，自主决定调用此工具。

【文档】search_docs(query) | get_doc_content(document_id) | create_doc(title,folder_token?,content?) | edit_doc(document_id,content) | wiki_get_node(node_token,space_id?) | wiki_list_nodes(space_id,parent_node_token?,count?)
【通讯录】search_contacts(query) | get_user_info(user_id)
【日历】get_calendar_events(calendar_id?,start_time?,end_time?)
【消息&群】send_message(receive_id|chat_id|open_id|user_id|email,content) | upload_image(content) | upload_file(content,title) | send_image(receive_id|chat_id|open_id|user_id|email,file_key) | send_file(receive_id|chat_id|open_id|user_id|email,file_key) | get_chat_info(chat_id) | get_chat_admins(chat_id) | list_chat_members(chat_id)
【审批】list_approvals(approval_code,start_time?,end_time?) | get_approval(instance_id) | create_approval(approval_code,open_id,form)
【多维表格】list_bitable_tables(app_token) | list_bitable_records(app_token,table_id,filter?) | create_bitable_record(app_token,table_id,fields) | update_bitable_record(app_token,table_id,record_id,fields)
【任务】create_task(summary,due_time?) | list_tasks() | complete_task(task_id)
【电子表格】read_sheet(spreadsheet_token,range) | write_sheet(spreadsheet_token,range,values)
【资源下载】download_message_resource(message_id,file_key,resource_type) — 下载消息中的图片/文件/音视频，返回文件名和大小

可多次调用组合使用。例如先 search_docs 再 get_doc_content，或 list_bitable_tables 再 list_bitable_records。`,
			InputSchema: schema,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params feishuAPIInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("解析参数失败: " + err.Error()), nil
			}
			startedAt := time.Now()
			result, err := dispatchFeishuAction(ctx, logger, provider, feishuIMAdapter, params)
			if options.AuditSink != nil {
				outcome := "ok"
				errMsg := ""
				if err != nil || (result != nil && result.IsError) {
					outcome = "error"
					if err != nil {
						errMsg = err.Error()
					}
				}
				_ = options.AuditSink.Write(ctx, map[string]any{
					"ts":             time.Now().UTC(),
					"tool":           "feishu_api",
					"action":         params.Action,
					"platform":       "feishu",
					"status":         outcome,
					"outcome":        outcome,
					"duration_ms":    time.Since(startedAt).Milliseconds(),
					"actor_type":     "agent",
					"target_kind":    feishuAuditTargetKind(params),
					"target_id_hash": feishuAuditTargetHash(params),
					"target": map[string]any{
						"kind":    feishuAuditTargetKind(params),
						"id_hash": feishuAuditTargetHash(params),
					},
					"content_len": len(params.Content),
					"error":       errMsg,
				})
			}
			if err != nil {
				return result, err
			}

			// 对非错误结果应用格式化
			if formatter != nil && result != nil && !result.IsError {
				result = formatToolResult(ctx, formatter, params.Action, result)
			}
			return result, nil
		},
	)

	logger.Info("飞书统一工具已注册", zap.String("tool", "feishu_api"))
}

// dispatchFeishuAction 按 action 分发到对应 handler。
func dispatchFeishuAction(ctx context.Context, logger *zap.Logger, provider FeishuToolProvider, imAdapter imcore.Adapter, params feishuAPIInput) (*mcphost.ToolResult, error) {
	switch params.Action {
	// 文档
	case "search_docs":
		return handleSearchDocs(ctx, logger, provider, params)
	case "get_doc_content":
		return handleGetDocContent(ctx, logger, provider, params)
	case "create_doc":
		return handleCreateDoc(ctx, logger, provider, params)
	case "edit_doc":
		return handleEditDoc(ctx, logger, provider, params)
	case "wiki_get_node":
		return handleWikiGetNode(ctx, logger, provider, params)
	case "wiki_list_nodes":
		return handleWikiListNodes(ctx, logger, provider, params)
	// 通讯录
	case "search_contacts":
		return handleSearchContacts(ctx, logger, provider, params)
	case "get_user_info":
		return handleGetUserInfo(ctx, logger, provider, params)
	// 日历
	case "get_calendar_events":
		return handleGetCalendarEvents(ctx, logger, provider, params)
	// 消息 & 群
	case "send_message":
		return handleSendMessage(ctx, logger, imAdapter, params)
	case "upload_image":
		return handleUploadImage(ctx, logger, provider, params)
	case "upload_file":
		return handleUploadFile(ctx, logger, provider, params)
	case "send_image":
		return handleSendImage(ctx, logger, provider, imAdapter, params)
	case "send_file":
		return handleSendFile(ctx, logger, provider, imAdapter, params)
	case "get_chat_info":
		return handleGetChatInfo(ctx, logger, provider, params)
	case "get_chat_admins":
		return handleGetChatAdmins(ctx, logger, provider, params)
	case "list_chat_members":
		return handleListChatMembers(ctx, logger, provider, params)
	// 审批
	case "list_approvals":
		return handleListApprovals(ctx, logger, provider, params)
	case "get_approval":
		return handleGetApproval(ctx, logger, provider, params)
	case "create_approval":
		return handleCreateApproval(ctx, logger, provider, params)
	// 多维表格
	case "list_bitable_tables":
		return handleListBitableTables(ctx, logger, provider, params)
	case "list_bitable_records":
		return handleListBitableRecords(ctx, logger, provider, params)
	case "create_bitable_record":
		return handleCreateBitableRecord(ctx, logger, provider, params)
	case "update_bitable_record":
		return handleUpdateBitableRecord(ctx, logger, provider, params)
	// 任务
	case "create_task":
		return handleCreateTask(ctx, logger, provider, params)
	case "list_tasks":
		return handleListTasks(ctx, logger, provider, params)
	case "complete_task":
		return handleCompleteTask(ctx, logger, provider, params)
	// 电子表格
	case "read_sheet":
		return handleReadSheet(ctx, logger, provider, params)
	case "write_sheet":
		return handleWriteSheet(ctx, logger, provider, params)
	// 资源下载
	case "download_message_resource":
		return handleDownloadMessageResource(ctx, logger, provider, params)
	default:
		return errorResult(fmt.Sprintf("不支持的 action: %s", params.Action)), nil
	}
}

func feishuAuditTargetKind(params feishuAPIInput) string {
	switch {
	case strings.TrimSpace(params.ReceiveID) != "", strings.TrimSpace(params.OpenID) != "", strings.TrimSpace(params.UserID) != "", strings.TrimSpace(params.Email) != "":
		return "recipient"
	case strings.TrimSpace(params.ChatID) != "":
		return "conversation"
	case strings.TrimSpace(params.DocumentID) != "":
		return "document"
	case strings.TrimSpace(params.SpaceID) != "":
		return "wiki_space"
	default:
		return "none"
	}
}

func feishuAuditTargetHash(params feishuAPIInput) string {
	for _, raw := range []string{params.ReceiveID, params.OpenID, params.Email, params.UserID, params.ChatID, params.DocumentID, params.SpaceID} {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			return imctx.SafeSenderID(trimmed)
		}
	}
	return ""
}

// formatToolResult 对工具返回的原始 JSON 内容应用格式化。
// Content 是 jsonText 编码的字符串，需要先解码再格式化再编码回去。
func formatToolResult(ctx context.Context, formatter ResultFormatter, action string, result *mcphost.ToolResult) *mcphost.ToolResult {
	// 从 jsonText 编码中提取原始字符串
	var rawStr string
	if err := json.Unmarshal(result.Content, &rawStr); err != nil {
		return result
	}
	formatted, err := formatter.Format(ctx, action, json.RawMessage(rawStr))
	if err != nil {
		return result
	}
	return textResult(formatted)
}

func handleSearchDocs(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.Query == "" {
		return errorResult("search_docs 需要 query 参数"), nil
	}
	count := params.Count
	if count <= 0 {
		count = 10
	}
	result, err := p.SearchDocs(ctx, params.Query, count)
	if err != nil {
		logger.Error("搜索飞书文档失败", zap.String("query", params.Query), zap.Error(err))
		return errorResult(fmt.Sprintf("搜索失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleGetDocContent(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.DocumentID == "" {
		return errorResult("get_doc_content 需要 document_id 参数"), nil
	}
	content, err := p.GetDocContent(ctx, params.DocumentID)
	if err != nil {
		logger.Error("获取飞书文档内容失败", zap.String("document_id", params.DocumentID), zap.Error(err))
		return errorResult(fmt.Sprintf("获取文档内容失败: %v", err)), nil
	}
	return textResult(content), nil
}

func handleSearchContacts(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.Query == "" {
		return errorResult("search_contacts 需要 query 参数"), nil
	}
	count := params.Count
	if count <= 0 {
		count = 10
	}
	result, err := p.SearchContacts(ctx, params.Query, count)
	if err != nil {
		logger.Error("搜索飞书通讯录失败", zap.String("query", params.Query), zap.Error(err))
		return errorResult(fmt.Sprintf("搜索通讯录失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleGetUserInfo(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.UserID == "" {
		return errorResult("get_user_info 需要 user_id 参数"), nil
	}
	result, err := p.GetUserInfo(ctx, params.UserID)
	if err != nil {
		logger.Error("获取飞书用户信息失败", zap.String("user_id_hash", imctx.SafeSenderID(params.UserID)), zap.Error(err))
		return errorResult(fmt.Sprintf("获取用户信息失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleGetCalendarEvents(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	now := time.Now()
	startTime := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	endTime := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location())

	if params.StartTime != "" {
		if t, err := time.Parse(time.RFC3339, params.StartTime); err == nil {
			startTime = t
		}
	}
	if params.EndTime != "" {
		if t, err := time.Parse(time.RFC3339, params.EndTime); err == nil {
			endTime = t
		}
	}

	calendarID := params.CalendarID
	if calendarID == "" {
		var err error
		calendarID, err = p.GetPrimaryCalendarID(ctx)
		if err != nil {
			logger.Error("获取主日历 ID 失败", zap.Error(err))
			return errorResult(fmt.Sprintf("获取主日历失败: %v", err)), nil
		}
	}

	result, err := p.ListCalendarEvents(ctx, calendarID, startTime, endTime)
	if err != nil {
		logger.Error("获取日历事件失败", zap.String("calendar_id", calendarID), zap.Error(err))
		return errorResult(fmt.Sprintf("获取日历事件失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleSendMessage(ctx context.Context, logger *zap.Logger, adapter imcore.Adapter, params feishuAPIInput) (*mcphost.ToolResult, error) {
	target, idType, err := resolveFeishuSendTargetInput(params)
	if err != nil {
		return errorResult("send_message 需要 receive_id/chat_id/open_id/user_id/email 参数: " + err.Error()), nil
	}
	if params.Content == "" {
		return errorResult("send_message 需要 content 参数"), nil
	}
	if adapter == nil {
		return errorResult("feishu provider not configured"), nil
	}
	scope, err := imcore.CallerScopeFromContext(ctx, imcore.PlatformFeishu)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	result, err := adapter.SendMessage(ctx, scope, imcore.SendTarget{
		Platform:       imcore.PlatformFeishu,
		RecipientID:    target,
		ExternalIDType: idType,
		Content:        params.Content,
	})
	if err != nil {
		logger.Error("发送飞书消息失败", zap.String("receive_id_hash", imctx.SafeSenderID(target)), zap.Error(err))
		return errorResult(fmt.Sprintf("发送失败: %v", err)), nil
	}
	return textResult(fmt.Sprintf("消息已发送到 %s", result.TargetID)), nil
}

func handleUploadImage(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	data := params.Data
	if data == "" {
		data = params.Content
	}
	if data == "" {
		return errorResult("upload_image 需要 data 参数"), nil
	}
	result, err := p.UploadImage(ctx, data)
	if err != nil {
		logger.Error("上传飞书图片失败", zap.Error(err))
		return errorResult(fmt.Sprintf("上传图片失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleUploadFile(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	data := params.Data
	if data == "" {
		data = params.Content
	}
	if data == "" {
		return errorResult("upload_file 需要 data 参数"), nil
	}
	fileName := params.FileName
	if fileName == "" {
		fileName = params.Title
	}
	if fileName == "" {
		return errorResult("upload_file 需要 filename 参数"), nil
	}
	result, err := p.UploadFile(ctx, data, fileName)
	if err != nil {
		logger.Error("上传飞书文件失败", zap.String("file_name", fileName), zap.Error(err))
		return errorResult(fmt.Sprintf("上传文件失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleSendImage(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, adapter imcore.Adapter, params feishuAPIInput) (*mcphost.ToolResult, error) {
	target, err := resolveFeishuSendTarget(ctx, adapter, params)
	if err != nil {
		return errorResult("send_image 需要 receive_id/chat_id/open_id/user_id/email 参数: " + err.Error()), nil
	}
	imageKey := params.ImageKey
	if imageKey == "" {
		imageKey = params.FileKey
	}
	if imageKey == "" {
		return errorResult("send_image 需要 image_key 参数"), nil
	}
	if err := p.SendImage(ctx, target, imageKey); err != nil {
		logger.Error("发送飞书图片失败", zap.String("receive_id_hash", imctx.SafeSenderID(target)), zap.Error(err))
		return errorResult(fmt.Sprintf("发送图片失败: %v", err)), nil
	}
	return textResult(fmt.Sprintf("图片已发送到 %s", target)), nil
}

func handleSendFile(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, adapter imcore.Adapter, params feishuAPIInput) (*mcphost.ToolResult, error) {
	target, err := resolveFeishuSendTarget(ctx, adapter, params)
	if err != nil {
		return errorResult("send_file 需要 receive_id/chat_id/open_id/user_id/email 参数: " + err.Error()), nil
	}
	if params.FileKey == "" {
		return errorResult("send_file 需要 file_key 参数"), nil
	}
	if err := p.SendFile(ctx, target, params.FileKey); err != nil {
		logger.Error("发送飞书文件失败", zap.String("receive_id_hash", imctx.SafeSenderID(target)), zap.Error(err))
		return errorResult(fmt.Sprintf("发送文件失败: %v", err)), nil
	}
	return textResult(fmt.Sprintf("文件已发送到 %s", target)), nil
}

func resolveFeishuSendTarget(ctx context.Context, adapter imcore.Adapter, params feishuAPIInput) (string, error) {
	target, idType, err := resolveFeishuSendTargetInput(params)
	if err != nil {
		return "", err
	}
	if adapter == nil {
		return "", fmt.Errorf("feishu provider not configured")
	}
	scope, err := imcore.CallerScopeFromContext(ctx, imcore.PlatformFeishu)
	if err != nil {
		return "", err
	}
	rec, err := adapter.ResolveRecipient(ctx, scope, imcore.RecipientLookup{
		RecipientID:    target,
		ExternalIDType: idType,
	})
	if err != nil {
		return "", err
	}
	return rec.ID, nil
}

func resolveFeishuSendTargetInput(params feishuAPIInput) (target string, externalIDType string, err error) {
	switch {
	case strings.TrimSpace(params.ReceiveID) != "":
		return strings.TrimSpace(params.ReceiveID), "receive_id", nil
	case strings.TrimSpace(params.OpenID) != "":
		return strings.TrimSpace(params.OpenID), "open_id", nil
	case strings.TrimSpace(params.Email) != "":
		return strings.TrimSpace(params.Email), "email", nil
	case strings.TrimSpace(params.UserID) != "":
		return strings.TrimSpace(params.UserID), "user_id", nil
	case strings.TrimSpace(params.ChatID) != "":
		return strings.TrimSpace(params.ChatID), "conversation_id", nil
	default:
		return "", "", fmt.Errorf("缺少接收目标")
	}
}

func handleCreateDoc(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.Title == "" {
		return errorResult("create_doc 需要 title 参数"), nil
	}
	docID, docURL, err := p.CreateDoc(ctx, params.Title, params.FolderToken)
	if err != nil {
		logger.Error("创建飞书文档失败", zap.String("title", params.Title), zap.Error(err))
		return errorResult(fmt.Sprintf("创建文档失败: %v", err)), nil
	}

	// 如果同时提供了 content，自动追加内容
	if params.Content != "" {
		if err := p.AppendDocContent(ctx, docID, params.Content); err != nil {
			logger.Error("追加文档内容失败", zap.String("document_id", docID), zap.Error(err))
			return errorResult(fmt.Sprintf("文档已创建（document_id: %s, url: %s），但追加内容失败: %v", docID, docURL, err)), nil
		}
	}

	return textResult(fmt.Sprintf("文档已创建\ndocument_id: %s\nurl: %s", docID, docURL)), nil
}

func handleEditDoc(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.DocumentID == "" {
		return errorResult("edit_doc 需要 document_id 参数"), nil
	}
	if params.Content == "" {
		return errorResult("edit_doc 需要 content 参数"), nil
	}
	if err := p.AppendDocContent(ctx, params.DocumentID, params.Content); err != nil {
		logger.Error("编辑飞书文档失败", zap.String("document_id", params.DocumentID), zap.Error(err))
		return errorResult(fmt.Sprintf("编辑文档失败: %v", err)), nil
	}
	return textResult(fmt.Sprintf("内容已追加到文档 %s", params.DocumentID)), nil
}

func handleWikiGetNode(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.NodeToken == "" {
		return errorResult("wiki_get_node 需要 node_token 参数"), nil
	}
	var (
		result json.RawMessage
		err    error
	)
	if params.SpaceID == "" {
		result, err = p.ResolveWikiNode(ctx, params.NodeToken)
	} else {
		result, err = p.GetWikiNode(ctx, params.SpaceID, params.NodeToken)
	}
	if err != nil {
		logger.Error("获取 wiki 节点失败", zap.String("space_id", params.SpaceID), zap.String("node_token", params.NodeToken), zap.Error(err))
		return errorResult(fmt.Sprintf("获取 wiki 节点失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleWikiListNodes(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.SpaceID == "" {
		return errorResult("wiki_list_nodes 需要 space_id 参数"), nil
	}
	count := params.Count
	if count <= 0 {
		count = 20
	}
	result, err := p.ListWikiNodes(ctx, params.SpaceID, params.ParentNodeToken, count)
	if err != nil {
		logger.Error("获取 wiki 节点列表失败", zap.String("space_id", params.SpaceID), zap.String("parent_node_token", params.ParentNodeToken), zap.Error(err))
		return errorResult(fmt.Sprintf("获取 wiki 节点列表失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

// --- 群管理 ---

func handleGetChatInfo(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.ChatID == "" {
		return errorResult("get_chat_info 需要 chat_id 参数"), nil
	}
	result, err := p.GetChatInfo(ctx, params.ChatID)
	if err != nil {
		logger.Error("获取群聊信息失败", zap.String("chat_id_hash", imctx.SafeSenderID(params.ChatID)), zap.Error(err))
		return errorResult(fmt.Sprintf("获取群聊信息失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleGetChatAdmins(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.ChatID == "" {
		return errorResult("get_chat_admins 需要 chat_id 参数"), nil
	}
	result, err := p.GetChatInfo(ctx, params.ChatID)
	if err != nil {
		logger.Error("获取群管理员信息失败", zap.String("chat_id_hash", imctx.SafeSenderID(params.ChatID)), zap.Error(err))
		return errorResult(fmt.Sprintf("获取群管理员信息失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleListChatMembers(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.ChatID == "" {
		return errorResult("list_chat_members 需要 chat_id 参数"), nil
	}
	count := params.Count
	if count <= 0 {
		count = 20
	}
	result, err := p.ListChatMembers(ctx, params.ChatID, count)
	if err != nil {
		logger.Error("获取群成员列表失败", zap.String("chat_id_hash", imctx.SafeSenderID(params.ChatID)), zap.Error(err))
		return errorResult(fmt.Sprintf("获取群成员列表失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

// --- 审批 ---

func handleListApprovals(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.ApprovalCode == "" {
		return errorResult("list_approvals 需要 approval_code 参数"), nil
	}
	var startTime, endTime int64
	if params.StartTime != "" {
		if t, err := time.Parse(time.RFC3339, params.StartTime); err == nil {
			startTime = t.UnixMilli()
		}
	}
	if params.EndTime != "" {
		if t, err := time.Parse(time.RFC3339, params.EndTime); err == nil {
			endTime = t.UnixMilli()
		}
	}
	count := params.Count
	if count <= 0 {
		count = 20
	}
	result, err := p.ListApprovalInstances(ctx, params.ApprovalCode, startTime, endTime, count)
	if err != nil {
		logger.Error("查询审批实例失败", zap.String("approval_code", params.ApprovalCode), zap.Error(err))
		return errorResult(fmt.Sprintf("查询审批实例失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleGetApproval(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.InstanceID == "" {
		return errorResult("get_approval 需要 instance_id 参数"), nil
	}
	result, err := p.GetApprovalInstance(ctx, params.InstanceID)
	if err != nil {
		logger.Error("获取审批实例详情失败", zap.String("instance_id", params.InstanceID), zap.Error(err))
		return errorResult(fmt.Sprintf("获取审批详情失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleCreateApproval(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.ApprovalCode == "" {
		return errorResult("create_approval 需要 approval_code 参数"), nil
	}
	if params.OpenID == "" {
		return errorResult("create_approval 需要 open_id 参数"), nil
	}
	if params.Form == "" {
		return errorResult("create_approval 需要 form 参数"), nil
	}
	instanceID, err := p.CreateApprovalInstance(ctx, params.ApprovalCode, params.OpenID, params.Form)
	if err != nil {
		logger.Error("创建审批实例失败", zap.String("approval_code", params.ApprovalCode), zap.Error(err))
		return errorResult(fmt.Sprintf("创建审批失败: %v", err)), nil
	}
	return textResult(fmt.Sprintf("审批实例已创建，instance_id: %s", instanceID)), nil
}

// --- 多维表格 ---

func handleListBitableTables(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.AppToken == "" {
		return errorResult("list_bitable_tables 需要 app_token 参数"), nil
	}
	result, err := p.ListBitableTables(ctx, params.AppToken)
	if err != nil {
		logger.Error("获取多维表格数据表列表失败", zap.String("app_token", params.AppToken), zap.Error(err))
		return errorResult(fmt.Sprintf("获取数据表列表失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleListBitableRecords(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.AppToken == "" {
		return errorResult("list_bitable_records 需要 app_token 参数"), nil
	}
	if params.TableID == "" {
		return errorResult("list_bitable_records 需要 table_id 参数"), nil
	}
	count := params.Count
	if count <= 0 {
		count = 20
	}
	result, err := p.ListBitableRecords(ctx, params.AppToken, params.TableID, count, params.Filter)
	if err != nil {
		logger.Error("获取多维表格记录失败", zap.String("app_token", params.AppToken), zap.String("table_id", params.TableID), zap.Error(err))
		return errorResult(fmt.Sprintf("获取记录失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleCreateBitableRecord(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.AppToken == "" {
		return errorResult("create_bitable_record 需要 app_token 参数"), nil
	}
	if params.TableID == "" {
		return errorResult("create_bitable_record 需要 table_id 参数"), nil
	}
	if len(params.Fields) == 0 {
		return errorResult("create_bitable_record 需要 fields 参数"), nil
	}
	result, err := p.CreateBitableRecord(ctx, params.AppToken, params.TableID, params.Fields)
	if err != nil {
		logger.Error("创建多维表格记录失败", zap.String("app_token", params.AppToken), zap.String("table_id", params.TableID), zap.Error(err))
		return errorResult(fmt.Sprintf("创建记录失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleUpdateBitableRecord(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.AppToken == "" {
		return errorResult("update_bitable_record 需要 app_token 参数"), nil
	}
	if params.TableID == "" {
		return errorResult("update_bitable_record 需要 table_id 参数"), nil
	}
	if params.RecordID == "" {
		return errorResult("update_bitable_record 需要 record_id 参数"), nil
	}
	if len(params.Fields) == 0 {
		return errorResult("update_bitable_record 需要 fields 参数"), nil
	}
	if err := p.UpdateBitableRecord(ctx, params.AppToken, params.TableID, params.RecordID, params.Fields); err != nil {
		logger.Error("更新多维表格记录失败", zap.String("record_id", params.RecordID), zap.Error(err))
		return errorResult(fmt.Sprintf("更新记录失败: %v", err)), nil
	}
	return textResult(fmt.Sprintf("记录 %s 已更新", params.RecordID)), nil
}

// --- 任务 ---

func handleCreateTask(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.Summary == "" {
		return errorResult("create_task 需要 summary 参数"), nil
	}
	result, err := p.CreateTask(ctx, params.Summary, params.DueTime)
	if err != nil {
		logger.Error("创建飞书任务失败", zap.String("summary", params.Summary), zap.Error(err))
		return errorResult(fmt.Sprintf("创建任务失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleListTasks(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	count := params.Count
	if count <= 0 {
		count = 20
	}
	result, err := p.ListTasks(ctx, count)
	if err != nil {
		logger.Error("获取飞书任务列表失败", zap.Error(err))
		return errorResult(fmt.Sprintf("获取任务列表失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleCompleteTask(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.TaskID == "" {
		return errorResult("complete_task 需要 task_id 参数"), nil
	}
	if err := p.CompleteTask(ctx, params.TaskID); err != nil {
		logger.Error("完成飞书任务失败", zap.String("task_id", params.TaskID), zap.Error(err))
		return errorResult(fmt.Sprintf("完成任务失败: %v", err)), nil
	}
	return textResult(fmt.Sprintf("任务 %s 已完成", params.TaskID)), nil
}

// --- 电子表格 ---

func handleReadSheet(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.SpreadsheetToken == "" {
		return errorResult("read_sheet 需要 spreadsheet_token 参数"), nil
	}
	if params.SheetRange == "" {
		return errorResult("read_sheet 需要 range 参数"), nil
	}
	result, err := p.ReadSheetRange(ctx, params.SpreadsheetToken, params.SheetRange)
	if err != nil {
		logger.Error("读取电子表格失败", zap.String("token", params.SpreadsheetToken), zap.Error(err))
		return errorResult(fmt.Sprintf("读取表格失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}

func handleWriteSheet(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.SpreadsheetToken == "" {
		return errorResult("write_sheet 需要 spreadsheet_token 参数"), nil
	}
	if params.SheetRange == "" {
		return errorResult("write_sheet 需要 range 参数"), nil
	}
	if len(params.Values) == 0 {
		return errorResult("write_sheet 需要 values 参数"), nil
	}
	if err := p.WriteSheetRange(ctx, params.SpreadsheetToken, params.SheetRange, params.Values); err != nil {
		logger.Error("写入电子表格失败", zap.String("token", params.SpreadsheetToken), zap.Error(err))
		return errorResult(fmt.Sprintf("写入表格失败: %v", err)), nil
	}
	return textResult(fmt.Sprintf("数据已写入 %s 的 %s", params.SpreadsheetToken, params.SheetRange)), nil
}

// --- 资源下载 ---

func handleDownloadMessageResource(ctx context.Context, logger *zap.Logger, p FeishuToolProvider, params feishuAPIInput) (*mcphost.ToolResult, error) {
	if params.MessageID == "" {
		return errorResult("download_message_resource 需要 message_id 参数"), nil
	}
	if params.FileKey == "" {
		return errorResult("download_message_resource 需要 file_key 参数"), nil
	}
	if params.ResourceType == "" {
		return errorResult("download_message_resource 需要 resource_type 参数"), nil
	}
	result, err := p.DownloadMessageResource(ctx, params.MessageID, params.FileKey, params.ResourceType)
	if err != nil {
		logger.Error("下载消息资源失败",
			zap.String("message_id", params.MessageID),
			zap.String("file_key", params.FileKey),
			zap.Error(err))
		return errorResult(fmt.Sprintf("下载资源失败: %v", err)), nil
	}
	return textResult(string(result)), nil
}
