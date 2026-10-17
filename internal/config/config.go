// Package config loads, substitutes and validates the easyclone manifest
// (SPEC §5).
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ffd114/easyclone-go/internal/logx"
)

// MoodleConfig is the `moodle` block of the manifest (SPEC §5.5).
type MoodleConfig struct {
	URL      string
	Path     string
	Patch    bool
	PatchDir string
	Cleanup  []string
}

// SubdirConfig is one entry of a repository's `subdirs` list (SPEC §5.6).
type SubdirConfig struct {
	Subdir string
	Target string
}

// RepoConfig is one entry of the `repositories` list (SPEC §5.6). URL is
// already normalized (SPEC §5.7).
type RepoConfig struct {
	URL       string
	Path      string
	IsPrivate bool
	Branch    string
	Hash      string
	Target    string
	Subdirs   []SubdirConfig // nil unless configured
	Enable    bool
	Skip      *bool // nil = unset; when set it overrides root `skip` (SPEC §7.2)
	Patch     bool
	Cleanup   []string
}

// Config is the parsed and validated manifest (SPEC §5.4).
type Config struct {
	Moodle       *MoodleConfig
	Cleanup      []string
	Skip         bool
	SSHKey       string
	Repositories []RepoConfig
}

var defaultRootCleanup = []string{".git", ".github"}

var defaultMoodleCleanup = []string{
	".git", ".github", ".gitattributes", ".gitignore",
	"README.txt", "readme_moodle.txt", "COPYING.txt",
}

// envRe implements the env substitution pattern of SPEC §5.2:
// an optional backslash, then ${VAR} or ${VAR:-default}, where the default
// is the non-greedy text up to the first `}`.
var envRe = regexp.MustCompile(`\\?\$\{(\w+)(?::-(.*?))?\}`)

