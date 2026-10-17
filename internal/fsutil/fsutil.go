// Package fsutil implements the shared filesystem primitives of SPEC §8:
// the guarded best-effort remove, directory copying and patch application.
package fsutil

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ffd114/easyclone-go/internal/logx"
)

// Remove implements the best-effort rm of SPEC §8.1. The cwd guard is
// fatal; every other error is swallowed.
func Remove(path string) error {
	// Guard: refuse to delete the cwd or an ancestor of it (SPEC §8.1).
	if wd, err := os.Getwd(); err == nil {
		if realCwd, err := realpath(wd); err == nil {
			if real, err := realpath(path); err == nil {
				if real == realCwd || isAncestor(real, realCwd) {
					return fmt.Errorf("Refusing to delete: %s | contains the working directory", path)
				}
			}
		}
	}

	st, err := os.Stat(path)
	if err != nil {
		// Not found, permission denied, broken symlink: all swallowed.
		return nil
	}
	if st.IsDir() || st.Mode().IsRegular() {
		logx.Logf("Deleting: %s", path)
		// Every removal error is swallowed (SPEC §8.1).
		_ = os.RemoveAll(path)
	}
	return nil
}

// realpath resolves symlinks in path and returns its absolute real path.
func realpath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// isAncestor reports whether dir is dirOrAncestor itself or an ancestor of it.
func isAncestor(dirOrAncestor, dir string) bool {
	if dirOrAncestor == dir {
		return true
	}
	return strings.HasPrefix(dir, dirOrAncestor+string(filepath.Separator))
}

// CopyDir implements SPEC §8.2: log, mkdir -p, then recurse.
func CopyDir(src, dest string) error {
	logx.Logf("Copying : %s | output %s", src, dest)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	return CopyDirContents(src, dest)
}

// CopyDirContents copies the contents of src into dest. Directories are
// recursed, regular files are copied with their permissions, and symlinks
// are preserved (SPEC Q10 fix). Other special files are skipped.
// The Moodle zip extraction path (SPEC §6.2) uses this without a log line
// or an initial mkdir.
func CopyDirContents(src, dest string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		s := filepath.Join(src, entry.Name())
		d := filepath.Join(dest, entry.Name())
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(s)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, d); err != nil {
				return err
			}
		case entry.IsDir():
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			if err := CopyDirContents(s, d); err != nil {
				return err
			}
		case entry.Type().IsRegular():
			if err := copyFile(s, d); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dest string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ApplyPatches implements SPEC §8.3. A missing or unreadable patchDir, or
// one without any *.patch files, is silently ignored.
func ApplyPatches(patchDir, targetDir string) error {
	st, err := os.Stat(patchDir)
	if err != nil || !st.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(patchDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		// Regular files only, top level only, name ends with ".patch"
		// (case-sensitive).
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".patch") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return nil
	}
	// Sorted lexicographically; use the 0001-…, 0002-… convention to control
	// order (SPEC §8.3 step 4).
	sort.Strings(names)

	noun := "patches"
	if len(names) == 1 {
		noun = "patch"
	}
	logx.Logf("Patching: %s | %d %s", targetDir, len(names), noun)

	// The `patch` subprocess runs in the working directory, so relative
	// targets and patch files resolve against it (SPEC §4).
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}

	for _, name := range names {
		logx.Logf("Patching: %s | output %s", name, targetDir)
		patchFile := filepath.Join(patchDir, name)
		if !filepath.IsAbs(patchFile) {
			patchFile = filepath.Join(pwd, patchFile)
		}
		cmd := exec.Command("patch", "-p1", "-d", targetDir, "-i", patchFile)
		cmd.Dir = pwd
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("Failed: patch %s | %s", name, strings.TrimSpace(stderr.String()))
		}
	}
	return nil
}
