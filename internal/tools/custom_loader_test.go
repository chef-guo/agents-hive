package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/sandbox"
)

// setupTestExecutor 为 custom_loader 测试注入 executor，返回清理函数。
func setupTestExecutor(t *testing.T) {
	t.Helper()
	shell, err := NewPersistentShell()
	require.NoError(t, err)
	inner := sandbox.NewLocalExecutor(shell, nil)
	wrapper := sandbox.NewSafeExecutorWrapper(inner, nil)
	old := globalExecutor
	globalExecutor = wrapper
	t.Cleanup(func() {
		globalExecutor = old
		shell.Close()
	})
}

func TestLoadCustomTools(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func(dir string)
		wantCount int
		wantNames []string
	}{
		{
			name: "空目录",
			setupFunc: func(dir string) {
				// 不创建任何文件
			},
			wantCount: 0,
		},
		{
			name: "单个工具",
			setupFunc: func(dir string) {
				tool := CustomTool{
					Name:        "test_tool",
					Description: "测试工具",
					Type:        "shell",
					Command:     "echo test",
					Timeout:     10,
				}
				data, _ := yaml.Marshal(tool)
				os.WriteFile(filepath.Join(dir, "test.yaml"), data, 0644)
			},
			wantCount: 1,
			wantNames: []string{"test_tool"},
		},
		{
			name: "多个工具（yaml和yml）",
			setupFunc: func(dir string) {
				tool1 := CustomTool{
					Name:        "tool1",
					Description: "工具1",
					Type:        "shell",
					Command:     "echo 1",
				}
				tool2 := CustomTool{
					Name:        "tool2",
					Description: "工具2",
					Type:        "http",
					URL:         "http://example.com",
				}
				data1, _ := yaml.Marshal(tool1)
				data2, _ := yaml.Marshal(tool2)
				os.WriteFile(filepath.Join(dir, "tool1.yaml"), data1, 0644)
				os.WriteFile(filepath.Join(dir, "tool2.yml"), data2, 0644)
			},
			wantCount: 2,
			wantNames: []string{"tool1", "tool2"},
		},
		{
			name: "忽略无效文件",
			setupFunc: func(dir string) {
				// 创建有效工具
				tool := CustomTool{
					Name:    "valid_tool",
					Type:    "shell",
					Command: "echo test",
				}
				data, _ := yaml.Marshal(tool)
				os.WriteFile(filepath.Join(dir, "valid.yaml"), data, 0644)

				// 创建无效文件
				os.WriteFile(filepath.Join(dir, "invalid.yaml"), []byte("invalid: yaml: syntax"), 0644)
				os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("readme"), 0644)
			},
			wantCount: 1,
			wantNames: []string{"valid_tool"},
		},
		{
			name: "设置默认超时",
			setupFunc: func(dir string) {
				tool := CustomTool{
					Name:    "no_timeout",
					Type:    "shell",
					Command: "echo test",
					// 不设置 Timeout
				}
				data, _ := yaml.Marshal(tool)
				os.WriteFile(filepath.Join(dir, "test.yaml"), data, 0644)
			},
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 创建临时目录
			tmpDir := t.TempDir()
			tt.setupFunc(tmpDir)

			// 加载工具
			tools, err := LoadCustomTools(tmpDir)
			require.NoError(t, err)
			assert.Len(t, tools, tt.wantCount)

			// 检查工具名称
			if tt.wantNames != nil {
				names := make([]string, len(tools))
				for i, tool := range tools {
					names[i] = tool.Name
				}
				assert.ElementsMatch(t, tt.wantNames, names)
			}

			// 检查默认超时
			if tt.wantCount > 0 && tt.name == "设置默认超时" {
				assert.Equal(t, 30, tools[0].Timeout)
			}
		})
	}
}

func TestLoadCustomTools_EmptyDir(t *testing.T) {
	// 空路径
	tools, err := LoadCustomTools("")
	assert.NoError(t, err)
	assert.Nil(t, tools)

	// 不存在的目录
	tools, err = LoadCustomTools("/nonexistent/path")
	assert.NoError(t, err)
	assert.Nil(t, tools)
}

