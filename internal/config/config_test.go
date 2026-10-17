package config

import (
	"os"
	"strings"
	"testing"
)

func TestSubstituteEnv(t *testing.T) {
	t.Setenv("SET_VAR", "hello")
	t.Setenv("EMPTY_VAR", "")

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string // empty = no error
	}{
		// §5.2 verified examples.
		{name: "escaped", input: `\${X}`, want: `${X}`},
		{name: "escaped with default", input: `\${X:-d}`, want: `${X:-d}`},
		{name: "default only", input: `${X:-}`, want: ""},
		{name: "default then rest", input: `${X:-x}y}`, want: "xy}"},
		{name: "default containing colon dash", input: `${X:-a:-b}`, want: "a:-b"},
		{name: "unset throws", input: `# note ${X}`, wantErr: "Environment variable X is not set and no default value is provided."},
		// Normal behavior.
		{name: "set var", input: `${SET_VAR}`, want: "hello"},
		{name: "set empty var used", input: `${EMPTY_VAR}`, want: ""},
		{name: "set var wins over default", input: `${SET_VAR:-fallback}`, want: "hello"},
		{name: "empty set var wins over default", input: `${EMPTY_VAR:-fallback}`, want: ""},
		{name: "default used when unset", input: `${X:-fallback}`, want: "fallback"},
		{name: "no dollar", input: `plain text`, want: "plain text"},
		{name: "unsupported $VAR syntax", input: `$VAR`, want: "$VAR"},
		{name: "escaped among text", input: `a \${X} b ${X:-c}`, want: "a ${X} b c"},
		{name: "multiple", input: `${SET_VAR}/${X:-d}`, want: "hello/d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := substituteEnv(tt.input)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("substituteEnv(%q) error = %v, want %q", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("substituteEnv(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("substituteEnv(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		isPrivate bool
		input     string
		want      string
	}{
		// §5.7 table, every row.
		{false, "org/repo", "https://github.com/org/repo.git"},
		{false, "https://github.com/org/repo", "https://github.com/org/repo"},
		{false, "org/repo.js", "org/repo.js"},
		{true, "org/repo", "git@github.com:org/repo.git"},
		{true, "https://github.com/org/repo", "git@github.com:org/repo.git"},
		{true, "https://github.com/org/repo.git", "git@github.com:org/repo.git"},
		{true, "https://gitlab.com/org/repo", "https://gitlab.com/org/repo"},
		// Extra edge cases implied by the spec.
		{false, "https://example.com/x.git", "https://example.com/x.git"},
		{true, "org/repo.js", "org/repo.js"},
		{true, "git@github.com:org/repo.git", "git@github.com:org/repo.git"},
	}
	for _, tt := range tests {
		got := normalizeURL(tt.input, tt.isPrivate)
		if got != tt.want {
			t.Errorf("normalizeURL(%q, %v) = %q, want %q", tt.input, tt.isPrivate, got, tt.want)
		}
	}
}

func mustParse(t *testing.T, yamlText string) *Config {
	t.Helper()
	config, err := parseText(yamlText)
	if err != nil {
		t.Fatalf("parseText: %v", err)
	}
	return config
}

func mustParseErr(t *testing.T, yamlText string) string {
	t.Helper()
	_, err := parseText(yamlText)
	if err == nil {
		t.Fatalf("parseText(%q) = nil error, want error", yamlText)
	}
	return err.Error()
}

func TestParseConfigBasics(t *testing.T) {
	t.Run("missing repositories", func(t *testing.T) {
		if got := mustParseErr(t, "moodle: {url: u, path: m}\n"); got != "repositories is a required field" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("null repositories", func(t *testing.T) {
		if got := mustParseErr(t, "repositories:\n"); got != "repositories is a required field" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("empty repositories is valid", func(t *testing.T) {
		config := mustParse(t, "repositories: []\n")
		if len(config.Repositories) != 0 {
			t.Fatalf("want no repositories")
		}
		if config.Moodle != nil {
			t.Fatalf("want no moodle")
		}
		if got, want := config.Cleanup, ".git,.github"; strings.Join(got, ",") != want {
			t.Fatalf("cleanup = %v, want %v", got, want)
		}
		if config.Skip {
			t.Fatal("root skip default must be false")
		}
	})
	t.Run("malformed yaml", func(t *testing.T) {
		if got := mustParseErr(t, "foo: [bar\n"); got != "Invalid YAML" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("moodle without url", func(t *testing.T) {
		if got := mustParseErr(t, "moodle: {path: m}\nrepositories: []\n"); got != "moodle.url is a required field" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("moodle without path", func(t *testing.T) {
		if got := mustParseErr(t, "moodle: {url: u}\nrepositories: []\n"); got != "moodle.path is a required field" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("moodle defaults", func(t *testing.T) {
		config := mustParse(t, "moodle: {url: u, path: m}\nrepositories: []\n")
		if !config.Moodle.Patch {
			t.Fatal("moodle.patch default must be true")
		}
		if config.Moodle.PatchDir != "patch" {
			t.Fatalf("patchDir = %q", config.Moodle.PatchDir)
		}
		want := ".git,.github,.gitattributes,.gitignore,README.txt,readme_moodle.txt,COPYING.txt"
		if got := strings.Join(config.Moodle.Cleanup, ","); got != want {
			t.Fatalf("moodle cleanup = %v", config.Moodle.Cleanup)
		}
	})
	t.Run("explicit empty cleanup disables", func(t *testing.T) {
		config := mustParse(t, "cleanup: []\nrepositories: []\n")
		if len(config.Cleanup) != 0 {
			t.Fatalf("cleanup = %v, want empty", config.Cleanup)
		}
	})
	t.Run("numeric branch coerced to string", func(t *testing.T) {
		config := mustParse(t, "repositories:\n  - url: o/r\n    branch: 2024041001\n    target: t\n")
		if config.Repositories[0].Branch != "2024041001" {
			t.Fatalf("branch = %q", config.Repositories[0].Branch)
		}
	})
	t.Run("empty string counts as missing", func(t *testing.T) {
		if got := mustParseErr(t, "repositories:\n  - url: \"\"\n    target: t\n"); !strings.Contains(got, "Either url or path must be provided") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("unknown keys ignored", func(t *testing.T) {
		config := mustParse(t, "bogus: 1\nrepositories:\n  - url: o/r\n    target: t\n    taget: typo\n")
		if len(config.Repositories) != 1 || config.Repositories[0].URL != "https://github.com/o/r.git" {
			t.Fatalf("unexpected parse: %+v", config.Repositories)
		}
	})
	t.Run("boolean-like strings coerced", func(t *testing.T) {
		config := mustParse(t, "skip: \"true\"\nrepositories:\n  - url: o/r\n    target: t\n    enable: \"false\"\n")
		if !config.Skip {
			t.Fatal("root skip = \"true\" must coerce")
		}
		if config.Repositories[0].Enable {
			t.Fatal("enable = \"false\" must coerce")
		}
	})
}

func TestParseRepoCrossFieldRules(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "rule 1 neither url nor path",
			yaml: "repositories:\n  - target: t\n",
			want: "Either url or path must be provided. Target: (t)",
		},
		{
			name: "rule 1 neither, target from subdirs",
			yaml: "repositories:\n  - subdirs:\n      - {subdir: s1, target: t1}\n      - {subdir: s2, target: t2}\n",
			want: "Either url or path must be provided. Target: (t1, t2)",
		},
		{
			name: "rule 2 both url and path",
			yaml: "repositories:\n  - url: o/r\n    path: p\n    target: t\n",
			want: "Either url or path must be provided, not both. Target: (t)",
		},
		{
			name: "rule 3 both hash and branch",
			yaml: "repositories:\n  - url: o/r\n    hash: abc\n    branch: b\n    target: t\n",
			want: "Either hash or branch must be provided, not both. Target: (t)",
		},
		{
			name: "rule 4 both target and subdirs",
			yaml: "repositories:\n  - url: o/r\n    target: t\n    subdirs:\n      - {subdir: s, target: t1}\n",
			want: "Either target or subdirs must be provided, not both. Source: (https://github.com/o/r.git)",
		},
		{
			name: "rule 4 neither target nor subdirs",
			yaml: "repositories:\n  - path: p\n",
			want: "Either target or subdirs must be provided, not both. Source: (p)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mustParseErr(t, tt.yaml); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseRepoSubdirsValidation(t *testing.T) {
	if got := mustParseErr(t, "repositories:\n  - url: o/r\n    subdirs: []\n"); !strings.Contains(got, "at least 1 items") {
		t.Fatalf("got %q", got)
	}
	if got := mustParseErr(t, "repositories:\n  - url: o/r\n    subdirs:\n      - {subdir: s}\n"); !strings.Contains(got, "subdirs.target is a required field") {
		t.Fatalf("got %q", got)
	}
}

func TestParseRepoSkipOverride(t *testing.T) {
	config := mustParse(t, "skip: true\nrepositories:\n  - url: o/r\n    target: t\n    skip: false\n")
	if config.Repositories[0].Skip == nil || *config.Repositories[0].Skip {
		t.Fatal("repo skip must be set to false")
	}
	config = mustParse(t, "repositories:\n  - url: o/r\n    target: t\n")
	if config.Repositories[0].Skip != nil {
		t.Fatal("repo skip must be unset")
	}
}

// parseText substitutes env vars and parses, mirroring Load but from a
// string instead of a file.
func parseText(text string) (*Config, error) {
	substituted, err := substituteEnv(text)
	if err != nil {
		return nil, err
	}
	return parse([]byte(substituted))
}

func TestLoadFile(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Load(dir + "/nope.yaml")
		if err == nil {
			t.Fatal("want error")
		}
		// OS error message (SPEC §9.2).
		if !strings.Contains(err.Error(), "no such file or directory") {
			t.Fatalf("got %q", err)
		}
	})
	t.Run("env substitution in file", func(t *testing.T) {
		t.Setenv("EC_TEST_URL", "https://example.com/moodle.tgz")
		dir := t.TempDir()
		path := dir + "/easyclone.yaml"
		content := "moodle:\n  url: ${EC_TEST_URL}\n  path: m\nrepositories: []\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		config, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if config.Moodle.URL != "https://example.com/moodle.tgz" {
			t.Fatalf("url = %q", config.Moodle.URL)
		}
	})
}
