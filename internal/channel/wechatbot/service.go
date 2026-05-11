package wechatbot

import (
	"context"
)

// ConnectionService 是 /api/v1/wechat/* 依赖的窄接口。
type ConnectionService interface {
	Status(ctx context.Context, ownerUserID string) (ConnectionStatus, error)
	Login(ctx context.Context, ownerUserID string, force bool) (ConnectionStatus, error)
	Logout(ctx context.Context, ownerUserID string) error
	Subscribe(ownerUserID string) (<-chan Event, func())
	ListConversations(ctx context.Context, ownerUserID string) ([]Conversation, error)
}

type Service struct {
	registry *BotRegistry
	store    Store
}

func NewService(registry *BotRegistry, store Store) *Service {
	return &Service{registry: registry, store: store}
}

func (s *Service) Status(ctx context.Context, ownerUserID string) (ConnectionStatus, error) {
	return s.registry.Status(ctx, ownerUserID)
}

func (s *Service) Login(ctx context.Context, ownerUserID string, force bool) (ConnectionStatus, error) {
	if _, err := s.registry.Ensure(ctx, ownerUserID, force); err != nil {
		status, _ := s.registry.Status(ctx, ownerUserID)
		if status.Error == "" {
			status.Error = err.Error()
		}
		return status, err
	}
	return s.registry.Status(ctx, ownerUserID)
}

func (s *Service) Logout(ctx context.Context, ownerUserID string) error {
	return s.registry.Logout(ctx, ownerUserID)
}

func (s *Service) Subscribe(ownerUserID string) (<-chan Event, func()) {
	return s.registry.Subscribe(ownerUserID)
}

func (s *Service) ListConversations(ctx context.Context, ownerUserID string) ([]Conversation, error) {
	if s.store == nil {
		return []Conversation{}, nil
	}
	records, err := s.store.ListWechatConversationsByOwner(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	return conversationsFromRecords(records), nil
}
