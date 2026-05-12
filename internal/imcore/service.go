package imcore

import (
	"context"
	"fmt"
	"strings"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

type Service struct {
	adapters map[Platform]Adapter
}

func NewService(adapters ...Adapter) *Service {
	s := &Service{adapters: map[Platform]Adapter{}}
	for _, adapter := range adapters {
		if adapter == nil || adapter.Platform() == "" {
			continue
		}
		s.adapters[adapter.Platform()] = adapter
	}
	return s
}

func (s *Service) Register(adapter Adapter) {
	if s == nil || adapter == nil || adapter.Platform() == "" {
		return
	}
	if s.adapters == nil {
		s.adapters = map[Platform]Adapter{}
	}
	s.adapters[adapter.Platform()] = adapter
}

func (s *Service) Adapter(platform Platform) (Adapter, error) {
	if s == nil {
		return nil, fmt.Errorf("im service not configured")
	}
	adapter := s.adapters[platform]
	if adapter == nil {
		return nil, fmt.Errorf("im platform %s not configured", platform)
	}
	return adapter, nil
}

func CallerScopeFromContext(ctx context.Context, platform Platform) (CallerScope, error) {
	scope := CallerScope{}
	if user := auth.UserFrom(ctx); user != nil {
		scope.OwnerUserID = user.ID
		scope.TenantKey = user.ID
	}
	if tc := toolctx.GetToolContext(ctx); tc != nil {
		scope.TraceID = tc.TurnIDOrTraceID()
	}
	if platform == PlatformWeChatBot && strings.TrimSpace(scope.OwnerUserID) == "" {
		return CallerScope{}, fmt.Errorf("wechatbot requires authenticated owner")
	}
	return scope, nil
}

func (s *Service) SearchRecipients(ctx context.Context, platform Platform, query string, limit int) ([]Recipient, error) {
	adapter, err := s.Adapter(platform)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 10
	}
	scope, err := CallerScopeFromContext(ctx, platform)
	if err != nil {
		return nil, err
	}
	return adapter.SearchRecipients(ctx, scope, query, limit)
}

func (s *Service) ListRecentConversations(ctx context.Context, platform Platform, limit int) ([]Recipient, error) {
	adapter, err := s.Adapter(platform)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	scope, err := CallerScopeFromContext(ctx, platform)
	if err != nil {
		return nil, err
	}
	return adapter.ListRecentConversations(ctx, scope, limit)
}

func (s *Service) ResolveRecipient(ctx context.Context, platform Platform, input RecipientLookup) (Recipient, error) {
	adapter, err := s.Adapter(platform)
	if err != nil {
		return Recipient{}, err
	}
	scope, err := CallerScopeFromContext(ctx, platform)
	if err != nil {
		return Recipient{}, err
	}
	return adapter.ResolveRecipient(ctx, scope, input)
}

func (s *Service) SendMessage(ctx context.Context, target SendTarget) (SendResult, error) {
	adapter, err := s.Adapter(target.Platform)
	if err != nil {
		return SendResult{}, err
	}
	if strings.TrimSpace(target.Content) == "" {
		return SendResult{}, fmt.Errorf("content is required")
	}
	scope, err := CallerScopeFromContext(ctx, target.Platform)
	if err != nil {
		return SendResult{}, err
	}
	return adapter.SendMessage(ctx, scope, target)
}
