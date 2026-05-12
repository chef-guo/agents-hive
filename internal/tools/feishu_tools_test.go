package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

// mockFeishuProvider 模拟飞书 API 提供者
type mockFeishuProvider struct {
	searchDocsResult      json.RawMessage
	searchDocsErr         error
	getDocContentResult   string
	getDocContentErr      error
	searchContactsResult  json.RawMessage
	searchContactsErr     error
	getUserInfoResult     json.RawMessage
	getUserInfoErr        error
	calendarEventsResult  json.RawMessage
	calendarEventsErr     error
	getChatInfoResult     json.RawMessage
	listChatMembersResult json.RawMessage
	uploadImageResult     json.RawMessage
	uploadFileResult      json.RawMessage
	wikiGetNodeResult     json.RawMessage
	wikiListNodesResult   json.RawMessage
	resolveWikiNodeResult json.RawMessage
	resolveWikiNodeErr    error
	primaryCalendarID     string
	primaryCalendarErr    error
	sendMessageErr        error
	createDocID           string
	createDocURL          string
	createDocErr          error
	appendDocContentErr   error

	lastAction     string
	lastQuery      string
	lastCount      int
	lastDocID      string
	lastUserID     string
	lastCalendarID string
	lastStartTime  time.Time
	lastEndTime    time.Time
	lastChatID     string
	lastContent    string
	lastSheetRange string

	callSequence        []string
	getUserInfoTraceIDs []string
	sendMessageTraceIDs []string
}

type captureFeishuAuditSink struct {
	records []any
}

func (s *captureFeishuAuditSink) Write(_ context.Context, record any) error {
	s.records = append(s.records, record)
	return nil
}

