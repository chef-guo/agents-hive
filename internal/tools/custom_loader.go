package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/sandbox"
)

// CustomTool 自定义工具定义
type CustomTool struct {
	Name           string            `yaml:"name"`
	Description    string            `yaml:"description"`
	Type           string            `yaml:"type"` // "shell", "http"
	Command        string            `yaml:"command,omitempty"`
	URL            string            `yaml:"url,omitempty"`
	Method         string            `yaml:"method,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty"`
	Parameters     []ToolParameter   `yaml:"parameters"`
	AllowWrite     bool              `yaml:"allow_write"`
	Timeout        int               `yaml:"timeout"`                   // 超时秒数
	AllowedDomains []string          `yaml:"allowed_domains,omitempty"` // HTTP 工具域名白名单
}

// ToolParameter 工具参数定义
type ToolParameter struct {
	Name        string      `yaml:"name"`
	Type        string      `yaml:"type"` // "string", "integer", "boolean", "array"
	Description string      `yaml:"description"`
	Default     interface{} `yaml:"default,omitempty"`
	Required    bool        `yaml:"required"`
}

// LoadCustomTools 扫描并加载自定义工具
// toolsDir: .claw/tools/ 目录路径
func LoadCustomTools(toolsDir string) ([]CustomTool, error) {
	if toolsDir == "" {
		return nil, nil
	}

	// 检查目录是否存在
	info, err := os.Stat(toolsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 目录不存在，返回空列表
		}
		return nil, errs.Wrap(errs.CodeInvalidInput, "访问工具目录失败", err)
	}
	if !info.IsDir() {
		return nil, errs.New(errs.CodeInvalidInput, fmt.Sprintf("%s 不是目录", toolsDir))
	}

	var tools []CustomTool

	// 读取所有 .yaml 和 .yml 文件
	pattern1 := filepath.Join(toolsDir, "*.yaml")
	pattern2 := filepath.Join(toolsDir, "*.yml")

	files1, _ := filepath.Glob(pattern1)
	files2, _ := filepath.Glob(pattern2)
	files := append(files1, files2...)

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue // 跳过无法读取的文件
		}

		var tool CustomTool
		if err := yaml.Unmarshal(data, &tool); err != nil {
			continue // 跳过格式错误的文件
		}

		// 基本验证
		if tool.Name == "" || tool.Type == "" {
			continue
		}

		// 设置默认超时
		if tool.Timeout == 0 {
			tool.Timeout = 30
		}

		tools = append(tools, tool)
	}

	return tools, nil
}

// RegisterCustomTool 注册自定义工具
func RegisterCustomTool(host *mcphost.Host, logger *zap.Logger, tool CustomTool) error {
	// 生成 JSON Schema
	schema := generateParameterSchema(tool.Parameters)

	// 注册工具
	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schema,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			// 解析参数
			params, err := parseParameters(input, tool.Parameters)
			if err != nil {
				return errorResult("参数解析失败: " + err.Error()), nil
			}

			// 根据类型执行
			var result string
			var execErr error

			switch tool.Type {
			case "shell":
				result, execErr = executeShellTool(tool, params, logger)
			case "http":
				result, execErr = executeHTTPTool(tool, params, logger)
			default:
				return errorResult("不支持的工具类型: " + tool.Type), nil
			}

			if execErr != nil {
				return errorResult("执行失败: " + execErr.Error()), nil
			}

			return textResult(truncateOutput(result)), nil
		},
	)

	return nil
}

// generateParameterSchema 生成 JSON Schema
func generateParameterSchema(params []ToolParameter) json.RawMessage {
	properties := make(map[string]interface{})
	var required []string

	for _, param := range params {
		prop := map[string]interface{}{
			"description": param.Description,
		}

		// 设置类型
		switch param.Type {
		case "integer":
			prop["type"] = "integer"
		case "boolean":
			prop["type"] = "boolean"
		case "array":
			prop["type"] = "array"
			prop["items"] = map[string]string{"type": "string"}
		default:
			prop["type"] = "string"
		}

		// 设置默认值
		if param.Default != nil {
			prop["default"] = param.Default
		}

		properties[param.Name] = prop

		if param.Required {
			required = append(required, param.Name)
		}
	}

	schema := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}

	data, _ := json.Marshal(schema)
	return data
}

