<!-- Copyright The DeviceChain Authors -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Terminal dependency audit

The inventory covers 45 module paths, 48 selected versions and 61 license/notice
files. Standalone and workspace builds select different versions of go-isatty,
x/exp and x/tools/go/expect; all selected versions are retained below.

Audited on 2026-11-25 against standalone CLI and workspace selected versions, using the
module-cache sources verified by `warm_modules`. The six roots are Bubble Tea,
Lip Gloss, Bubbles, termenv, x/term, and test-only teatest at the specification's
exact pins. No Huh or Charm v2 module is adopted.

The audit parses each selected module's own requirements with Go's parser and
follows **every requirement edge at its MVS-selected version**, not
just packages imported on Linux. It includes test and Windows requirements,
including coninput, cancelreader, go-localereader, fuzzy, and the golden tools.
All selected module versions are permissively licensed: MIT, BSD-3-Clause, or Apache-2.0.
Go's additional patent grant is retained. No copyleft obligation was found.

`testdata/dependency-licenses.tsv` records every reviewed license/notice file's
SHA-256. Nested notices include stringish's `utf8/LICENSE` (Go BSD-3-Clause).
go-udiff carries both MIT and BSD texts. go-localereader v0.0.1 has no LICENSE
file: its README explicitly declares MIT and names Yasuhiro Matsumoto; NOTICE
retains that declaration and the full MIT terms with author attribution.

The root NOTICE contains the complete texts and copyright notices, including
test-only modules, conservatively retaining the entire audited closure. The CLI
GoReleaser archive config explicitly includes the repository LICENSE and NOTICE
at the archive root for both tar.gz and Windows zip releases.

`TestResolvedLicensesAndNotices` recomputes the closure, checks selected versions
and source hashes in both contexts, and requires the upstream texts in NOTICE.
Standalone metadata is isolated so the audit leaves the CLI sums tidy.
It also checks
teatest, golden and go-udiff have no external-process import: golden diffs are
in-process and require no `diff` command on Windows. `TestDependencySurface`
executes an in-memory Tea/teatest loop and checks the adopted Bubbles APIs.