func TestGenerateParameterSchema(t *testing.T) {
	tests := []struct {
		name   string
		params []ToolParameter
		check  func(t *testing.T, schema json.RawMessage)
	}{
		{
			name:   "空参数",
			params: []ToolParameter{},
			check: func(t *testing.T, schema json.RawMessage) {
				var s map[string]interface{}
				json.Unmarshal(schema, &s)
				assert.Equal(t, "object", s["type"])
				assert.NotNil(t, s["properties"])
			},
		},
		{
			name: "字符串参数",
			params: []ToolParameter{
				{Name: "name", Type: "string", Description: "名称", Required: true},
			},
			check: func(t *testing.T, schema json.RawMessage) {
				var s map[string]interface{}
				json.Unmarshal(schema, &s)
				props := s["properties"].(map[string]interface{})
				name := props["name"].(map[string]interface{})
				assert.Equal(t, "string", name["type"])
				assert.Equal(t, "名称", name["description"])
				req := s["required"].([]interface{})
				assert.Contains(t, req, "name")
			},
		},
		{
			name: "多种类型参数",
			params: []ToolParameter{
				{Name: "count", Type: "integer", Description: "数量", Default: 10},
				{Name: "enabled", Type: "boolean", Description: "启用"},
				{Name: "items", Type: "array", Description: "项目列表"},
			},
			check: func(t *testing.T, schema json.RawMessage) {
				var s map[string]interface{}
				json.Unmarshal(schema, &s)
				props := s["properties"].(map[string]interface{})

				count := props["count"].(map[string]interface{})
				assert.Equal(t, "integer", count["type"])
				assert.Equal(t, float64(10), count["default"])

				enabled := props["enabled"].(map[string]interface{})
				assert.Equal(t, "boolean", enabled["type"])

				items := props["items"].(map[string]interface{})
				assert.Equal(t, "array", items["type"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := generateParameterSchema(tt.params)
			tt.check(t, schema)
		})
	}
}

func TestParseParameters(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		params    []ToolParameter
		wantErr   bool
		checkFunc func(t *testing.T, result map[string]interface{})
	}{
		{
			name:  "所有必填参数都存在",
			input: `{"name":"test","age":25}`,
			params: []ToolParameter{
				{Name: "name", Type: "string", Required: true},
				{Name: "age", Type: "integer", Required: true},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, result map[string]interface{}) {
				assert.Equal(t, "test", result["name"])
				assert.Equal(t, float64(25), result["age"])
			},
		},
		{
			name:  "缺少必填参数",
			input: `{"name":"test"}`,
			params: []ToolParameter{
				{Name: "name", Type: "string", Required: true},
				{Name: "age", Type: "integer", Required: true},
			},
			wantErr: true,
		},
		{
			name:  "使用默认值",
			input: `{"name":"test"}`,
			params: []ToolParameter{
				{Name: "name", Type: "string", Required: true},
				{Name: "count", Type: "integer", Default: 10},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, result map[string]interface{}) {
				assert.Equal(t, "test", result["name"])
				assert.Equal(t, 10, result["count"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseParameters(json.RawMessage(tt.input), tt.params)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, result)
				}
			}
		})
	}
}

