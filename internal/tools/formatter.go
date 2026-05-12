package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ResultFormatter 工具结果格式化接口。
// 按工具名分发格式化策略，将原始 JSON 转为人类可读文本。
type ResultFormatter interface {
	Format(ctx context.Context, toolName string, rawResult json.RawMessage) (string, error)
}

// FormatStrategy 单个工具的格式化策略。
type FormatStrategy interface {
	Format(raw json.RawMessage) (string, error)
}

// HumanReadableFormatter 按工具名选择格式化策略的实现。
// 没有专用策略的工具 fallback 到原始 JSON。
type HumanReadableFormatter struct {
	strategies map[string]FormatStrategy
}

// NewHumanReadableFormatter 创建格式化器，注册所有飞书 action 的策略。
func NewHumanReadableFormatter() *HumanReadableFormatter {
	f := &HumanReadableFormatter{
		strategies: make(map[string]FormatStrategy),
	}
	f.strategies["search_docs"] = &searchDocsStrategy{}
	f.strategies["wiki_get_node"] = &wikiGetNodeStrategy{}
	f.strategies["wiki_list_nodes"] = &wikiListNodesStrategy{}
	f.strategies["search_contacts"] = &searchContactsStrategy{}
	f.strategies["get_user_info"] = &getUserInfoStrategy{}
	f.strategies["get_calendar_events"] = &calendarEventsStrategy{}
	f.strategies["get_chat_info"] = &chatInfoStrategy{}
	f.strategies["get_chat_admins"] = &chatAdminsStrategy{}
	f.strategies["list_chat_members"] = &listChatMembersStrategy{}
	f.strategies["upload_image"] = &uploadImageStrategy{}
	f.strategies["upload_file"] = &uploadFileStrategy{}
	f.strategies["list_approvals"] = &listApprovalsStrategy{}
	f.strategies["get_approval"] = &getApprovalStrategy{}
	f.strategies["list_bitable_tables"] = &listBitableTablesStrategy{}
	f.strategies["list_bitable_records"] = &listBitableRecordsStrategy{}
	f.strategies["create_bitable_record"] = &createBitableRecordStrategy{}
	f.strategies["create_task"] = &createTaskStrategy{}
	f.strategies["list_tasks"] = &listTasksStrategy{}
	f.strategies["read_sheet"] = &readSheetStrategy{}
	return f
}

func (f *HumanReadableFormatter) Format(_ context.Context, toolName string, rawResult json.RawMessage) (string, error) {
	strategy, ok := f.strategies[toolName]
	if !ok {
		return string(rawResult), nil
	}
	return strategy.Format(rawResult)
}

// --- 格式化策略实现 ---

// search_docs: []DocItem
type searchDocsStrategy struct{}

func (s *searchDocsStrategy) Format(raw json.RawMessage) (string, error) {
	var items []struct {
		Title    string `json:"title"`
		URL      string `json:"url"`
		DocToken string `json:"docs_token"`
		DocType  string `json:"docs_type"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return string(raw), nil
	}
	if len(items) == 0 {
		return "未找到相关文档。", nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "找到 %d 个文档：\n", len(items))
	for i, item := range items {
		fmt.Fprintf(&buf, "%d. %s", i+1, item.Title)
		if item.DocType != "" {
			fmt.Fprintf(&buf, " [%s]", item.DocType)
		}
		buf.WriteByte('\n')
		if item.URL != "" {
			fmt.Fprintf(&buf, "   %s\n", item.URL)
		}
	}
	return buf.String(), nil
}

type wikiNodeView struct {
	SpaceID         string `json:"space_id"`
	NodeToken       string `json:"node_token"`
	ObjToken        string `json:"obj_token"`
	ObjType         string `json:"obj_type"`
	ParentNodeToken string `json:"parent_node_token"`
	Title           string `json:"title"`
	HasChild        *bool  `json:"has_child"`
}

type wikiGetNodeStrategy struct{}

func (s *wikiGetNodeStrategy) Format(raw json.RawMessage) (string, error) {
	var result struct {
		Node wikiNodeView `json:"node"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return string(raw), nil
	}
	var buf strings.Builder
	buf.WriteString("Wiki 节点：\n")
	writeField(&buf, "标题", result.Node.Title)
	writeField(&buf, "space_id", result.Node.SpaceID)
	writeField(&buf, "node_token", result.Node.NodeToken)
	writeField(&buf, "obj_type", result.Node.ObjType)
	writeField(&buf, "obj_token", result.Node.ObjToken)
	writeField(&buf, "parent_node_token", result.Node.ParentNodeToken)
	if result.Node.HasChild != nil {
		if *result.Node.HasChild {
			buf.WriteString("  有子节点: 是\n")
		} else {
			buf.WriteString("  有子节点: 否\n")
		}
	}
	return buf.String(), nil
}