func (m *mockFeishuProvider) SearchDocs(ctx context.Context, query string, count int) (json.RawMessage, error) {
	m.lastAction = "search_docs"
	m.lastQuery = query
	m.lastCount = count
	return m.searchDocsResult, m.searchDocsErr
}
func (m *mockFeishuProvider) GetDocContent(ctx context.Context, documentID string) (string, error) {
	m.lastAction = "get_doc_content"
	m.lastDocID = documentID
	return m.getDocContentResult, m.getDocContentErr
}
func (m *mockFeishuProvider) SearchContacts(ctx context.Context, query string, pageSize int) (json.RawMessage, error) {
	m.lastAction = "search_contacts"
	m.lastQuery = query
	m.lastCount = pageSize
	return m.searchContactsResult, m.searchContactsErr
}
func (m *mockFeishuProvider) GetUserInfo(ctx context.Context, userID string) (json.RawMessage, error) {
	m.lastAction = "get_user_info"
	m.lastUserID = userID
	m.callSequence = append(m.callSequence, "GetUserInfo:"+userID)
	m.getUserInfoTraceIDs = append(m.getUserInfoTraceIDs, toolctx.GetToolContext(ctx).TurnIDOrTraceID())
	return m.getUserInfoResult, m.getUserInfoErr
}
func (m *mockFeishuProvider) ListCalendarEvents(ctx context.Context, calendarID string, startTime, endTime time.Time) (json.RawMessage, error) {
	m.lastAction = "get_calendar_events"
	m.lastCalendarID = calendarID
	m.lastStartTime = startTime
	m.lastEndTime = endTime
	return m.calendarEventsResult, m.calendarEventsErr
}
func (m *mockFeishuProvider) GetPrimaryCalendarID(ctx context.Context) (string, error) {
	return m.primaryCalendarID, m.primaryCalendarErr
}
func (m *mockFeishuProvider) SendMessage(ctx context.Context, chatID, content string) error {
	m.lastAction = "send_message"
	m.lastChatID = chatID
	m.lastContent = content
	m.callSequence = append(m.callSequence, "SendMessage:"+chatID+":"+content)
	m.sendMessageTraceIDs = append(m.sendMessageTraceIDs, toolctx.GetToolContext(ctx).TurnIDOrTraceID())
	return m.sendMessageErr
}
func (m *mockFeishuProvider) CreateDoc(ctx context.Context, title string, folderToken string) (string, string, error) {
	m.lastAction = "create_doc"
	return m.createDocID, m.createDocURL, m.createDocErr
}
func (m *mockFeishuProvider) AppendDocContent(ctx context.Context, documentID string, content string) error {
	m.lastAction = "edit_doc"
	m.lastDocID = documentID
	m.lastContent = content
	return m.appendDocContentErr
}
func (m *mockFeishuProvider) ListApprovalInstances(ctx context.Context, approvalCode string, startTime, endTime int64, pageSize int) (json.RawMessage, error) {
	m.lastAction = "list_approvals"
	return json.RawMessage(`[]`), nil
}
func (m *mockFeishuProvider) GetApprovalInstance(ctx context.Context, instanceID string) (json.RawMessage, error) {
	m.lastAction = "get_approval"
	return json.RawMessage(`{}`), nil
}
func (m *mockFeishuProvider) CreateApprovalInstance(ctx context.Context, approvalCode, openID, form string) (string, error) {
	m.lastAction = "create_approval"
	return "inst_001", nil
}
func (m *mockFeishuProvider) ListBitableRecords(ctx context.Context, appToken, tableID string, pageSize int, filter string) (json.RawMessage, error) {
	m.lastAction = "list_bitable_records"
	return json.RawMessage(`[]`), nil
}
func (m *mockFeishuProvider) CreateBitableRecord(ctx context.Context, appToken, tableID string, fields map[string]interface{}) (json.RawMessage, error) {
	m.lastAction = "create_bitable_record"
	return json.RawMessage(`{"record_id":"rec_001"}`), nil
}
func (m *mockFeishuProvider) UpdateBitableRecord(ctx context.Context, appToken, tableID, recordID string, fields map[string]interface{}) error {
	m.lastAction = "update_bitable_record"
	return nil
}
func (m *mockFeishuProvider) ListBitableTables(ctx context.Context, appToken string) (json.RawMessage, error) {
	m.lastAction = "list_bitable_tables"
	return json.RawMessage(`[]`), nil
}
func (m *mockFeishuProvider) CreateTask(ctx context.Context, summary, dueTimestamp string) (json.RawMessage, error) {
	m.lastAction = "create_task"
	return json.RawMessage(`{"task_id":"task_001"}`), nil
}
func (m *mockFeishuProvider) ListTasks(ctx context.Context, pageSize int) (json.RawMessage, error) {
	m.lastAction = "list_tasks"
	return json.RawMessage(`[]`), nil
}
func (m *mockFeishuProvider) CompleteTask(ctx context.Context, taskID string) error {
	m.lastAction = "complete_task"
	return nil
}
func (m *mockFeishuProvider) ReadSheetRange(ctx context.Context, spreadsheetToken, sheetRange string) (json.RawMessage, error) {
	m.lastAction = "read_sheet"
	m.lastDocID = spreadsheetToken
	m.lastSheetRange = sheetRange
	return json.RawMessage(`[["A","B"],["1","2"]]`), nil
}
func (m *mockFeishuProvider) WriteSheetRange(ctx context.Context, spreadsheetToken, sheetRange string, values [][]interface{}) error {
	m.lastAction = "write_sheet"
	return nil
}
func (m *mockFeishuProvider) GetChatInfo(ctx context.Context, chatID string) (json.RawMessage, error) {
	m.lastAction = "get_chat_info"
	if len(m.getChatInfoResult) > 0 {
		return m.getChatInfoResult, nil
	}
	return json.RawMessage(`{"name":"测试群"}`), nil
}
func (m *mockFeishuProvider) ListChatMembers(ctx context.Context, chatID string, pageSize int) (json.RawMessage, error) {
	m.lastAction = "list_chat_members"
	m.lastCount = pageSize
	if len(m.listChatMembersResult) > 0 {
		return m.listChatMembersResult, nil
	}
	return json.RawMessage(`[]`), nil
}
func (m *mockFeishuProvider) DownloadMessageResource(ctx context.Context, messageID, fileKey, resourceType string) (json.RawMessage, error) {
	m.lastAction = "download_message_resource"
	return json.RawMessage(`{"file_name":"test.png","size":1024}`), nil
}
func (m *mockFeishuProvider) GetWikiNode(ctx context.Context, spaceID, nodeToken string) (json.RawMessage, error) {
	m.lastAction = "wiki_get_node"
	m.lastCalendarID = spaceID
	m.lastDocID = nodeToken
	if len(m.wikiGetNodeResult) > 0 {
		return m.wikiGetNodeResult, nil
	}
	return json.RawMessage(`{"node":{"space_id":"space_1","node_token":"node_1","title":"默认节点","obj_type":"docx","obj_token":"doc_1"}}`), nil
}
func (m *mockFeishuProvider) ResolveWikiNode(ctx context.Context, nodeToken string) (json.RawMessage, error) {
	m.lastAction = "resolve_wiki_node"
	m.lastDocID = nodeToken
	if len(m.resolveWikiNodeResult) > 0 || m.resolveWikiNodeErr != nil {
		return m.resolveWikiNodeResult, m.resolveWikiNodeErr
	}
	return json.RawMessage(`{"node":{"space_id":"space_1","node_token":"node_1","title":"默认节点","obj_type":"docx","obj_token":"doc_1"}}`), nil
}
func (m *mockFeishuProvider) ListWikiNodes(ctx context.Context, spaceID, parentNodeToken string, count int) (json.RawMessage, error) {
	m.lastAction = "wiki_list_nodes"
	m.lastCalendarID = spaceID
	m.lastDocID = parentNodeToken
	m.lastCount = count
	if len(m.wikiListNodesResult) > 0 {
		return m.wikiListNodesResult, nil
	}
	return json.RawMessage(`{"items":[{"space_id":"space_1","node_token":"node_1","title":"默认节点","obj_type":"docx","obj_token":"doc_1"}],"has_more":false}`), nil
}
func (m *mockFeishuProvider) UploadImage(ctx context.Context, imageBase64 string) (json.RawMessage, error) {
	m.lastAction = "upload_image"
	m.lastContent = imageBase64
	if len(m.uploadImageResult) > 0 {
		return m.uploadImageResult, nil
	}
	return json.RawMessage(`{"image_key":"img_v3_001"}`), nil
}
func (m *mockFeishuProvider) UploadFile(ctx context.Context, fileBase64, fileName string) (json.RawMessage, error) {
	m.lastAction = "upload_file"
	m.lastContent = fileBase64
	m.lastDocID = fileName
	if len(m.uploadFileResult) > 0 {
		return m.uploadFileResult, nil
	}
	return json.RawMessage(`{"file_key":"file_v3_001","file_name":"report.pdf"}`), nil
}
func (m *mockFeishuProvider) SendImage(ctx context.Context, chatID, imageKey string) error {
	m.lastAction = "send_image"
	m.lastChatID = chatID
	m.lastContent = imageKey
	return nil
}
func (m *mockFeishuProvider) SendFile(ctx context.Context, chatID, fileKey string) error {
	m.lastAction = "send_file"
	m.lastChatID = chatID
	m.lastContent = fileKey
	return nil
}

