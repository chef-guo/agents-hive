package wechatbot

import (
	"context"

	sdk "github.com/corespeed-io/wechatbot/golang"
)

// SDKMessage 是官方 SDK 入站消息的最小别名，避免上层包直接依赖 SDK 细节。
type SDKMessage = sdk.IncomingMessage

// Credentials 是登录后可持久化/展示的账号身份。
type Credentials struct {
	AccountID string
	UserID    string
	SavedAt   string
}

// Backend 封装官方 wechatbot SDK，便于单测替换。
type Backend interface {
	Login(ctx context.Context, force bool) (*Credentials, error)
	OnMessage(handler func(*SDKMessage))
	Run(ctx context.Context) error
	Stop()
	Send(ctx context.Context, userID, text string) error
}

// BackendOptions 是官方 SDK adapter 的构造参数。
type BackendOptions struct {
	BaseURL   string
	CredPath  string
	LogLevel  string
	OnQRURL   func(string)
	OnScanned func()
	OnExpired func()
	OnError   func(error)
}

type sdkBackend struct {
	bot *sdk.Bot
}

// NewBackend 创建官方 wechatbot SDK adapter。
func NewBackend(opts BackendOptions) Backend {
	bot := sdk.New(sdk.Options{
		BaseURL:   opts.BaseURL,
		CredPath:  opts.CredPath,
		LogLevel:  opts.LogLevel,
		OnQRURL:   opts.OnQRURL,
		OnScanned: opts.OnScanned,
		OnExpired: opts.OnExpired,
		OnError:   opts.OnError,
	})
	return &sdkBackend{bot: bot}
}

func (b *sdkBackend) Login(ctx context.Context, force bool) (*Credentials, error) {
	creds, err := b.bot.Login(ctx, force)
	if err != nil {
		return nil, err
	}
	return &Credentials{
		AccountID: creds.AccountID,
		UserID:    creds.UserID,
		SavedAt:   creds.SavedAt,
	}, nil
}

func (b *sdkBackend) OnMessage(handler func(*SDKMessage)) {
	b.bot.OnMessage(handler)
}

func (b *sdkBackend) Run(ctx context.Context) error {
	return b.bot.Run(ctx)
}

func (b *sdkBackend) Stop() {
	b.bot.Stop()
}

func (b *sdkBackend) Send(ctx context.Context, userID, text string) error {
	return b.bot.Send(ctx, userID, text)
}
