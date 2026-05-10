package eval

import (
	"path/filepath"
	"testing"
)

func TestLoadCasesAndValidate(t *testing.T) {
	cases, err := LoadCases(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("LoadCases returned error: %v", err)
	}
	if len(cases) != RequiredFixtureCount {
		t.Fatalf("len(cases) = %d, want %d", len(cases), RequiredFixtureCount)
	}

	required := 0
	for _, loaded := range cases {
		if err := ValidateCase(loaded.Case); err != nil {
			t.Fatalf("ValidateCase(%s) returned error: %v", loaded.Path, err)
		}
		if loaded.Case.Required {
			required++
		}
	}
	if required != RequiredFixtureCount {
		t.Fatalf("required = %d, want %d cases required", required, RequiredFixtureCount)
	}
}

func TestValidateCaseRejectsUnknownExpectedID(t *testing.T) {
	err := ValidateCase(Case{
		ID:     "bad",
		Name:   "bad",
		Query:  "q",
		UserID: "u1",
		Memories: []MemoryFixture{
			{ID: 1, UserID: "u1", Type: "user", Content: "x"},
		},
		WantInjectedIDs: []int64{2},
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
}
