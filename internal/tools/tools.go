package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/lsp"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/memory"
	"github.com/chef-guo/agents-hive/internal/plugin"
	"github.com/chef-guo/agents-hive/internal/sandbox"
	"github.com/chef-guo/agents-hive/internal/search"
	"github.com/chef-guo/agents-hive/internal/taskboard"
)

var (
	// 全局 ShellPool、ReadTracker 和 FileTracker（简化版本，后续可扩展为 session-aware）
	globalShellPool   *ShellPool
	globalReadTracker *ReadTracker
	globalFileTracker *FileTracker // E3/E4: 追踪已编辑文件 hash，检测外部修改
	// 全局 LSP ServerManager
	globalLSPManager *lsp.ServerManager
	// 全局插件管理器（用于 ShellEnv hook 等）
	globalPluginMgr *plugin.Manager
	// 全局沙箱执行器（由 bootstrap 注入，所有命令执行路径委托给它）
	globalExecutor sandboxExecutor
	// 全局审批桥接（由 bootstrap 注入，用于 create_tool HITL 审批）
	globalApprovalBridge ApprovalBridge
	// 全局 HTTP 工具域名白名单（由 config 注入）
	globalAllowedDomains []string

	// 全局可插拔搜索引擎（由 RegisterBuiltinTools 初始化）
	globalGlobEngine search.GlobEngine
	globalGrepEngine search.GrepEngine

	// allowAllPaths 为 true 时禁用路径安全校验（通过环境变量 TOOLS_ALLOW_ALL_PATHS=true 开启）
	// 某些需要全路径访问的场景（如系统级工具）可使用此开关
	allowAllPaths = os.Getenv("TOOLS_ALLOW_ALL_PATHS") == "true"
)

// sandboxExecutor 直接使用 sandbox.Executor 接口。
type sandboxExecutor = sandbox.Executor

// SetExecutor 注入沙箱执行器（由 bootstrap 调用）。
func SetExecutor(exec sandboxExecutor) {
	globalExecutor = exec
}

// SetApprovalBridge 注入审批桥接（由 bootstrap 调用，用于 create_tool HITL 审批）。
func SetApprovalBridge(bridge ApprovalBridge) {
	globalApprovalBridge = bridge
}

// SetExecutorChecker 为 SafeExecutorWrapper 延迟注入安全策略检查器。
// 由 master 初始化安全规则后调用，确保 executor 主路径也经过安全检查。
func SetExecutorChecker(checker sandbox.SafeExecChecker) {
	if w, ok := globalExecutor.(*sandbox.SafeExecutorWrapper); ok {
		w.SetChecker(checker)
	}
}

const (
	// MaxToolOutputSize 工具输出的最大大小（10MB）
	// 超过此大小将触发截断，防止OOM
	MaxToolOutputSize = 10 * 1024 * 1024 // 10MB

	// ToolOutputWarnSize 工具输出的警告大小（5MB）
	// 超过此大小会在截断信息中显示警告
	ToolOutputWarnSize = 5 * 1024 * 1024 // 5MB

	// TruncateHeadSize 截断时保留的头部大小
	TruncateHeadSize = 5 * 1024 * 1024 // 5MB

	// TruncateTailSize 截断时保留的尾部大小
	TruncateTailSize = 1 * 1024 * 1024 // 1MB
)

const (
	// binaryDetectSize 二进制文件检测时读取的字节数
	binaryDetectSize = 4096 // 4KB
	// binaryNonPrintableThreshold 非打印字符占比超过此值判定为二进制
	binaryNonPrintableThreshold = 0.30
	// maxReadOutputSize ReadFile 输出最大字节数（50KB）
	maxReadOutputSize = 50 * 1024 // 50KB
)

// truncateOutput 截断过大的工具输出，防止OOM
// 保留前5MB和后1MB，中间显示省略信息
func truncateOutput(output string) string {
	size := len(output)
	if size <= MaxToolOutputSize {
		return output
	}

	// 计算省略的字节数
	omittedSize := size - TruncateHeadSize - TruncateTailSize

	// 构建截断消息
	truncateMsg := fmt.Sprintf(
		"\n\n... [输出过大，已截断 %d 字节（%.2f MB），仅显示前 %d MB 和后 %d MB] ...\n\n",
		omittedSize,
		float64(omittedSize)/(1024*1024),
		TruncateHeadSize/(1024*1024),
		TruncateTailSize/(1024*1024),
	)

	// 拼接：头部 + 截断消息 + 尾部
	head := output[:TruncateHeadSize]
	tail := output[size-TruncateTailSize:]

	return head + truncateMsg + tail
}

// SafeExecChecker 安全执行检查接口（避免直接依赖 security 包）
type SafeExecChecker interface {
	// MatchPolicy 返回命令的执行策略: "allow", "ask", "deny"
	MatchPolicy(command string) string
}

// 包级安全执行检查器
var globalSafeExec SafeExecChecker

// SetSafeExecutor 注入安全执行器
func SetSafeExecutor(exec SafeExecChecker) {
	globalSafeExec = exec
}

