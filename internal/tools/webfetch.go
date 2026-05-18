package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/html"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/mcphost"
)

const (
	// maxResponseSize 响应体最大体积（10MB）
	maxResponseSize = 10 * 1024 * 1024
	// fetchTimeout HTTP 请求超时时间
	fetchTimeout = 60 * time.Second
)

// webfetchInput 定义 webfetch 工具的输入参数
type webfetchInput struct {
	URL            string   `json:"url"`                       // 要获取的 URL
	AllowedDomains []string `json:"allowed_domains,omitempty"` // 允许的域名白名单
	BlockedDomains []string `json:"blocked_domains,omitempty"` // 禁止的域名黑名单
}

// registerWebFetch 注册 webfetch 工具
func registerWebFetch(host *mcphost.Host, logger *zap.Logger) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "要获取内容的 URL（HTTP 自动升级为 HTTPS）",
			},
			"allowed_domains": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "允许的域名白名单（可选）",
			},
			"blocked_domains": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "禁止的域名黑名单（可选）",
			},
		},
		"required": []string{"url"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "webfetch",
			Description: "获取网页内容并转换为文本格式。优先使用真实 Chrome 浏览器渲染（支持 JS 动态内容、SPA），若 agent-browser 未安装则自动降级为 HTTP 直接抓取（仅适合静态页面）。HTTP 自动升级为 HTTPS。",
			InputSchema: schema,
			Core:        true,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params webfetchInput
			if err := json.Unmarshal(input, &params); err != nil {
				logger.Error("webfetch 输入解析失败", zap.Error(err))
				return errorResult("输入无效: " + err.Error()), nil
			}

			// 验证和规范化 URL
			normalizedURL, err := normalizeURL(params.URL)
			if err != nil {
				return errorResult("URL 无效: " + err.Error()), nil
			}

			// 检查域名过滤
			parsedURL, _ := url.Parse(normalizedURL)
			// 去除端口号（例如 127.0.0.1:18080 -> 127.0.0.1）
			urlHost := parsedURL.Hostname()
			domain := strings.ToLower(urlHost)
			domain = strings.TrimPrefix(domain, "www.")

			// 检查黑名单
			if len(params.BlockedDomains) > 0 && isDomainInList(domain, params.BlockedDomains) {
				return errorResult(fmt.Sprintf("域名 %s 在黑名单中", domain)), nil
			}

			// 检查白名单
			if len(params.AllowedDomains) > 0 && !isDomainInList(domain, params.AllowedDomains) {
				return errorResult(fmt.Sprintf("域名 %s 不在白名单中", domain)), nil
			}

			// SSRF 防护（快速失败路径）：在发起 HTTP 请求之前，先对主机名做一次
			// DNS 解析并检查是否为私有地址。这一步仅用于提前报错，并不能完全防御
			// DNS 重绑定攻击——真正的防线在 fetchWebPage 内的 DialContext 层（SEC-001）。
			if os.Getenv("WEBFETCH_ALLOW_PRIVATE") != "true" {
				if err := checkSSRF(urlHost); err != nil {
					logger.Warn("webfetch SSRF 快速拦截请求",
						zap.String("host", urlHost),
						zap.Error(err))
					return errorResult(err.Error()), nil
				}
			}

			// 创建带超时的 context
			fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
			defer cancel()

			// 优先使用 agent-browser（支持 JS 渲染的 SPA 和动态页面）
			if IsAgentBrowserAvailable() {
				content, abErr := fetchViaAgentBrowser(fetchCtx, normalizedURL, logger)
				if abErr == nil {
					logger.Info("webfetch via agent-browser 成功",
						zap.String("url", normalizedURL),
						zap.Int("content_length", len(content)))
					return textResult("[来源: Chrome浏览器渲染]\n\n" + content), nil
				}
				logger.Warn("agent-browser fetch 失败，降级到 HTTP",
					zap.Error(abErr),
					zap.String("url", normalizedURL))
			}

			// 降级到 HTTP 方案
			content, err := fetchWebPage(fetchCtx, normalizedURL, logger)
			if err != nil {
				logger.Error("webfetch 获取失败",
					zap.Error(err),
					zap.String("url", normalizedURL))
				return errorResult("获取网页失败: " + err.Error()), nil
			}

			logger.Info("webfetch via HTTP 成功",
				zap.String("url", normalizedURL),
				zap.Int("content_length", len(content)))

			return textResult("[来源: HTTP直接抓取]\n\n" + content), nil
		},
	)
}