func TestRenderTemplate(t *testing.T) {
	tests := []struct {
		name     string
		template string
		params   map[string]interface{}
		envVars  map[string]string
		want     string
	}{
		{
			name:     "替换参数",
			template: "echo {{.name}} {{.age}}",
			params:   map[string]interface{}{"name": "test", "age": 25},
			want:     "echo test 25",
		},
		{
			name:     "替换环境变量",
			template: "API_KEY={{env.API_KEY}}",
			envVars:  map[string]string{"API_KEY": "secret123"},
			want:     "API_KEY=secret123",
		},
		{
			name:     "混合替换",
			template: "curl -H 'Authorization: Bearer {{env.TOKEN}}' {{.url}}",
			params:   map[string]interface{}{"url": "http://api.example.com"},
			envVars:  map[string]string{"TOKEN": "abc123"},
			want:     "curl -H 'Authorization: Bearer abc123' http://api.example.com",
		},
		{
			name:     "无需替换",
			template: "echo hello",
			want:     "echo hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 设置环境变量
			for k, v := range tt.envVars {
				os.Setenv(k, v)
				defer os.Unsetenv(k)
			}

			result := renderTemplate(tt.template, tt.params, false)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestContainsWriteCommand(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"echo hello", false},
		{"ls -la", false},
		{"cat file.txt", false},
		{"rm file.txt", true},
		{"rm -rf /tmp/test", true},
		{"mv old new", true},
		{"cp src dst", true},
		{"echo test > file.txt", true},
		{"cat file >> log.txt", true},
		{"chmod 755 script.sh", true},
		{"chown user:group file", true},
		{"dd if=/dev/zero of=/dev/sda", true},
		{"mkfs.ext4 /dev/sda1", true},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := containsWriteCommand(tt.command)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestExecuteShellTool(t *testing.T) {
	setupTestExecutor(t)
	logger := zap.NewNop()

	tests := []struct {
		name       string
		tool       CustomTool
		params     map[string]interface{}
		wantErr    bool
		wantOutput string
	}{
		{
			name: "简单命令",
			tool: CustomTool{
				Command:    "echo {{.msg}}",
				AllowWrite: false,
				Timeout:    5,
			},
			params:     map[string]interface{}{"msg": "hello"},
			wantErr:    false,
			wantOutput: "hello\n",
		},
		{
			name: "写操作被拒绝",
			tool: CustomTool{
				Command:    "rm {{.file}}",
				AllowWrite: false,
				Timeout:    5,
			},
			params:  map[string]interface{}{"file": "test.txt"},
			wantErr: true,
		},
		{
			name: "写操作被允许",
			tool: CustomTool{
				Command:    "echo test > {{.file}}",
				AllowWrite: true,
				Timeout:    5,
			},
			params:  map[string]interface{}{"file": "/tmp/test_custom_tool.txt"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := executeShellTool(tt.tool, tt.params, logger)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.wantOutput != "" {
					assert.Equal(t, tt.wantOutput, output)
				}
			}
		})
	}
}

func TestExecuteHTTPTool(t *testing.T) {
	logger := zap.NewNop()

	// 创建测试 HTTP 服务器（绑定在 127.0.0.1）
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			w.Header().Set("X-Auth-Received", auth)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	// 注意：httptest.NewServer 绑定在 127.0.0.1，SSRF 防护会拦截内网地址
	// 以下测试验证 SSRF 防护对 httptest 服务器生效
	tests := []struct {
		name    string
		tool    CustomTool
		params  map[string]interface{}
		wantErr bool
		errMsg  string
		check   func(t *testing.T, output string)
	}{
		{
			name: "GET 请求被 SSRF 拦截（httptest 绑定 127.0.0.1）",
			tool: CustomTool{
				URL:     server.URL + "/test",
				Method:  "GET",
				Timeout: 5,
			},
			wantErr: true,
			errMsg:  "禁止访问内网地址",
		},
		{
			name: "带参数的 URL 被 SSRF 拦截",
			tool: CustomTool{
				URL:     server.URL + "/api?name={{.name}}",
				Method:  "GET",
				Timeout: 5,
			},
			params:  map[string]interface{}{"name": "test"},
			wantErr: true,
			errMsg:  "禁止访问内网地址",
		},
		{
			name: "带 Header 被 SSRF 拦截",
			tool: CustomTool{
				URL:    server.URL + "/api",
				Method: "GET",
				Headers: map[string]string{
					"Authorization": "Bearer {{env.TEST_TOKEN}}",
				},
				Timeout: 5,
			},
			wantErr: true,
			errMsg:  "禁止访问内网地址",
		},
	}

	// 设置测试环境变量
	os.Setenv("TEST_TOKEN", "test_token_123")
	defer os.Unsetenv("TEST_TOKEN")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := executeHTTPTool(tt.tool, tt.params, logger)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				if tt.check != nil {
					tt.check(t, output)
				}
			}
		})
	}
}

func TestRegisterCustomTool(t *testing.T) {
	setupTestExecutor(t)
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	tool := CustomTool{
		Name:        "test_custom",
		Description: "测试自定义工具",
		Type:        "shell",
		Command:     "echo {{.msg}}",
		Parameters: []ToolParameter{
			{Name: "msg", Type: "string", Description: "消息", Required: true},
		},
		Timeout: 5,
	}

	err := RegisterCustomTool(host, logger, tool)
	require.NoError(t, err)

	// 验证工具已注册
	tools := host.ListTools()
	found := false
	for _, td := range tools {
		if td.Name == "test_custom" {
			found = true
			assert.Equal(t, "测试自定义工具", td.Description)
			break
		}
	}
	assert.True(t, found, "工具应该已注册")

	// 测试工具执行
	input := json.RawMessage(`{"msg":"hello"}`)
	result, err := host.ExecuteTool(context.Background(), "test_custom", input)
	require.NoError(t, err)
	assert.False(t, result.IsError)

	var output string
	json.Unmarshal(result.Content, &output)
	assert.Equal(t, "hello\n", output)
}