func callFeishuAPI(t *testing.T, provider *mockFeishuProvider, params map[string]any) *mcphost.ToolResult {
	t.Helper()
	return callFeishuAPIWithContext(t, context.Background(), provider, params)
}

func callFeishuAPIWithContext(t *testing.T, ctx context.Context, provider *mockFeishuProvider, params map[string]any) *mcphost.ToolResult {
	t.Helper()
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	RegisterFeishuTools(host, logger, provider, NewHumanReadableFormatter())

	input, _ := json.Marshal(params)
	result, err := host.ExecuteTool(ctx, "feishu_api", input)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	return result
}

func executeFeishuAPITestTool(t *testing.T, host *mcphost.Host, ctx context.Context, params map[string]any) *mcphost.ToolResult {
	t.Helper()
	input, _ := json.Marshal(params)
	result, err := host.ExecuteTool(ctx, "feishu_api", input)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	return result
}

func assertStringSliceEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("调用序列长度不匹配: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("调用序列不匹配: got=%v want=%v", got, want)
		}
	}
}

// --- 工具注册 ---

func TestFeishuAPIToolRegistered(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	RegisterFeishuTools(host, logger, &mockFeishuProvider{}, NewHumanReadableFormatter())

	tools := host.ListTools()
	found := false
	for _, tool := range tools {
		if tool.Name == "feishu_api" {
			found = true
			break
		}
	}
	if !found {
		t.Error("feishu_api 工具未注册")
	}
}

func TestFeishuAPIToolRegistered_DisablesBinaryActionsWhenConfigured(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	RegisterFeishuToolsWithOptions(host, logger, &mockFeishuProvider{}, NewHumanReadableFormatter(), FeishuToolOptions{
		EnableBinaryTransfer: false,
	})

	tools := host.ListTools()
	for _, tool := range tools {
		if tool.Name != "feishu_api" {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("unmarshal schema failed: %v", err)
		}
		props := schema["properties"].(map[string]any)
		action := props["action"].(map[string]any)
		enumValues := action["enum"].([]any)
		for _, value := range enumValues {
			name := value.(string)
			if name == "upload_image" || name == "upload_file" || name == "send_image" || name == "send_file" {
				t.Fatalf("binary action %q should be hidden when disabled", name)
			}
		}
		return
	}
	t.Fatal("feishu_api 工具未注册")
}