// parseParameters 解析输入参数
func parseParameters(input json.RawMessage, params []ToolParameter) (map[string]interface{}, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, err
	}

	result := make(map[string]interface{})

	for _, param := range params {
		value, exists := raw[param.Name]

		// 检查必填参数
		if param.Required && !exists {
			return nil, errs.New(errs.CodeInvalidInput, fmt.Sprintf("缺少必填参数: %s", param.Name))
		}

		// 使用默认值
		if !exists && param.Default != nil {
			value = param.Default
		}

		if exists || param.Default != nil {
			result[param.Name] = value
		}
	}

	return result, nil
}

// executeShellTool 执行 shell 类型工具
func executeShellTool(tool CustomTool, params map[string]interface{}, logger *zap.Logger) (string, error) {
	// 1. 渲染命令模板（替换 {{.param}}），启用 shell escaping 防止命令注入
	command := renderTemplate(tool.Command, params, true)

	// 2. 工具级安全检查
	if !tool.AllowWrite && containsWriteCommand(command) {
		return "", errs.New(errs.CodePermissionDenied, fmt.Sprintf("工具不允许写操作，命令被拒绝: %s", command))
	}

	// 3. 委托给 globalExecutor（SafeExecutorWrapper 统一处理安全检查）
	if globalExecutor == nil {
		return "", errs.New(errs.CodeExecutionFailed, "沙箱执行器未初始化，无法执行自定义工具命令")
	}

	workDir, _ := os.Getwd()
	result, err := globalExecutor.Execute(context.Background(), sandbox.ExecRequest{
		Command:   command,
		SessionID: "custom-tool",
		Timeout:   time.Duration(tool.Timeout) * time.Second,
		WorkDir:   workDir,
	})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", errs.New(errs.CodeSkillExecFailed, formatCommandFailure(result))
	}
	return formatCommandOutput(result), nil
}

// isDomainAllowed 检查 URL 的域名是否在白名单中
// 支持精确匹配和通配符匹配（*.example.com）
func isDomainAllowed(rawURL string, allowedDomains []string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname() // 去掉端口

	for _, domain := range allowedDomains {
		if strings.HasPrefix(domain, "*.") {
			// 通配符匹配：*.example.com 只匹配子域名（如 foo.example.com），不匹配根域 example.com
			suffix := domain[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) {
				return true
			}
		} else {
			// 精确匹配
			if host == domain {
				return true
			}
		}
	}
	return false
}

// isPrivateIP 检查 host 是否为内网/回环/云元数据/特殊地址（SSRF 防护）
func isPrivateIP(host string) bool {
	// 先检查常见的特殊主机名
	lower := strings.ToLower(host)
	if lower == "localhost" || lower == "metadata.google.internal" {
		return true
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// 可能带端口，尝试解析
		h, _, err := net.SplitHostPort(host)
		if err != nil {
			return false
		}
		ip = net.ParseIP(h)
		if ip == nil {
			return false
		}
	}

	// 回环地址（127.0.0.1, ::1）
	if ip.IsLoopback() {
		return true
	}
	// 私有地址（10.x, 172.16-31.x, 192.168.x）
	if ip.IsPrivate() {
		return true
	}
	// 链路本地（169.254.x.x，包括 AWS 元数据 169.254.169.254）
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	// 未指定地址（0.0.0.0, ::）
	if ip.IsUnspecified() {
		return true
	}
	// 多播地址
	if ip.IsMulticast() {
		return true
	}

	return false
}

// ssrfSafeDialContext 返回自定义 DialContext，在 TCP 连接建立时复检解析后的 IP 地址。
// 防止 DNS rebinding 攻击：攻击者在预检和实际连接之间切换 DNS 记录指向内网。
// 重定向时 http.Client 会对新目标重新调用 DialContext，自动覆盖重定向绕过场景。
func ssrfSafeDialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, errs.New(errs.CodePermissionDenied, fmt.Sprintf("无法解析连接地址: %s", addr))
		}

		// DNS 解析后检查所有 IP
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, errs.Wrap(errs.CodeSkillExecFailed, "DNS 解析失败", err)
		}
		for _, ipAddr := range ips {
			if isPrivateIP(ipAddr.IP.String()) {
				return nil, errs.New(errs.CodePermissionDenied, fmt.Sprintf("DNS 解析到内网地址: %s -> %s", host, ipAddr.IP))
			}
		}

		// 检查通过，使用默认 Dialer 连接
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(host, port))
	}
}

