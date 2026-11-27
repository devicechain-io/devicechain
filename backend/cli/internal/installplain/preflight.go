// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installplain

import (
	"fmt"
	"io"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/fatih/color"
)

// The preflight report's sections. The doctor names a section by one of these
// codes; how it is drawn is this package's business, and a terminal UI draws
// the same codes its own way.
const (
	SectionTooling           installflow.MessageCode = "preflight.tooling"
	SectionDockerEngine      installflow.MessageCode = "preflight.docker-engine"
	SectionPorts             installflow.MessageCode = "preflight.ports"
	SectionKubeContexts      installflow.MessageCode = "preflight.kube-contexts"
	SectionKubeServerVersion installflow.MessageCode = "preflight.kube-server-version"
	SectionKernel            installflow.MessageCode = "preflight.kernel"
	SectionDisk              installflow.MessageCode = "preflight.disk"
	SectionWSL               installflow.MessageCode = "preflight.wsl2"
	SectionSystem            installflow.MessageCode = "preflight.system"
	// SectionCloud is the advisory diagnostics of an existing cluster. Only the
	// terminal UI's preflight screen runs them; the plain report never has one.
	SectionCloud installflow.MessageCode = "preflight.cloud"
)

type sectionHeading struct{ emoji, title string }

var sectionHeadings = map[installflow.MessageCode]sectionHeading{
	SectionTooling:           {"🧰", "Tooling"},
	SectionDockerEngine:      {"🐳", "Docker engine"},
	SectionPorts:             {"🔌", "Ports & registry"},
	SectionKubeContexts:      {"☸️", "Kubernetes contexts"},
	SectionKubeServerVersion: {"🧩", "Kubernetes server version"},
	SectionKernel:            {"🧬", "Kernel & cgroups"},
	SectionDisk:              {"💾", "Disk"},
	SectionWSL:               {"🪟", "WSL2"},
	SectionSystem:            {"🖥️", "System"},
	SectionCloud:             {"☁️", "Existing cluster (advisory)"},
}

// SectionTitle is the words for a section code, the same words the plain report
// heads it with (without its emoji). A code with no wording is its own title, so
// a section is never shown nameless.
func SectionTitle(code installflow.MessageCode) string {
	if h, ok := sectionHeadings[code]; ok {
		return h.title
	}
	return string(code)
}

// greenUnderline is dcctl's house style for a section heading.
var greenUnderline = color.New(color.Underline, color.FgHiGreen).SprintFunc()

// RenderObservation writes one preflight observation as the doctor has always
// printed it. A section or verdict this package has no wording for is still
// printed, under its code, rather than dropped: a check nobody can see is a
// check that did not run, as far as the operator can tell.
func RenderObservation(w io.Writer, o installflow.PreflightObservation) {
	switch o.Observation {
	case installflow.ObserveSection:
		h, ok := sectionHeadings[o.Section]
		if !ok {
			h = sectionHeading{"?", string(o.Section)}
		}
		fmt.Fprintf(w, "\n%s\n", greenUnderline(h.emoji+" "+h.title))
	case installflow.ObserveCheck:
		renderCheck(w, o)
	case installflow.ObserveSummary:
		fmt.Fprintln(w)
		switch {
		case o.Fails > 0:
			fmt.Fprintln(w, color.New(color.FgRed, color.Bold).Sprintf("Preflight: %d failure(s), %d warning(s) — fix failures before bootstrap.", o.Fails, o.Warns))
		case o.Warns > 0:
			fmt.Fprintln(w, color.New(color.FgYellow, color.Bold).Sprintf("Preflight: ready, with %d warning(s) — review above.", o.Warns))
		default:
			fmt.Fprintln(w, color.New(color.FgGreen, color.Bold).Sprint("Preflight: all checks passed. ✨"))
		}
	default:
		fmt.Fprintf(w, "  ? %s %s\n", o.Observation, o.Text)
	}
}

func renderCheck(w io.Writer, o installflow.PreflightObservation) {
	switch o.Verdict {
	case installflow.CheckPass:
		fmt.Fprintf(w, "  %s %s%s\n", color.GreenString("✅"), o.Text, highlight(o))
	case installflow.CheckInfo:
		fmt.Fprintf(w, "  %s %s%s\n", color.HiBlackString("•"), o.Text, highlight(o))
	case installflow.CheckWarn:
		fmt.Fprintf(w, "  %s %s\n     %s\n", color.YellowString("⚠️"), o.Text, color.YellowString("↳ "+o.Fix.String()))
	case installflow.CheckFail:
		fmt.Fprintf(w, "  %s %s\n     %s\n", color.RedString("❌"), o.Text, color.RedString("↳ "+o.Fix.String()))
	default:
		// Unknown and reserved verdicts are shown as words, with their fix.
		fmt.Fprintf(w, "  %s %s\n", verdictWord(o.Verdict), o.Text)
		if !o.Fix.IsZero() {
			fmt.Fprintf(w, "     ↳ %s\n", o.Fix)
		}
	}
}

// highlight is the emphasised value a check calls out, in green as the doctor has
// always drawn it. The value is passed to the colour library as its format, as the
// doctor always passed it, so the bytes do not move even for a value holding a '%'.
func highlight(o installflow.PreflightObservation) string {
	if o.Highlight.IsZero() {
		return ""
	}
	return color.GreenString(o.Highlight.String())
}

func verdictWord(v installflow.CheckVerdict) string {
	switch v {
	case installflow.CheckUnknown:
		return "UNKNOWN"
	case installflow.CheckNotNeeded:
		return "NOT NEEDED"
	case installflow.CheckNotChecked:
		return "NOT CHECKED"
	}
	return "? " + string(v)
}