func TestRegisterCustomTool_UnsupportedType(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	tool := CustomTool{
		Name:        "bad_tool",
		Description: "不支持的类型",
		Type:        "unsupported",
		Timeout:     5,
	}

	err := RegisterCustomTool(host, logger, tool)
	require.NoError(t, err)

	// 执行应该返回错误
	input := json.RawMessage(`{}`)
	result, err := host.ExecuteTool(context.Background(), "bad_tool", input)
	require.NoError(t, err)
	assert.True(t, result.IsError)

	var errMsg string
	json.Unmarshal(result.Content, &errMsg)
	assert.Contains(t, errMsg, "不支持的工具类型")
}

func TestIntegration_CustomToolWorkflow(t *testing.T) {
	setupTestExecutor(t)
	// 集成测试：完整的自定义工具加载和执行流程
	logger := zap.NewNop()
	tmpDir := t.TempDir()

	// 1. 创建工具定义文件
	tool := CustomTool{
		Name:        "echo_test",
		Description: "回显测试",
		Type:        "shell",
		Command:     "echo {{.msg}}",
		Parameters: []ToolParameter{
			{Name: "msg", Type: "string", Description: "消息", Default: "hello"},
		},
		AllowWrite: false,
		Timeout:    10,
	}
	data, _ := yaml.Marshal(tool)
	os.WriteFile(filepath.Join(tmpDir, "echo_test.yaml"), data, 0644)

	// 2. 加载工具
	tools, err := LoadCustomTools(tmpDir)
	require.NoError(t, err)
	require.Len(t, tools, 1)

	// 3. 注册工具
	host := mcphost.NewHost(logger)
	for _, tool := range tools {
		err := RegisterCustomTool(host, logger, tool)
		require.NoError(t, err)
	}

	// 4. 执行工具
	input := json.RawMessage(`{"msg":"test message"}`)
	result, err := host.ExecuteTool(context.Background(), "echo_test", input)
	require.NoError(t, err)
	assert.False(t, result.IsError)

	var output string
	json.Unmarshal(result.Content, &output)
	// 输出应该包含我们的消息
	if !strings.Contains(output, "test message") {
		t.Errorf("输出不包含预期消息，得到: %s", output)
	}
}

// ==================== 安全测试 ====================

func TestShellEscape(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"普通字符串", "hello", "'hello'"},
		{"包含空格", "hello world", "'hello world'"},
		{"包含单引号", "it's", "'it'\\''s'"},
		{"命令注入 - 分号", "; rm -rf /", "'; rm -rf /'"},
		{"命令注入 - 子命令", "$(curl evil.com)", "'$(curl evil.com)'"},
		{"命令注入 - 反引号", "`whoami`", "'`whoami`'"},
		{"命令注入 - 管道", "' || true '", "''\\'' || true '\\'''"},
		{"空字符串", "", "''"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shellEscape(tt.input)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestRenderTemplateEscaping(t *testing.T) {
	setupTestExecutor(t)
	logger := zap.NewNop()

	// 验证 shell escaping 模式下注入 payload 被正确转义
	tests := []struct {
		name    string
		tmpl    string
		params  map[string]interface{}
		wantSub string // 结果中应包含的子串
	}{
		{
			name:    "分号注入被转义",
			tmpl:    "echo {{.msg}}",
			params:  map[string]interface{}{"msg": "; rm -rf /"},
			wantSub: "'; rm -rf /'",
		},
		{
			name:    "子命令注入被转义",
			tmpl:    "echo {{.msg}}",
			params:  map[string]interface{}{"msg": "$(curl evil.com)"},
			wantSub: "'$(curl evil.com)'",
		},
		{
			name:    "反引号注入被转义",
			tmpl:    "echo {{.msg}}",
			params:  map[string]interface{}{"msg": "`whoami`"},
			wantSub: "'`whoami`'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := renderTemplate(tt.tmpl, tt.params, true)
			assert.Contains(t, result, tt.wantSub)
		})
	}

	// 验证 shell escaping 后命令执行安全
	t.Run("注入参数不会执行恶意命令", func(t *testing.T) {
		tool := CustomTool{
			Command:    "echo {{.msg}}",
			AllowWrite: false,
			Timeout:    5,
		}
		// 如果注入成功，会创建 /tmp/pwned 文件
		output, err := executeShellTool(tool, map[string]interface{}{"msg": "$(touch /tmp/pwned)"}, logger)
		assert.NoError(t, err)
		// 输出应该是字面量，而非执行结果
		assert.Contains(t, output, "$(touch /tmp/pwned)")
		// 确认恶意命令没有被执行
		_, statErr := os.Stat("/tmp/pwned")
		assert.True(t, os.IsNotExist(statErr), "/tmp/pwned 不应该被创建")
	})
}

