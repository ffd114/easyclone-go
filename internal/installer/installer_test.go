// End-to-end tests for the acceptance scenarios of SPEC §12. Each test runs
// in its own temp working directory (via chdirTemp), writes an easyclone.yaml
// config, and goes through the public installer.Run entry point.
package installer

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// --- helpers ---------------------------------------------------------------

// chdirTemp points the process at a fresh temp dir for the duration of the
// test, mirroring the per-test cwd of SPEC §12. Relative paths in the config
// resolve against it.
func chdirTemp(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

// runCLI mimics the real CLI: write the manifest, then run the installer.
func runCLI(t *testing.T, yamlText string) error {
	t.Helper()
	write(t, "easyclone.yaml", yamlText)
	return Run("easyclone.yaml")
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// gitRun runs a git command in dir with an isolated identity, for setting up
// test fixtures.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// initSourceRepo creates a git repo with one commit containing the given
// files, and returns the full SHA of that commit.
func initSourceRepo(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	gitRun(t, dir, "init", "--initial-branch=main")
	for name, content := range files {
		write(t, filepath.Join(dir, name), content)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "initial")
	return gitRun(t, dir, "rev-parse", "HEAD")
}

// bareRepo returns a file:// URL to a bare clone of src, configured to allow
// fetching arbitrary SHAs (needed for the `hash` mode, SPEC §7.4).
func bareRepo(t *testing.T, src, dir string) string {
	t.Helper()
	gitRun(t, dir, "clone", "--bare", src, dir+"/repo.git")
	gitRun(t, dir+"/repo.git", "config", "uploadpack.allowAnySHA1InWant", "true")
	return "file://" + dir + "/repo.git"
}

// --- in-memory archives (SPEC §6 fixtures) ----------------------------------

type tarGzWriter struct {
	gw *gzip.Writer
	tw *tar.Writer
}

func newTarGzWriter(w io.Writer) *tarGzWriter {
	gw := gzip.NewWriter(w)
	return &tarGzWriter{gw: gw, tw: tar.NewWriter(gw)}
}

func (t *tarGzWriter) WriteHeader(h *tar.Header) error { return t.tw.WriteHeader(h) }
func (t *tarGzWriter) Write(p []byte) (int, error)     { return t.tw.Write(p) }
func (t *tarGzWriter) Close() error {
	if err := t.tw.Close(); err != nil {
		return err
	}
	return t.gw.Close()
}

func dirHeader(name string) *tar.Header {
	return &tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755}
}

func fileHeader(name string, size int64) *tar.Header {
	return &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: size, Mode: 0o644}
}