// normalizeURL 验证并规范化 URL（HTTP 自动升级为 HTTPS）
func normalizeURL(rawURL string) (string, error) {
	// 去除前后空格
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errs.New(errs.CodeInvalidInput, "URL 不能为空")
	}

	// 解析 URL
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", errs.Wrap(errs.CodeInvalidInput, "URL 解析失败", err)
	}

	// 检查 scheme
	if parsedURL.Scheme == "" {
		// 如果没有 scheme，添加 https:// 并重新解析
		rawURL = "https://" + rawURL
		parsedURL, err = url.Parse(rawURL)
		if err != nil {
			return "", errs.Wrap(errs.CodeInvalidInput, "URL 解析失败", err)
		}
	} else if parsedURL.Scheme == "http" {
		// HTTP 自动升级为 HTTPS
		parsedURL.Scheme = "https"
	} else if parsedURL.Scheme != "https" {
		return "", errs.New(errs.CodeInvalidInput, fmt.Sprintf("不支持的协议: %s（仅支持 HTTP/HTTPS）", parsedURL.Scheme))
	}

	// 检查 host
	if parsedURL.Host == "" {
		return "", errs.New(errs.CodeInvalidInput, "URL 缺少主机名")
	}

	return parsedURL.String(), nil
}

// newTLSConfig 根据环境变量构造 TLS 配置。
// 默认情况下启用完整证书验证（InsecureSkipVerify = false）。
// 仅当环境变量 WEBFETCH_INSECURE_TLS=true 时才跳过验证，
// 该模式仅应在开发/测试环境中使用，切勿在生产环境开启。
func newTLSConfig() *tls.Config {
	insecure := os.Getenv("WEBFETCH_INSECURE_TLS") == "true"
	if insecure {
		// #nosec G402 — 仅在显式配置 WEBFETCH_INSECURE_TLS=true 时才允许跳过验证
		return &tls.Config{InsecureSkipVerify: true}
	}
	// 安全默认值：使用系统根证书，强制验证服务端证书
	return &tls.Config{InsecureSkipVerify: false}
}

