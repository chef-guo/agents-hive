package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHumanReadableFormatter_Fallback(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{"unknown":"data"}`)
	got, err := f.Format(context.Background(), "unknown_action", raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"unknown":"data"}` {
		t.Errorf("fallback 应返回原始 JSON，got: %s", got)
	}
}

func TestFormatSearchDocs(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`[
		{"title":"设计文档","url":"https://example.com/doc1","docs_type":"docx"},
		{"title":"需求文档","url":"https://example.com/doc2","docs_type":"doc"}
	]`)
	got, err := f.Format(context.Background(), "search_docs", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "找到 2 个文档")
	assertContains(t, got, "1. 设计文档 [docx]")
	assertContains(t, got, "https://example.com/doc1")
	assertContains(t, got, "2. 需求文档 [doc]")
}

func TestFormatSearchDocsEmpty(t *testing.T) {
	f := NewHumanReadableFormatter()
	got, _ := f.Format(context.Background(), "search_docs", json.RawMessage(`[]`))
	assertContains(t, got, "未找到相关文档")
}

func TestFormatSearchContacts(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`[
		{"name":"张三","email":"zhangsan@example.com","mobile":"13800000001","user_id":"u1","open_id":"ou_1","status":"active"},
		{"name":"李四","email":"","mobile":"","user_id":"u2","status":"frozen"}
	]`)
	got, err := f.Format(context.Background(), "search_contacts", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "找到 2 个联系人")
	assertContains(t, got, "1. 张三")
	assertContains(t, got, "邮箱: zhangsan@example.com")
	assertContains(t, got, "open_id: ou_1")
	assertContains(t, got, "2. 李四（已冻结）")
	assertContains(t, got, "send_message 的 user_id 字段")
}

func TestFormatGetUserInfo(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{
		"name":"王五","en_name":"Wang Wu","email":"wangwu@example.com",
		"mobile":"13900000000","job_title":"工程师","city":"北京",
		"user_id":"u123","open_id":"ou_abc"
	}`)
	got, err := f.Format(context.Background(), "get_user_info", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "用户信息")
	assertContains(t, got, "姓名: 王五 (Wang Wu)")
	assertContains(t, got, "邮箱: wangwu@example.com")
	assertContains(t, got, "职位: 工程师")
}

func TestFormatCalendarEvents(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`[
		{"summary":"周会","start_time":"1700000000","end_time":"1700003600","location":"会议室A","status":"confirmed","attendees":["张三","李四"]}
	]`)
	got, err := f.Format(context.Background(), "get_calendar_events", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "共 1 个日历事件")
	assertContains(t, got, "1. 周会")
	assertContains(t, got, "地点: 会议室A")
	assertContains(t, got, "状态: 已确认")
	assertContains(t, got, "参与人: 张三、李四")
}

func TestFormatCalendarEventsEmpty(t *testing.T) {
	f := NewHumanReadableFormatter()
	got, _ := f.Format(context.Background(), "get_calendar_events", json.RawMessage(`[]`))
	assertContains(t, got, "该时间段没有日历事件")
}

func TestFormatChatInfo(t *testing.T) {
	f := NewHumanReadableFormatter()
	name := "测试群"
	desc := "这是一个测试群"
	ownerID := "ou_owner"
	chatMode := "group"
	chatType := "public"
	ext := true
	raw, _ := json.Marshal(map[string]any{
		"name": &name, "description": &desc, "owner_id": &ownerID,
		"chat_mode": &chatMode, "chat_type": &chatType, "external": &ext,
	})
	got, err := f.Format(context.Background(), "get_chat_info", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "群聊信息")
	assertContains(t, got, "群名称: 测试群")
	assertContains(t, got, "外部群: 是")
}

func TestFormatListChatMembers(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{"items":[{"name":"张三","member_id":"m1"},{"name":"李四","member_id":"m2"}],"member_total":2}`)
	got, err := f.Format(context.Background(), "list_chat_members", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "群成员（共 2 人）")
	assertContains(t, got, "1. 张三")
	assertContains(t, got, "2. 李四")
}

func TestFormatGetChatAdmins(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{
		"name":"项目群",
		"owner_id":"ou_owner",
		"user_manager_id_list":["ou_admin_1","ou_admin_2","ou_owner"],
		"bot_manager_id_list":["ou_bot_1"]
	}`)
	got, err := f.Format(context.Background(), "get_chat_admins", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "群管理员信息")
	assertContains(t, got, "群名称: 项目群")
	assertContains(t, got, "群主: ou_owner")
	assertContains(t, got, "用户管理员（2）: ou_admin_1、ou_admin_2")
	assertContains(t, got, "机器人管理员（1）: ou_bot_1")
	assertContains(t, got, "管理员总数: 4")
}

func TestFormatListApprovals(t *testing.T) {
	f := NewHumanReadableFormatter()
	hasMore := true
	raw, _ := json.Marshal(map[string]any{
		"instance_code_list": []string{"inst_001", "inst_002"},
		"has_more":           &hasMore,
	})
	got, err := f.Format(context.Background(), "list_approvals", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "找到 2 个审批实例")
	assertContains(t, got, "1. inst_001")
	assertContains(t, got, "还有更多")
}

func TestFormatGetApproval(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{
		"approval_name":"请假审批","serial_number":"202401001",
		"status":"APPROVED","user_id":"u1","start_time":"1700000000","end_time":"1700100000"
	}`)
	got, err := f.Format(context.Background(), "get_approval", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "审批详情")
	assertContains(t, got, "审批名称: 请假审批")
	assertContains(t, got, "状态: 已通过")
	assertContains(t, got, "编号: 202401001")
}

