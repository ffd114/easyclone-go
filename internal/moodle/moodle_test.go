package moodle

import (
	"io"
	"testing"
)

func TestCountingWriterReportsRunningTotal(t *testing.T) {
	bar := newProgressBar(-1)
	w := &countingWriter{w: io.Discard, onWrite: bar.update}
	for _, chunk := range []string{"abc", "de", "f"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if bar.current != 6 {
		t.Fatalf("bar.current = %d, want cumulative 6", bar.current)
	}
}