// RegisterBuiltinTools 注册所有内置工具到 MCP host
// cfg 为可选参数，如果提供则根据配置注册 LSP 工具
// questionBridge 为可选参数，如果提供则注册 question 工具
// taskExecutor 为可选参数，如果提供则注册 task 工具
// customToolsDir 为可选参数，如果提供则加载并注册自定义工具
// router 为可选参数，如果提供则注册 send_im_message 工具
// skillReg 为可选参数，如果提供则注册 skill 工具
// pluginMgr 为可选参数，预留给插件系统扩展
// agentSpawner 为可选参数，如果提供则注册 spawn_agent 工具
func RegisterBuiltinTools(host *mcphost.Host, logger *zap.Logger, cfg *config.Config, questionBridge QuestionBridge, taskExecutor TaskExecutor, customToolsDir string, router interface{}, skillRegI interface{}, pluginMgrI interface{}, wechatOpsI interface{}, memStore memory.MemoryStore, agentSpawnerI ...interface{}) {
	// 初始化 ShellPool、ReadTracker 和 FileTracker
	globalShellPool = NewShellPool()
	globalReadTracker = NewReadTracker(5 * time.Minute)
	globalFileTracker = NewFileTracker(0) // E3/E4: 默认 500 个文件上限

	// 注入全局 HTTP 域名白名单
	if cfg != nil && len(cfg.Tools.AllowedDomains) > 0 {
		globalAllowedDomains = cfg.Tools.AllowedDomains
	}

	// 初始化可插拔搜索引擎（优先使用高级实现）
	globalGlobEngine = search.NewDoublestarGlob()
	if globalExecutor != nil {
		globalGrepEngine = search.NewRipgrepEngine(globalExecutor)
	} else {
		// executor 未注入时使用 nil，registerGrep 内部会检查
		globalGrepEngine = nil
	}

	var agentSpawner AgentSpawner
	var todoStore SessionTodoStore
	var todoBroadcaster TodoSnapshotBroadcaster
	var nestedToolGate NestedToolGate
	var planRuntimeObserver PlanRuntimeObserver
	var board taskboard.TaskBoard
	for _, optional := range agentSpawnerI {
		if optional == nil {
			continue
		}
		if spawner, ok := optional.(AgentSpawner); ok && agentSpawner == nil {
			agentSpawner = spawner
		}
		if store, ok := optional.(SessionTodoStore); ok && todoStore == nil {
			todoStore = store
		}
		if broadcaster, ok := optional.(TodoSnapshotBroadcaster); ok && todoBroadcaster == nil {
			todoBroadcaster = broadcaster
		}
		if gate, ok := optional.(NestedToolGate); ok && nestedToolGate == nil {
			nestedToolGate = gate
		}
		if observer, ok := optional.(PlanRuntimeObserver); ok && planRuntimeObserver == nil {
			planRuntimeObserver = observer
		}
		if taskBoard, ok := optional.(taskboard.TaskBoard); ok && board == nil {
			board = taskBoard
		}
	}

	registerReadFile(host, logger, globalReadTracker)
	registerWriteFile(host, logger, globalReadTracker)
	registerGlob(host, logger)
	registerGrep(host, logger)
	registerBash(host, logger, globalShellPool)
	registerEdit(host, logger, globalReadTracker)
	registerLS(host, logger)
	registerMultiEdit(host, logger, globalReadTracker)
	// P0-B：websearch strict 模式受 QualityGuards.WebsearchStrict 控制。
	// 开关关闭时保持旧行为，开启后零结果会转为 IsError=true 触发 ReAct 重试。
	websearchStrict := false
	if cfg != nil {
		websearchStrict = cfg.Agent.QualityGuards.WebsearchStrict
	}
	// 生产路径：client=nil → registerWebSearch 内部实例化 defaultSearchClient，每个 host 独立。
	registerWebSearch(host, logger, websearchStrict, nil)
	registerWebFetch(host, logger)
	registerBrowserInteract(host, logger)
	registerBatch(host, logger, nestedToolGate)
	registerApplyPatch(host, logger)
	registerToolSearch(host, logger)
	registerCreateTool(host, logger, customToolsDir, cfg, globalApprovalBridge)
	registerRemoveTool(host, logger, customToolsDir)

	count := 16

	// 如果启用 LSP 且提供了配置，注册 LSP 工具
	if cfg != nil && cfg.LSP.Enabled {
		// 将 config.LSPConfig 转换为 lsp.LSPConfig
		lspCfg := lsp.LSPConfig{
			Enabled:        cfg.LSP.Enabled,
			MaxServers:     cfg.LSP.MaxServers,
			Timeout:        cfg.LSP.Timeout,
			HealthInterval: cfg.LSP.HealthInterval,
			Languages:      make(map[string]lsp.LanguageSpec),
		}
		for lang, spec := range cfg.LSP.Languages {
			lspCfg.Languages[lang] = lsp.LanguageSpec{
				Command:    spec.Command,
				Args:       spec.Args,
				Extensions: spec.Extensions,
				Disabled:   spec.Disabled,
			}
		}

		// 获取工作区根路径
		rootPath, _ := os.Getwd()

		// 创建 LSP ServerManager
		globalLSPManager = lsp.NewServerManager(lspCfg, rootPath, logger)
		lsp.RegisterTools(host, globalLSPManager, logger)
		count += 9
	}

	// 如果提供了 questionBridge，注册 question 工具
	if questionBridge != nil {
		registerQuestion(host, logger, questionBridge)
		count++
	}

	// 如果提供了 taskExecutor，注册 task 和 parallel_dispatch 工具
	if taskExecutor != nil {
		var delegationObserver DelegationObserver
		if o, ok := taskExecutor.(DelegationObserver); ok {
			delegationObserver = o
		}
		taskTimeout := time.Duration(0)
		spawnTimeout := time.Duration(0)
		if cfg != nil {
			taskTimeout = cfg.RuntimePolicy.TaskTimeout
			spawnTimeout = cfg.RuntimePolicy.SpawnAgentTimeout
		}

		registerTask(host, taskExecutor, logger, delegationObserver, taskTimeout)
		count++

		// 尝试将 taskExecutor 转换为 ParallelDispatchBroadcaster（Master 实现了此接口）
		var broadcaster ParallelDispatchBroadcaster
		if b, ok := taskExecutor.(ParallelDispatchBroadcaster); ok {
			broadcaster = b
		}
		registerParallelDispatch(host, taskExecutor, broadcaster, logger, delegationObserver, nestedToolGate)
		count++

		// 如果提供了 agentSpawner，注册 spawn_agent 工具
		if agentSpawner != nil {
			registerSpawnAgent(host, taskExecutor, agentSpawner, logger, delegationObserver, spawnTimeout)
			count++
		}
	}

	if todoStore != nil {
		registerTodoWrite(host, logger, todoStore, todoBroadcaster, planRuntimeObserver)
		registerPlanModeTools(host, logger, todoStore, todoBroadcaster, planRuntimeObserver)
		registerHandoffSummary(host, logger, todoStore)
		count += 5
		if board != nil {
			registerPromoteTodosToTaskboard(host, logger, todoStore, todoBroadcaster, board)
			count++
		}
	}

	// 如果提供了 router，注册 send_im_message 工具
	if router != nil {
		if imRouter, ok := router.(IMRouter); ok {
			var convStore wechatConversationLookup
			if st, ok := memStore.(wechatConversationLookup); ok {
				convStore = st
			}
			RegisterSendIMMessageWithStore(host, logger, imRouter, convStore)
			count++
		}
	}

	// 如果提供了 skillReg，注册 skill 工具
	if skillRegI != nil {
		registerSkillFromInterface(host, logger, skillRegI)
		count++
	}

	// 设置全局插件管理器（用于 ShellEnv hook）
	if pluginMgrI != nil {
		if mgr, ok := pluginMgrI.(*plugin.Manager); ok {
			globalPluginMgr = mgr
		}
	}

	_ = wechatOpsI // 旧微信操作工具已下线，保留参数避免破坏调用方签名。

	// 如果提供了 memoryStore，注册 memory 工具
	if memStore != nil {
		registerMemory(host, logger, memStore)
		count++
	}

	logger.Info("内置工具已注册", zap.Int("count", count))

	// 加载并注册自定义工具
	if customToolsDir != "" {
		customTools, err := LoadCustomTools(customToolsDir)
		if err != nil {
			logger.Warn("加载自定义工具失败", zap.Error(err))
		} else if len(customTools) > 0 {
			customCount := 0
			for _, tool := range customTools {
				if err := RegisterCustomTool(host, logger, tool); err != nil {
					logger.Warn("注册自定义工具失败",
						zap.String("tool", tool.Name),
						zap.Error(err))
				} else {
					logger.Debug("注册自定义工具成功", zap.String("tool", tool.Name))
					customCount++
				}
			}
			logger.Info("自定义工具已注册", zap.Int("count", customCount))
		}
	}

	// 插件 ToolDefinition hook：允许插件修改已注册工具的描述和 schema
	if globalPluginMgr != nil {
		applyToolDefinitionHooks(host, logger, globalPluginMgr)
	}
}

