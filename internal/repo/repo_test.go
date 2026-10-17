package repo

import "testing"

func TestRedact(t *testing.T) {
	tests := map[string]string{
		"https://user:token@github.com/org/repo.git": "https://github.com/org/repo.git",
		"https://token@github.com/org/repo":          "https://github.com/org/repo",
		"git@github.com:org/repo.git":                "git@github.com:org/repo.git",
		"https://github.com/org/repo.git":            "https://github.com/org/repo.git",
		"clone https://u:p@host/x.git dest":          "clone https://host/x.git dest",
	}
	for input, want := range tests {
		if got := redact(input); got != want {
			t.Errorf("redact(%q) = %q, want %q", input, got, want)
		}
	}
}