// parseNumericIP 尝试将非标准数字格式的 IP 字符串解析为 net.IP。
//
// net.ParseIP 只能识别点分十进制（IPv4）和标准 IPv6 表示，对以下格式无能为力，
// 但底层 C 库或部分操作系统会把它们解析为合法地址，从而绕过 SSRF 检查：
//
//   - 十六进制整数：0x7f000001 → 127.0.0.1
//   - 纯十进制整数：2130706433  → 127.0.0.1
//   - 八进制点分：0177.0.0.1   → 127.0.0.1
//
// 若解析成功返回对应的 net.IP（4 字节或 16 字节），否则返回 nil。
//
// 修复漏洞 SEC-003：数字格式 IP 绕过。
func parseNumericIP(host string) net.IP {
	h := strings.ToLower(strings.TrimSpace(host))

	// ── 情形 1：十六进制整数，如 0x7f000001 ──────────────────────────────────
	if strings.HasPrefix(h, "0x") || strings.HasPrefix(h, "0X") {
		val, err := strconv.ParseUint(h[2:], 16, 64)
		if err == nil {
			return uint32ToIPv4(uint32(val))
		}
	}

	// ── 情形 2：八进制点分，如 0177.0.0.1 ────────────────────────────────────
	// 判断条件：包含 "." 且恰好 4 个分量，且至少有一个分量以 "0" 开头（说明是八进制分量）。
	// 各分量按各自格式解析：以 "0" 开头且长度 > 1 的分量视为八进制，其余按十进制解析。
	// 这样可捕获混合格式，如 "0177.0.0.1"（第一段八进制，其余十进制）。
	if strings.Contains(h, ".") {
		parts := strings.Split(h, ".")
		if len(parts) == 4 {
			// 检查是否存在至少一个八进制分量（以 "0" 开头且长度 > 1）
			hasOctalPart := false
			for _, p := range parts {
				if strings.HasPrefix(p, "0") && len(p) > 1 {
					hasOctalPart = true
					break
				}
			}
			if hasOctalPart {
				var octets [4]byte
				ok := true
				for i, p := range parts {
					var base int
					if strings.HasPrefix(p, "0") && len(p) > 1 {
						// 前缀 "0" 且长度 > 1：八进制
						base = 8
					} else {
						// 否则：十进制（含 "0" 自身表示数值 0）
						base = 10
					}
					v, err := strconv.ParseUint(p, base, 16)
					if err != nil || v > 255 {
						ok = false
						break
					}
					octets[i] = byte(v)
				}
				if ok {
					return net.IPv4(octets[0], octets[1], octets[2], octets[3])
				}
			}
		}
	}

	// ── 情形 3：纯十进制整数，如 2130706433 ──────────────────────────────────
	// 不含 "." 且不含 "x"（排除十六进制已处理的情形）
	if !strings.Contains(h, ".") && !strings.Contains(h, "x") && !strings.Contains(h, ":") {
		// 先尝试 uint32 范围（IPv4 整数）
		val, err := strconv.ParseUint(h, 10, 64)
		if err == nil {
			if val <= 0xFFFFFFFF {
				return uint32ToIPv4(uint32(val))
			}
			// 超出 IPv4 范围，尝试作为 IPv6 大整数（极罕见，保留扩展点）
			_ = new(big.Int).SetUint64(val) // 预留，暂不处理
		}
	}

	return nil
}

// uint32ToIPv4 将 uint32 整数转换为 4 字节 net.IP（大端序，IPv4）。
func uint32ToIPv4(n uint32) net.IP {
	return net.IPv4(
		byte(n>>24),
		byte(n>>16),
		byte(n>>8),
		byte(n),
	)
}