// RegisterQuestionTool 单独注册 question 工具（需要 QuestionBridge）
func RegisterQuestionTool(host *mcphost.Host, logger *zap.Logger, bridge QuestionBridge) {
	registerQuestion(host, logger, bridge)
	logger.Info("question 工具已注册")
}

// RegisterTaskTool 单独注册 task 工具（需要 TaskExecutor）
func RegisterTaskTool(host *mcphost.Host, logger *zap.Logger, executor TaskExecutor) {
	registerTask(host, executor, logger, nil, 0)
	logger.Info("task 工具已注册")
}

// Cleanup 清理所有工具资源（如 LSP 服务器）
func Cleanup(logger *zap.Logger) {
	if globalLSPManager != nil {
		logger.Info("停止所有 LSP 服务器")
		globalLSPManager.StopAll()
	}
}

// --- read_file ---

type readFileInput struct {
	Path            string `json:"path"`
	Offset          int    `json:"offset,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	ShowLineNumbers bool   `json:"show_line_numbers,omitempty"`
}

func registerReadFile(host *mcphost.Host, logger *zap.Logger, tracker *ReadTracker) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":              map[string]any{"type": "string", "description": "文件的绝对路径"},
			"offset":            map[string]any{"type": "integer", "description": "起始行号（从0开始）"},
			"limit":             map[string]any{"type": "integer", "description": "最大读取行数"},
			"show_line_numbers": map[string]any{"type": "boolean", "description": "是否在每行前显示原始文件行号（默认 false）"},
		},
		"required": []string{"path"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:              "read_file",
			Description:       "Read the contents of a file from disk. This is the primary tool for inspecting file content before editing, searching, or making decisions.\n\n**When to use**: Inspect any text file to understand its structure, content, or current state. Always read a file before editing it with the edit tool. Read files to understand the codebase, check existing implementations, verify test expectations, or gather context for writing new code.\n\n**Parameters**:\n- `path`: Absolute path to the file. Required.\n- `offset` (optional): Line number to start reading from (0-indexed). Use this to skip to a specific section of a large file.\n- `limit` (optional): Maximum number of lines to read. Use to avoid reading entire large files.\n- `show_line_numbers` (optional): When true, each line is prefixed with its line number in the format 'line│content'. This is the format expected by the edit tool for locating edits by line number. Recommended: always set this to true when reading code files.\n\n**Safety constraints**:\n- Device files (e.g., /dev/zero, /dev/random) are blocked to prevent system access.\n- Binary files are detected by both extension (.so, .a, .png, .jpg, .exe, etc.) and content inspection (non-printable character ratio > 30% in first 4KB). Binary files return an error rather than content.\n- Files larger than 50KB are truncated and a warning is returned. Use offset and limit parameters to read large files in sections.\n- Empty files return an empty result with a note.\n- UTF-16 LE BOM is automatically detected and content is decoded correctly.\n- PDF, image, and binary files return an error message explaining they cannot be read as text.\n\n**Relationship to other tools**:\n- Must be called before edit on any file (read-before-edit enforcement).\n- Use grep to search within files without reading the entire file.\n- Use glob to find files by name pattern before reading them.\n- Use write_file to create new files or overwrite entirely.\n\n**Output format**: Returns the file content as plain text. If show_line_numbers is true, lines are prefixed with 'line│' where line is the 1-based line number. Error responses include a descriptive message explaining the failure reason.",
			InputSchema:       schema,
			Core:              true,
			IsConcurrencySafe: true, // 只读无副作用，可并发执行
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params readFileInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}

			resolvedPath, err := resolveToolPath(params.Path)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			params.Path = resolvedPath

			// 检查文件是否存在并获取文件信息
			fileInfo, err := os.Stat(params.Path)
			if err != nil {
				return errorResult("读取失败: " + err.Error()), nil
			}

			// 图片/PDF 文件检测：转为 base64 data URI 返回给多模态模型
			mimeType, isImage, isPDF := detectMIMEType(params.Path)
			if isImage || isPDF {
				// 文件大小限制: 图片 10MB, PDF 20MB
				maxSize := 10 * 1024 * 1024 // 10MB
				if isPDF {
					maxSize = 20 * 1024 * 1024 // 20MB
				}
				if fileInfo.Size() > int64(maxSize) {
					return errorResult(fmt.Sprintf("文件过大 (%d 字节), 超过 %dMB 限制",
						fileInfo.Size(), maxSize/(1024*1024))), nil
				}

				data, err := os.ReadFile(params.Path)
				if err != nil {
					return errorResult("读取文件失败: " + err.Error()), nil
				}

				// 转为 base64 data URI
				b64 := base64.StdEncoding.EncodeToString(data)
				dataURI := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)

				// 记录文件读取
				if tracker != nil {
					tracker.RecordRead(params.Path)
				}

				return textResult(dataURI), nil
			}

			// 二进制扩展名快速检测（非图片/PDF 的二进制文件）
			if isBinaryByExtension(params.Path) {
				sizeKB := float64(fileInfo.Size()) / 1024.0
				ext := strings.ToLower(filepath.Ext(params.Path))
				return textResult(fmt.Sprintf("这是一个二进制文件 (扩展名: %s)，大小: %.1f KB", ext, sizeKB)), nil
			}

			// 二进制文件检测：读取前 4KB 进行检查
			isBinary, detectedType := detectBinaryFile(params.Path)
			if isBinary {
				sizeKB := float64(fileInfo.Size()) / 1024.0
				return textResult(fmt.Sprintf("这是一个二进制文件 (类型: %s)，大小: %.1f KB", detectedType, sizeKB)), nil
			}

			// 大文件保护：避免全量读入内存导致 OOM
			var data []byte
			if fileInfo.Size() > int64(maxReadOutputSize*2) {
				f, err := os.Open(params.Path)
				if err != nil {
					return errorResult("读取失败: " + err.Error()), nil
				}
				defer f.Close()
				limitBytes := int64(maxReadOutputSize + 1024)
				data, err = io.ReadAll(io.LimitReader(f, limitBytes))
				if err != nil {
					return errorResult("读取失败: " + err.Error()), nil
				}
			} else {
				var err error
				data, err = os.ReadFile(params.Path)
				if err != nil {
					return errorResult("读取失败: " + err.Error()), nil
				}
			}

			// 编码处理：非 UTF-8 文件尝试 GBK/GB18030 解码
			var content string
			if !utf8.Valid(data) {
				decoder := simplifiedchinese.GB18030.NewDecoder()
				decoded, _, gbkErr := transform.Bytes(decoder, data)
				if gbkErr == nil && utf8.Valid(decoded) {
					content = string(decoded)
				} else {
					content = string(data)
				}
			} else {
				content = string(data)
			}

			// 应用 offset/limit
			if params.Offset > 0 || params.Limit > 0 {
				lines := strings.Split(content, "\n")
				start := params.Offset
				if start > len(lines) {
					start = len(lines)
				}
				end := len(lines)
				if params.Limit > 0 && start+params.Limit < end {
					end = start + params.Limit
				}
				content = strings.Join(lines[start:end], "\n")
			}

			// 输出大小限制
			if len(content) > maxReadOutputSize {
				truncPos := maxReadOutputSize
				for truncPos > 0 && !utf8.RuneStart(content[truncPos]) {
					truncPos--
				}
				content = content[:truncPos] + fmt.Sprintf("\n\n[...输出已截断，原始大小: %.1f KB，限制: %d KB]",
					float64(len(data))/1024.0, maxReadOutputSize/1024)
			}

			// 行号：在 offset/limit 处理完成后添加，使用原始文件行号（1-based + offset）
			if params.ShowLineNumbers {
				lines := strings.Split(content, "\n")
				// 过滤末尾空行（strings.Split 在末尾换行符时产生空元素）
				if len(lines) > 0 && lines[len(lines)-1] == "" {
					lines = lines[:len(lines)-1]
				}
				var sb strings.Builder
				for i, line := range lines {
					fmt.Fprintf(&sb, "%6d\t%s\n", params.Offset+i+1, line)
				}
				content = sb.String()
			}

			// 记录文件读取
			if tracker != nil {
				tracker.RecordRead(params.Path)
			}

			return textResult(content), nil
		},
	)
}

// --- write_file ---

type writeFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func registerWriteFile(host *mcphost.Host, logger *zap.Logger, tracker *ReadTracker) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "文件的绝对路径"},
			"content": map[string]any{"type": "string", "description": "要写入的内容"},
		},
		"required": []string{"path", "content"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "write_file",
			Description: "Write or overwrite a file with the given content. Automatically creates any missing parent directories. Use this when creating a new file from scratch or completely replacing an existing file.\n\n**When to use**: Create brand new files that do not exist yet. Overwrite an entire file when the new content is completely different from the existing content. Use when edit is not suitable (e.g., the old content is unknown or too complex to match).\n\n**Parameters**:\n- `path`: Absolute path to the file to write. Required.\n- `content`: The complete content to write to the file. Required. This replaces the entire file content.\n\n**Relationship to other tools**:\n- Use edit instead of write_file when making targeted modifications to existing files. edit is safer and preserves more context.\n- Use write_file when creating new files or performing complete rewrites.\n- Do not use write_file to create README.md or other documentation files unless explicitly requested.\n\n**Safety constraints**:\n- This tool completely overwrites the file. Any existing content is lost.\n- No undo or backup is created automatically. Use edit if you need to preserve the original.\n- Large file writes are supported but use with caution on files over 1MB.\n- Automatically creates parent directories if they do not exist.\n\n**Output format**: Returns a success message confirming the file was written, or an error message describing what went wrong.",
			InputSchema: schema,
			Core:        true,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params writeFileInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}

			resolvedPath, err := resolveToolPath(params.Path)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			params.Path = resolvedPath

			// 获取文件锁，序列化对同一文件的并发写入
			unlock := globalFileLock.Lock(params.Path)
			defer unlock()

			// 检查文件是否存在
			_, statErr := os.Stat(params.Path)
			fileExists := statErr == nil

			// 如果文件已存在，检查是否最近读取过
			if fileExists && tracker != nil {
				if err := tracker.CheckRead(params.Path); err != nil {
					return errorResult(err.Error()), nil
				}
			}

			// 创建父目录（权限 0o700：仅拥有者可读写执行，防止目录内容泄漏）
			if dir := filepath.Dir(params.Path); dir != "" {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return errorResult("创建目录失败: " + err.Error()), nil
				}
			}

			// 对新创建的文件使用 0o600（仅拥有者可读写）；
			// 已存在文件：先写入临时内容再保持原权限
			fileMode := os.FileMode(0o600)
			if fileExists {
				// 获取已有文件的权限位，写入后保持不变
				if info, err := os.Stat(params.Path); err == nil {
					fileMode = info.Mode().Perm()
				}
			}
			if err := os.WriteFile(params.Path, []byte(params.Content), fileMode); err != nil {
				return errorResult("写入失败: " + err.Error()), nil
			}

			// E4: 写入成功后记录文件 hash，用于后续外部修改检测
			if globalFileTracker != nil {
				_ = globalFileTracker.Track(params.Path)
			}

			// 写入成功后获取 LSP 诊断
			diagInfo := fetchLSPDiagnostics(params.Path, 2*time.Second)
			resultMsg := fmt.Sprintf("已写入 %d 字节到 %s", len(params.Content), params.Path)
			if diagInfo != "" {
				resultMsg += "\n\n" + diagInfo
			}
			return textResult(resultMsg), nil
		},
	)
}

// --- glob ---

type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

func registerGlob(host *mcphost.Host, logger *zap.Logger) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Glob 模式（如 **/*.go、src/**/*.ts），支持 ** 递归匹配"},
			"path":    map[string]any{"type": "string", "description": "搜索基础目录（默认当前目录）"},
		},
		"required": []string{"pattern"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:              "glob",
			Description:       "Find files by glob pattern matching. Use this to locate files by name or extension patterns before reading or editing them.\n\n**When to use**: Find all files matching a naming pattern (e.g., all .go files, all test files, all files in a specific directory). Locate files you want to read or modify. Discover the structure of a codebase.\n\n**Parameters**:\n- `pattern`: Glob pattern to match file names against. Examples: '*.go' (all Go files), '**/*.ts' (all TypeScript files recursively), 'src/**/*.js' (JS files in src directory). The ** pattern matches zero or more directories. Required.\n- `path` (optional): Base directory to search from. Defaults to the current working directory.\n\n**Relationship to other tools**:\n- Use glob to find files, then use read_file to read them.\n- Use grep to search inside file contents.\n- If you need to find files by content rather than name, use grep instead.\n\n**Safety constraints**:\n- Results are limited to 100 files by default to prevent excessive output.\n- Use more specific patterns to narrow down results.\n\n**Output format**: Returns a list of matching file paths, one per line. Error responses explain why the search failed.",
			InputSchema:       schema,
			Core:              true,
			IsConcurrencySafe: true, // 只读无副作用，可并发执行
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params globInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}

			baseDir := params.Path
			if baseDir == "" {
				baseDir = "."
			}

			resolvedBaseDir, err := resolveToolPath(baseDir)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			baseDir = resolvedBaseDir

			if globalGlobEngine == nil {
				return errorResult("搜索引擎未初始化"), nil
			}

			matches, err := globalGlobEngine.Glob(ctx, params.Pattern, baseDir)
			if err != nil {
				return errorResult("搜索失败: " + err.Error()), nil
			}

			if len(matches) == 0 {
				return textResult("未找到匹配文件"), nil
			}
			return textResult(strings.Join(matches, "\n")), nil
		},
	)
}

// --- grep ---

type grepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	TypeFilter string `json:"type,omitempty"`
	Context    int    `json:"context,omitempty"`
	Before     int    `json:"before,omitempty"`
	After      int    `json:"after,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
	Multiline  bool   `json:"multiline,omitempty"`
}