// executeHTTPTool 执行 HTTP 类型工具
func executeHTTPTool(tool CustomTool, params map[string]interface{}, logger *zap.Logger) (string, error) {
	// 1. 渲染 URL 和 Headers（HTTP 不需要 shell escaping）
	renderedURL := renderTemplate(tool.URL, params, false)
	headers := make(map[string]string)
	for k, v := range tool.Headers {
		headers[k] = renderTemplate(v, params, false)
	}

	// 2. 域名白名单检查（渲染后检查，防止模板变量绕过）
	// 优先使用工具级白名单，回退到全局白名单
	allowedDomains := tool.AllowedDomains
	if len(allowedDomains) == 0 {
		allowedDomains = globalAllowedDomains
	}
	if len(allowedDomains) > 0 {
		if !isDomainAllowed(renderedURL, allowedDomains) {
			return "", errs.New(errs.CodePermissionDenied, fmt.Sprintf("域名不在白名单中: %s", renderedURL))
		}
	}

	// 3. SSRF 防护：拒绝内网/回环/云元数据地址
	parsed, err := url.Parse(renderedURL)
	if err != nil {
		return "", errs.Wrap(errs.CodeSkillExecFailed, "URL 解析失败", err)
	}
	if isPrivateIP(parsed.Hostname()) {
		return "", errs.New(errs.CodePermissionDenied, fmt.Sprintf("禁止访问内网地址: %s", parsed.Hostname()))
	}

	// 4. 设置默认方法
	method := tool.Method
	if method == "" {
		method = "GET"
	}

	// 5. 创建 HTTP 请求
	req, err := http.NewRequest(method, renderedURL, nil)
	if err != nil {
		return "", errs.Wrap(errs.CodeSkillExecFailed, "创建请求失败", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// 4. 执行请求（使用自定义 DialContext 防止 DNS rebinding 绕过 SSRF 防护）
	client := &http.Client{
		Timeout: time.Duration(tool.Timeout) * time.Second,
		Transport: &http.Transport{
			DialContext:       ssrfSafeDialContext(),
			DisableKeepAlives: true,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errs.Wrap(errs.CodeSkillExecFailed, "请求执行失败", err)
	}
	defer resp.Body.Close()

	// 5. 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errs.Wrap(errs.CodeSkillExecFailed, "读取响应失败", err)
	}

	// 检查 HTTP 状态码
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errs.New(errs.CodeSkillExecFailed, fmt.Sprintf("HTTP 错误 %d: %s", resp.StatusCode, string(body)))
	}

	return string(body), nil
}

// shellEscape 对字符串进行 shell 转义，用单引号包裹并转义内部单引号
// 例如: hello -> 'hello', it's -> 'it'\”s'
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// renderTemplate 渲染模板（替换 {{.param}} 和 {{env.VAR}}）
// 当 escapeForShell 为 true 时，对替换值进行 shell escaping（用于 shell 命令）
func renderTemplate(template string, params map[string]interface{}, escapeForShell bool) string {
	result := template

	// 替换参数 {{.param}}
	for key, value := range params {
		placeholder := fmt.Sprintf("{{.%s}}", key)
		val := fmt.Sprint(value)
		if escapeForShell {
			val = shellEscape(val)
		}
		result = strings.ReplaceAll(result, placeholder, val)
	}

	// 替换环境变量 {{env.VAR}}
	envRegex := regexp.MustCompile(`\{\{env\.(\w+)\}\}`)
	result = envRegex.ReplaceAllStringFunc(result, func(match string) string {
		envVar := envRegex.FindStringSubmatch(match)[1]
		val := os.Getenv(envVar)
		if escapeForShell {
			val = shellEscape(val)
		}
		return val
	})

	return result
}

// containsWriteCommand 检查命令是否包含写操作
// 简单启发式检测，可根据需要扩展
func containsWriteCommand(command string) bool {
	writePatterns := []string{
		"rm ", "rm\t", "rm\n",
		"mv ", "mv\t", "mv\n",
		"cp ", "cp\t", "cp\n",
		">", ">>",
		"dd ", "\tdd", "\ndd", "|dd",
		"mkfs", "fdisk", "parted",
		"chmod", "chown",
	}

	lowerCmd := strings.ToLower(command)
	for _, pattern := range writePatterns {
		if strings.Contains(lowerCmd, pattern) {
			return true
		}
	}
	// 检查命令是否以 dd 开头
	if strings.HasPrefix(lowerCmd, "dd ") {
		return true
	}
	return false
}