func TestIsDomainAllowed(t *testing.T) {
	tests := []struct {
		name           string
		rawURL         string
		allowedDomains []string
		want           bool
	}{
		{"精确匹配", "https://api.example.com/path", []string{"api.example.com"}, true},
		{"精确不匹配", "https://evil.com/path", []string{"api.example.com"}, false},
		{"通配符匹配子域名", "https://foo.example.com/path", []string{"*.example.com"}, true},
		{"通配符不匹配根域名", "https://example.com/path", []string{"*.example.com"}, false},
		{"通配符不匹配其他域名", "https://evil.com/path", []string{"*.example.com"}, false},
		{"带端口精确匹配", "https://api.example.com:8080/path", []string{"api.example.com"}, true},
		{"空白名单", "https://any.com/path", []string{}, false},
		{"无效 URL", "not-a-url", []string{"example.com"}, false},
		{"多个白名单域名", "https://b.com/path", []string{"a.com", "b.com"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isDomainAllowed(tt.rawURL, tt.allowedDomains)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		name string
		host string
		want bool
	}{
		{"localhost", "localhost", true},
		{"127.0.0.1", "127.0.0.1", true},
		{"IPv6 loopback", "::1", true},
		{"10.x 内网", "10.0.0.1", true},
		{"172.16.x 内网", "172.16.0.1", true},
		{"192.168.x 内网", "192.168.1.1", true},
		{"AWS 元数据", "169.254.169.254", true},
		{"0.0.0.0 未指定", "0.0.0.0", true},
		{":: IPv6 未指定", "::", true},
		{"公网 IP", "8.8.8.8", false},
		{"公网域名", "example.com", false},
		{"Google 元数据", "metadata.google.internal", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPrivateIP(tt.host)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestExecuteHTTPToolSSRF(t *testing.T) {
	logger := zap.NewNop()

	ssrfURLs := []string{
		"http://127.0.0.1/admin",
		"http://localhost/secret",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.1/internal",
		"http://192.168.1.1/router",
	}

	for _, u := range ssrfURLs {
		t.Run(u, func(t *testing.T) {
			tool := CustomTool{
				URL:     u,
				Method:  "GET",
				Timeout: 5,
			}
			_, err := executeHTTPTool(tool, nil, logger)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "禁止访问内网地址")
		})
	}
}

func TestExecuteHTTPToolDomainWhitelist(t *testing.T) {
	logger := zap.NewNop()

	// 创建测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	t.Run("域名不在白名单中被拒绝", func(t *testing.T) {
		tool := CustomTool{
			URL:            "https://evil.com/steal",
			Method:         "GET",
			Timeout:        5,
			AllowedDomains: []string{"api.example.com"},
		}
		_, err := executeHTTPTool(tool, nil, logger)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "域名不在白名单中")
	})

	t.Run("白名单为空时允许公网域名（SSRF 仍会拦截内网）", func(t *testing.T) {
		// httptest.NewServer 绑定在 127.0.0.1，会被 SSRF 防护拦截
		// 这里验证白名单为空时不做域名检查，但 SSRF 检查仍然生效
		tool := CustomTool{
			URL:            server.URL + "/test",
			Method:         "GET",
			Timeout:        5,
			AllowedDomains: nil, // 空白名单
		}
		_, err := executeHTTPTool(tool, nil, logger)
		// 应该被 SSRF 拦截（因为 127.0.0.1 是内网地址），而非域名白名单
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "禁止访问内网地址")
	})
}

// ==================== create_tool 审批测试 ====================

// mockApprovalBridge 模拟审批桥接
type mockApprovalBridge struct {
	approved bool
	err      error
	called   bool
	details  map[string]string
}