type wikiListNodesStrategy struct{}

func (s *wikiListNodesStrategy) Format(raw json.RawMessage) (string, error) {
	var result struct {
		Items   []wikiNodeView `json:"items"`
		HasMore *bool          `json:"has_more"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return string(raw), nil
	}
	if len(result.Items) == 0 {
		return "Wiki 节点列表为空。", nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "Wiki 节点列表（%d 个）：\n", len(result.Items))
	for i, item := range result.Items {
		fmt.Fprintf(&buf, "%d. %s", i+1, item.Title)
		if item.ObjType != "" {
			fmt.Fprintf(&buf, " [%s]", item.ObjType)
		}
		buf.WriteByte('\n')
		if item.NodeToken != "" {
			fmt.Fprintf(&buf, "   node_token: %s\n", item.NodeToken)
		}
		if item.ObjToken != "" {
			fmt.Fprintf(&buf, "   obj_token: %s\n", item.ObjToken)
		}
		if item.HasChild != nil {
			if *item.HasChild {
				buf.WriteString("   has_child: 是\n")
			} else {
				buf.WriteString("   has_child: 否\n")
			}
		}
	}
	if result.HasMore != nil && *result.HasMore {
		buf.WriteString("还有更多，请继续分页拉取。\n")
	}
	return buf.String(), nil
}

// search_contacts: []ContactItem
type searchContactsStrategy struct{}

func (s *searchContactsStrategy) Format(raw json.RawMessage) (string, error) {
	var items []struct {
		Name   string `json:"name"`
		Email  string `json:"email"`
		Mobile string `json:"mobile"`
		UserID string `json:"user_id"`
		OpenID string `json:"open_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return string(raw), nil
	}
	if len(items) == 0 {
		return "未找到匹配的联系人。", nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "找到 %d 个联系人：\n", len(items))
	for i, c := range items {
		fmt.Fprintf(&buf, "%d. %s", i+1, c.Name)
		if c.Status == "frozen" {
			buf.WriteString("（已冻结）")
		}
		buf.WriteByte('\n')
		if c.Email != "" {
			fmt.Fprintf(&buf, "   邮箱: %s\n", c.Email)
		}
		if c.Mobile != "" {
			fmt.Fprintf(&buf, "   手机: %s\n", c.Mobile)
		}
		if c.UserID != "" {
			fmt.Fprintf(&buf, "   user_id: %s\n", c.UserID)
		}
		if c.OpenID != "" {
			fmt.Fprintf(&buf, "   open_id: %s\n", c.OpenID)
		} else if c.UserID != "" {
			buf.WriteString("   提示: 发消息前可用 get_user_info(user_id) 获取 open_id，或直接把 user_id 传给 send_message 的 user_id 字段。\n")
		}
	}
	return buf.String(), nil
}

// get_user_info: UserDetail
type getUserInfoStrategy struct{}

