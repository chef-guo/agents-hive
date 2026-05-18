package config

import (
	"strings"
	"time"
)

// AssetConfig 统一对象存储配置（asset:// URI）。
type AssetConfig struct {
	Provider string          `json:"provider,omitempty"`
	Local    AssetLocalConfig `json:"local,omitempty"`
	MinIO    AssetS3Config   `json:"minio,omitempty"`
	S3       AssetS3Config   `json:"s3,omitempty"`
}

// AssetLocalConfig 本地文件存储。
type AssetLocalConfig struct {
	BasePath string `json:"base_path,omitempty"`
}

// AssetS3Config MinIO / S3 兼容存储。
type AssetS3Config struct {
	Endpoint  string `json:"endpoint,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Region    string `json:"region,omitempty"`
	UseSSL    bool   `json:"use_ssl,omitempty"`
}

// FileConvConfig KB ingest 文档转 Markdown 配置。
type FileConvConfig struct {
	Markdown MarkdownConversionConfig `json:"markdown,omitempty"`
}

// MarkdownConversionConfig Markdown 转换子配置。
type MarkdownConversionConfig struct {
	PDF PDFMarkdownConfig `json:"pdf,omitempty"`
}

// PDFMarkdownConfig PDF 转 Markdown。
type PDFMarkdownConfig struct {
	Provider string                    `json:"provider,omitempty"`
	Timeout  time.Duration             `json:"timeout,omitempty"`
	Command  ExternalPDFCommandConfig  `json:"command,omitempty"`
	Install  PDFMarkdownInstallConfig  `json:"install,omitempty"`
}

// ExternalPDFCommandConfig 外部 PDF 转换命令。
type ExternalPDFCommandConfig struct {
	Name         string   `json:"name,omitempty"`
	Binary       string   `json:"binary,omitempty"`
	Args         []string `json:"args,omitempty"`
	MarkdownPath string   `json:"markdown_path,omitempty"`
	AssetDir     string   `json:"asset_dir,omitempty"`
}

// PDFMarkdownInstallConfig 启动期安装 MinerU 等依赖。
type PDFMarkdownInstallConfig struct {
	Enabled    *bool       `json:"enabled,omitempty"`
	InstallDir string      `json:"install_dir,omitempty"`
	Timeout    time.Duration `json:"timeout,omitempty"`
	Command    CommandSpec `json:"command,omitempty"`
}

// CommandSpec 通用命令规格。
type CommandSpec struct {
	Binary string   `json:"binary,omitempty"`
	Args   []string `json:"args,omitempty"`
}

// NormalizeAssetConfig 合并默认值；显式字段（含 use_ssl=false）不被默认值覆盖。
func NormalizeAssetConfig(in AssetConfig) AssetConfig {
	out := DefaultAssetConfig
	if p := strings.TrimSpace(in.Provider); p != "" {
		out.Provider = p
	}
	if in.Local.BasePath != "" {
		out.Local.BasePath = in.Local.BasePath
	}
	out.MinIO = mergeAssetS3(out.MinIO, in.MinIO)
	out.S3 = mergeAssetS3(out.S3, in.S3)
	return out
}

// NormalizeFileConvConfig 合并 fileconv 默认值。
func NormalizeFileConvConfig(in FileConvConfig) FileConvConfig {
	out := DefaultFileConvConfig
	if in.Markdown.PDF.Provider != "" {
		out.Markdown.PDF.Provider = in.Markdown.PDF.Provider
	}
	if in.Markdown.PDF.Timeout > 0 {
		out.Markdown.PDF.Timeout = in.Markdown.PDF.Timeout
	}
	if in.Markdown.PDF.Command.Name != "" {
		out.Markdown.PDF.Command.Name = in.Markdown.PDF.Command.Name
	}
	if in.Markdown.PDF.Command.Binary != "" {
		out.Markdown.PDF.Command.Binary = in.Markdown.PDF.Command.Binary
	}
	if len(in.Markdown.PDF.Command.Args) > 0 {
		out.Markdown.PDF.Command.Args = append([]string(nil), in.Markdown.PDF.Command.Args...)
	}
	if in.Markdown.PDF.Install.InstallDir != "" {
		out.Markdown.PDF.Install.InstallDir = in.Markdown.PDF.Install.InstallDir
	}
	if in.Markdown.PDF.Install.Timeout > 0 {
		out.Markdown.PDF.Install.Timeout = in.Markdown.PDF.Install.Timeout
	}
	if in.Markdown.PDF.Install.Enabled != nil {
		out.Markdown.PDF.Install.Enabled = in.Markdown.PDF.Install.Enabled
	}
	if in.Markdown.PDF.Install.Command.Binary != "" {
		out.Markdown.PDF.Install.Command.Binary = in.Markdown.PDF.Install.Command.Binary
	}
	if len(in.Markdown.PDF.Install.Command.Args) > 0 {
		out.Markdown.PDF.Install.Command.Args = append([]string(nil), in.Markdown.PDF.Install.Command.Args...)
	}
	return out
}

func mergeAssetS3(defaults, in AssetS3Config) AssetS3Config {
	out := defaults
	if in.Endpoint != "" {
		out.Endpoint = in.Endpoint
	}
	if in.AccessKey != "" {
		out.AccessKey = in.AccessKey
	}
	if in.SecretKey != "" {
		out.SecretKey = in.SecretKey
	}
	if in.Bucket != "" {
		out.Bucket = in.Bucket
	}
	if in.Region != "" {
		out.Region = in.Region
	}
	if in.Endpoint != "" || in.Bucket != "" || in.AccessKey != "" || in.SecretKey != "" {
		out.UseSSL = in.UseSSL
	}
	return out
}