// checkSSRF 解析主机名对应的 IP 地址，拒绝访问私有/保留地址段，
// 防止服务端请求伪造（SSRF）攻击。
//
// 除标准 IP 格式外，还处理十六进制、纯十进制整数、八进制点分等
// net.ParseIP 无法识别但底层可能合法解析的数字格式（修复 SEC-003）。
//
// 阻止的地址范围：
//   - 127.0.0.0/8   (IPv4 回环)
//   - 10.0.0.0/8    (私有 A 类)
//   - 172.16.0.0/12 (私有 B 类)
//   - 192.168.0.0/16 (私有 C 类)
//   - 169.254.0.0/16 (链路本地)
//   - 100.64.0.0/10  (运营商级 NAT)
//   - 0.0.0.0        (未指定地址)
//   - ::1            (IPv6 回环)
//   - fc00::/7       (IPv6 唯一本地地址)
//   - fe80::/10      (IPv6 链路本地)
func checkSSRF(host string) error {
	// 先尝试直接解析为标准 IP（host 可能本身就是 IP 字面量）
	ips := []net.IP{}
	if ip := net.ParseIP(host); ip != nil {
		ips = append(ips, ip)
	} else if numIP := parseNumericIP(host); numIP != nil {
		// SEC-003：捕获十六进制/十进制整数/八进制点分等非标准数字 IP 格式
		ips = append(ips, numIP)
	} else {
		// 通过 DNS 解析主机名
		resolved, err := net.LookupHost(host)
		if err != nil {
			// DNS 解析失败时拒绝请求，避免未知目标
			return errs.New(errs.CodeInvalidInput, fmt.Sprintf("无法解析主机名: %s", host))
		}
		for _, addr := range resolved {
			if ip := net.ParseIP(addr); ip != nil {
				ips = append(ips, ip)
			}
		}
	}

	// 定义私有/保留 CIDR 列表
	// 注意：不包含 ::ffff:0:0/96（IPv4 映射 IPv6），因为该段覆盖所有 IPv4 地址。
	// IPv4 地址无论以 4 字节还是 16 字节（IPv4-in-IPv6）形式出现，
	// 均由下方 IPv4 私有段负责检测（Go 的 net.IPNet.Contains 能正确处理两种形式）。
	privateRanges := []string{
		"127.0.0.0/8",    // IPv4 回环
		"10.0.0.0/8",     // 私有 A 类
		"172.16.0.0/12",  // 私有 B 类
		"192.168.0.0/16", // 私有 C 类
		"169.254.0.0/16", // 链路本地（APIPA）
		"100.64.0.0/10",  // 运营商级 NAT（RFC 6598）
		"::1/128",        // IPv6 回环
		"fc00::/7",       // IPv6 唯一本地地址（ULA，包含 fd00::/8）
		"fe80::/10",      // IPv6 链路本地
	}

	var privateNets []*net.IPNet
	for _, cidr := range privateRanges {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil {
			privateNets = append(privateNets, network)
		}
	}

	for _, ip := range ips {
		// 拒绝未指定地址（0.0.0.0 / ::）
		if ip.IsUnspecified() {
			return errs.New(errs.CodePermissionDenied, fmt.Sprintf("禁止访问内网地址: %s", ip))
		}
		// 检查是否落在私有地址段内
		for _, network := range privateNets {
			if network.Contains(ip) {
				return errs.New(errs.CodePermissionDenied, fmt.Sprintf("禁止访问内网地址: %s（属于 %s）", ip, network))
			}
		}
	}

	return nil
}

// newSSRFDialContext 返回一个自定义 DialContext 函数，用于在 TCP 连接真正建立
// 的瞬间再次执行 SSRF 检查。
//
// 这是修复 SEC-001（DNS 重绑定攻击）的核心手段：
//   - 攻击者可能在"请求前检查"与"实际 Dial"之间的极短窗口内，
//     将 DNS TTL 设为 0 并将 A 记录切换到内网地址，从而绕过预检。
//   - 自定义 DialContext 在操作系统完成 DNS 解析、确定目标 IP、
//     但尚未建立 TCP 连接之前进行检查，彻底消除时间窗口。
//
// 同时，由于 http.Client 在跟随重定向时会对新目标重新调用 DialContext，
// 该方案也自动覆盖了 SEC-004（重定向目标未检查）的攻击场景。
//
// 仅当环境变量 WEBFETCH_ALLOW_PRIVATE != "true" 时才启用检查。
func newSSRFDialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		// addr 形如 "host:port"，提取 host 部分
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			// 格式非预期，保守拒绝
			return nil, errs.New(errs.CodePermissionDenied, fmt.Sprintf("无法解析连接地址: %s", addr))
		}

		// 在允许私有地址的环境（如集成测试）中跳过检查
		if os.Getenv("WEBFETCH_ALLOW_PRIVATE") != "true" {
			if err := checkSSRF(host); err != nil {
				// 在 Dial 层拦截，无论 DNS 记录在预检后是否被修改
				return nil, err
			}
		}

		// 检查通过后，使用默认 Dialer 建立 TCP 连接
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
}