func (m *mockApprovalBridge) RequestApproval(ctx context.Context, toolName, description string, details map[string]string) (bool, error) {
	m.called = true
	m.details = details
	return m.approved, m.err
}

func TestCreateToolRequiresApproval(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	cfg := &config.Config{
		Tools: config.ToolsConfig{CreateRequiresApproval: true},
	}
	bridge := &mockApprovalBridge{approved: true}

	registerCreateTool(host, logger, t.TempDir(), cfg, bridge)

	// 创建工具
	input, _ := json.Marshal(createToolInput{
		Name:        "approved_tool",
		Description: "需要审批的工具",
		Type:        "shell",
		Command:     "echo hello",
	})

	result, err := host.ExecuteTool(context.Background(), "create_tool", input)
	require.NoError(t, err)
	assert.False(t, result.IsError, "审批通过后工具应该创建成功")
	assert.True(t, bridge.called, "应该调用了审批桥接")
	assert.Equal(t, "approved_tool", bridge.details["name"])
}

func TestCreateToolApprovalDenied(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	cfg := &config.Config{
		Tools: config.ToolsConfig{CreateRequiresApproval: true},
	}
	bridge := &mockApprovalBridge{approved: false}

	registerCreateTool(host, logger, t.TempDir(), cfg, bridge)

	// 创建工具
	input, _ := json.Marshal(createToolInput{
		Name:        "denied_tool",
		Description: "被拒绝的工具",
		Type:        "shell",
		Command:     "echo hello",
	})

	result, err := host.ExecuteTool(context.Background(), "create_tool", input)
	require.NoError(t, err)
	assert.True(t, result.IsError, "审批被拒绝后应该返回错误")

	var errMsg string
	json.Unmarshal(result.Content, &errMsg)
	assert.Contains(t, errMsg, "用户拒绝创建工具")

	// 验证工具未被注册
	tools := host.ListTools()
	for _, td := range tools {
		assert.NotEqual(t, "denied_tool", td.Name, "被拒绝的工具不应该被注册")
	}
}

func TestCreateToolNoApprovalWhenDisabled(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	cfg := &config.Config{
		Tools: config.ToolsConfig{CreateRequiresApproval: false},
	}
	bridge := &mockApprovalBridge{approved: false} // 即使设为 false 也不应被调用

	registerCreateTool(host, logger, t.TempDir(), cfg, bridge)

	input, _ := json.Marshal(createToolInput{
		Name:        "no_approval_tool",
		Description: "不需要审批的工具",
		Type:        "shell",
		Command:     "echo hello",
	})

	result, err := host.ExecuteTool(context.Background(), "create_tool", input)
	require.NoError(t, err)
	assert.False(t, result.IsError, "不需要审批时应该直接创建成功")
	assert.False(t, bridge.called, "不应该调用审批桥接")
}

func TestCreateToolRejectsKnownHostToolFromRouterRegistry(t *testing.T) {
	logger := zap.NewNop()
	for _, name := range []string{"webfetch", "web_fetch", "read_file", "create_tool", "remove_tool", "lsp_diagnostics"} {
		t.Run(name, func(t *testing.T) {
			host := mcphost.NewHost(logger)
			registerCreateTool(host, logger, t.TempDir(), &config.Config{}, nil)

			input, _ := json.Marshal(createToolInput{
				Name:        name,
				Description: "attempt to shadow builtin tool",
				Type:        "http",
				URL:         "https://example.com",
			})

			result, err := host.ExecuteTool(context.Background(), "create_tool", input)
			require.NoError(t, err)
			require.True(t, result.IsError)
			assert.Contains(t, result.DecodeContent(), "不能覆盖内置工具: "+name)
		})
	}
}

func TestRemoveToolRejectsKnownHostToolFromRouterRegistry(t *testing.T) {
	logger := zap.NewNop()
	for _, name := range []string{"webfetch", "web_fetch", "read_file", "create_tool", "remove_tool", "lsp_diagnostics"} {
		t.Run(name, func(t *testing.T) {
			host := mcphost.NewHost(logger)
			registerRemoveTool(host, logger, t.TempDir())

			input, _ := json.Marshal(removeToolInput{Name: name})

			result, err := host.ExecuteTool(context.Background(), "remove_tool", input)
			require.NoError(t, err)
			require.True(t, result.IsError)
			assert.Contains(t, result.DecodeContent(), "不能删除内置工具: "+name)
		})
	}
}