// --- search_docs ---

func TestFeishuAPISearchDocs(t *testing.T) {
	p := &mockFeishuProvider{searchDocsResult: json.RawMessage(`[{"title":"设计文档"}]`)}
	result := callFeishuAPI(t, p, map[string]any{"action": "search_docs", "query": "设计", "count": 5})
	if result.IsError {
		t.Error("预期成功")
	}
	if p.lastQuery != "设计" || p.lastCount != 5 {
		t.Errorf("参数不匹配: query=%s count=%d", p.lastQuery, p.lastCount)
	}
}

func TestFeishuAPISearchDocsDefaultCount(t *testing.T) {
	p := &mockFeishuProvider{searchDocsResult: json.RawMessage(`[]`)}
	callFeishuAPI(t, p, map[string]any{"action": "search_docs", "query": "test"})
	if p.lastCount != 10 {
		t.Errorf("默认 count 应为 10，实际: %d", p.lastCount)
	}
}

func TestFeishuAPISearchDocsEmptyQuery(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{"action": "search_docs", "query": ""})
	if !result.IsError {
		t.Error("空 query 应返回错误")
	}
}

func TestFeishuAPISearchDocsError(t *testing.T) {
	p := &mockFeishuProvider{searchDocsErr: errors.New("timeout")}
	result := callFeishuAPI(t, p, map[string]any{"action": "search_docs", "query": "test"})
	if !result.IsError {
		t.Error("API 错误应返回 IsError")
	}
}

// --- get_doc_content ---

func TestFeishuAPIGetDocContent(t *testing.T) {
	p := &mockFeishuProvider{getDocContentResult: "文档内容"}
	result := callFeishuAPI(t, p, map[string]any{"action": "get_doc_content", "document_id": "doc123"})
	if result.IsError {
		t.Error("预期成功")
	}
	if p.lastDocID != "doc123" {
		t.Errorf("document_id 不匹配: %s", p.lastDocID)
	}
}

func TestFeishuAPIGetDocContentEmptyID(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{"action": "get_doc_content"})
	if !result.IsError {
		t.Error("空 document_id 应返回错误")
	}
}

func TestFeishuAPIGetChatAdmins(t *testing.T) {
	p := &mockFeishuProvider{
		getChatInfoResult: json.RawMessage(`{
			"name":"项目群",
			"owner_id":"ou_owner",
			"user_manager_id_list":["ou_admin_1","ou_admin_2","ou_owner"],
			"bot_manager_id_list":["ou_bot_1"]
		}`),
	}

	result := callFeishuAPI(t, p, map[string]any{"action": "get_chat_admins", "chat_id": "oc_123"})
	if result.IsError {
		t.Fatalf("预期成功, got error: %s", result.DecodeContent())
	}
	if p.lastAction != "get_chat_info" {
		t.Fatalf("应复用 get_chat_info, actual action=%s", p.lastAction)
	}
	assertContains(t, result.DecodeContent(), "群管理员信息")
	assertContains(t, result.DecodeContent(), "群主: ou_owner")
	assertContains(t, result.DecodeContent(), "用户管理员（2）")
	assertContains(t, result.DecodeContent(), "ou_admin_1")
	assertContains(t, result.DecodeContent(), "机器人管理员（1）")
	assertContains(t, result.DecodeContent(), "管理员总数: 4")
}

func TestFeishuAPIGetChatAdminsEmptyChatID(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{"action": "get_chat_admins"})
	if !result.IsError {
		t.Fatal("空 chat_id 应返回错误")
	}
}

func TestFeishuAPIWikiGetNode(t *testing.T) {
	p := &mockFeishuProvider{
		wikiGetNodeResult: json.RawMessage(`{
			"node":{
				"space_id":"space_alpha",
				"node_token":"node_root",
				"title":"产品文档",
				"obj_type":"docx",
				"obj_token":"doc_123",
				"has_child":true
			}
		}`),
	}

	result := callFeishuAPI(t, p, map[string]any{
		"action":     "wiki_get_node",
		"space_id":   "space_alpha",
		"node_token": "node_root",
	})
	if result.IsError {
		t.Fatalf("预期成功, got error: %s", result.DecodeContent())
	}
	if p.lastAction != "wiki_get_node" {
		t.Fatalf("action 不匹配: %s", p.lastAction)
	}
	if p.lastCalendarID != "space_alpha" || p.lastDocID != "node_root" {
		t.Fatalf("参数未透传: space=%s node=%s", p.lastCalendarID, p.lastDocID)
	}
	assertContains(t, result.DecodeContent(), "Wiki 节点")
	assertContains(t, result.DecodeContent(), "标题: 产品文档")
	assertContains(t, result.DecodeContent(), "obj_type: docx")
}