func (s *getUserInfoStrategy) Format(raw json.RawMessage) (string, error) {
	var u struct {
		Name     string `json:"name"`
		EnName   string `json:"en_name"`
		Email    string `json:"email"`
		Mobile   string `json:"mobile"`
		JobTitle string `json:"job_title"`
		City     string `json:"city"`
		UserID   string `json:"user_id"`
		OpenID   string `json:"open_id"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return string(raw), nil
	}
	var buf strings.Builder
	buf.WriteString("用户信息：\n")
	fmt.Fprintf(&buf, "  姓名: %s", u.Name)
	if u.EnName != "" {
		fmt.Fprintf(&buf, " (%s)", u.EnName)
	}
	buf.WriteByte('\n')
	writeField(&buf, "邮箱", u.Email)
	writeField(&buf, "手机", u.Mobile)
	writeField(&buf, "职位", u.JobTitle)
	writeField(&buf, "城市", u.City)
	writeField(&buf, "user_id", u.UserID)
	writeField(&buf, "open_id", u.OpenID)
	return buf.String(), nil
}

// get_calendar_events: []CalendarEvent
type calendarEventsStrategy struct{}

func (s *calendarEventsStrategy) Format(raw json.RawMessage) (string, error) {
	var events []struct {
		Summary   string   `json:"summary"`
		StartTime string   `json:"start_time"`
		EndTime   string   `json:"end_time"`
		Location  string   `json:"location"`
		Status    string   `json:"status"`
		Attendees []string `json:"attendees"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		return string(raw), nil
	}
	if len(events) == 0 {
		return "该时间段没有日历事件。", nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "共 %d 个日历事件：\n", len(events))
	for i, e := range events {
		fmt.Fprintf(&buf, "%d. %s\n", i+1, e.Summary)
		if e.StartTime != "" || e.EndTime != "" {
			fmt.Fprintf(&buf, "   时间: %s ~ %s\n", formatTimestamp(e.StartTime), formatTimestamp(e.EndTime))
		}
		if e.Location != "" {
			fmt.Fprintf(&buf, "   地点: %s\n", e.Location)
		}
		if e.Status != "" {
			fmt.Fprintf(&buf, "   状态: %s\n", translateEventStatus(e.Status))
		}
		if len(e.Attendees) > 0 {
			fmt.Fprintf(&buf, "   参与人: %s\n", strings.Join(e.Attendees, "、"))
		}
	}
	return buf.String(), nil
}

type uploadImageStrategy struct{}

func (s *uploadImageStrategy) Format(raw json.RawMessage) (string, error) {
	var data struct {
		ImageKey string `json:"image_key"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw), nil
	}
	if data.ImageKey == "" {
		return string(raw), nil
	}
	return fmt.Sprintf("图片上传成功：\n  image_key: %s\n", data.ImageKey), nil
}

type uploadFileStrategy struct{}

func (s *uploadFileStrategy) Format(raw json.RawMessage) (string, error) {
	var data struct {
		FileKey  string `json:"file_key"`
		FileName string `json:"file_name"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw), nil
	}
	if data.FileKey == "" {
		return string(raw), nil
	}
	var buf strings.Builder
	buf.WriteString("文件上传成功：\n")
	writeField(&buf, "file_key", data.FileKey)
	writeField(&buf, "文件名", data.FileName)
	return buf.String(), nil
}

// get_chat_info: SDK GetChatRespData (json.Marshal)
type chatInfoStrategy struct{}

