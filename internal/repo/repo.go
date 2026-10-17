// Package repo implements the repository processing phase of SPEC §7 and
// the git operations of SPEC §7.4.
package repo

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ffd114/easyclone-go/internal/config"
	"github.com/ffd114/easyclone-go/internal/fsutil"
	"github.com/ffd114/easyclone-go/internal/logx"
)

type install struct {
	subdir string // empty when the whole source is installed
	target string
}

// Process implements SPEC §7.
func Process(cfg *config.Config, r *config.RepoConfig) error {
	rootDir := "."
	if cfg.Moodle != nil {
		rootDir = cfg.Moodle.Path
	}

	// Install list (SPEC §7.1).
	var installs []install
	if r.Subdirs != nil {
		for _, s := range r.Subdirs {
			installs = append(installs, install{subdir: s.Subdir, target: filepath.Join(rootDir, s.Target)})
		}
	} else {
		installs = append(installs, install{target: filepath.Join(rootDir, r.Target)})
	}

	// Skip / enable decision (SPEC §7.2).
	allExist := true
	for _, in := range installs {
		if st, err := os.Stat(in.target); err != nil || !st.IsDir() {
			allExist = false
			break
		}
	}
	skip := cfg.Skip
	if r.Skip != nil {
		skip = *r.Skip
	}
	if skip && r.Enable && allExist {
		for _, in := range installs {
			logx.Logf("Skipping: %s", in.target)
		}
		return nil
	}
	if !r.Enable {
		// A complete no-op; existing targets are left untouched.
		return nil
	}

	// Install (SPEC §7.3): delete every target first.
	for _, in := range installs {
		if err := fsutil.Remove(in.target); err != nil {
			return err
		}
	}

	switch {
	case r.Path != "":
		// Mode A: local path (whole or per subdir).
		for _, in := range installs {
			source := r.Path
			if in.subdir != "" {
				source = filepath.Join(r.Path, in.subdir)
			}
			if st, err := os.Stat(source); err != nil || !st.IsDir() {
				return fmt.Errorf("Missing: %s", source)
			}
			if err := fsutil.CopyDir(source, in.target); err != nil {
				return err
			}
		}
	case len(r.Subdirs) == 0:
		// Mode B: url without subdirs.
		if r.Hash != "" {
			if err := checkoutHash(cfg, r.URL, installs[0].target, r.Hash); err != nil {
				return err
			}
		} else {
			if err := cloneBranch(cfg, r.URL, installs[0].target, r.Branch); err != nil {
				return err
			}
		}
	default:
		// Mode C: url with subdirs, staged inside rootDir (SPEC §7.3 Mode C).
		if err := installFromGitSubdirs(cfg, r, installs, rootDir); err != nil {
			return err
		}
	}

	// Post-install, per install target, in order (SPEC §7.5).
	for _, in := range installs {
		if err := postInstall(cfg, r, in, rootDir); err != nil {
			return err
		}
	}
	return nil
}

// installFromGitSubdirs clones into a staging dir inside rootDir, then moves
// each subdir to its target. The staging dir is always removed, even on
// error (SPEC §7.3 Mode C step 4).
func installFromGitSubdirs(cfg *config.Config, r *config.RepoConfig, installs []install, rootDir string) error {
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(rootDir, ".easyclone-")
	if err != nil {
		return err
	}
	defer func() { _ = fsutil.Remove(staging) }()

	if r.Hash != "" {
		if err := checkoutHash(cfg, r.URL, staging, r.Hash); err != nil {
			return err
		}
	} else {
		if err := cloneBranch(cfg, r.URL, staging, r.Branch); err != nil {
			return err
		}
	}

	for _, in := range installs {
		source := filepath.Join(staging, in.subdir)
		if st, err := os.Stat(source); err != nil || !st.IsDir() {
			return fmt.Errorf("Missing: %s | %s", in.subdir, logx.Redact(r.URL))
		}
		logx.Logf("Moving: %s | output %s", source, in.target)
		if err := os.MkdirAll(filepath.Dir(in.target), 0o755); err != nil {
			return err
		}
		if err := os.Rename(source, in.target); err != nil {
			return err
		}
	}
	return nil
}

// postInstall implements SPEC §7.5: repo patches, then root cleanup, then
// repo cleanup.
func postInstall(cfg *config.Config, r *config.RepoConfig, in install, rootDir string) error {
	if r.Patch {
		patchDir := filepath.Join(in.target, "patch")
		if st, err := os.Stat(patchDir); err != nil || !st.IsDir() {
			return fmt.Errorf("Missing: %s", patchDir)
		}
		// Patches apply to the Moodle root, not the plugin.
		if err := fsutil.ApplyPatches(patchDir, rootDir); err != nil {
			return err
		}
		if err := fsutil.Remove(patchDir); err != nil {
			return err
		}
	}
	for _, name := range cfg.Cleanup {
		if err := fsutil.Remove(filepath.Join(in.target, name)); err != nil {
			return err
		}
	}
	for _, name := range r.Cleanup {
		if err := fsutil.Remove(filepath.Join(in.target, name)); err != nil {
			return err
		}
	}
	return nil
}

// gitEnv builds the environment for every git subprocess: the inherited env
// plus GIT_SSH_COMMAND when a root sshKey is set (SPEC §7.4).
func gitEnv(cfg *config.Config) []string {
	env := os.Environ()
	if cfg.SSHKey != "" {
		env = append(env, "GIT_SSH_COMMAND=ssh -i "+cfg.SSHKey+" -o StrictHostKeyChecking=no")
	}
	return env
}

// runGit runs git with captured output. On a non-zero exit it throws
// `Failed: git <args> | <stderr>` (SPEC §7.4).
func runGit(cfg *config.Config, dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(cfg)
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Failed: git %s | %s",
			logx.Redact(strings.Join(args, " ")),
			logx.Redact(fsutil.ErrDetail(err, stderr.String())))
	}
	return nil
}

// cloneBranch implements SPEC §7.4 cloneBranch.
func cloneBranch(cfg *config.Config, url, dest, branch string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if branch != "" {
		logx.Logf("Cloning: %s branch %s | output %s", logx.Redact(url), branch, dest)
		return runGit(cfg, dir, "clone", "--depth=1", "--single-branch", "--branch", branch, url, dest)
	}
	// Note the double space when no branch is given (SPEC §9.1).
	logx.Logf("Cloning: %s  | output %s", logx.Redact(url), dest)
	return runGit(cfg, dir, "clone", "--depth=1", "--single-branch", url, dest)
}

// checkoutHash implements SPEC §7.4 checkoutHash: init an empty repo and
// fetch exactly one commit. The server must allow fetching by SHA, and in
// practice a full SHA is required.
func checkoutHash(cfg *config.Config, url, dest, hash string) error {
	logx.Logf("Fetching: %s hash %s | output %s", logx.Redact(url), hash, dest)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	steps := [][]string{
		{"init"},
		{"remote", "add", "origin", url},
		{"fetch", "--depth=1", "origin", hash},
		{"checkout", "FETCH_HEAD"},
	}
	for _, step := range steps {
		if err := runGit(cfg, dest, step...); err != nil {
			return err
		}
	}
	return nil
}
