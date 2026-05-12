package imcore

import (
	"context"
	"encoding/json"
)

type FeishuProvider interface {
	SearchContacts(ctx context.Context, query string, pageSize int) (json.RawMessage, error)
	GetUserInfo(ctx context.Context, userID string) (json.RawMessage, error)
	SendMessage(ctx context.Context, chatID, content string) error
}
