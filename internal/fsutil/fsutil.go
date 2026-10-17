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
	"unicode/utf16"

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
	rel, err := filepath.Rel(dirOrAncestor, dir)
	if err != nil {
		return false
	}
	// Rel handles the filesystem root ("/" has no trailing component to
	// append a separator to) and never yields a ".."-prefixed path for a
	// descendant.
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
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
	if err := out.Close(); err != nil {
		return err
	}
	// OpenFile's mode is filtered by the umask; set it explicitly so the
	// permissions match the source exactly, like the OS copy (SPEC §8.2).
	return os.Chmod(dest, info.Mode().Perm())
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
	// Sorted lexicographically by UTF-16 code units; use the 0001-…, 0002-…
	// convention to control order (SPEC §8.3 step 4).
	sort.Slice(names, func(i, j int) bool { return lessUTF16(names[i], names[j]) })

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
			return fmt.Errorf("Failed: patch %s | %s", name, ErrDetail(err, stderr.String()))
		}
	}
	return nil
}

// ErrDetail is the text after `|` in a subprocess failure: its stderr, or
// the run error itself when stderr is empty (e.g. the binary is not
// installed), so the cause is never lost.
func ErrDetail(err error, stderr string) string {
	if s := strings.TrimSpace(stderr); s != "" {
		return s
	}
	return err.Error()
}

// lessUTF16 compares strings by UTF-16 code units, matching the original
// JavaScript sort (SPEC §8.3). It differs from a byte-wise compare only for
// characters outside the Basic Multilingual Plane.
func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}