// substituteEnv runs on the raw config text, before YAML parsing — including
// comments (SPEC §5.2).
func substituteEnv(text string) (string, error) {
	var firstErr error
	out := envRe.ReplaceAllStringFunc(text, func(match string) string {
		if strings.HasPrefix(match, `\`) {
			// Escaped: drop the backslash, no lookup (SPEC §5.2 rule 1).
			return match[1:]
		}
		sub := envRe.FindStringSubmatch(match)
		name := sub[1]
		// A default was provided iff the match contains ":-"; the variable
		// name cannot contain ":", so this distinguishes ${VAR} from
		// ${VAR:-} (whose default is the empty string).
		hasDefault := strings.Contains(match, ":-")
		if value, ok := os.LookupEnv(name); ok {
			// Inserted raw, even if empty (SPEC §5.2 rule 2).
			return value
		}
		if hasDefault {
			return sub[2]
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("Environment variable %s is not set and no default value is provided.", name)
		}
		return ""
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// Load implements the pipeline of SPEC §5.1: read, substitute env vars,
// parse YAML, validate and coerce.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parse(data)
}

func parse(data []byte) (*Config, error) {
	substituted, err := substituteEnv(string(data))
	if err != nil {
		return nil, err
	}
	var raw any
	if err := yaml.Unmarshal([]byte(substituted), &raw); err != nil {
		// The parser's details are discarded (SPEC §5.1 step 3).
		return nil, errors.New("Invalid YAML")
	}
	root, ok := raw.(map[string]any)
	if !ok {
		if raw == nil {
			root = map[string]any{}
		} else {
			return nil, errors.New("Invalid config: expected a YAML mapping at the top level")
		}
	}
	return parseConfig(root)
}

func parseConfig(root map[string]any) (*Config, error) {
	config := &Config{}

	// moodle (SPEC §5.5). A null `moodle:` counts as absent.
	if v, present := root["moodle"]; present && v != nil {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("moodle must be an object")
		}
		url, hasURL, err := stringField(m, "url")
		if err != nil {
			return nil, err
		}
		if !hasURL {
			return nil, errors.New("moodle.url is a required field")
		}
		path, hasPath, err := stringField(m, "path")
		if err != nil {
			return nil, err
		}
		if !hasPath {
			return nil, errors.New("moodle.path is a required field")
		}
		patch := true
		if b, present, err := boolField(m, "patch"); err != nil {
			return nil, err
		} else if present {
			patch = b
		}
		// patchDir defaults to "patch" when absent, but an explicit empty
		// string fails `required` (SPEC §5.5).
		patchDir := "patch"
		if v, present := m["patchDir"]; present && v != nil {
			s, ok := asString(v)
			if !ok {
				return nil, errors.New("patchDir must be a string")
			}
			if s == "" {
				return nil, errors.New("moodle.patchDir is a required field")
			}
			patchDir = s
		}
		cleanup := defaultMoodleCleanup
		if l, present, err := stringListField(m, "cleanup"); err != nil {
			return nil, err
		} else if present {
			cleanup = l
		}
		config.Moodle = &MoodleConfig{
			URL:      url,
			Path:     path,
			Patch:    patch,
			PatchDir: patchDir,
			Cleanup:  cleanup,
		}
	}

	// Root cleanup (SPEC §5.4). An explicit [] disables it.
	config.Cleanup = defaultRootCleanup
	if l, present, err := stringListField(root, "cleanup"); err != nil {
		return nil, err
	} else if present {
		config.Cleanup = l
	}

	// Root skip (SPEC §5.4).
	if b, present, err := boolField(root, "skip"); err != nil {
		return nil, err
	} else if present {
		config.Skip = b
	}

	// sshKey (SPEC §5.4).
	if s, present, err := stringField(root, "sshKey"); err != nil {
		return nil, err
	} else if present {
		config.SSHKey = s
	}

	// repositories: required, present and non-null; an empty list is valid.
	v, present := root["repositories"]
	if !present || v == nil {
		return nil, errors.New("repositories is a required field")
	}
	list, ok := v.([]any)
	if !ok {
		return nil, errors.New("repositories must be a list")
	}
	for _, item := range list {
		repo, err := parseRepo(item)
		if err != nil {
			return nil, err
		}
		config.Repositories = append(config.Repositories, repo)
	}
	return config, nil
}

func parseRepo(item any) (RepoConfig, error) {
	var repo RepoConfig
	m, ok := item.(map[string]any)
	if !ok {
		return repo, errors.New("every entry of repositories must be an object")
	}

	var err error
	var hasURL, hasPath, hasBranch, hasHash, hasTarget bool
	if repo.URL, hasURL, err = stringField(m, "url"); err != nil {
		return repo, err
	}
	if repo.Path, hasPath, err = stringField(m, "path"); err != nil {
		return repo, err
	}
	if repo.IsPrivate, _, err = boolField(m, "isPrivate"); err != nil {
		return repo, err
	}
	if repo.Branch, hasBranch, err = stringField(m, "branch"); err != nil {
		return repo, err
	}
	if repo.Hash, hasHash, err = stringField(m, "hash"); err != nil {
		return repo, err
	}
	if repo.Target, hasTarget, err = stringField(m, "target"); err != nil {
		return repo, err
	}
	repo.Enable = true
	if b, present, err := boolField(m, "enable"); err != nil {
		return repo, err
	} else if present {
		repo.Enable = b
	}
	if b, present, err := boolField(m, "skip"); err != nil {
		return repo, err
	} else if present {
		repo.Skip = &b
	}
	if repo.Patch, _, err = boolField(m, "patch"); err != nil {
		return repo, err
	}
	repo.Cleanup = []string{}
	if l, present, err := stringListField(m, "cleanup"); err != nil {
		return repo, err
	} else if present {
		repo.Cleanup = l
	}

	subdirs, hasSubdirs, err := parseSubdirs(m)
	if err != nil {
		return repo, err
	}
	repo.Subdirs = subdirs

	// URL normalization happens before the cross-field rules (SPEC §5.6).
	if hasURL {
		repo.URL = normalizeURL(repo.URL, repo.IsPrivate)
	}

	// Cross-field rules (SPEC §5.6), checked in order, first failure throws.
	// <T> is `target` if set, otherwise the subdirs' targets joined with ", ".
	targetDesc := repo.Target
	if !hasTarget && hasSubdirs {
		targets := make([]string, 0, len(repo.Subdirs))
		for _, s := range repo.Subdirs {
			targets = append(targets, s.Target)
		}
		targetDesc = strings.Join(targets, ", ")
	}
	if !hasURL && !hasPath {
		return repo, fmt.Errorf("Either url or path must be provided. Target: (%s)", targetDesc)
	}
	if hasURL && hasPath {
		return repo, fmt.Errorf("Either url or path must be provided, not both. Target: (%s)", targetDesc)
	}
	if hasHash && hasBranch {
		return repo, fmt.Errorf("Either hash or branch must be provided, not both. Target: (%s)", targetDesc)
	}
	if hasTarget == hasSubdirs {
		// Note: the message says "not both" even when neither is set (SPEC Q13).
		source := repo.Path
		if hasURL {
			source = logx.Redact(repo.URL)
		}
		return repo, fmt.Errorf("Either target or subdirs must be provided, not both. Source: (%s)", source)
	}
	return repo, nil
}

func parseSubdirs(m map[string]any) ([]SubdirConfig, bool, error) {
	v, present := m["subdirs"]
	if !present || v == nil {
		return nil, false, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, false, errors.New("subdirs must be a list of {subdir, target} objects")
	}
	if len(list) == 0 {
		return nil, false, errors.New("subdirs field must have at least 1 items")
	}
	out := make([]SubdirConfig, 0, len(list))
	for _, item := range list {
		sm, ok := item.(map[string]any)
		if !ok {
			return nil, false, errors.New("every entry of subdirs must be an object")
		}
		subdir, hasSubdir, err := stringField(sm, "subdir")
		if err != nil {
			return nil, false, err
		}
		if !hasSubdir {
			return nil, false, errors.New("subdirs.subdir is a required field")
		}
		target, hasTarget, err := stringField(sm, "target")
		if err != nil {
			return nil, false, err
		}
		if !hasTarget {
			return nil, false, errors.New("subdirs.target is a required field")
		}
		out = append(out, SubdirConfig{Subdir: subdir, Target: target})
	}
	return out, true, nil
}

// shortRe matches the GitHub shortcut `org/repo`. Dots are not allowed, so
// `org/repo.js` is not a shortcut (SPEC §5.7).
var shortRe = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_-]+$`)

// normalizeURL implements SPEC §5.7.
func normalizeURL(url string, isPrivate bool) string {
	if shortRe.MatchString(url) {
		if isPrivate {
			return "git@github.com:" + url + ".git"
		}
		return "https://github.com/" + url + ".git"
	}
	const httpsPrefix = "https://github.com/"
	if isPrivate && strings.HasPrefix(url, httpsPrefix) {
		rest := strings.TrimSuffix(url[len(httpsPrefix):], ".git")
		return "git@github.com:" + rest + ".git"
	}
	return url
}

// stringField reads a scalar field and coerces it to a string (SPEC §5.3).
// A missing key, a null value or an empty string all count as "missing".
func stringField(m map[string]any, key string) (string, bool, error) {
	v, present := m[key]
	if !present || v == nil {
		return "", false, nil
	}
	s, ok := asString(v)
	if !ok {
		return "", false, fmt.Errorf("%s must be a string", key)
	}
	if s == "" {
		return "", false, nil
	}
	return s, true, nil
}

// boolField reads a scalar field and coerces it to a bool (SPEC §5.3).
// A missing key or a null value counts as "missing"; anything that does not
// coerce (including an empty string) is a type error, as in yup.
func boolField(m map[string]any, key string) (bool, bool, error) {
	v, present := m[key]
	if !present || v == nil {
		return false, false, nil
	}
	b, ok := asBool(v)
	if !ok {
		return false, false, fmt.Errorf("%s must be a boolean", key)
	}
	return b, true, nil
}

// stringListField reads a list field whose items are coerced to strings
// (SPEC §5.3). An empty list counts as present (it can disable a default).
func stringListField(m map[string]any, key string) ([]string, bool, error) {
	v, present := m[key]
	if !present || v == nil {
		return nil, false, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, false, fmt.Errorf("%s must be a list of strings", key)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := asString(item)
		if !ok {
			return nil, false, fmt.Errorf("%s must be a list of strings", key)
		}
		out = append(out, s)
	}
	return out, true, nil
}

// asString coerces YAML scalars the way yup's non-strict string cast does:
// numbers and booleans become strings (SPEC §5.3).
func asString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case int:
		return strconv.Itoa(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case uint64:
		return strconv.FormatUint(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
}

// asBool coerces values the way yup's non-strict boolean cast does
// (SPEC §5.3): only "true"/"1" and "false"/"0" (case-insensitive) and the
// numbers 1/0.
func asBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(t) {
		case "true", "1":
			return true, true
		case "false", "0":
			return false, true
		}
	case int:
		if t == 1 {
			return true, true
		}
		if t == 0 {
			return false, true
		}
	case float64:
		if t == 1 {
			return true, true
		}
		if t == 0 {
			return false, true
		}
	}
	return false, false
}
