# easyclone-go

A Go rewrite of [easyclone](https://github.com/ffd114/easyclone) v0.1.1
(Deno implementation, commit `5cc802e`), built from the behavioral
specification in [`docs/SPEC.md`](docs/SPEC.md).

easyclone builds a Moodle installation from a YAML manifest, in one go:

1. Optionally download a Moodle distribution archive (cached under
   `.cache/`), extract it, apply local patches to it, and delete unwanted
   files.
2. Install a list of Moodle plugins from git repositories (branch/tag,
   commit hash, or default branch) or local directories, either whole or as
   selected subdirectories.
3. Optionally apply core patches shipped inside a plugin to the Moodle root,
   then delete unwanted files from each plugin.

It is meant for container builds, where the filesystem is fresh on every
run. It **only installs, never reconciles**: targets are always deleted and
recreated. The only exception is `skip`.

## Usage

```
easyclone [-c <path> | --config <path> | --config=<path>]
```

- The config file defaults to `easyclone.yaml`, resolved against the
  **working directory** of the process (not the script's directory).
- Progress logs go to stdout, errors to stderr, exit code `1` on any error.
- `-h/--help` and `--version` are supported; unknown flags and positional
  arguments are silently ignored.

## Install

### Run directly with `go run`

From the repository root (needs Go 1.27+):

```
go run .                            # uses ./easyclone.yaml
go run . -c /path/to/manifest.yaml  # explicit config
go run . --config=path/to/manifest.yaml
```

### Build the binary

```
go build -o easyclone .    # from the repo root
```

This produces a single `easyclone` binary. The runtime subprocesses
(`git`, `patch`, `tar`, `unzip`, `ssh`) must be present wherever it runs —
the binary itself has no other dependencies.

Then:

```
./easyclone                       # uses ./easyclone.yaml
./easyclone -c /path/to/manifest.yaml
./easyclone --help
./easyclone --version
```

### Cross-compiling (for container builds / CI)

```
GOOS=linux GOARCH=amd64 go build -o easyclone-linux-amd64 .
GOOS=linux GOARCH=arm64 go build -o easyclone-linux-arm64 .
CGO_ENABLED=0 GOOS=linux go build -o easyclone .   # fully static, avoids a libc dependency in scratch/alpine images
```

`CGO_ENABLED=0` makes the binary fully static (pure Go), which keeps the
same container story as the original `deno compile` build.

Runtime subprocesses — `git`, `ssh` (via `GIT_SSH_COMMAND`), `patch`, and
`tar`/`unzip` for non-zip/zip archives — are not checked up front; a missing
binary surfaces when first used inside the container or host.

## Config quick reference

See [`docs/SPEC.md`](docs/SPEC.md) §5 for the full schema, and
[`easyclone.example.yaml`](easyclone.example.yaml) for a commented example.

- `${VAR}` / `${VAR:-default}` substitution runs on the **raw file text**
  before YAML parsing (including comments). `\${VAR}` escapes it.
- Relative paths (`moodle.path`, `moodle.patchDir`, `path`, `patchDir`…)
  resolve against the cwd.
- URL shortcuts: `org/repo` → `https://github.com/org/repo.git`;
  with `isPrivate: true` → `git@github.com:org/repo.git`.
- Unknown keys are silently ignored; numeric scalars coerce to strings
  (`branch: 2024041001` → `"2024041001"`), so quote them if you prefer.

## Project layout

`main.go` is the CLI only; all logic lives in `internal/` packages:

| Package | Contents |
|---|---|
| `internal/installer` | Top-level flow (SPEC §4): load config → Moodle setup → each repository in order. Also hosts the end-to-end acceptance tests. |
| `internal/config` | Manifest loading, `${VAR}` substitution, YAML parsing, validation and URL normalization (SPEC §5). |
| `internal/moodle` | Moodle download with cache, extraction, core patches and cleanup (SPEC §6). |
| `internal/repo` | Repository processing: install modes, skip/enable logic, git operations (SPEC §7). |
| `internal/fsutil` | Filesystem primitives: guarded remove, directory copy, patch application (SPEC §8). |
| `internal/logx` | Progress logging to stdout (SPEC §9). |

## Quirk decisions (SPEC §11)

Where the original had bugs, this rewrite keeps or fixes them as follows:

| # | Decision | Rationale |
|---|---|---|
| Q1 | Keep the fix | `moodle.path` is required; `rm` refuses cwd/ancestors (§8.1). |
| Q2 | Keep the fix | Per-repo `skip` overrides root `skip`, both ways (§7.2). |
| Q3 | Keep (silent) | `enable: false` stays silent, matching the original and §7.2. |
| Q4 | Keep | Env substitution still runs before YAML parsing; `${UNSET}` in a comment throws. Documented here. |
| Q5 | **Fixed** | Downloads stream to `<archive>.part` and rename atomically; an interrupted download can no longer poison the cache. |
| Q6 | **Fixed** | `tar`/`unzip` failures are fatal (`Failed: tar … \| <stderr>` / `Failed: unzip … \| <stderr>`). |
| Q7 | Keep | Substring-based archive extension detection, as specified in §6. |
| Q8 | **Fixed** | `userinfo` credentials are redacted from `Cloning:`/`Fetching:`/`Downloading Moodle from:` logs, `Failed: git …` and `Missing: <subdir> \| <url>` errors, and the rule 4 validation message. |
| Q9 | Keep | Relative paths resolve against cwd (container use). Documented here. |
| Q10 | **Fixed** | Symlinks are preserved by local copies and zip extraction. |
| Q11 | Keep | `sshKey` unquoted in `GIT_SSH_COMMAND`, `StrictHostKeyChecking=no` always on (§7.4). |
| Q12 | Keep | Unknown config keys are silently accepted. |
| Q13 | Keep | Rule 4's message says "not both" even when neither is set (§5.6). |
| Q14 | Keep | `Missing: <target>/patch` check happens after the clone (§7.5). |
| Q15 | Keep | YAML errors print exactly `Invalid YAML`. |
| Q16 | Partially fixed | `-h/--help` and `--version` added; other unknown flags are still silently ignored so extra arguments don't break scripts. |
| Q17 | Keep | `branch` accepts branches and tags, as intended (§7.4). |

## Development

```
go build ./...   # build
go vet ./...     # static analysis
go test ./...    # unit + end-to-end tests (uses local git repos and an
                 # in-process HTTP server; no network required)
```

Test layout, mapped to the acceptance scenarios in `docs/SPEC.md` §12:

- `internal/config/config_test.go` — env substitution (§12.3), URL
  normalization (§12.8), schema/cross-field validation (§12.4-6),
  coercion (§12.7).
- `internal/fsutil/fsutil_test.go` — `rm` guard (§12.15a), `copyDir` incl.
  symlink preservation, `applyPatches` ordering/failures (§12.13-14).
- `internal/logx/logx_test.go` — credential redaction in logs (Q8).
- `internal/moodle/moodle_test.go` — download progress byte counting.
- `internal/installer/installer_test.go` — all end-to-end scenarios: Moodle
  download/cache/extract/patch/cleanup (§12.9-15a) and plugin installation
  from local repos over `file://` URLs (§12.16-29).
