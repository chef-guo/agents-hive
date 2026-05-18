package tools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/airouter"
	"github.com/chef-guo/agents-hive/internal/mcphost"
)

// generateImageID 生成随机图片文件名（16字节十六进制，使用 crypto/rand 不引入新依赖）
func generateImageID() string {
	b := make([]byte, 16)
	rand.Read(b) //nolint:errcheck // crypto/rand.Read 在所有支持平台不会失败
	return fmt.Sprintf("%x", b)
}

// RegisterImageGen registers the generate_image tool.
// serverBaseURL 是服务器对外可访问的 base URL（如 "http://localhost:18080"），
// 用于构造保存到本地的图片的完整访问地址，以便 WenYan MCP 等外部工具能 fetch。
func RegisterImageGen(host *mcphost.Host, router *airouter.Router, logger *zap.Logger, serverBaseURL string) {
	if router == nil {
		return
	}

	tool := mcphost.ToolDefinition{
		Name:        "generate_image",
		Description: "根据文字描述生成图片。支持 DALL-E、即梦等多种图片生成模型。**重要**：图片生成成功后，必须使用 Markdown 图片语法将图片嵌入回复：`![图片描述](url)`，不要只输出 URL 文本。",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"prompt": {
					"type": "string",
					"description": "图片的详细描述（英文效果更佳）"
				},
				"size": {
					"type": "string",
					"enum": ["1024x1024", "1792x1024", "1024x1792"],
					"description": "图片尺寸，默认 1024x1024"
				},
				"quality": {
					"type": "string",
					"enum": ["standard", "hd", "auto"],
					"description": "图片质量，默认 auto"
				},
				"style": {
					"type": "string",
					"enum": ["vivid", "natural"],
					"description": "图片风格（仅 DALL-E 3 支持）"
				}
			},
			"required": ["prompt"]
		}`),
	}

	handler := func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		var params struct {
			Prompt  string `json:"prompt"`
			Size    string `json:"size"`
			Quality string `json:"quality"`
			Style   string `json:"style"`
		}
		if err := json.Unmarshal(input, &params); err != nil {
			return errorResult(fmt.Sprintf("参数解析失败: %v", err)), nil
		}

		if params.Prompt == "" {
			return errorResult("prompt 不能为空"), nil
		}

		resp, err := router.Execute(ctx, airouter.ServiceRequest{
			Type: airouter.ServiceImageGen,
			Params: map[string]any{
				"prompt":  params.Prompt,
				"size":    params.Size,
				"quality": params.Quality,
				"style":   params.Style,
			},
		})
		if err != nil {
			return errorResult(fmt.Sprintf("图片生成失败: %v", err)), nil
		}

		// Return URL or base64 content
		result := map[string]any{}

		// 优先处理二进制数据（Gemini inlineData / Imagen）：保存到临时目录，返回 HTTP URL
		if len(resp.Data) > 0 {
			ext := ".jpg"
			if resp.MimeType == "image/png" {
				ext = ".png"
			} else if resp.MimeType == "image/webp" {
				ext = ".webp"
			}
			tmpDir := filepath.Join(os.TempDir(), "hive-images")
			if mkErr := os.MkdirAll(tmpDir, 0755); mkErr == nil {
				filename := generateImageID() + ext
				imgPath := filepath.Join(tmpDir, filename)
				if writeErr := os.WriteFile(imgPath, resp.Data, 0644); writeErr == nil {
					result["url"] = serverBaseURL + "/api/images/" + filename
					result["message"] = "图片已生成，请使用 ![图片描述](url) 语法将图片嵌入回复"
				} else {
					result["error"] = fmt.Sprintf("图片保存失败: %v", writeErr)
				}
			} else {
				result["error"] = fmt.Sprintf("临时目录创建失败: %v", mkErr)
			}
		} else if resp.URL != "" && !strings.HasPrefix(resp.URL, "data:") {
			// 正常 HTTP URL（DALL-E、即梦等）
			result["url"] = resp.URL
			result["message"] = "图片已生成，请使用 ![图片描述](url) 语法将图片嵌入回复"
		} else if resp.URL != "" {
			// 兜底：data URI（理论上不再走到这里）
			result["url"] = resp.URL
			result["message"] = "图片已生成，请使用 ![图片描述](url) 语法将图片嵌入回复"
		}
		if resp.Metadata != nil {
			if rp, ok := resp.Metadata["revised_prompt"].(string); ok && rp != "" {
				result["revised_prompt"] = rp
			}
		}

		resultJSON, _ := json.Marshal(result)
		return textResult(string(resultJSON)), nil
	}

	host.RegisterTool(tool, handler)
	logger.Info("generate_image 工具已注册")
}
