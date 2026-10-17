// Package moodle implements the Moodle setup phase of SPEC §6: download
// (with cache), extraction, patching and cleanup.
package moodle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ffd114/easyclone-go/internal/config"
	"github.com/ffd114/easyclone-go/internal/fsutil"
	"github.com/ffd114/easyclone-go/internal/logx"
)

// Setup implements SPEC §6. It runs only if moodle is present and
// moodle.url is non-empty.
func Setup(cfg *config.Config) error {
	m := cfg.Moodle
	if m == nil || m.URL == "" {
		return nil
	}

	target := m.Path
	if err := fsutil.Remove(target); err != nil {
		return err
	}

	pwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cacheDir := filepath.Join(pwd, ".cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}

	// Cache key: lowercase hex SHA-256 of the url string (SPEC §6).
	sum := sha256.Sum256([]byte(m.URL))
	key := hex.EncodeToString(sum[:])
	low := strings.ToLower(m.URL)
	var ext string
	switch {
	case strings.Contains(low, ".zip"):
		ext = ".zip"
	case strings.Contains(low, ".tgz") || strings.Contains(low, ".tar.gz"):
		ext = ".tar.gz"
	default:
		ext = ".tgz"
	}
	archive := filepath.Join(cacheDir, key+ext)

	if st, err := os.Stat(archive); err == nil && st.Mode().IsRegular() {
		logx.Logf("Using cached Moodle archive: %s", archive)
	} else {
		logx.Logf("Downloading Moodle from: %s", logx.Redact(m.URL))
		if err := download(m.URL, archive); err != nil {
			return err
		}
	}

	logx.Logf("Extracting Moodle to %s", target)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}

	// The extractor is chosen from the original (case-sensitive) url
	// (SPEC §6.2). Extraction failures are fatal (SPEC Q6 fix).
	if strings.Contains(m.URL, ".zip") {
		if err := extractZip(archive, target); err != nil {
			return err
		}
	} else {
		cmd := exec.Command("tar", "-xf", archive, "--strip-components=1", "-C", target)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("Failed: tar -xf %s --strip-components=1 -C %s | %s",
				archive, target, fsutil.ErrDetail(err, stderr.String()))
		}
	}

	// A missing patchDir is silently ignored (SPEC §6.3).
	if m.Patch {
		if err := fsutil.ApplyPatches(m.PatchDir, target); err != nil {
			return err
		}
	}
	for _, name := range m.Cleanup {
		if err := fsutil.Remove(filepath.Join(target, name)); err != nil {
			return err
		}
	}
	return nil
}

// download fetches url into dest (cache location, SPEC §6.1). The body is
// written to a .part file and renamed atomically, so an interrupted download
// never leaves a corrupt archive in the cache (SPEC Q5 fix).
func download(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Failed to download Moodle: %s", statusText(resp))
	}

	part := dest + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}

	bar := newProgressBar(resp.ContentLength)
	counting := &countingWriter{w: f, onWrite: bar.update}
	_, copyErr := io.Copy(counting, resp.Body)
	if copyErr == nil {
		bar.finish()
	}
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(part)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(part)
		return closeErr
	}
	if err := os.Rename(part, dest); err != nil {
		_ = os.Remove(part)
		return err
	}
	return nil
}

// statusText extracts the reason phrase from the response status line
// (e.g. "Not Found" for 404), falling back to the standard text.
func statusText(resp *http.Response) string {
	code := strconv.Itoa(resp.StatusCode)
	if text := strings.TrimPrefix(resp.Status, code+" "); text != "" {
		return text
	}
	return http.StatusText(resp.StatusCode)
}

// countingWriter counts the bytes streamed through it and reports the
// running total (used for the download progress bar).
type countingWriter struct {
	w       io.Writer
	written int64
	onWrite func(total int64)
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.written += int64(n)
	w.onWrite(w.written)
	return n, err
}

// progressBar renders the TTY-only progress bar of SPEC §6.1 step 4. It is
// cosmetic: any equivalent would do (SPEC §13).
type progressBar struct {
	total    int64 // <= 0 when unknown
	current  int64 // bytes received so far
	isTTY    bool
	lastDraw time.Time
	done     bool
}

const barWidth = 40

func newProgressBar(total int64) *progressBar {
	return &progressBar{total: total, isTTY: isTTY(os.Stdout)}
}

// update records the running byte total and redraws the bar.
func (b *progressBar) update(current int64) {
	b.current = current
	if !b.isTTY {
		return
	}
	// Throttle redraws to ~10 per second.
	now := time.Now()
	if !b.lastDraw.IsZero() && now.Sub(b.lastDraw) < 100*time.Millisecond {
		return
	}
	b.lastDraw = now
	b.draw(current)
}

func (b *progressBar) finish() {
	if !b.isTTY {
		return
	}
	if b.done {
		return
	}
	b.done = true
	// Draw the bytes actually received; total is -1 when the size is unknown.
	b.draw(b.current)
	fmt.Fprint(os.Stdout, "\n")
}

func (b *progressBar) draw(current int64) {
	if b.total > 0 {
		if current > b.total {
			current = b.total
		}
		fraction := float64(current) / float64(b.total)
		filled := int(fraction * barWidth)
		fmt.Fprintf(os.Stdout, "\rDownloading [%s%s] %d%% | %.2fMB / %.2fMB",
			strings.Repeat("█", filled),
			strings.Repeat("░", barWidth-filled),
			int(fraction*100),
			mb(current), mb(b.total))
	} else {
		fmt.Fprintf(os.Stdout, "\rDownloading | %.2fMB", mb(current))
	}
}

func mb(n int64) float64 {
	return float64(n) / 1024 / 1024
}

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// extractZip implements the zip branch of SPEC §6.2. Zip extraction failures
// are fatal (SPEC Q6 fix).
func extractZip(archive, target string) error {
	tmp, err := os.MkdirTemp("", "easyclone-")
	if err != nil {
		return err
	}
	defer func() { _ = fsutil.Remove(tmp) }()

	cmd := exec.Command("unzip", "-q", archive, "-d", tmp)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Failed: unzip -q %s -d %s | %s",
			archive, tmp, fsutil.ErrDetail(err, stderr.String()))
	}

	// Take the first directory entry, in directory listing order (File.ReadDir
	// does not sort, unlike os.ReadDir), as the source; if there is none, use
	// the temp dir itself (SPEC §6.2 step 2).
	dir, err := os.Open(tmp)
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return err
	}
	src := tmp
	for _, entry := range entries {
		if entry.IsDir() {
			src = filepath.Join(tmp, entry.Name())
			break
		}
	}
	// Copy the source's contents into target (symlinks are preserved,
	// SPEC Q10 fix).
	return fsutil.CopyDirContents(src, target)
}