func TestFeishuAPIWikiGetNodeWithOnlyNodeToken(t *testing.T) {
	p := &mockFeishuProvider{
		resolveWikiNodeResult: json.RawMessage(`{
			"node":{
				"space_id":"space_alpha",
				"node_token":"node_root",
				"title":"产品文档",
				"obj_type":"sheet",
				"obj_token":"sheet_123",
				"has_child":false
			}
		}`),
	}

	result := callFeishuAPI(t, p, map[string]any{
		"action":     "wiki_get_node",
		"node_token": "node_root",
	})
	if result.IsError {
		t.Fatalf("只有 node_token 时应能反查 wiki 节点, got error: %s", result.DecodeContent())
	}
	if p.lastAction != "resolve_wiki_node" || p.lastDocID != "node_root" {
		t.Fatalf("未走 token-only wiki 反查: action=%s node=%s", p.lastAction, p.lastDocID)
	}
	assertContains(t, result.DecodeContent(), "标题: 产品文档")
	assertContains(t, result.DecodeContent(), "obj_type: sheet")
	assertContains(t, result.DecodeContent(), "obj_token: sheet_123")
}

func TestFeishuAPIWikiGetNodeMissingNodeToken(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{
		"action":   "wiki_get_node",
		"space_id": "space_alpha",
	})
	if !result.IsError {
		t.Fatal("缺少 node_token 应返回错误")
	}
}

func TestFeishuAPIWikiListNodes(t *testing.T) {
	p := &mockFeishuProvider{
		wikiListNodesResult: json.RawMessage(`{
			"items":[
				{"space_id":"space_alpha","node_token":"node_a","title":"目录A","obj_type":"docx","obj_token":"doc_a","has_child":true},
				{"space_id":"space_alpha","node_token":"node_b","title":"目录B","obj_type":"sheet","obj_token":"sheet_b","has_child":false}
			],
			"has_more":true
		}`),
	}

	result := callFeishuAPI(t, p, map[string]any{
		"action":            "wiki_list_nodes",
		"space_id":          "space_alpha",
		"parent_node_token": "node_root",
		"count":             5,
	})
	if result.IsError {
		t.Fatalf("预期成功, got error: %s", result.DecodeContent())
	}
	if p.lastAction != "wiki_list_nodes" {
		t.Fatalf("action 不匹配: %s", p.lastAction)
	}
	if p.lastCalendarID != "space_alpha" || p.lastDocID != "node_root" || p.lastCount != 5 {
		t.Fatalf("参数未透传: space=%s parent=%s count=%d", p.lastCalendarID, p.lastDocID, p.lastCount)
	}
	assertContains(t, result.DecodeContent(), "Wiki 节点列表（2 个）")
	assertContains(t, result.DecodeContent(), "1. 目录A")
	assertContains(t, result.DecodeContent(), "2. 目录B")
	assertContains(t, result.DecodeContent(), "还有更多")
}

func TestFeishuAPIWikiListNodesDefaultCount(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{
		"action":   "wiki_list_nodes",
		"space_id": "space_alpha",
	})
	if result.IsError {
		t.Fatalf("预期成功, got error: %s", result.DecodeContent())
	}
	if p.lastCount != 20 {
		t.Fatalf("默认 count 应为 20，实际: %d", p.lastCount)
	}
}

func TestFeishuAPIWikiListNodesMissingSpaceID(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{"action": "wiki_list_nodes"})
	if !result.IsError {
		t.Fatal("缺少 space_id 应返回错误")
	}
}

// --- search_contacts ---

func TestFeishuAPISearchContacts(t *testing.T) {
	p := &mockFeishuProvider{searchContactsResult: json.RawMessage(`[{"name":"张三"}]`)}
	result := callFeishuAPI(t, p, map[string]any{"action": "search_contacts", "query": "张三"})
	if result.IsError {
		t.Error("预期成功")
	}
	if p.lastQuery != "张三" {
		t.Errorf("query 不匹配: %s", p.lastQuery)
	}
}

func TestFeishuAPISearchContactsEmptyQuery(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{"action": "search_contacts"})
	if !result.IsError {
		t.Error("空 query 应返回错误")
	}
}

// --- get_user_info ---