// makeTgz builds an in-memory .tar.gz archive with a top-level directory.
func makeTgz(t *testing.T, topDir string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := newTarGzWriter(&buf)
	if err := tw.WriteHeader(dirHeader(topDir)); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		full := topDir + "/" + name
		if err := tw.WriteHeader(fileHeader(full, int64(len(content)))); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// makeZip builds an in-memory .zip archive with a top-level directory.
func makeZip(t *testing.T, topDir string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(topDir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// serveArchive serves bytes over HTTP, counting requests.
func serveArchive(t *testing.T, body []byte, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --- moodle (SPEC §6, scenarios 9-16) ----------------------------------------

func TestMoodleTgzDownloadAndCache(t *testing.T) {
	chdirTemp(t)
	hits := 0
	body := makeTgz(t, "moodle", map[string]string{"version.php": "<?php // v1\n"})
	srv := serveArchive(t, body, &hits)
	yaml := fmt.Sprintf("moodle:\n  url: %s/moodle.tgz\n  path: m\nrepositories: []\n", srv.URL)

	if err := runCLI(t, yaml); err != nil {
		t.Fatal(err)
	}

	// Scenario 9: extracted with the top-level dir stripped.
	if got := readFile(t, "m/version.php"); !strings.Contains(got, "v1") {
		t.Fatalf("version.php = %q", got)
	}
	if exists("m/moodle") {
		t.Fatal("top-level dir should be stripped")
	}

	// Scenario 9: cached at .cache/<sha256>.tar.gz.
	entries, err := os.ReadDir(".cache")
	if err != nil || len(entries) != 1 {
		t.Fatalf(".cache entries: %v, %v", entries, err)
	}
	name := entries[0].Name()
	if !regexp.MustCompile(`^[0-9a-f]{64}\.tar\.gz$`).MatchString(name) {
		t.Fatalf("cache name = %q", name)
	}
	if hits != 1 {
		t.Fatalf("http hits = %d, want 1", hits)
	}

	// Scenario 10: second run uses the cache, no HTTP request.
	if err := runCLI(t, yaml); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("http hits after cached run = %d, want 1", hits)
	}
}

func TestMoodleZipExtractsInnerFolder(t *testing.T) {
	chdirTemp(t)
	hits := 0
	body := makeZip(t, "moodle", map[string]string{"index.php": "<?php\n"})
	srv := serveArchive(t, body, &hits)

	// Scenario 11: the inner folder's contents land in path.
	err := runCLI(t, fmt.Sprintf(
		"moodle:\n  url: %s/moodle.zip\n  path: m\nrepositories: []\n", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, "m/index.php"); got != "<?php\n" {
		t.Fatalf("index.php = %q", got)
	}
	entries, _ := os.ReadDir(".cache")
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".zip") {
		t.Fatalf("cache = %v", entries)
	}
}

func TestMoodleDownload404(t *testing.T) {
	chdirTemp(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	err := runCLI(t, fmt.Sprintf(
		"moodle:\n  url: %s/moodle.tgz\n  path: m\nrepositories: []\n", srv.URL))
	if err == nil || err.Error() != "Failed to download Moodle: Not Found" {
		t.Fatalf("err = %v", err)
	}
	// Scenario 12: no cache file left behind.
	if entries, _ := os.ReadDir(".cache"); len(entries) != 0 {
		t.Fatalf("cache should be empty, got %v", entries)
	}
}

func TestMoodlePatches(t *testing.T) {
	if _, err := exec.LookPath("patch"); err != nil {
		t.Skip("patch not installed")
	}
	chdirTemp(t)
	hits := 0
	body := makeTgz(t, "moodle", map[string]string{"version.php": "base\n"})
	srv := serveArchive(t, body, &hits)

	// Scenario 13: 0001 then 0002, non-.patch ignored. 0002's context only
	// exists after 0001 is applied, so order is observable.
	write(t, "patches/0002-two.patch", "--- a/version.php\n+++ b/version.php\n@@ -2 +2,2 @@\n one\n+two\n")
	write(t, "patches/0001-one.patch", "--- a/version.php\n+++ b/version.php\n@@ -1 +1,2 @@\n base\n+one\n")
	write(t, "patches/README.txt", "not a patch\n")

	err := runCLI(t, fmt.Sprintf(`
moodle:
  url: %s/moodle.tgz
  path: m
  patchDir: patches
repositories: []
`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, "m/version.php"); got != "base\none\ntwo\n" {
		t.Fatalf("version.php = %q, want ordered application", got)
	}

	// Scenario 14: a failing patch aborts with `Failed: patch <name> | …`.
	write(t, "badpatches/0001-bad.patch", "--- a/version.php\n+++ b/version.php\n@@ -1 +1 @@\n-nope\n+yes\n")
	err = runCLI(t, fmt.Sprintf(`
moodle:
  url: %s/moodle.tgz
  path: m
  patchDir: badpatches
repositories: []
`, srv.URL))
	if err == nil || !strings.HasPrefix(err.Error(), "Failed: patch 0001-bad.patch | ") {
		t.Fatalf("err = %v", err)
	}
}

func TestMoodleCleanup(t *testing.T) {
	chdirTemp(t)
	hits := 0
	body := makeTgz(t, "moodle", map[string]string{
		"junk.txt":       "x",
		"keep.txt":       "k",
		"sub/nested.txt": "n",
	})
	srv := serveArchive(t, body, &hits)

	// Scenario 15: entries removed; missing entries (the default cleanup
	// names) are silently ignored.
	err := runCLI(t, fmt.Sprintf(`
moodle:
  url: %s/moodle.tgz
  path: m
  cleanup: [junk.txt, sub]
repositories: []
`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if exists("m/junk.txt") || exists("m/sub") {
		t.Fatal("cleanup entries should be removed")
	}
	if !exists("m/keep.txt") {
		t.Fatal("keep.txt should survive")
	}
}

func TestMoodlePathRefusesCwd(t *testing.T) {
	chdirTemp(t)
	hits := 0
	body := makeTgz(t, "moodle", map[string]string{"f.txt": "x"})
	srv := serveArchive(t, body, &hits)

	// Scenario 15a: moodle.path: . → Refusing to delete, nothing deleted.
	err := runCLI(t, fmt.Sprintf(
		"moodle:\n  url: %s/moodle.tgz\n  path: .\nrepositories: []\n", srv.URL))
	if err == nil || !strings.Contains(err.Error(), "Refusing to delete: . | contains the working directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestPluginRootWithoutMoodleIsCwd(t *testing.T) {
	chdirTemp(t)
	// Scenario 16: without a moodle section, plugins install relative to cwd.
	repoDir := t.TempDir()
	initSourceRepo(t, repoDir, map[string]string{"index.php": "<?php\n"})
	url := bareRepo(t, repoDir, t.TempDir())

	err := runCLI(t, fmt.Sprintf("repositories:\n  - url: %s\n    target: mod/x\n", url))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("mod/x/index.php"); err != nil {
		t.Fatalf("plugin not installed at mod/x: %v", err)
	}
}

// --- repositories (SPEC §7, scenarios 17-29) ---------------------------------

func TestRepoCloneBranchAndCleanup(t *testing.T) {
	chdirTemp(t)
	repoDir := t.TempDir()
	initSourceRepo(t, repoDir, map[string]string{"y.php": "<?php\n"})
	gitRun(t, repoDir, "checkout", "-b", "v2")
	write(t, filepath.Join(repoDir, "y.php"), "<?php // v2\n")
	gitRun(t, repoDir, "add", ".")
	gitRun(t, repoDir, "commit", "-m", "v2")
	url := bareRepo(t, repoDir, t.TempDir())

	err := runCLI(t, fmt.Sprintf(`
repositories:
  - url: %s
    branch: v2
    target: blocks/y
`, url))
	if err != nil {
		t.Fatal(err)
	}
	// Scenario 17: shallow single-branch clone at that branch/tag; root
	// cleanup removes .git.
	if got := readFile(t, "blocks/y/y.php"); !strings.Contains(got, "v2") {
		t.Fatalf("y.php = %q, want v2 branch content", got)
	}
	if exists("blocks/y/.git") {
		t.Fatal(".git should be removed by default cleanup")
	}
}

func TestRepoCheckoutHash(t *testing.T) {
	chdirTemp(t)
	repoDir := t.TempDir()
	sha := initSourceRepo(t, repoDir, map[string]string{"z.php": "one\n"})
	write(t, filepath.Join(repoDir, "z.php"), "two\n")
	gitRun(t, repoDir, "add", ".")
	gitRun(t, repoDir, "commit", "-m", "second")
	url := bareRepo(t, repoDir, t.TempDir())

	// `cleanup: []` keeps .git around for the detached-HEAD check below.
	err := runCLI(t, fmt.Sprintf(
		"cleanup: []\nrepositories:\n  - url: %s\n    hash: %s\n    target: mod/z\n", url, sha))
	if err != nil {
		t.Fatal(err)
	}
	// Scenario 18: working tree at that commit.
	if got := readFile(t, "mod/z/z.php"); got != "one\n" {
		t.Fatalf("z.php = %q, want content of %s", got, sha[:8])
	}
	// Detached HEAD (SPEC §7.4): `git symbolic-ref HEAD` must fail.
	cmd := exec.Command("git", "-C", "mod/z", "symbolic-ref", "-q", "HEAD")
	if out, err := cmd.CombinedOutput(); err == nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("want detached HEAD, symbolic-ref = %q (err %v)", out, err)
	}
}

func TestRepoDefaultBranch(t *testing.T) {
	chdirTemp(t)
	repoDir := t.TempDir()
	initSourceRepo(t, repoDir, map[string]string{"a.txt": "default\n"})
	url := bareRepo(t, repoDir, t.TempDir())

	// Scenario 19: no branch/hash → default branch.
	err := runCLI(t, fmt.Sprintf("repositories:\n  - url: %s\n    target: dest\n", url))
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, "dest/a.txt"); got != "default\n" {
		t.Fatalf("a.txt = %q", got)
	}
}

func TestRepoLocalPath(t *testing.T) {
	chdirTemp(t)
	// Scenario 20: a missing path → Missing: <path>.
	err := runCLI(t, "repositories:\n  - path: src\n    target: dest\n")
	if err == nil || err.Error() != "Missing: src" {
		t.Fatalf("err = %v, want Missing: src", err)
	}

	write(t, "src/a.txt", "a")
	write(t, "src/.git/config", "x")
	write(t, "src/sub/b.txt", "b")
	// Root cleanup is disabled here so we can verify that Mode A copies
	// dotfiles verbatim (SPEC §7.3 Mode A).
	if err := runCLI(t, "cleanup: []\nrepositories:\n  - path: src\n    target: dest\n"); err != nil {
		t.Fatal(err)
	}
	// Scenario 20: recursive copy, including dotfiles.
	for _, p := range []string{"dest/a.txt", "dest/.git/config", "dest/sub/b.txt"} {
		if !exists(p) {
			t.Errorf("%s missing", p)
		}
	}
}

func TestRepoSubdirsFromGit(t *testing.T) {
	chdirTemp(t)
	repoDir := t.TempDir()
	initSourceRepo(t, repoDir, map[string]string{
		"blocks_a/a.php": "a\n",
		"mod_b/b.php":    "b\n",
		"other.txt":      "o\n",
	})
	url := bareRepo(t, repoDir, t.TempDir())

	// Scenario 21: each subdir lands at its target.
	err := runCLI(t, fmt.Sprintf(`
repositories:
  - url: %s
    subdirs:
      - subdir: blocks_a
        target: blocks/a
      - subdir: mod_b
        target: mod/b
`, url))
	if err != nil {
		t.Fatal(err)
	}
	if !exists("blocks/a/a.php") || !exists("mod/b/b.php") {
		t.Fatal("subdirs not installed")
	}
	if exists("other.txt") {
		t.Fatal("files outside subdirs must not be installed")
	}
	if entries, _ := os.ReadDir("."); hasStaging(entries) {
		t.Fatal("no .easyclone-* may be left in rootDir")
	}
}

func TestRepoSubdirsMissing(t *testing.T) {
	chdirTemp(t)
	repoDir := t.TempDir()
	initSourceRepo(t, repoDir, map[string]string{"real/keep.txt": "x\n"})
	url := bareRepo(t, repoDir, t.TempDir())

	err := runCLI(t, fmt.Sprintf(`
repositories:
  - url: %s
    subdirs:
      - subdir: real
        target: ok
      - subdir: nope
        target: bad
`, url))
	if err == nil || err.Error() != "Missing: nope | "+url {
		t.Fatalf("err = %v, want Missing: nope | %s", err, url)
	}
	// Scenario 21: no .easyclone-* left in rootDir, even when a subdir is
	// missing.
	if entries, _ := os.ReadDir("."); hasStaging(entries) {
		t.Fatal("staging dir must be removed even on error")
	}
}

func TestRepoSubdirsFromLocalPath(t *testing.T) {
	chdirTemp(t)
	write(t, "src/a/one.txt", "1")
	write(t, "src/b/two.txt", "2")

	// Scenario 22: subdirs from a local path are copied.
	err := runCLI(t, `
repositories:
  - path: src
    subdirs:
      - subdir: a
        target: dest/a
      - subdir: b
        target: dest/b
`)
	if err != nil {
		t.Fatal(err)
	}
	if !exists("dest/a/one.txt") || !exists("dest/b/two.txt") {
		t.Fatal("subdirs not copied")
	}
}

func TestRepoEnableFalse(t *testing.T) {
	chdirTemp(t)
	write(t, "dest/keep.txt", "old")

	// Scenario 23: enable: false → target untouched, no output.
	err := runCLI(t, "repositories:\n  - path: src\n    target: dest\n    enable: false\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, "dest/keep.txt"); got != "old" {
		t.Fatalf("dest/keep.txt = %q, want untouched", got)
	}
}

func TestRepoSkipLogic(t *testing.T) {
	chdirTemp(t)
	write(t, "dest/keep.txt", "old")

	// Scenario 24: skip: true + existing target → Skipping: and no change.
	err := runCLI(t, "repositories:\n  - path: src\n    target: dest\n    skip: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, "dest/keep.txt"); got != "old" {
		t.Fatalf("existing target was modified: %q", got)
	}

	// Scenario 24: skip: true + missing target → installs.
	write(t, "src/new.txt", "new")
	err = runCLI(t, "repositories:\n  - path: src\n    target: dest2\n    skip: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !exists("dest2/new.txt") {
		t.Fatal("missing target should be installed despite skip: true")
	}
}

func TestRepoSkipOverride(t *testing.T) {
	chdirTemp(t)
	write(t, "dest/keep.txt", "old")
	write(t, "src/new.txt", "new")

	// Scenario 25: root skip: true, repo skip: false → reinstalled.
	err := runCLI(t, "skip: true\nrepositories:\n  - path: src\n    target: dest\n    skip: false\n")
	if err != nil {
		t.Fatal(err)
	}
	if !exists("dest/new.txt") {
		t.Fatal("repo skip: false must override root skip: true")
	}

	// Scenario 25: root skip: false, repo skip: true → skipped.
	write(t, "dest2/keep.txt", "old")
	err = runCLI(t, "repositories:\n  - path: src\n    target: dest2\n    skip: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, "dest2/keep.txt"); got != "old" {
		t.Fatal("repo skip: true must skip even when root skip is false")
	}
}

func TestRepoPatchTrue(t *testing.T) {
	if _, err := exec.LookPath("patch"); err != nil {
		t.Skip("patch not installed")
	}
	chdirTemp(t)
	// Scenario 26: <target>/patch applied to the Moodle root, then removed.
	hits := 0
	body := makeTgz(t, "moodle", map[string]string{"version.php": "base\n"})
	srv := serveArchive(t, body, &hits)

	repoDir := t.TempDir()
	initSourceRepo(t, repoDir, map[string]string{
		"blocks_p/p.php":        "p\n",
		"patch/0001-core.patch": "--- a/version.php\n+++ b/version.php\n@@ -1 +1,2 @@\n base\n+patched\n",
	})
	url := bareRepo(t, repoDir, t.TempDir())

	err := runCLI(t, fmt.Sprintf(`
moodle:
  url: %s/moodle.tgz
  path: m
repositories:
  - url: %s
    target: blocks/p
    patch: true
`, srv.URL, url))
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, "m/version.php"); got != "base\npatched\n" {
		t.Fatalf("m/version.php = %q, want patched", got)
	}
	if exists("blocks/p/patch") {
		t.Fatal("<target>/patch must be removed after use")
	}

	// Scenario 26: missing patch dir → Missing: <target>/patch.
	repoDir2 := t.TempDir()
	initSourceRepo(t, repoDir2, map[string]string{"blocks_q/q.php": "q\n"})
	url2 := bareRepo(t, repoDir2, t.TempDir())
	err = runCLI(t, fmt.Sprintf(`
repositories:
  - url: %s
    target: blocks/q
    patch: true
`, url2))
	if err == nil || err.Error() != "Missing: blocks/q/patch" {
		t.Fatalf("err = %v, want Missing: blocks/q/patch", err)
	}
}

func TestRepoCleanupOrder(t *testing.T) {
	chdirTemp(t)
	// Scenario 27: root cleanup runs before repo cleanup; both apply to
	// every subdirs target.
	for _, dir := range []string{"src/a", "src/b"} {
		write(t, dir+"/rootjunk.txt", "x")
		write(t, dir+"/repojunk.txt", "x")
		write(t, dir+"/keep.txt", "k")
	}

	err := runCLI(t, `
cleanup: [rootjunk.txt]
repositories:
  - path: src
    subdirs:
      - subdir: a
        target: one
      - subdir: b
        target: two
    cleanup: [repojunk.txt]
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"one", "two"} {
		if exists(dir+"/rootjunk.txt") || exists(dir+"/repojunk.txt") {
			t.Errorf("%s: junk not cleaned", dir)
		}
		if !exists(dir + "/keep.txt") {
			t.Errorf("%s: keep.txt missing", dir)
		}
	}
}

func TestGitFailureAborts(t *testing.T) {
	chdirTemp(t)
	write(t, "first/keep.txt", "x")

	// Scenario 28: git failure → `Failed: git clone … | <stderr>`; earlier
	// repos remain installed.
	err := runCLI(t, `
repositories:
  - path: first
    target: installed-first
  - url: /definitely/not/a/repo.git
    target: never
`)
	if err == nil || !strings.HasPrefix(err.Error(), "Failed: git clone") {
		t.Fatalf("err = %v", err)
	}
	if !exists("installed-first/keep.txt") {
		t.Fatal("earlier repos must remain installed")
	}
}

func TestSSHKeyEnv(t *testing.T) {
	chdirTemp(t)
	// Scenario 29: sshKey set → the git subprocess env contains
	// GIT_SSH_COMMAND=ssh -i <key> -o StrictHostKeyChecking=no.
	// Verified with a fake `git` that records the env and fails.
	if err := os.MkdirAll("bin", 0o755); err != nil {
		t.Fatal(err)
	}
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$GIT_SSH_COMMAND\" > " + filepath.Join(pwd, "sshcmd.txt") + "\nexit 1\n"
	if err := os.WriteFile("bin/git", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", filepath.Join(pwd, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))

	err = runCLI(t, "sshKey: /path/with space/key\nrepositories:\n  - url: git@github.com:o/r.git\n    target: dest\n")
	if err == nil {
		t.Fatal("fake git should fail")
	}
	got, readErr := os.ReadFile("sshcmd.txt")
	if readErr != nil {
		t.Fatalf("fake git did not run: %v (err: %v)", readErr, err)
	}
	want := "ssh -i /path/with space/key -o StrictHostKeyChecking=no\n"
	if string(got) != want {
		t.Fatalf("GIT_SSH_COMMAND = %q, want %q", got, want)
	}
}

// --- misc -------------------------------------------------------------------

func hasStaging(entries []os.DirEntry) bool {
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".easyclone-") {
			return true
		}
	}
	return false
}
