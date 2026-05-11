package observability

var allowedMetricLabels = map[string]map[string]bool{
	"*": {
		"route":              true,
		"status":             true,
		"failure_type":       true,
		"decision":           true,
		"retry_reason":       true,
		"tool_name":          true,
		"model":              true,
		"operation":          true,
		"reason":             true,
		"scenario":           true,
		"differs":            true,
		"decision_label":     true,
		"im":                 true,
		"policy":             true,
		"kind":               true,
		"severity":           true,
		"reflection_trigger": true,
		"target_type":        true,
		"result":             true,
		"msg_type":           true,
	},
	"hive.eventbus.dropped": {
		"msg_type": true,
	},
}

var forbiddenMetricLabels = map[string]bool{
	"session_id":  true,
	"user_id":     true,
	"trace_id":    true,
	"span_id":     true,
	"tool_args":   true,
	"prompt":      true,
	"prompt_hash": true,
}

// SanitizeMetricLabels 只保留低基数字段，避免指标存储被动态 ID 或用户输入污染。
func SanitizeMetricLabels(name string, labels map[string]any) map[string]any {
	if len(labels) == 0 {
		return labels
	}

	out := make(map[string]any, len(labels))
	for k, v := range labels {
		if forbiddenMetricLabels[k] {
			continue
		}
		if allowedMetricLabels["*"][k] || allowedMetricLabels[name][k] {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