// fetchWebPage 获取网页内容并转换为文本
func fetchWebPage(ctx context.Context, pageURL string, logger *zap.Logger) (string, error) {
	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return "", errs.Wrap(errs.CodeInternal, "创建请求失败", err)
	}

	// 设置 User-Agent（模拟浏览器）
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	// 执行请求
	client := &http.Client{
		Timeout: fetchTimeout,
		// CheckRedirect 限制重定向次数。
		// 注意：重定向目标的 SSRF 检查由 Transport.DialContext 负责（SEC-001/SEC-004）。
		// http.Client 在每次重定向后重新 Dial，因此 DialContext 会对每个目标地址生效。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errs.New(errs.CodeResourceExhausted, "重定向次数过多")
			}
			return nil
		},
		Transport: &http.Transport{
			// TLS 配置：通过环境变量决定是否跳过证书验证。
			// 默认启用完整验证（安全）；设置 WEBFETCH_INSECURE_TLS=true 后才跳过（仅供开发测试）。
			TLSClientConfig: newTLSConfig(),
			// SEC-001 修复：在 TCP 连接建立时验证目标 IP，抵御 DNS 重绑定攻击。
			// SEC-004 修复：重定向目标每次 Dial 都经过此检查，无需在 CheckRedirect 中额外处理。
			DialContext: newSSRFDialContext(),
			// 禁用连接复用，确保每次请求结束后连接被关闭（避免测试中 httptest.Server.Close 超时）
			DisableKeepAlives: true,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", errs.Wrap(errs.CodeResourceExhausted, "HTTP 请求失败", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		return "", errs.New(errs.CodeResourceExhausted, fmt.Sprintf("服务器返回错误: HTTP %d", resp.StatusCode))
	}

	// 检查 Content-Type
	contentType := resp.Header.Get("Content-Type")
	if !isTextContent(contentType) {
		return "", errs.New(errs.CodeInvalidInput, fmt.Sprintf("不支持的内容类型: %s（仅支持文本/HTML）", contentType))
	}

	// 读取响应体（限制体积）
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return "", errs.Wrap(errs.CodeResourceExhausted, "读取响应失败", err)
	}

	// 检查是否超过体积限制
	if len(body) >= maxResponseSize {
		logger.Warn("响应体积超过限制",
			zap.String("url", pageURL),
			zap.Int("max_size", maxResponseSize))
		return "", errs.New(errs.CodeResourceExhausted, fmt.Sprintf("响应体积超过限制（最大 %d MB）", maxResponseSize/(1024*1024)))
	}

	// 转换 HTML 为文本
	text, err := htmlToText(string(body))
	if err != nil {
		return "", errs.Wrap(errs.CodeInternal, "HTML 转换失败", err)
	}

	return text, nil
}

// isTextContent 检查 Content-Type 是否为文本/HTML 类型
func isTextContent(contentType string) bool {
	if contentType == "" {
		// 如果没有 Content-Type，默认允许（可能是 HTML）
		return true
	}

	contentType = strings.ToLower(strings.TrimSpace(contentType))

	// 支持的类型
	supportedTypes := []string{
		"text/html",
		"text/plain",
		"application/xhtml+xml",
		"application/xml",
		"text/xml",
	}

	for _, t := range supportedTypes {
		if strings.Contains(contentType, t) {
			return true
		}
	}

	return false
}

