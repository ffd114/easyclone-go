// Package logx contains the progress logging shared by all easyclone
// packages. Progress logs go to stdout, errors to stderr (SPEC §9).
package logx

import (
	"fmt"
	"os"
)

// Logf writes a progress log line to stdout (SPEC §9).
func Logf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}