func registerGrep(host *mcphost.Host, logger *zap.Logger) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern":     map[string]any{"type": "string", "description": "正则表达式搜索模式"},
			"path":        map[string]any{"type": "string", "description": "搜索文件或目录"},
			"glob":        map[string]any{"type": "string", "description": "文件过滤模式（如 *.go）"},
			"type":        map[string]any{"type": "string", "description": "按文件类型过滤（如 go、ts、py）"},
			"context":     map[string]any{"type": "integer", "description": "前后上下文行数（-C）"},
			"before":      map[string]any{"type": "integer", "description": "匹配前上下文行数（-B）"},
			"after":       map[string]any{"type": "integer", "description": "匹配后上下文行数（-A）"},
			"max_results": map[string]any{"type": "integer", "description": "每个文件的最大匹配数（0 表示不限制）"},
			"multiline":   map[string]any{"type": "boolean", "description": "跨行匹配模式"},
		},
		"required": []string{"pattern"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:              "grep",
			Description:       "Search for text patterns within files using regular expressions. Use this to find where a function is defined, where a variable is used, or any text content across one or more files.\n\n**When to use**: Find occurrences of a string, identifier, function name, or pattern across multiple files. Locate all places where a specific feature is implemented. Search for TODO comments, error messages, or debug statements.\n\n**Parameters**:\n- `pattern`: Regular expression to search for. Supports full regex syntax including character classes ([a-z]), quantifiers (*, +, ?), anchors (^, $), and alternation (|). Required.\n- `path` (optional): Directory or file path to search in. Defaults to the current working directory.\n- `output_mode` (optional): Format of the output. 'content' (default) shows matching lines with context. 'files' shows only file names containing matches. 'count' shows only the count per file.\n- `head_limit` (optional): Maximum number of matching lines to return across all files. Defaults to 250. Use this to avoid overwhelming output on large codebases.\n- `file_type` (optional): Filter by file type/extension. Example: 'go', 'ts', 'py'.\n- `max_results` (optional): Maximum number of matches per file. 0 means unlimited. Default is 0.\n- `context` (optional): Number of lines of surrounding context to include for each match.\n- `multiline` (optional): When true, allows ^ and $ to match at start/end of each line within a multiline string. Default is false.\n\n**Relationship to other tools**:\n- Use grep to find content, then use read_file or edit to work with those files.\n- Use glob to find files by name first, then grep within those results.\n- Use grep instead of read_file when you only need to find where something appears, not the full file content.\n\n**Safety constraints**:\n- Paths are validated against directory traversal attacks.\n- Binary files are automatically excluded.\n\n**Output format**: Returns matching lines with the file path and line number prefix. Format depends on output_mode. Error responses explain search failures.",
			InputSchema:       schema,
			Core:              true,
			IsConcurrencySafe: true, // 只读无副作用，可并发执行
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params grepInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}

			searchPath := params.Path
			if searchPath == "" {
				searchPath = "."
			}

			resolvedSearchPath, err := resolveToolPath(searchPath)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			searchPath = resolvedSearchPath

			if globalGrepEngine == nil {
				return errorResult("搜索引擎未初始化"), nil
			}

			result, err := globalGrepEngine.Grep(ctx, search.GrepRequest{
				Pattern:    params.Pattern,
				Path:       searchPath,
				GlobFilter: params.Glob,
				TypeFilter: params.TypeFilter,
				Context:    params.Context,
				Before:     params.Before,
				After:      params.After,
				MaxResults: params.MaxResults,
				Multiline:  params.Multiline,
			})
			if err != nil {
				return errorResult(err.Error()), nil
			}

			if len(result.Matches) == 0 {
				return textResult("未找到匹配"), nil
			}

			// 格式化输出：保持与原 grep -rn 兼容的格式
			var sb strings.Builder
			for _, m := range result.Matches {
				fmt.Fprintf(&sb, "%s:%d:%s\n", m.File, m.Line, m.Content)
			}
			return textResult(truncateOutput(sb.String())), nil
		},
	)
}