func TestFormatListBitableTables(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{"items":[{"table_id":"tbl_1","name":"用户表"},{"table_id":"tbl_2","name":"订单表"}],"total":2}`)
	got, err := f.Format(context.Background(), "list_bitable_tables", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "共 2 个数据表")
	assertContains(t, got, "1. 用户表 (table_id: tbl_1)")
}

func TestFormatListBitableRecords(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{"items":[{"record_id":"rec_1","fields":{"姓名":"张三","年龄":30}}],"total":1}`)
	got, err := f.Format(context.Background(), "list_bitable_records", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "共 1 条记录")
	assertContains(t, got, "record_id: rec_1")
	assertContains(t, got, "姓名: 张三")
}

func TestFormatCreateBitableRecord(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{"record":{"record_id":"rec_new","fields":{"状态":"待处理"}}}`)
	got, err := f.Format(context.Background(), "create_bitable_record", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "记录已创建，record_id: rec_new")
	assertContains(t, got, "状态: 待处理")
}

func TestFormatCreateTask(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{"task":{"id":"task_123","summary":"完成报告"}}`)
	got, err := f.Format(context.Background(), "create_task", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "任务已创建")
	assertContains(t, got, "任务 ID: task_123")
	assertContains(t, got, "摘要: 完成报告")
}

func TestFormatListTasks(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{"items":[
		{"id":"t1","summary":"任务A","complete_time":"0"},
		{"id":"t2","summary":"任务B","complete_time":"1700000000"}
	]}`)
	got, err := f.Format(context.Background(), "list_tasks", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "共 2 个任务")
	assertContains(t, got, "[进行中] 任务A")
	assertContains(t, got, "[已完成] 任务B")
}

func TestFormatReadSheet(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{"valueRange":{"values":[["姓名","年龄"],["张三","30"],["李四","25"]]}}`)
	got, err := f.Format(context.Background(), "read_sheet", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "表格数据（3 行）")
	assertContains(t, got, "姓名 | 年龄")
	assertContains(t, got, "张三 | 30")
}

func TestFormatReadSheetEmpty(t *testing.T) {
	f := NewHumanReadableFormatter()
	got, _ := f.Format(context.Background(), "read_sheet", json.RawMessage(`{"valueRange":{"values":[]}}`))
	assertContains(t, got, "表格数据为空")
}

func TestFormatWikiGetNode(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{
		"node":{
			"space_id":"space_alpha",
			"node_token":"node_root",
			"title":"产品文档",
			"obj_type":"docx",
			"obj_token":"doc_123",
			"parent_node_token":"parent_1",
			"has_child":true
		}
	}`)
	got, err := f.Format(context.Background(), "wiki_get_node", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "Wiki 节点")
	assertContains(t, got, "标题: 产品文档")
	assertContains(t, got, "space_id: space_alpha")
	assertContains(t, got, "obj_type: docx")
	assertContains(t, got, "有子节点: 是")
}

func TestFormatWikiListNodes(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{
		"items":[
			{"space_id":"space_alpha","node_token":"node_a","title":"目录A","obj_type":"docx","obj_token":"doc_a","has_child":true},
			{"space_id":"space_alpha","node_token":"node_b","title":"目录B","obj_type":"sheet","obj_token":"sheet_b","has_child":false}
		],
		"has_more":true
	}`)
	got, err := f.Format(context.Background(), "wiki_list_nodes", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "Wiki 节点列表（2 个）")
	assertContains(t, got, "1. 目录A [docx]")
	assertContains(t, got, "2. 目录B [sheet]")
	assertContains(t, got, "has_child: 是")
	assertContains(t, got, "还有更多")
}

func TestFormatUploadImage(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{"image_key":"img_v3_001"}`)
	got, err := f.Format(context.Background(), "upload_image", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "图片上传成功")
	assertContains(t, got, "image_key: img_v3_001")
}

func TestFormatUploadFile(t *testing.T) {
	f := NewHumanReadableFormatter()
	raw := json.RawMessage(`{"file_key":"file_v3_001","file_name":"report.pdf"}`)
	got, err := f.Format(context.Background(), "upload_file", raw)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, got, "文件上传成功")
	assertContains(t, got, "file_key: file_v3_001")
	assertContains(t, got, "文件名: report.pdf")
}

func TestFormatToolResult_Integration(t *testing.T) {
	// 测试 formatToolResult 函数：模拟 feishu_tools.go 中的调用路径
	formatter := NewHumanReadableFormatter()
	// textResult 将字符串 JSON 编码为 jsonText
	original := textResult(`[{"title":"测试文档","url":"https://example.com","docs_type":"doc"}]`)
	result := formatToolResult(context.Background(), formatter, "search_docs", original)

	var formatted string
	if err := json.Unmarshal(result.Content, &formatted); err != nil {
		t.Fatalf("解码格式化结果失败: %v", err)
	}
	assertContains(t, formatted, "找到 1 个文档")
	assertContains(t, formatted, "测试文档 [doc]")
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("期望包含 %q，实际:\n%s", substr, s)
	}
}