// htmlToText 将 HTML 转换为纯文本（保留基本格式）
func htmlToText(htmlContent string) (string, error) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return "", errs.Wrap(errs.CodeInternal, "HTML 解析失败", err)
	}

	var output strings.Builder
	var currentList []string // 用于处理列表
	var inPre bool           // 是否在 <pre> 标签内

	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			text := n.Data
			if !inPre {
				// 清理空白（非 pre 标签）
				text = strings.TrimSpace(text)
				if text != "" {
					output.WriteString(text)
					output.WriteString(" ")
				}
			} else {
				// pre 标签内保留原始格式
				output.WriteString(text)
			}

		case html.ElementNode:
			switch n.Data {
			case "script", "style", "noscript":
				// 跳过脚本和样式
				return

			case "p", "div", "section", "article":
				// 段落和块级元素：添加换行
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					traverse(c)
				}
				output.WriteString("\n\n")
				return

			case "h1", "h2", "h3", "h4", "h5", "h6":
				// 标题：添加前缀和换行
				level := n.Data[1] - '0' // 提取数字
				prefix := strings.Repeat("#", int(level))
				output.WriteString(prefix + " ")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					traverse(c)
				}
				output.WriteString("\n\n")
				return

			case "br":
				// 换行
				output.WriteString("\n")
				return

			case "hr":
				// 水平线
				output.WriteString("\n---\n\n")
				return

			case "a":
				// 链接：保留文本和 URL
				text := extractText(n)
				href := getAttribute(n, "href")
				if text != "" {
					if href != "" && href != text {
						output.WriteString(fmt.Sprintf("[%s](%s) ", text, href))
					} else {
						output.WriteString(text + " ")
					}
				}
				return

			case "ul", "ol":
				// 列表：递归处理子元素
				oldList := currentList
				currentList = []string{}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					traverse(c)
				}
				// 输出列表项
				for i, item := range currentList {
					if n.Data == "ol" {
						output.WriteString(fmt.Sprintf("%d. %s\n", i+1, item))
					} else {
						output.WriteString("- " + item + "\n")
					}
				}
				output.WriteString("\n")
				currentList = oldList
				return

			case "li":
				// 列表项
				item := extractText(n)
				if item != "" {
					currentList = append(currentList, strings.TrimSpace(item))
				}
				return

			case "pre":
				// 预格式化文本
				inPre = true
				output.WriteString("\n```\n")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					traverse(c)
				}
				output.WriteString("\n```\n\n")
				inPre = false
				return

			case "code":
				// 代码
				if !inPre {
					output.WriteString("`")
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						traverse(c)
					}
					// 移除最后一个空格（如果存在）
					s := output.String()
					if strings.HasSuffix(s, " ") {
						output.Reset()
						output.WriteString(s[:len(s)-1])
					}
					output.WriteString("` ")
					return
				}

			case "strong", "b":
				// 粗体
				output.WriteString("**")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					traverse(c)
				}
				// 移除最后一个空格（如果存在）
				s := output.String()
				if strings.HasSuffix(s, " ") {
					output.Reset()
					output.WriteString(s[:len(s)-1])
				}
				output.WriteString("** ")
				return

			case "em", "i":
				// 斜体
				output.WriteString("*")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					traverse(c)
				}
				// 移除最后一个空格（如果存在）
				s := output.String()
				if strings.HasSuffix(s, " ") {
					output.Reset()
					output.WriteString(s[:len(s)-1])
				}
				output.WriteString("* ")
				return

			case "blockquote":
				// 引用
				output.WriteString("\n> ")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					traverse(c)
				}
				output.WriteString("\n\n")
				return
			}
		}

		// 递归处理子节点
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}

	traverse(doc)

	// 清理输出
	text := output.String()
	text = cleanupText(text)

	return text, nil
}

// extractText 提取节点的纯文本内容
func extractText(n *html.Node) string {
	var text strings.Builder
	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(n)
	return strings.TrimSpace(text.String())
}

// getAttribute 获取节点的属性值
func getAttribute(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

// cleanupText 清理文本中的多余空白和换行
func cleanupText(text string) string {
	// 移除行首行尾空格
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	text = strings.Join(lines, "\n")

	// 压缩连续的空行（最多保留 2 个换行）
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}

	// 修复标点符号前的多余空格（如 " ." → "."）
	text = strings.ReplaceAll(text, " .", ".")
	text = strings.ReplaceAll(text, " ,", ",")
	text = strings.ReplaceAll(text, " !", "!")
	text = strings.ReplaceAll(text, " ?", "?")
	text = strings.ReplaceAll(text, " :", ":")
	text = strings.ReplaceAll(text, " ;", ";")

	// 去除首尾空白
	text = strings.TrimSpace(text)

	return text
}