// --- bash ---

type bashInput struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // 秒
	WorkDir string `json:"work_dir,omitempty"`
}

func registerBash(host *mcphost.Host, logger *zap.Logger, pool *ShellPool) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":  map[string]any{"type": "string", "description": "要执行的 Shell 命令"},
			"timeout":  map[string]any{"type": "integer", "description": "超时时间（秒，默认30）"},
			"work_dir": map[string]any{"type": "string", "description": "命令的工作目录"},
		},
		"required": []string{"command"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "bash",
			Description: "Execute a shell command in a persistent bash session. This is the most powerful and dangerous tool. Use it when no specialized tool exists for your task, or when you need to run system commands, git operations, npm scripts, compilers, or any other command-line tool.\n\n**When to use**: Run shell commands that cannot be done with other tools: git operations (commit, push, branch, diff), npm/yarn/pnpm package management, running tests and build tools (go build, npm run, make), compiling code, installing dependencies, running linters or formatters, exploring directory structure with ls, checking file sizes, running database migrations, executing scripts, and any other command-line operations.\n\n**Parameters**:\n- `command`: The shell command to execute. Required. Can be any valid bash command or pipeline.\n- `timeout` (optional): Maximum execution time in seconds. Default is 60 seconds. Set higher for long-running operations like builds or network requests.\n- `work_dir` (optional): Working directory for the command. If not specified, uses the current working directory.\n\n**Session behavior**: Commands run in a persistent bash session that maintains state between calls. Environment variables, working directory changes, and shell variables persist. This means you can cd into a directory in one call and subsequent calls will stay in that directory.\n\n**Safety constraints - read-only vs write operations**:\n- Commands that only read data (git status, ls, cat, grep, find, echo) are generally safe.\n- Commands that modify files or system state (git commit, npm install, mkdir, rm, mv, cp, tee) are write operations and may be subject to security policy checks.\n- DANGEROUS commands requiring elevated caution: 'rm -rf' (recursive delete), 'dd' (raw disk write), ':(){:|:&};:' (fork bomb), commands that overwrite system files.\n- Network commands (curl, wget) may require explicit permission depending on security configuration.\n- Commands using pipe or redirection operators are supported but be careful with overwrite redirection (>).\n\n**Git operations**:\n- This environment enforces git security protocols. Commands that could lose uncommitted work may be blocked.\n- git add, git commit, git push, git branch, git checkout, git merge, git rebase are all supported.\n- git status and git diff are always permitted (read-only).\n\n**Sed and text editing**:\n- sed is supported but prefer the edit tool for structured file modifications.\n- If using sed with in-place editing (-i flag), be aware that it modifies files directly.\n\n**Best practices**:\n- Always check the exit code and output to verify the command succeeded.\n- Use absolute paths when possible to avoid working directory confusion.\n- Prefer specialized tools (edit, read_file, write_file) over shell commands for file operations.\n- Avoid long-running commands in tight loops. Use timeout parameter for long operations.\n- Do not use sleep in polling loops. Check for file existence using test conditions instead.\n\n**Output format**: Returns stdout and stderr as separate fields, plus the exit code (0 = success). On timeout or cancellation, partial output may be returned.",
			InputSchema: schema,
			Core:        true,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params bashInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}

			// 安全策略检查：在执行前检查命令策略
			if globalSafeExec != nil {
				policy := globalSafeExec.MatchPolicy(params.Command)
				switch policy {
				case "deny":
					return errorResult("命令被安全策略拒绝: " + params.Command), nil
				case "ask":
					if globalApprovalBridge == nil {
						return errorResult("命令需要审批但审批系统未初始化: " + params.Command), nil
					}
					approved, err := globalApprovalBridge.RequestApproval(ctx, "bash",
						"执行命令需要审批",
						map[string]string{"command": params.Command},
					)
					if err != nil {
						return errorResult("审批请求失败: " + err.Error()), nil
					}
					if !approved {
						return errorResult("命令审批被拒绝: " + params.Command), nil
					}
				}
			}

			// 插件 ShellEnv hook：获取插件注入的环境变量
			envMap := buildShellEnvMap(ctx, params.Command, params.WorkDir, logger)

			// 沙箱执行器必须已初始化（由 bootstrap/CLI 注入）
			if globalExecutor == nil {
				return errorResult("沙箱执行器未初始化，无法执行命令"), nil
			}

			timeout := 60 * time.Second
			if params.Timeout > 0 {
				timeout = time.Duration(params.Timeout) * time.Second
			}

			result, err := globalExecutor.Execute(ctx, sandbox.ExecRequest{
				Command:   params.Command,
				SessionID: "default",
				Timeout:   timeout,
				WorkDir:   params.WorkDir,
				Env:       envMap,
			})
			if err != nil {
				if result.Diagnostic != nil {
					return commandFailureToolResult(result), nil
				}
				return errorResult(fmt.Sprintf("执行错误: %v", err)), nil
			}

			if result.ExitCode != 0 {
				return commandFailureToolResult(result), nil
			}

			return textResult(truncateOutput(formatCommandOutput(result))), nil
		},
	)
}