func TestFeishuAPIGetUserInfo(t *testing.T) {
	p := &mockFeishuProvider{getUserInfoResult: json.RawMessage(`{"name":"张三"}`)}
	result := callFeishuAPI(t, p, map[string]any{"action": "get_user_info", "user_id": "u123"})
	if result.IsError {
		t.Error("预期成功")
	}
	if p.lastUserID != "u123" {
		t.Errorf("user_id 不匹配: %s", p.lastUserID)
	}
}

func TestFeishuAPIGetUserInfoEmptyID(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{"action": "get_user_info"})
	if !result.IsError {
		t.Error("空 user_id 应返回错误")
	}
}

// --- get_calendar_events ---

func TestFeishuAPICalendarEventsAutoCalendar(t *testing.T) {
	p := &mockFeishuProvider{
		primaryCalendarID:    "cal_primary",
		calendarEventsResult: json.RawMessage(`[{"summary":"周会"}]`),
	}
	result := callFeishuAPI(t, p, map[string]any{"action": "get_calendar_events"})
	if result.IsError {
		var msg string
		json.Unmarshal(result.Content, &msg)
		t.Errorf("预期成功: %s", msg)
	}
	if p.lastCalendarID != "cal_primary" {
		t.Errorf("应使用主日历，实际: %s", p.lastCalendarID)
	}
}

func TestFeishuAPICalendarEventsWithID(t *testing.T) {
	p := &mockFeishuProvider{calendarEventsResult: json.RawMessage(`[]`)}
	callFeishuAPI(t, p, map[string]any{"action": "get_calendar_events", "calendar_id": "cal_custom"})
	if p.lastCalendarID != "cal_custom" {
		t.Errorf("应使用指定日历，实际: %s", p.lastCalendarID)
	}
}

func TestFeishuAPICalendarEventsWithTimeRange(t *testing.T) {
	p := &mockFeishuProvider{
		primaryCalendarID:    "cal_primary",
		calendarEventsResult: json.RawMessage(`[]`),
	}
	start := "2024-01-15T00:00:00+08:00"
	end := "2024-01-15T23:59:59+08:00"
	callFeishuAPI(t, p, map[string]any{
		"action": "get_calendar_events", "start_time": start, "end_time": end,
	})
	expectedStart, _ := time.Parse(time.RFC3339, start)
	expectedEnd, _ := time.Parse(time.RFC3339, end)
	if !p.lastStartTime.Equal(expectedStart) {
		t.Errorf("start_time 不匹配")
	}
	if !p.lastEndTime.Equal(expectedEnd) {
		t.Errorf("end_time 不匹配")
	}
}

func TestFeishuAPICalendarPrimaryError(t *testing.T) {
	p := &mockFeishuProvider{primaryCalendarErr: errors.New("no calendar")}
	result := callFeishuAPI(t, p, map[string]any{"action": "get_calendar_events"})
	if !result.IsError {
		t.Error("主日历获取失败应返回错误")
	}
}

// --- send_message ---

func TestFeishuAPISendMessage(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{
		"action": "send_message", "chat_id": "chat123", "content": "hello",
	})
	if result.IsError {
		t.Error("预期成功")
	}
	if p.lastChatID != "chat123" || p.lastContent != "hello" {
		t.Errorf("参数不匹配: chat=%s content=%s", p.lastChatID, p.lastContent)
	}
}

func TestFeishuAPISendMessageWithOpenID(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{
		"action": "send_message", "open_id": "ou_123", "content": "hello",
	})
	if result.IsError {
		t.Fatalf("预期成功: %s", result.DecodeContent())
	}
	if p.lastChatID != "ou_123" || p.lastContent != "hello" {
		t.Errorf("open_id 未作为接收目标透传: target=%s content=%s", p.lastChatID, p.lastContent)
	}
}

func TestFeishuAPISendMessageResolvesUserIDToOpenID(t *testing.T) {
	p := &mockFeishuProvider{getUserInfoResult: json.RawMessage(`{"user_id":"afde2a69","open_id":"ou_e48"}`)}
	result := callFeishuAPI(t, p, map[string]any{
		"action": "send_message", "user_id": "afde2a69", "content": "hello",
	})
	if result.IsError {
		t.Fatalf("预期成功: %s", result.DecodeContent())
	}
	if p.lastChatID != "ou_e48" {
		t.Fatalf("user_id 应自动解析为 open_id，实际 target=%s", p.lastChatID)
	}
}

