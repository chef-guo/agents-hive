package agentquality

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildValidationCommands(t *testing.T) {
	t.Run("deduplicates and sorts internal packages", func(t *testing.T) {
		changed := ChangedFileSet{
			Files: []string{
				"internal/zeta/alpha.go",
				"internal/alpha/first.go",
				"internal/alpha/second.go",
				"internal/zeta/nested/beta.go",
				"internal/not-go/readme.md",
			},
		}

		got := BuildValidationCommands(changed)

		assert.Equal(t, []ValidationCommand{
			{
				Name:       "go test ./internal/alpha",
				Command:    "go test ./internal/alpha -count=1",
				TimeoutSec: 120,
			},
			{
				Name:       "go test ./internal/zeta",
				Command:    "go test ./internal/zeta -count=1",
				TimeoutSec: 120,
			},
		}, got)
	})

	t.Run("derives cmd package from changed file", func(t *testing.T) {
		changed := ChangedFileSet{
			Files: []string{
				"cmd/server/main.go",
				"cmd/server/http/router.go",
				"cmd/claw/root.go",
			},
		}

		got := BuildValidationCommands(changed)

		assert.Equal(t, []ValidationCommand{
			{
				Name:       "go test ./cmd/claw",
				Command:    "go test ./cmd/claw -count=1",
				TimeoutSec: 120,
			},
			{
				Name:       "go test ./cmd/server",
				Command:    "go test ./cmd/server -count=1",
				TimeoutSec: 120,
			},
		}, got)
	})

	t.Run("generates frontend build once for ts and tsx files", func(t *testing.T) {
		changed := ChangedFileSet{
			Files: []string{
				"frontend/src/pages/Home.tsx",
				"frontend/src/store/index.ts",
				"frontend/src/styles.css",
			},
		}

		got := BuildValidationCommands(changed)

		assert.Equal(t, []ValidationCommand{
			{
				Name:       "frontend build",
				Command:    "cd frontend && npm run build",
				TimeoutSec: 600,
			},
		}, got)
	})

	t.Run("combines frontend internal and cmd commands in stable order", func(t *testing.T) {
		changed := ChangedFileSet{
			Files: []string{
				"frontend/src/components/App.tsx",
				"cmd/server/main.go",
				"internal/agentquality/test_feedback.go",
			},
		}

		got := BuildValidationCommands(changed)

		assert.Equal(t, []ValidationCommand{
			{
				Name:       "frontend build",
				Command:    "cd frontend && npm run build",
				TimeoutSec: 600,
			},
			{
				Name:       "go test ./cmd/server",
				Command:    "go test ./cmd/server -count=1",
				TimeoutSec: 120,
			},
			{
				Name:       "go test ./internal/agentquality",
				Command:    "go test ./internal/agentquality -count=1",
				TimeoutSec: 120,
			},
		}, got)
	})

	t.Run("normalizes absolute paths inside repository", func(t *testing.T) {
		cwd, err := filepath.Abs(".")
		assert.NoError(t, err)
		changed := ChangedFileSet{
			Files: []string{
				filepath.Join(cwd, "test_feedback.go"),
			},
		}

		got := BuildValidationCommands(changed)

		assert.Equal(t, []ValidationCommand{
			{
				Name:       "go test ./internal/agentquality",
				Command:    "go test ./internal/agentquality -count=1",
				TimeoutSec: 120,
			},
		}, got)
	})
}