// --- edit ---

type editInput struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

func registerEdit(host *mcphost.Host, logger *zap.Logger, tracker *ReadTracker) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":        map[string]any{"type": "string", "description": "文件的绝对路径"},
			"old_string":  map[string]any{"type": "string", "description": "要查找替换的原始文本"},
			"new_string":  map[string]any{"type": "string", "description": "替换后的文本"},
			"replace_all": map[string]any{"type": "boolean", "description": "是否替换所有匹配（默认 false）"},
		},
		"required": []string{"path", "old_string", "new_string"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "edit",
			Description: "Edit a file by replacing an exact string match. This is the primary tool for modifying existing file content. Use this when you know the exact text that needs to change and want to replace it with new content.\n\n**When to use**: Modify specific lines or sections in a file. Make targeted changes to function bodies, variable values, imports, comments, or any textual content. Ideal for small to medium changes where you can identify the exact surrounding context.\n\n**Parameters**:\n- `path`: Absolute path to the file. Required.\n- `old_string`: The exact text to find and replace. Must match the file content character-for-character, including all whitespace, indentation, and line breaks. This tool locates the edit by finding this exact string, so precision is critical.\n- `new_string`: The replacement text. Can be a single line or multiple lines. The tool will substitute old_string with this value verbatim.\n- `replace_all` (optional): When true, replaces ALL occurrences of old_string in the file. Default is false (replaces only the first match). Use when you need to change the same text in multiple places at once.\n\n**Relationship to other tools**:\n- Use write_file when creating entirely new files or completely rewriting a file from scratch.\n- Use apply_patch when applying unified diff patches or making multi-file changes atomically.\n- Prefer edit over write_file whenever possible, as it is safer (shows exact change) and preserves file metadata.\n\n**Safety constraints**:\n- UTF-16 BOM files are automatically detected and handled.\n- Binary files (detected by extension or content) are rejected.\n- Files larger than 1GiB are rejected to prevent memory issues.\n- .ipynb (Jupyter notebook) files are rejected.\n- The tool enforces a read-before-edit policy: you must have read the file in this session before editing it. This prevents edits to stale or unrelated files.\n- If the file was modified externally since you last read it, the edit will be rejected. Re-read the file and retry.\n- old_string equal to new_string is rejected as a no-op.\n\n**Output format**: Returns a success message with the number of replacements made, or an error message describing what went wrong.",
			InputSchema: schema,
			Core:        true,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params editInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}

			resolvedPath, err := resolveToolPath(params.Path)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			params.Path = resolvedPath

			// 获取文件锁，序列化对同一文件的并发编辑
			unlock := globalFileLock.Lock(params.Path)
			defer unlock()

			// 检查文件是否最近读取过
			if tracker != nil {
				if err := tracker.CheckRead(params.Path); err != nil {
					return errorResult(err.Error()), nil
				}
			}

			// E3: 检测文件自上次编辑后是否被外部修改（仅已追踪的文件才检查）
			if globalFileTracker != nil {
				if changed, err := globalFileTracker.HasChanged(params.Path); err == nil && changed {
					return errorResult("文件自上次编辑后已被外部修改，请重新读取后再编辑"), nil
				}
			}

			// 读取文件前先获取原有权限，写回时保持一致（已存在文件不改变权限）
			fileInfo, err := os.Stat(params.Path)
			if err != nil {
				return errorResult("获取文件信息失败: " + err.Error()), nil
			}
			origPerm := fileInfo.Mode().Perm()

			data, err := os.ReadFile(params.Path)
			if err != nil {
				return errorResult("读取失败: " + err.Error()), nil
			}

			content := string(data)

			// 确定实际用于替换的 oldString（支持模糊匹配降级）
			actualOldString := params.OldString
			fuzzyNote := ""
			if !strings.Contains(content, params.OldString) {
				// 精确匹配失败，尝试模糊查找
				found, level, ok := FuzzyFindString(content, params.OldString, logger)
				if !ok {
					return errorResult("未在文件中找到 old_string（精确匹配和模糊匹配均失败）"), nil
				}
				actualOldString = found
				fuzzyNote = fmt.Sprintf("（通过「%s」匹配成功）", matchLevelNames[level])
			}

			var newContent string
			if params.ReplaceAll {
				newContent = strings.ReplaceAll(content, actualOldString, params.NewString)
			} else {
				count := strings.Count(content, actualOldString)
				if count > 1 {
					return errorResult(fmt.Sprintf("old_string 出现 %d 次——请使用 replace_all 或提供更多上下文", count)), nil
				}
				newContent = strings.Replace(content, actualOldString, params.NewString, 1)
			}

			// 写回时使用原有权限，保持已存在文件的权限位不变
			if err := os.WriteFile(params.Path, []byte(newContent), origPerm); err != nil {
				return errorResult("写入失败: " + err.Error()), nil
			}

			// E4: 编辑成功后记录文件 hash，用于后续外部修改检测
			if globalFileTracker != nil {
				_ = globalFileTracker.Track(params.Path) // 忽略错误（非关键路径）
			}

			// 写入成功后获取 LSP 诊断
			diagInfo := fetchLSPDiagnostics(params.Path, 2*time.Second)
			resultMsg := "编辑已成功应用" + fuzzyNote
			if diagInfo != "" {
				resultMsg += "\n\n" + diagInfo
			}
			return textResult(resultMsg), nil
		},
	)
}