func TestFeishuAPISendMessageResolvesLegacyChatIDUserID(t *testing.T) {
	p := &mockFeishuProvider{getUserInfoResult: json.RawMessage(`{"user_id":"afde2a69","open_id":"ou_e48"}`)}
	result := callFeishuAPI(t, p, map[string]any{
		"action": "send_message", "chat_id": "afde2a69", "content": "hello",
	})
	if result.IsError {
		t.Fatalf("预期成功: %s", result.DecodeContent())
	}
	if p.lastChatID != "ou_e48" {
		t.Fatalf("legacy chat_id=user_id 应自动解析为 open_id，实际 target=%s", p.lastChatID)
	}
}

func TestFeishuAPISendMessageRegisterWithOptionsReusesAdapterProviderPath(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	p := &mockFeishuProvider{
		getUserInfoResult: json.RawMessage(`{"user_id":"afde2a69","open_id":"ou_e48"}`),
	}
	RegisterFeishuToolsWithOptions(host, logger, p, NewHumanReadableFormatter(), FeishuToolOptions{
		EnableBinaryTransfer: true,
	})

	for _, content := range []string{"first", "second"} {
		result := executeFeishuAPITestTool(t, host, context.Background(), map[string]any{
			"action": "send_message", "user_id": "afde2a69", "content": content,
		})
		if result.IsError {
			t.Fatalf("预期成功: %s", result.DecodeContent())
		}
	}

	assertStringSliceEqual(t, p.callSequence, []string{
		"GetUserInfo:afde2a69",
		"SendMessage:ou_e48:first",
		"GetUserInfo:afde2a69",
		"SendMessage:ou_e48:second",
	})
}

func TestFeishuAPISendMessagePassesToolTraceContextThroughAdapterProviderCalls(t *testing.T) {
	ctx := toolctx.WithToolContext(context.Background(), &toolctx.ToolContext{
		TraceID:    "trace-send",
		ToolCallID: "call-send",
	})
	p := &mockFeishuProvider{
		getUserInfoResult: json.RawMessage(`{"user_id":"afde2a69","open_id":"ou_e48"}`),
	}

	result := callFeishuAPIWithContext(t, ctx, p, map[string]any{
		"action": "send_message", "user_id": "afde2a69", "content": "hello",
	})
	if result.IsError {
		t.Fatalf("预期成功: %s", result.DecodeContent())
	}

	assertStringSliceEqual(t, p.getUserInfoTraceIDs, []string{"trace-send"})
	assertStringSliceEqual(t, p.sendMessageTraceIDs, []string{"trace-send"})
}

func TestFeishuAPIAuditSinkHashesTargets(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	p := &mockFeishuProvider{}
	sink := &captureFeishuAuditSink{}
	rawOpenID := "ou_raw_user_001"
	RegisterFeishuToolsWithOptions(host, logger, p, NewHumanReadableFormatter(), FeishuToolOptions{
		EnableBinaryTransfer: true,
		AuditSink:            sink,
	})

	result := executeFeishuAPITestTool(t, host, context.Background(), map[string]any{
		"action":  "send_message",
		"open_id": rawOpenID,
		"content": "hello",
	})
	if result.IsError {
		t.Fatalf("预期成功: %s", result.DecodeContent())
	}
	if len(sink.records) != 1 {
		t.Fatalf("audit records = %d, want 1", len(sink.records))
	}
	record, ok := sink.records[0].(map[string]any)
	if !ok {
		t.Fatalf("audit record type = %T, want map[string]any", sink.records[0])
	}
	assertFeishuAuditField(t, record, "tool", "feishu_api")
	assertFeishuAuditField(t, record, "action", "send_message")
	assertFeishuAuditField(t, record, "platform", "feishu")
	assertFeishuAuditField(t, record, "status", "ok")
	assertFeishuAuditField(t, record, "outcome", "ok")
	assertFeishuAuditField(t, record, "target_kind", "recipient")
	assertFeishuAuditField(t, record, "target_id_hash", imctx.SafeSenderID(rawOpenID))
	assertFeishuAuditField(t, record, "content_len", 5)
	target, ok := record["target"].(map[string]any)
	if !ok {
		t.Fatalf("audit target type = %T, want map[string]any", record["target"])
	}
	assertFeishuAuditField(t, target, "kind", "recipient")
	assertFeishuAuditField(t, target, "id_hash", imctx.SafeSenderID(rawOpenID))
	for _, key := range []string{"chat_id", "receive_id", "open_id", "user_id", "email"} {
		if _, exists := record[key]; exists {
			t.Fatalf("audit record must not include raw target key %q: %#v", key, record)
		}
		if _, exists := target[key]; exists {
			t.Fatalf("audit target must not include raw target key %q: %#v", key, target)
		}
	}
	encoded, _ := json.Marshal(record)
	if strings.Contains(string(encoded), rawOpenID) {
		t.Fatalf("audit record leaked raw target id: %s", encoded)
	}
}

