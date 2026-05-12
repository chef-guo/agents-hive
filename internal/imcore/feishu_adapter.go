package imcore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type FeishuAdapter struct {
	provider FeishuProvider
}

func NewFeishuAdapter(provider FeishuProvider) Adapter {
	return &FeishuAdapter{provider: provider}
}

func (a *FeishuAdapter) Platform() Platform {
	return PlatformFeishu
}

func (a *FeishuAdapter) Capabilities() []Capability {
	return []Capability{
		CapabilitySearchRecipients,
		CapabilityListRecentConversations,
		CapabilitySendText,
	}
}

func (a *FeishuAdapter) SearchRecipients(ctx context.Context, _ CallerScope, query string, limit int) ([]Recipient, error) {
	if a.provider == nil {
		return nil, fmt.Errorf("feishu provider not configured")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 10
	}
	raw, err := a.provider.SearchContacts(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	items := parseFeishuContacts(raw)
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (a *FeishuAdapter) ListRecentConversations(context.Context, CallerScope, int) ([]Recipient, error) {
	return nil, fmt.Errorf("feishu recent conversation listing is not supported")
}

func (a *FeishuAdapter) ResolveRecipient(ctx context.Context, scope CallerScope, input RecipientLookup) (Recipient, error) {
	id := strings.TrimSpace(input.RecipientID)
	fromConversation := false
	if id == "" {
		id = strings.TrimSpace(input.ConversationID)
		fromConversation = id != ""
	}
	if id == "" && strings.TrimSpace(input.Query) != "" {
		items, err := a.SearchRecipients(ctx, scope, input.Query, 2)
		if err != nil {
			return Recipient{}, err
		}
		if len(items) == 0 {
			return Recipient{}, fmt.Errorf("recipient not found")
		}
		if len(items) > 1 {
			return Recipient{}, fmt.Errorf("recipient is ambiguous")
		}
		return items[0], nil
	}
	if id == "" {
		return Recipient{}, fmt.Errorf("recipient_id or conversation_id is required")
	}
	idType := strings.TrimSpace(input.ExternalIDType)
	if idType == "user_id" {
		return a.resolveFeishuUserID(ctx, id)
	}
	if isDirectFeishuTarget(id) {
		return feishuRecipientFromID(id, id), nil
	}
	if rec, err := a.resolveFeishuUserID(ctx, id); err == nil && rec.ID != "" {
		return rec, nil
	}
	if fromConversation || idType == "conversation_id" || idType == "receive_id" {
		return feishuRecipientFromID(id, id), nil
	}
	return Recipient{}, fmt.Errorf("recipient info missing open_id")
}

func (a *FeishuAdapter) resolveFeishuUserID(ctx context.Context, id string) (Recipient, error) {
	if a.provider == nil {
		return Recipient{}, fmt.Errorf("feishu provider not configured")
	}
	raw, err := a.provider.GetUserInfo(ctx, id)
	if err != nil {
		return Recipient{}, err
	}
	rec := parseFeishuUser(raw)
	if rec.ID == "" {
		return Recipient{}, fmt.Errorf("recipient info missing open_id")
	}
	return rec, nil
}

func (a *FeishuAdapter) SendMessage(ctx context.Context, scope CallerScope, target SendTarget) (SendResult, error) {
	if a.provider == nil {
		return SendResult{}, fmt.Errorf("feishu provider not configured")
	}
	rec, err := a.ResolveRecipient(ctx, scope, RecipientLookup{
		RecipientID:    target.RecipientID,
		ConversationID: target.ConversationID,
		ExternalIDType: target.ExternalIDType,
	})
	if err != nil {
		return SendResult{}, err
	}
	if target.DryRun {
		return SendResult{Platform: PlatformFeishu, TargetID: rec.ID, TargetKind: rec.Kind, Delivered: false, DryRun: true}, nil
	}
	if err := a.provider.SendMessage(ctx, rec.ID, target.Content); err != nil {
		return SendResult{}, err
	}
	return SendResult{Platform: PlatformFeishu, TargetID: rec.ID, TargetKind: rec.Kind, Delivered: true}, nil
}

type feishuContactShape struct {
	UserID string `json:"user_id"`
	OpenID string `json:"open_id"`
	Name   string `json:"name"`
	User   struct {
		UserID string `json:"user_id"`
		OpenID string `json:"open_id"`
		Name   string `json:"name"`
	} `json:"user"`
}

func parseFeishuContacts(raw json.RawMessage) []Recipient {
	var direct []feishuContactShape
	if json.Unmarshal(raw, &direct) == nil && len(direct) > 0 {
		return feishuContactsToRecipients(direct)
	}
	var wrapped struct {
		Contacts []feishuContactShape `json:"contacts"`
		Items    []feishuContactShape `json:"items"`
		Users    []feishuContactShape `json:"users"`
		Data     struct {
			Contacts []feishuContactShape `json:"contacts"`
			Items    []feishuContactShape `json:"items"`
			Users    []feishuContactShape `json:"users"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &wrapped) != nil {
		return nil
	}
	switch {
	case len(wrapped.Contacts) > 0:
		return feishuContactsToRecipients(wrapped.Contacts)
	case len(wrapped.Items) > 0:
		return feishuContactsToRecipients(wrapped.Items)
	case len(wrapped.Users) > 0:
		return feishuContactsToRecipients(wrapped.Users)
	case len(wrapped.Data.Contacts) > 0:
		return feishuContactsToRecipients(wrapped.Data.Contacts)
	case len(wrapped.Data.Items) > 0:
		return feishuContactsToRecipients(wrapped.Data.Items)
	case len(wrapped.Data.Users) > 0:
		return feishuContactsToRecipients(wrapped.Data.Users)
	default:
		return nil
	}
}

func feishuContactsToRecipients(items []feishuContactShape) []Recipient {
	out := make([]Recipient, 0, len(items))
	for _, item := range items {
		rec := feishuRecipientFromContact(item)
		if rec.ID == "" {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func feishuRecipientFromContact(item feishuContactShape) Recipient {
	id := strings.TrimSpace(item.OpenID)
	idType := "open_id"
	if id == "" {
		id = strings.TrimSpace(item.User.OpenID)
	}
	if id == "" {
		id = strings.TrimSpace(item.UserID)
		idType = "user_id"
	}
	if id == "" {
		id = strings.TrimSpace(item.User.UserID)
		idType = "user_id"
	}
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = strings.TrimSpace(item.User.Name)
	}
	if name == "" {
		name = id
	}
	return Recipient{
		Platform:       PlatformFeishu,
		ID:             id,
		DisplayName:    name,
		Kind:           "user",
		ExternalIDType: idType,
		CanSend:        true,
		SendState:      "ready",
	}
}

func parseFeishuUser(raw json.RawMessage) Recipient {
	var detail feishuContactShape
	if json.Unmarshal(raw, &detail) != nil {
		return Recipient{}
	}
	rec := feishuRecipientFromContact(detail)
	if rec.ID != "" {
		return rec
	}
	return feishuRecipientFromContact(feishuContactShape{
		UserID: detail.User.UserID,
		OpenID: detail.User.OpenID,
		Name:   detail.User.Name,
	})
}

func feishuRecipientFromID(id, displayName string) Recipient {
	idType := "recipient_id"
	kind := "user"
	if strings.HasPrefix(id, "oc_") {
		idType = "conversation_id"
		kind = "conversation"
	} else if strings.HasPrefix(id, "ou_") || strings.HasPrefix(id, "on_") {
		idType = "open_id"
	} else if strings.Contains(id, "@") {
		idType = "email"
	}
	return Recipient{
		Platform:       PlatformFeishu,
		ID:             id,
		DisplayName:    displayName,
		Kind:           kind,
		ExternalIDType: idType,
		CanSend:        true,
		SendState:      "ready",
	}
}

func isDirectFeishuTarget(id string) bool {
	id = strings.TrimSpace(id)
	return strings.HasPrefix(id, "oc_") ||
		strings.HasPrefix(id, "ou_") ||
		strings.HasPrefix(id, "on_") ||
		strings.Contains(id, "@")
}