| Module | Selected version | Reviewed terms |
| --- | --- | --- |
| `github.com/MakeNowJust/heredoc` | `v1.0.0` | MIT |
| `github.com/atotto/clipboard` | `v0.1.4` | BSD-3-Clause |
| `github.com/aymanbagabas/go-osc52/v2` | `v2.0.1` | MIT |
| `github.com/aymanbagabas/go-udiff` | `v0.3.1` | BSD-3-Clause, MIT |
| `github.com/bits-and-blooms/bitset` | `v1.24.4` | BSD-3-Clause |
| `github.com/charmbracelet/bubbles` | `v0.21.1` | MIT |
| `github.com/charmbracelet/bubbletea` | `v1.3.10` | MIT |
| `github.com/charmbracelet/colorprofile` | `v0.4.1` | MIT |
| `github.com/charmbracelet/harmonica` | `v0.2.0` | MIT |
| `github.com/charmbracelet/lipgloss` | `v1.1.0` | MIT |
| `github.com/charmbracelet/x/ansi` | `v0.11.5` | MIT |
| `github.com/charmbracelet/x/cellbuf` | `v0.0.15` | MIT |
| `github.com/charmbracelet/x/exp/golden` | `v0.0.0-20241011142426-46044092ad91` | MIT |
| `github.com/charmbracelet/x/exp/teatest` | `v0.0.0-20260713092006-0d683c34c74b` | MIT |
| `github.com/charmbracelet/x/term` | `v0.2.2` | MIT |
| `github.com/clipperhouse/displaywidth` | `v0.9.0` | MIT |
| `github.com/clipperhouse/stringish` | `v0.1.1` | BSD-3-Clause, MIT |
| `github.com/clipperhouse/uax29/v2` | `v2.5.0` | MIT |
| `github.com/dustin/go-humanize` | `v1.0.1` | MIT |
| `github.com/erikgeiser/coninput` | `v0.0.0-20211004153227-1c3628e74d0f` | MIT |
| `github.com/google/go-cmp` | `v0.7.0` | BSD-3-Clause |
| `github.com/kylelemons/godebug` | `v1.1.0` | Apache-2.0 |
| `github.com/lucasb-eyer/go-colorful` | `v1.3.0` | MIT |
| `github.com/mattn/go-isatty` | `v0.0.20` | MIT |
| `github.com/mattn/go-isatty` | `v0.0.24` | MIT |
| `github.com/mattn/go-localereader` | `v0.0.1` | MIT |
| `github.com/mattn/go-runewidth` | `v0.0.19` | MIT |
| `github.com/muesli/ansi` | `v0.0.0-20230316100256-276c6243b2f6` | MIT |
| `github.com/muesli/cancelreader` | `v0.2.2` | MIT |
| `github.com/muesli/termenv` | `v0.16.0` | MIT |
| `github.com/rivo/uniseg` | `v0.4.7` | MIT |
| `github.com/sahilm/fuzzy` | `v0.1.1` | MIT |
| `github.com/xo/terminfo` | `v0.0.0-20220910002029-abceb7e1c41e` | MIT |
| `github.com/yuin/goldmark` | `v1.4.13` | MIT |
| `golang.org/x/crypto` | `v0.59.0` | BSD-3-Clause, Go-patent-grant |
| `golang.org/x/exp` | `v0.0.0-20260410095643-746e56fc9e2f` | BSD-3-Clause, Go-patent-grant |
| `golang.org/x/exp` | `v0.0.0-20261014143641-f0251eb4aa3e` | BSD-3-Clause, Go-patent-grant |
| `golang.org/x/mod` | `v0.43.0` | BSD-3-Clause, Go-patent-grant |
| `golang.org/x/net` | `v0.63.0` | BSD-3-Clause, Go-patent-grant |
| `golang.org/x/sync` | `v0.25.0` | BSD-3-Clause, Go-patent-grant |
| `golang.org/x/sys` | `v0.50.0` | BSD-3-Clause, Go-patent-grant |
| `golang.org/x/telemetry` | `v0.0.0-20261116191925-e77a43154acb` | BSD-3-Clause, Go-patent-grant |
| `golang.org/x/term` | `v0.48.0` | BSD-3-Clause, Go-patent-grant |
| `golang.org/x/text` | `v0.44.0` | BSD-3-Clause, Go-patent-grant |
| `golang.org/x/tools` | `v0.53.0` | BSD-3-Clause, Go-patent-grant |
| `golang.org/x/tools/go/expect` | `v0.1.0-deprecated` | BSD-3-Clause |
| `golang.org/x/tools/go/expect` | `v0.1.1-deprecated` | BSD-3-Clause |
| `golang.org/x/tools/go/packages/packagestest` | `v0.1.1-deprecated` | BSD-3-Clause |

Run validation through mcp-gate, module `backend/cli`:

- Fast policy, dependency and audit checks: packages `./internal/installtui/...`.
- Build-only check: package `./internal/installtui/_buildcheck`. The underscore
  directory deliberately excludes the expensive check from ordinary `./...`
  tests. It runs `CGO_ENABLED=0 go build ./...` for linux/darwin/windows on
  amd64/arm64 with GOWORK=off, matching the gate's module mode, then compiles the
  test-only Charm surface for each target without executing foreign binaries.
  The audit separately verifies workspace-selected module ZIP and go.mod hashes
  are covered by go.work.sum or a workspace module's go.sum, which is how Go
  resolves workspace checksums. The outer gate checks standalone module tidy.
- Existing byte-for-byte output/flag goldens: package `./cmd`.

No CLI flag, default, screen, prompt or output is exposed by this slice. The
default selection remains plain. Terminal probing and Tea's console lifecycle
will be wired at entry in S7; `terminal.Evaluate` only reads injected values.
SSH is eligible for a static TUI, and never eligible for animation.