func (s *chatInfoStrategy) Format(raw json.RawMessage) (string, error) {
	var info struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		OwnerID     *string `json:"owner_id"`
		ChatMode    *string `json:"chat_mode"`
		ChatType    *string `json:"chat_type"`
		External    *bool   `json:"external"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return string(raw), nil
	}
	var buf strings.Builder
	buf.WriteString("群聊信息：\n")
	writeFieldPtr(&buf, "群名称", info.Name)
	writeFieldPtr(&buf, "群描述", info.Description)
	writeFieldPtr(&buf, "群主 ID", info.OwnerID)
	writeFieldPtr(&buf, "群模式", info.ChatMode)
	writeFieldPtr(&buf, "群类型", info.ChatType)
	if info.External != nil && *info.External {
		buf.WriteString("  外部群: 是\n")
	}
	return buf.String(), nil
}

// get_chat_admins: SDK GetChatRespData 中的 owner / manager 字段摘要
type chatAdminsStrategy struct{}

func (s *chatAdminsStrategy) Format(raw json.RawMessage) (string, error) {
	var info struct {
		Name              *string  `json:"name"`
		OwnerID           *string  `json:"owner_id"`
		UserManagerIDList []string `json:"user_manager_id_list"`
		BotManagerIDList  []string `json:"bot_manager_id_list"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return string(raw), nil
	}

	userManagers := uniqueNonEmpty(info.UserManagerIDList)
	botManagers := uniqueNonEmpty(info.BotManagerIDList)
	ownerID := derefStrFmt(info.OwnerID)
	userManagers = removeString(userManagers, ownerID)

	totalAdmins := len(userManagers) + len(botManagers)
	if ownerID != "" {
		totalAdmins++
	}

	var buf strings.Builder
	buf.WriteString("群管理员信息：\n")
	writeFieldPtr(&buf, "群名称", info.Name)
	if ownerID != "" {
		fmt.Fprintf(&buf, "  群主: %s\n", ownerID)
	}
	if len(userManagers) > 0 {
		fmt.Fprintf(&buf, "  用户管理员（%d）: %s\n", len(userManagers), strings.Join(userManagers, "、"))
	} else {
		buf.WriteString("  用户管理员（0）: 无\n")
	}
	if len(botManagers) > 0 {
		fmt.Fprintf(&buf, "  机器人管理员（%d）: %s\n", len(botManagers), strings.Join(botManagers, "、"))
	} else {
		buf.WriteString("  机器人管理员（0）: 无\n")
	}
	fmt.Fprintf(&buf, "  管理员总数: %d\n", totalAdmins)
	return buf.String(), nil
}

// list_chat_members: SDK GetChatMembersRespData
type listChatMembersStrategy struct{}

func (s *listChatMembersStrategy) Format(raw json.RawMessage) (string, error) {
	var data struct {
		Items []struct {
			Name     *string `json:"name"`
			MemberID *string `json:"member_id"`
		} `json:"items"`
		MemberTotal *int `json:"member_total"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw), nil
	}
	var buf strings.Builder
	if data.MemberTotal != nil {
		fmt.Fprintf(&buf, "群成员（共 %d 人）：\n", *data.MemberTotal)
	} else {
		fmt.Fprintf(&buf, "群成员（%d 人）：\n", len(data.Items))
	}
	for i, m := range data.Items {
		name := derefStrFmt(m.Name)
		if name == "" {
			name = derefStrFmt(m.MemberID)
		}
		fmt.Fprintf(&buf, "%d. %s\n", i+1, name)
	}
	return buf.String(), nil
}

// list_approvals: SDK ListInstanceRespData
type listApprovalsStrategy struct{}

func (s *listApprovalsStrategy) Format(raw json.RawMessage) (string, error) {
	var data struct {
		InstanceCodeList []string `json:"instance_code_list"`
		HasMore          *bool    `json:"has_more"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw), nil
	}
	if len(data.InstanceCodeList) == 0 {
		return "未找到审批实例。", nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "找到 %d 个审批实例：\n", len(data.InstanceCodeList))
	for i, code := range data.InstanceCodeList {
		fmt.Fprintf(&buf, "%d. %s\n", i+1, code)
	}
	if data.HasMore != nil && *data.HasMore {
		buf.WriteString("（还有更多，可翻页查看）\n")
	}
	return buf.String(), nil
}

// get_approval: SDK GetInstanceRespData
type getApprovalStrategy struct{}

