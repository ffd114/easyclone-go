package fsutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// chdirTemp points the process at a fresh temp dir for the duration of the
// test, mirroring the per-test cwd of SPEC §12.
func chdirTemp(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestRemove(t *testing.T) {
	t.Run("deletes dir recursively with log", func(t *testing.T) {
		chdirTemp(t)
		write(t, "target/a/b.txt", "x")
		if err := Remove("target"); err != nil {
			t.Fatal(err)
		}
		if exists("target") {
			t.Fatal("target should be gone")
		}
	})
	t.Run("deletes file", func(t *testing.T) {
		chdirTemp(t)
		write(t, "file.txt", "x")
		if err := Remove("file.txt"); err != nil {
			t.Fatal(err)
		}
		if exists("file.txt") {
			t.Fatal("file should be gone")
		}
	})
	t.Run("missing path is silently ok", func(t *testing.T) {
		chdirTemp(t)
		if err := Remove("nope"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("refuses cwd (SPEC scenario 15a)", func(t *testing.T) {
		chdirTemp(t)
		write(t, "keep.txt", "x")
		err := Remove(".")
		if err == nil || !strings.Contains(err.Error(), "Refusing to delete: . | contains the working directory") {
			t.Fatalf("err = %v", err)
		}
		if !exists("keep.txt") {
			t.Fatal("cwd contents must survive")
		}
	})
	t.Run("refuses ancestor of cwd", func(t *testing.T) {
		parent := t.TempDir()
		child := filepath.Join(parent, "child")
		if err := os.Mkdir(child, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(child)
		err := Remove(parent)
		if err == nil || !strings.Contains(err.Error(), "Refusing to delete") {
			t.Fatalf("err = %v, want refusal", err)
		}
		if _, err := os.Lstat(child); err != nil {
			t.Fatal("cwd must survive")
		}
	})
	t.Run("refuses symlink pointing at cwd", func(t *testing.T) {
		chdirTemp(t)
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		write(t, "keep.txt", "x")
		if err := os.Symlink(wd, "link"); err != nil {
			t.Fatal(err)
		}
		if err := Remove("link"); err == nil {
			t.Fatal("want refusal for symlink resolving to cwd")
		}
		if !exists("keep.txt") {
			t.Fatal("cwd contents must survive")
		}
	})
	t.Run("broken symlink left alone", func(t *testing.T) {
		chdirTemp(t)
		if err := os.Symlink("does-not-exist", "broken"); err != nil {
			t.Fatal(err)
		}
		if err := Remove("broken"); err != nil {
			t.Fatal(err)
		}
		if !exists("broken") {
			t.Fatal("broken symlink should be left alone (SPEC §8.1)")
		}
	})
}

func TestCopyDir(t *testing.T) {
	t.Run("recursive copy with dotfiles and symlinks", func(t *testing.T) {
		chdirTemp(t)
		write(t, "src/a.txt", "a")
		write(t, "src/.hidden", "h")
		write(t, "src/sub/b.txt", "b")
		if err := os.Symlink("a.txt", "src/link.txt"); err != nil {
			t.Fatal(err)
		}
		if err := CopyDir("src", "dest"); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"dest/a.txt", "dest/.hidden", "dest/sub/b.txt"} {
			if !exists(p) {
				t.Errorf("%s missing", p)
			}
		}
		// Q10 fix: symlinks preserved.
		if target, err := os.Readlink("dest/link.txt"); err != nil || target != "a.txt" {
			t.Fatalf("symlink not preserved: %q, %v", target, err)
		}
	})
}

func TestApplyPatches(t *testing.T) {
	if _, err := exec.LookPath("patch"); err != nil {
		t.Skip("patch not installed")
	}
	t.Run("applies in lexicographic order (SPEC scenario 13)", func(t *testing.T) {
		chdirTemp(t)
		// 0002 depends on the result of 0001, so order is observable.
		write(t, "work/file.txt", "base\n")
		write(t, "patch/0002-b.patch", "--- a/file.txt\n+++ b/file.txt\n@@ -2 +2,2 @@\n step1\n+step2\n")
		write(t, "patch/0001-a.patch", "--- a/file.txt\n+++ b/file.txt\n@@ -1 +1,2 @@\n base\n+step1\n")
		write(t, "patch/notes.txt", "ignored: not a .patch file\n")

		if err := ApplyPatches("patch", "work"); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile("work/file.txt")
		if err != nil {
			t.Fatal(err)
		}
		if got := string(content); got != "base\nstep1\nstep2\n" {
			t.Fatalf("content = %q", got)
		}
	})
	t.Run("missing dir is silent", func(t *testing.T) {
		chdirTemp(t)
		if err := ApplyPatches("nope", "work"); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})
	t.Run("dir without patches is silent", func(t *testing.T) {
		chdirTemp(t)
		write(t, "empty/keep.txt", "x")
		if err := ApplyPatches("empty", "work"); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})
	t.Run("failing patch errors (SPEC scenario 14)", func(t *testing.T) {
		chdirTemp(t)
		write(t, "work/other.txt", "x\n")
		write(t, "patch/0001-bad.patch", "--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-nope\n+yes\n")
		err := ApplyPatches("patch", "work")
		if err == nil || !strings.HasPrefix(err.Error(), "Failed: patch 0001-bad.patch | ") {
			t.Fatalf("err = %v", err)
		}
	})
}
