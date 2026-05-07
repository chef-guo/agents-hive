package airouter

import "testing"

func TestModelScoreSupportsAutoReasoningEffort(t *testing.T) {
	tests := []struct {
		name  string
		model ModelScore
		want  bool
	}{
		{
			name: "reasoning capability and metadata supports auto",
			model: ModelScore{
				Model:        "o3-mini",
				Capabilities: []string{"tools", "reasoning"},
			},
			want: true,
		},
		{
			name: "manual config does not imply auto support",
			model: ModelScore{
				Model:           "gpt-4o",
				Capabilities:    []string{"tools"},
				ReasoningEffort: "high",
			},
			want: false,
		},
		{
			name: "unknown model no-ops safely",
			model: ModelScore{
				Model:        "custom-reasoning",
				Capabilities: []string{"reasoning"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.model.SupportsAutoReasoningEffort(); got != tt.want {
				t.Fatalf("SupportsAutoReasoningEffort() = %v, want %v", got, tt.want)
			}
		})
	}
}