func (s *getApprovalStrategy) Format(raw json.RawMessage) (string, error) {
	var data struct {
		ApprovalName *string `json:"approval_name"`
		SerialNumber *string `json:"serial_number"`
		Status       *string `json:"status"`
		StartTime    *string `json:"start_time"`
		EndTime      *string `json:"end_time"`
		UserID       *string `json:"user_id"`
		DepartmentID *string `json:"department_id"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw), nil
	}
	var buf strings.Builder
	buf.WriteString("审批详情：\n")
	writeFieldPtr(&buf, "审批名称", data.ApprovalName)
	writeFieldPtr(&buf, "编号", data.SerialNumber)
	if data.Status != nil {
		fmt.Fprintf(&buf, "  状态: %s\n", translateApprovalStatus(*data.Status))
	}
	writeFieldPtr(&buf, "发起人 ID", data.UserID)
	if data.StartTime != nil {
		fmt.Fprintf(&buf, "  发起时间: %s\n", formatTimestamp(*data.StartTime))
	}
	if data.EndTime != nil && *data.EndTime != "0" {
		fmt.Fprintf(&buf, "  完成时间: %s\n", formatTimestamp(*data.EndTime))
	}
	return buf.String(), nil
}

// list_bitable_tables: SDK ListAppTableRespData
type listBitableTablesStrategy struct{}

func (s *listBitableTablesStrategy) Format(raw json.RawMessage) (string, error) {
	var data struct {
		Items []struct {
			TableID *string `json:"table_id"`
			Name    *string `json:"name"`
		} `json:"items"`
		Total *int `json:"total"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw), nil
	}
	if len(data.Items) == 0 {
		return "该多维表格没有数据表。", nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "共 %d 个数据表：\n", len(data.Items))
	for i, t := range data.Items {
		name := derefStrFmt(t.Name)
		tableID := derefStrFmt(t.TableID)
		fmt.Fprintf(&buf, "%d. %s (table_id: %s)\n", i+1, name, tableID)
	}
	return buf.String(), nil
}

// list_bitable_records: SDK ListAppTableRecordRespData
type listBitableRecordsStrategy struct{}

func (s *listBitableRecordsStrategy) Format(raw json.RawMessage) (string, error) {
	var data struct {
		Items []struct {
			RecordID *string                `json:"record_id"`
			Fields   map[string]interface{} `json:"fields"`
		} `json:"items"`
		Total *int `json:"total"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw), nil
	}
	if len(data.Items) == 0 {
		return "未找到记录。", nil
	}
	var buf strings.Builder
	total := len(data.Items)
	if data.Total != nil {
		total = *data.Total
	}
	fmt.Fprintf(&buf, "共 %d 条记录：\n", total)
	for i, r := range data.Items {
		rid := derefStrFmt(r.RecordID)
		fmt.Fprintf(&buf, "%d. record_id: %s\n", i+1, rid)
		for k, v := range r.Fields {
			fmt.Fprintf(&buf, "   %s: %v\n", k, v)
		}
	}
	return buf.String(), nil
}

// create_bitable_record: SDK CreateAppTableRecordRespData
type createBitableRecordStrategy struct{}

func (s *createBitableRecordStrategy) Format(raw json.RawMessage) (string, error) {
	var data struct {
		Record *struct {
			RecordID *string                `json:"record_id"`
			Fields   map[string]interface{} `json:"fields"`
		} `json:"record"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw), nil
	}
	if data.Record == nil {
		return "记录已创建。", nil
	}
	var buf strings.Builder
	rid := derefStrFmt(data.Record.RecordID)
	fmt.Fprintf(&buf, "记录已创建，record_id: %s\n", rid)
	for k, v := range data.Record.Fields {
		fmt.Fprintf(&buf, "  %s: %v\n", k, v)
	}
	return buf.String(), nil
}

// create_task: SDK CreateTaskRespData (v1)
type createTaskStrategy struct{}