func TestFeishuAPISendMessageMissingParams(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{"action": "send_message"})
	if !result.IsError {
		t.Error("缺少参数应返回错误")
	}
}

func assertFeishuAuditField(t *testing.T, record map[string]any, key string, want any) {
	t.Helper()
	got, exists := record[key]
	if !exists {
		t.Fatalf("audit field %q missing: %#v", key, record)
	}
	if got != want {
		t.Fatalf("audit field %q = %v (%T), want %v (%T)", key, got, got, want, want)
	}
}

func TestFeishuAPIUploadImage(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{
		"action": "upload_image", "content": "base64-image",
	})
	if result.IsError {
		t.Fatalf("预期成功: %s", result.DecodeContent())
	}
	if p.lastAction != "upload_image" || p.lastContent != "base64-image" {
		t.Fatalf("参数未透传: action=%s content=%s", p.lastAction, p.lastContent)
	}
}

func TestFeishuAPIUploadFile(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{
		"action": "upload_file", "content": "base64-file", "title": "report.pdf",
	})
	if result.IsError {
		t.Fatalf("预期成功: %s", result.DecodeContent())
	}
	if p.lastAction != "upload_file" || p.lastContent != "base64-file" || p.lastDocID != "report.pdf" {
		t.Fatalf("参数未透传: action=%s content=%s file=%s", p.lastAction, p.lastContent, p.lastDocID)
	}
}

func TestFeishuAPISendImage(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{
		"action": "send_image", "chat_id": "oc_1", "file_key": "img_v3_001",
	})
	if result.IsError {
		t.Fatalf("预期成功: %s", result.DecodeContent())
	}
	if p.lastAction != "send_image" || p.lastChatID != "oc_1" || p.lastContent != "img_v3_001" {
		t.Fatalf("参数未透传: action=%s chat=%s key=%s", p.lastAction, p.lastChatID, p.lastContent)
	}
}

func TestFeishuAPISendFile(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{
		"action": "send_file", "chat_id": "oc_1", "file_key": "file_v3_001",
	})
	if result.IsError {
		t.Fatalf("预期成功: %s", result.DecodeContent())
	}
	if p.lastAction != "send_file" || p.lastChatID != "oc_1" || p.lastContent != "file_v3_001" {
		t.Fatalf("参数未透传: action=%s chat=%s key=%s", p.lastAction, p.lastChatID, p.lastContent)
	}
}

// --- download_message_resource ---

func TestFeishuAPIDownloadMessageResource(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{
		"action": "download_message_resource", "message_id": "om_abc", "file_key": "img_v2_key", "resource_type": "image",
	})
	if result.IsError {
		t.Error("预期成功")
	}
	if p.lastAction != "download_message_resource" {
		t.Errorf("action 不匹配: %s", p.lastAction)
	}
}

func TestFeishuAPIDownloadMessageResourceMissingMessageID(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{
		"action": "download_message_resource", "file_key": "img_v2_key", "resource_type": "image",
	})
	if !result.IsError {
		t.Error("缺少 message_id 应返回错误")
	}
}

func TestFeishuAPIDownloadMessageResourceMissingFileKey(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{
		"action": "download_message_resource", "message_id": "om_abc", "resource_type": "image",
	})
	if !result.IsError {
		t.Error("缺少 file_key 应返回错误")
	}
}

func TestFeishuAPIDownloadMessageResourceMissingType(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{
		"action": "download_message_resource", "message_id": "om_abc", "file_key": "img_v2_key",
	})
	if !result.IsError {
		t.Error("缺少 resource_type 应返回错误")
	}
}

// --- 无效 action ---

func TestFeishuAPIInvalidAction(t *testing.T) {
	p := &mockFeishuProvider{}
	result := callFeishuAPI(t, p, map[string]any{"action": "invalid_action"})
	if !result.IsError {
		t.Error("无效 action 应返回错误")
	}
}

func TestFeishuAPIInvalidJSON(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	RegisterFeishuTools(host, logger, &mockFeishuProvider{}, NewHumanReadableFormatter())

	result, err := host.ExecuteTool(context.Background(), "feishu_api", json.RawMessage(`{bad`))
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if !result.IsError {
		t.Error("无效 JSON 应返回错误")
	}
}
