package kb

import (
	"context"
	"strings"
	"time"

	"github.com/chef-guo/agents-hive/internal/agentquality"
)

type Service struct {
	store            Store
	resolver         BindingResolver
	summaryGenerator SummaryGenerator
	tokenCounter     TokenCounter
	assetUploader    AssetUploader
	qualityRecorder  QualityRecorder
	maxNodeIDs       int
	maxSectionBytes  int
}

type QualityRecorder interface {
	RecordKBQualityEvent(sessionID string, event agentquality.Event)
}

type ActiveBindingHintInput struct {
	OwnerScope    OwnerScope
	OwnerID       string
	BindingType   BindingType
	BindingTarget string
	DomainID      string
	Now           time.Time
}

func boolPtr(v bool) *bool {
	return &v
}

type activeBindingDomainFinder interface {
	FindActiveBindingDomains(ctx context.Context, query BindingQuery, now time.Time) ([]string, error)
}

type ServiceOption func(*Service)

func NewService(store Store, opts ...ServiceOption) *Service {
	s := &Service{
		store:           store,
		resolver:        NewBindingResolver(store),
		tokenCounter:    EstimateTokenCounter{},
		maxNodeIDs:      8,
		maxSectionBytes: 64 * 1024,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func WithBindingResolver(resolver BindingResolver) ServiceOption {
	return func(s *Service) {
		s.resolver = resolver
	}
}

func WithSummaryGenerator(generator SummaryGenerator) ServiceOption {
	return func(s *Service) {
		s.summaryGenerator = generator
	}
}

func WithTokenCounter(counter TokenCounter) ServiceOption {
	return func(s *Service) {
		s.tokenCounter = counter
	}
}

func WithAssetUploader(uploader AssetUploader) ServiceOption {
	return func(s *Service) {
		s.assetUploader = uploader
	}
}

func WithQualityRecorder(recorder QualityRecorder) ServiceOption {
	return func(s *Service) {
		s.qualityRecorder = recorder
	}
}

func (s *Service) SetQualityRecorder(recorder QualityRecorder) {
	if s == nil {
		return
	}
	s.qualityRecorder = recorder
}

func (s *Service) ActiveBindingHint(ctx context.Context, input ActiveBindingHintInput) (string, bool, error) {
	if s == nil || s.store == nil {
		return "", false, ErrInvalidInput
	}
	if input.OwnerScope == "" || strings.TrimSpace(input.OwnerID) == "" || input.BindingType == "" || strings.TrimSpace(input.BindingTarget) == "" {
		return "", false, ErrInvalidScope
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	domainID := strings.TrimSpace(input.DomainID)
	var domains []string
	if domainID != "" {
		domains = []string{domainID}
	} else if finder, ok := s.store.(activeBindingDomainFinder); ok {
		found, err := finder.FindActiveBindingDomains(ctx, BindingQuery{
			OwnerScope:    input.OwnerScope,
			OwnerID:       strings.TrimSpace(input.OwnerID),
			BindingType:   input.BindingType,
			BindingTarget: strings.TrimSpace(input.BindingTarget),
			Enabled:       boolPtr(true),
		}, now)
		if err != nil {
			return "", false, err
		}
		domains = found
	}
	for _, d := range domains {
		bindings, err := s.store.ListBindingsForManagement(ctx, BindingQuery{
			DomainID:      d,
			OwnerScope:    input.OwnerScope,
			OwnerID:       strings.TrimSpace(input.OwnerID),
			BindingType:   input.BindingType,
			BindingTarget: strings.TrimSpace(input.BindingTarget),
			Enabled:       boolPtr(true),
		})
		if err != nil {
			return "", false, err
		}
		for _, binding := range bindings {
			if binding.Active(now) {
				return binding.DomainID, true, nil
			}
		}
	}
	return "", false, nil
}

func WithSectionLimits(maxNodeIDs, maxBytes int) ServiceOption {
	return func(s *Service) {
		if maxNodeIDs > 0 {
			s.maxNodeIDs = maxNodeIDs
		}
		if maxBytes > 0 {
			s.maxSectionBytes = maxBytes
		}
	}
}