func (s *createTaskStrategy) Format(raw json.RawMessage) (string, error) {
	var data struct {
		Task *struct {
			ID      *string `json:"id"`
			Summary *string `json:"summary"`
		} `json:"task"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw), nil
	}
	if data.Task == nil {
		return "任务已创建。", nil
	}
	var buf strings.Builder
	buf.WriteString("任务已创建：\n")
	writeFieldPtr(&buf, "任务 ID", data.Task.ID)
	writeFieldPtr(&buf, "摘要", data.Task.Summary)
	return buf.String(), nil
}

// list_tasks: SDK ListTaskRespData (v1)
type listTasksStrategy struct{}

func (s *listTasksStrategy) Format(raw json.RawMessage) (string, error) {
	var data struct {
		Items []struct {
			ID           *string `json:"id"`
			Summary      *string `json:"summary"`
			CompleteTime *string `json:"complete_time"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw), nil
	}
	if len(data.Items) == 0 {
		return "没有任务。", nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "共 %d 个任务：\n", len(data.Items))
	for i, t := range data.Items {
		summary := derefStrFmt(t.Summary)
		status := "进行中"
		if t.CompleteTime != nil && *t.CompleteTime != "" && *t.CompleteTime != "0" {
			status = "已完成"
		}
		fmt.Fprintf(&buf, "%d. [%s] %s\n", i+1, status, summary)
	}
	return buf.String(), nil
}

// read_sheet: 电子表格数据（结构不固定，做通用 pretty-print）
type readSheetStrategy struct{}

func (s *readSheetStrategy) Format(raw json.RawMessage) (string, error) {
	var data struct {
		ValueRange struct {
			Values [][]interface{} `json:"values"`
		} `json:"valueRange"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw), nil
	}
	values := data.ValueRange.Values
	if len(values) == 0 {
		return "表格数据为空。", nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "表格数据（%d 行）：\n", len(values))
	for i, row := range values {
		cells := make([]string, len(row))
		for j, cell := range row {
			cells[j] = fmt.Sprintf("%v", cell)
		}
		fmt.Fprintf(&buf, "%d. %s\n", i+1, strings.Join(cells, " | "))
	}
	return buf.String(), nil
}

// --- 辅助函数 ---

func writeField(buf *strings.Builder, label, value string) {
	if value != "" {
		fmt.Fprintf(buf, "  %s: %s\n", label, value)
	}
}

func writeFieldPtr(buf *strings.Builder, label string, value *string) {
	if value != nil && *value != "" {
		fmt.Fprintf(buf, "  %s: %s\n", label, *value)
	}
}

func derefStrFmt(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func uniqueNonEmpty(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func removeString(items []string, target string) []string {
	if target == "" || len(items) == 0 {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if item == target {
			continue
		}
		out = append(out, item)
	}
	return out
}

func translateEventStatus(status string) string {
	switch status {
	case "tentative":
		return "待定"
	case "confirmed":
		return "已确认"
	case "cancelled":
		return "已取消"
	default:
		return status
	}
}

func translateApprovalStatus(status string) string {
	switch status {
	case "PENDING":
		return "审批中"
	case "APPROVED":
		return "已通过"
	case "REJECTED":
		return "已拒绝"
	case "CANCELED":
		return "已撤回"
	case "DELETED":
		return "已删除"
	default:
		return status
	}
}

// formatTimestamp 尝试将 Unix 时间戳字符串转为可读时间，失败则原样返回。
func formatTimestamp(ts string) string {
	if ts == "" || ts == "0" {
		return ""
	}
	// 飞书时间戳可能是秒级或毫秒级
	var sec int64
	if _, err := fmt.Sscanf(ts, "%d", &sec); err != nil {
		return ts
	}
	if sec > 1e12 {
		sec = sec / 1000 // 毫秒转秒
	}
	return time.Unix(sec, 0).Format("2006-01-02 15:04")
}
