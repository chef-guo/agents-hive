package agentquality

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ChangedFileSet struct {
	Files []string
}

type ValidationCommand struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec"`
}

func BuildValidationCommands(changed ChangedFileSet) []ValidationCommand {
	commands := make(map[string]ValidationCommand)
	frontend := false
	for _, raw := range changed.Files {
		path := normalizeChangedPath(raw)
		if path == "" {
			continue
		}
		if isFrontendTS(path) {
			frontend = true
			continue
		}
		if pkg, ok := goPackageForChangedFile(path); ok {
			name := "go test " + pkg
			commands[name] = ValidationCommand{
				Name:       name,
				Command:    name + " -count=1",
				TimeoutSec: 120,
			}
		}
	}
	if frontend {
		commands["frontend build"] = ValidationCommand{
			Name:       "frontend build",
			Command:    "cd frontend && npm run build",
			TimeoutSec: 600,
		}
	}
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ValidationCommand, 0, len(names))
	for _, name := range names {
		out = append(out, commands[name])
	}
	return out
}

func normalizeChangedPath(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) {
		if root, ok := findRepositoryRoot(); ok {
			if rel, err := filepath.Rel(root, clean); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
				clean = rel
			}
		}
	}
	return filepath.ToSlash(clean)
}

func findRepositoryRoot() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func isFrontendTS(path string) bool {
	if !strings.HasPrefix(path, "frontend/src/") {
		return false
	}
	return strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx")
}

func goPackageForChangedFile(path string) (string, bool) {
	if !strings.HasSuffix(path, ".go") {
		return "", false
	}
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		return "", false
	}
	switch parts[0] {
	case "internal":
		return "./internal/" + parts[1], true
	case "cmd":
		return "./cmd/" + parts[1], true
	default:
		return "", false
	}
}