// --- 辅助函数 ---

// fetchLSPDiagnostics 获取文件的 LSP 诊断信息
// 返回格式化的诊断字符串，如果无诊断或 LSP 未启用则返回空字符串
// timeout 控制最大等待时间，避免阻塞工具返回
func fetchLSPDiagnostics(filePath string, timeout time.Duration) string {
	// LSP 未启用
	if globalLSPManager == nil {
		return ""
	}

	// 检查文件类型是否有对应的语言服务器
	langID := globalLSPManager.LanguageIDForFile(filePath)
	if langID == "" {
		return ""
	}

	// 设置超时上下文
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 获取语言服务器
	server, err := globalLSPManager.GetServerForFile(ctx, filePath)
	if err != nil {
		return ""
	}

	// 获取诊断信息
	diagnostics, err := server.GetFileDiagnostics(ctx, filePath, langID)
	if err != nil || len(diagnostics) == 0 {
		return ""
	}

	// 过滤：仅保留 Error(1) 和 Warning(2)
	var filtered []lsp.Diagnostic
	for _, d := range diagnostics {
		if d.Severity == 1 || d.Severity == 2 {
			filtered = append(filtered, d)
		}
	}
	if len(filtered) == 0 {
		return ""
	}

	// 按严重程度排序（Error > Warning）
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Severity < filtered[j].Severity
	})

	// 格式化输出
	var sb strings.Builder
	sb.WriteString("📋 LSP 诊断:\n")
	for _, d := range filtered {
		severity := "Warning"
		if d.Severity == 1 {
			severity = "Error"
		}
		sb.WriteString(fmt.Sprintf("  [%s] 行 %d: %s", severity, d.Range.Start.Line+1, d.Message))
		if d.Source != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", d.Source))
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// validatePath 校验路径安全性，防止路径穿越攻击（包括符号链接绕过）。
//
// 校验逻辑：
//  1. 通过 filepath.Clean 消除 ../ 等相对路径片段，得到候选绝对路径
//  2. 对工作目录做 filepath.EvalSymlinks，解析其真实物理路径，防止工作目录本身含 symlink
//  3. 对目标路径做 filepath.EvalSymlinks，解析所有符号链接得到真实路径；
//     若解析失败（文件尚不存在，如 write_file 写新文件），则退回到 Clean 后的路径继续校验
//  4. 对解析后的真实路径做 HasPrefix 检查，确保不超出允许的工作目录
//
// 如果环境变量 TOOLS_ALLOW_ALL_PATHS=true，则跳过所有校验。
func validatePath(p string) error {
	// 全路径访问模式：跳过校验
	if allowAllPaths {
		return nil
	}

	// 空路径不允许
	if p == "" {
		return fmt.Errorf("路径不能为空")
	}

	// 获取允许的工作目录根：优先读取环境变量，否则使用当前进程工作目录
	allowedRoot := os.Getenv("TOOLS_WORK_DIR")
	if allowedRoot == "" {
		var err error
		allowedRoot, err = os.Getwd()
		if err != nil {
			// 无法获取工作目录时放行，避免误伤
			return nil
		}
	}

	// 对工作目录本身做符号链接解析，防止工作目录包含 symlink 导致前缀匹配失效
	if resolved, err := filepath.EvalSymlinks(allowedRoot); err == nil {
		allowedRoot = resolved
	}
	// 规范化工作目录路径（EvalSymlinks 已返回 Clean 路径，此处保险起见再次 Clean）
	allowedRoot = filepath.Clean(allowedRoot)

	// 将目标路径规范化为绝对路径（消除 ../ 等相对路径片段）
	absPath := filepath.Clean(p)
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(allowedRoot, absPath)
	}
	absPath = filepath.Clean(absPath)

	// 解析目标路径的符号链接，得到真实物理路径。
	// 若文件尚不存在（如 write_file 写入新文件），EvalSymlinks 会返回错误，
	// 此时使用 Clean 后的 absPath 继续做校验，保证写新文件不受影响。
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = resolved
	}

	// 确保路径位于允许的工作目录内（前缀匹配，末尾加 "/" 防止 /foo 匹配 /foobar）
	prefix := allowedRoot
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	// 允许路径等于工作目录本身，或者是其子路径
	if absPath != allowedRoot && !strings.HasPrefix(absPath, prefix) {
		return fmt.Errorf("路径安全校验失败：%q 超出允许的工作目录 %q", p, allowedRoot)
	}

	return nil
}

// resolveToolPath 将工具输入路径解析到当前工作目录内。
// 兼容模型把仓库根相对路径误写成 "/README.md" 的情况：
// - 对于工作区内文件，"/foo/bar" 会被解释为 "<workdir>/foo/bar"
// - 对于真实系统绝对路径（如 /etc/passwd），仍然拒绝
func resolveToolPath(p string) (string, error) {
	if allowAllPaths {
		if p == "" {
			return "", fmt.Errorf("路径不能为空")
		}
		return p, nil
	}

	if p == "" {
		return "", fmt.Errorf("路径不能为空")
	}

	allowedRoot := os.Getenv("TOOLS_WORK_DIR")
	if allowedRoot == "" {
		var err error
		allowedRoot, err = os.Getwd()
		if err != nil {
			return "", nil
		}
	}
	if resolved, err := filepath.EvalSymlinks(allowedRoot); err == nil {
		allowedRoot = resolved
	}
	allowedRoot = filepath.Clean(allowedRoot)

	candidate := filepath.Clean(p)
	if filepath.IsAbs(candidate) {
		if candidate == string(filepath.Separator) || candidate == filepath.VolumeName(candidate)+string(filepath.Separator) {
			return "", fmt.Errorf("路径安全校验失败：%q 超出允许的工作目录 %q", p, allowedRoot)
		}

		trimmed := strings.TrimPrefix(candidate, string(filepath.Separator))
		workspaceRelative := filepath.Clean(filepath.Join(allowedRoot, trimmed))
		if !looksLikeSystemAbsolutePath(trimmed) && (workspaceRelative == allowedRoot || strings.HasPrefix(workspaceRelative, allowedRoot+string(filepath.Separator))) && workspacePathExists(workspaceRelative) {
			return workspaceRelative, nil
		}

		if err := validatePath(candidate); err != nil {
			return "", err
		}
		return candidate, nil
	}

	return filepath.Clean(filepath.Join(allowedRoot, candidate)), nil
}

