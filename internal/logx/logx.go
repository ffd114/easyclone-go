// Package logx contains the progress logging shared by all easyclone
// packages. Progress logs go to stdout, errors to stderr (SPEC §9).
package logx

import (
	"fmt"
	"os"
	"regexp"
)

// Logf writes a progress log line to stdout (SPEC §9).
func Logf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}

// credRe matches a userinfo component in a URL (e.g. user:token@) so that
// credentials are not printed in logs or errors (SPEC Q8 fix). It matches
// anywhere in the string, since URLs are often embedded in log lines and
// joined git argument lists.
var credRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)

// Redact strips userinfo from any URL in the given string.
func Redact(s string) string {
	return credRe.ReplaceAllString(s, "$1")
}
