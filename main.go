// easyclone builds a Moodle installation from a YAML manifest.
//
// It downloads (and caches) a Moodle distribution, extracts it, then installs
// a list of plugins from git repositories or local directories. It only
// installs, never reconciles: targets are always deleted and recreated.
//
// The implementation lives in internal packages:
//
//	internal/installer — top-level flow (SPEC §4)
//	internal/config    — manifest loading and validation (§5)
//	internal/moodle    — Moodle download/extract/patch (§6)
//	internal/repo      — repository processing and git (§7)
//	internal/fsutil    — filesystem primitives (§8)
//	internal/logx      — progress logging (§9)
//
// See docs/SPEC.md for the full behavioral specification.
package main

import (
	"fmt"
	"os"

	"github.com/ffd114/easyclone-go/internal/installer"
)

const version = "0.1.1"

const defaultConfigPath = "easyclone.yaml"

func main() {
	configPath := resolveConfigPath(os.Args[1:])
	if err := installer.Run(configPath); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

// resolveConfigPath implements the CLI of SPEC §3. Unknown flags and
// positional arguments are silently ignored. The last occurrence of the
// config flag wins.
func resolveConfigPath(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			printUsage()
			os.Exit(0)
		case arg == "--version":
			fmt.Println("easyclone " + version)
			os.Exit(0)
		case arg == "-c" || arg == "--config":
			if i+1 < len(args) {
				return args[i+1]
			}
			return defaultConfigPath
		case len(arg) > len("--config=") && arg[:9] == "--config=":
			return arg[9:]
		}
	}
	return defaultConfigPath
}

func printUsage() {
	fmt.Printf(`easyclone %s

Builds a Moodle installation from a YAML manifest.

Usage:
  easyclone [-c <path> | --config <path> | --config=<path>]

Options:
  -c, --config <path>  Path to the config file (default: %s)
  -h, --help           Show this help and exit
      --version        Show version and exit
`, version, defaultConfigPath)
}