func looksLikeSystemAbsolutePath(path string) bool {
	first, _, _ := strings.Cut(filepath.ToSlash(path), "/")
	switch first {
	case "Applications", "Library", "Network", "System", "Users", "Volumes",
		"bin", "boot", "dev", "etc", "home", "lib", "lib64", "opt",
		"private", "proc", "root", "run", "sbin", "sys", "tmp", "usr", "var":
		return true
	default:
		return false
	}
}

func workspacePathExists(path string) bool {
	if _, err := os.Lstat(path); err == nil {
		return true
	}
	return false
}

func textResult(text string) *mcphost.ToolResult {
	return &mcphost.ToolResult{Content: jsonText(text)}
}

func errorResult(msg string) *mcphost.ToolResult {
	return &mcphost.ToolResult{Content: jsonText(msg), IsError: true}
}

func jsonText(text string) json.RawMessage {
	data, _ := json.Marshal(text)
	return data
}

// 常见图片扩展名 → MIME 类型映射
var imageExtensions = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
}

// PDF 扩展名 → MIME 类型映射
var pdfExtensions = map[string]string{
	".pdf": "application/pdf",
}

// detectMIMEType 根据扩展名检测文件 MIME 类型
// 返回 (MIME 类型, 是否为图片, 是否为 PDF)
func detectMIMEType(path string) (mimeType string, isImage bool, isPDF bool) {
	ext := strings.ToLower(filepath.Ext(path))
	if mime, ok := imageExtensions[ext]; ok {
		return mime, true, false
	}
	if mime, ok := pdfExtensions[ext]; ok {
		return mime, false, true
	}
	return "", false, false
}

// 常见二进制文件扩展名（非图片/PDF）
var binaryExtensions = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".7z": true, ".rar": true,
	".bin": true, ".dat": true, ".db": true, ".sqlite": true,
	".wasm": true, ".pyc": true, ".class": true, ".o": true, ".a": true,
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wav": true,
	".ttf": true, ".otf": true, ".woff": true, ".woff2": true,
	".jar": true, ".war": true, ".ear": true,
}

// isBinaryByExtension 通过扩展名快速判断是否为二进制文件
func isBinaryByExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return binaryExtensions[ext]
}

// detectBinaryFile 检测文件是否为二进制文件。
// 读取前 4KB，先尝试 UTF-8，再尝试 GBK/GB18030，最后按非打印字符占比判断。
// 返回 (是否二进制, 检测到的 MIME 类型)。
func detectBinaryFile(path string) (bool, string) {
	f, err := os.Open(path)
	if err != nil {
		return false, ""
	}
	defer f.Close()

	buf := make([]byte, binaryDetectSize)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return false, ""
	}
	buf = buf[:n]

	// 使用 http.DetectContentType 检测 MIME 类型
	mimeType := http.DetectContentType(buf)

	// null 字节：大概率是二进制，直接返回（GBK 合法文本不含 null）
	for _, b := range buf {
		if b == 0 {
			return true, mimeType
		}
	}

	// 非打印字符占比检查（优先于编码检测）：控制字符多的直接判为二进制
	// 注意：只统计真正的控制字符（< 0x20），不把无效 UTF-8 字节（GBK 高字节）计入
	ctrlChars := 0
	totalChars := 0
	for i := 0; i < n; {
		r, size := utf8.DecodeRune(buf[i:])
		if r != utf8.RuneError && r < 32 && r != '\n' && r != '\r' && r != '\t' {
			ctrlChars++
		}
		totalChars++
		i += size
	}
	if totalChars > 0 && float64(ctrlChars)/float64(totalChars) > binaryNonPrintableThreshold {
		return true, mimeType
	}

	// 合法 UTF-8 且控制字符不多：作为文本处理
	if utf8.Valid(buf) {
		return false, mimeType
	}

	// 非 UTF-8：尝试 GBK/GB18030 解码（覆盖中文场景）
	decoder := simplifiedchinese.GB18030.NewDecoder()
	decoded, _, gbkErr := transform.Bytes(decoder, buf)
	if gbkErr == nil && utf8.Valid(decoded) {
		return false, mimeType
	}

	return false, mimeType
}

// applyToolDefinitionHooks 对所有已注册工具触发 ToolDefinition hook
// 允许插件修改工具的描述和参数 schema
func applyToolDefinitionHooks(host *mcphost.Host, logger *zap.Logger, mgr *plugin.Manager) {
	allTools := host.ListTools()
	modified := 0
	for _, t := range allTools {
		input := &plugin.ToolDefinitionInput{
			Name:        t.Name,
			Description: t.Description,
			ArgsSchema:  t.InputSchema,
		}
		if err := mgr.TriggerToolDefinition(context.Background(), input); err != nil {
			logger.Warn("插件 ToolDefinition hook 失败",
				zap.String("tool", t.Name),
				zap.Error(err))
			continue
		}
		// 检查插件是否修改了工具定义
		descChanged := input.Description != t.Description
		schemaChanged := string(input.ArgsSchema) != string(t.InputSchema)
		if descChanged || schemaChanged {
			if err := host.UpdateToolDefinition(t.Name, input.Description, input.ArgsSchema); err != nil {
				logger.Warn("更新工具定义失败",
					zap.String("tool", t.Name),
					zap.Error(err))
			} else {
				modified++
				logger.Debug("插件已修改工具定义", zap.String("tool", t.Name))
			}
		}
	}
	if modified > 0 {
		logger.Info("插件已修改工具定义", zap.Int("count", modified))
	}
}

// buildShellEnvMap 获取插件注入的环境变量 map，直接传给 Executor。
func buildShellEnvMap(ctx context.Context, command, workDir string, logger *zap.Logger) map[string]string {
	if globalPluginMgr == nil {
		return nil
	}
	input := &plugin.ShellEnvInput{
		Command: command,
		WorkDir: workDir,
	}
	envMap, err := globalPluginMgr.TriggerShellEnv(ctx, input)
	if err != nil {
		logger.Warn("插件 ShellEnv hook 失败", zap.Error(err))
		return nil
	}
	return envMap
}

// buildShellEnvExports 构建环境变量导出命令
// 将插件注入的环境变量转换为 export 命令前缀
func buildShellEnvExports(ctx context.Context, command, workDir string, logger *zap.Logger) string {
	if globalPluginMgr == nil {
		return ""
	}

	input := &plugin.ShellEnvInput{
		Command: command,
		WorkDir: workDir,
	}
	envMap, err := globalPluginMgr.TriggerShellEnv(ctx, input)
	if err != nil {
		logger.Warn("插件 ShellEnv hook 失败", zap.Error(err))
		return ""
	}
	if len(envMap) == 0 {
		return ""
	}

	// 构建 export 命令
	var exports strings.Builder
	for k, v := range envMap {
		// 使用单引号包裹值，防止 shell 注入
		exports.WriteString(fmt.Sprintf("export %s='%s'\n", k, strings.ReplaceAll(v, "'", "'\\''")))
	}
	return exports.String()
}
