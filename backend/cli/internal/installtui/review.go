// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/result"
)

// The review screen's words.
const (
	reviewTitle = "Review"
	reviewIntro = "This is the plan the install will run, as the command line would resolve it. Nothing has been " +
		"changed yet. Approve starts it; Back returns to the choices, and a change there needs its own checks " +
		"and its own review."
	reviewCommandTitle = "Equivalent command"
	reviewTemplate     = "template: replace each <...> placeholder with its value before running it"
	reviewTooNarrow    = "This terminal is too narrow to show the equivalent command exactly. dcctl prints it in " +
		"full when the installer exits."
	reviewNoCommand       = "The equivalent command could not be composed for this plan."
	reviewWarningsTitle   = "Warnings"
	reviewApproveLabel    = "Approve and install"
	reviewNotApprovable   = "Approve is unavailable: this plan's checks have not passed. Go back and Recheck."
	reviewApprovalPending = "Approve becomes available once this screen has settled; a key pressed before then does nothing."
)

// The review screen's actions, in order. Focus starts on Back.
const (
	reviewBack = iota
	reviewApprove
	reviewExit
)

// reviewState is the review screen.
type reviewState struct {
	// settleGate arms each visit to the screen: only once the visit and the keyboard
	// have both been quiet for the settle period (see settleGate). Nothing left over from
	// an earlier visit arms a later one.
	settleGate
	focus int
	// offset is the first line of the plan shown, when it does not fit.
	offset int
}

// approval is the operator's approval of one plan, as it stood when it was given.
type approval struct {
	// plan is the plan's generation: any change makes a new one, and the approval is
	// for this one only.
	plan        int
	kubeContext string
	cluster     string
}

// enterReview shows the review for the plan as it stands, focused on Back and taking
// no key until it has settled.
func (p *prerun) enterReview() tea.Cmd {
	p.screen = screenReview
	p.approval = nil
	p.rv.focus, p.rv.offset = reviewBack, 0
	return p.rv.enter()
}

// leaveReview goes back to the choices. Nothing is approved, and the next visit is a
// new one, which settles again before it takes a key.
func (p *prerun) leaveReview() {
	p.screen = screenChoices
	p.rv.leave()
	p.rv.focus, p.rv.offset = reviewBack, 0
	p.approval = nil
}

// canApprove is the one gate on Approve: the screen has settled, and the plan on it is
// still the one whose checks passed.
func (p *prerun) canApprove() bool { return p.rv.armed && p.canContinue() }

func (p *prerun) reviewActive() []key.Binding {
	k := p.keys
	return []key.Binding{k.next, k.enter, k.scroll, k.back, k.exit, k.cancel}
}

func (p *prerun) reviewKey(k tea.KeyPressMsg) (tea.Cmd, prerunOutcome) {
	if ok, cmd := p.rv.admit(); !ok {
		return cmd, stay // typed before the screen settled: dropped, whatever it was
	}
	switch {
	case key.Matches(k, p.keys.next):
		p.rv.focus = (p.rv.focus + 1) % 3
	case key.Matches(k, p.keys.prev):
		p.rv.focus = (p.rv.focus + 2) % 3
	case key.Matches(k, p.keys.up):
		p.rv.offset = max(p.rv.offset-1, 0)
	case key.Matches(k, p.keys.down):
		p.rv.offset++ // clamped when drawn, where the room is known
	case key.Matches(k, p.keys.exit):
		p.rv.focus = reviewExit
	case key.Matches(k, p.keys.back):
		p.leaveReview()
	case key.Matches(k, p.keys.enter):
		switch p.rv.focus {
		case reviewBack:
			p.leaveReview()
		case reviewApprove:
			if !p.canApprove() {
				return nil, stay
			}
			p.approval = &approval{plan: p.plan, kubeContext: p.facts.KubeContext, cluster: p.facts.Cluster}
			return nil, proceed
		case reviewExit:
			return nil, leave
		}
	}
	return nil, stay
}

// approvedKindCluster is the managed kind cluster whose creation the approval answers
// for: the plan's own, and only while the plan is still the one approved.
func (p *prerun) approvedKindCluster() (string, bool) {
	a := p.approval
	if a == nil || a.plan != p.plan || a.kubeContext != "" || a.cluster == "" {
		return "", false
	}
	return a.cluster, true
}

// ---- view ---------------------------------------------------------------------

func (p *prerun) reviewLines(w, rows int) []string {
	t := p.theme
	head := []string{t.Bold.Render(reviewTitle) + " " + t.Glyphs.Dot + " " + p.planShort(), ""}

	var list []string
	list = append(list, wrap(reviewIntro, w)...)
	list = append(list, "")
	for _, r := range p.review.Rows {
		lead := "  " + t.Dim.Render(padCells(truncCells(p.text(r.Label), 12, t.Glyphs.Ellipsis), 12)) + " "
		list = append(list, hang(lead, p.text(r.Value), w)...)
	}
	if len(p.review.Warnings) > 0 {
		list = append(list, "", t.Bold.Render(reviewWarningsTitle))
		for _, s := range p.review.Warnings {
			list = append(list, hang("  "+t.Warn.Render("WARN")+" ", p.text(s), w)...)
		}
	}
	list = append(list, "")
	list = append(list, p.preflightStatus(w)...)
	if q := p.approvesWords(); q != "" {
		list = append(list, "")
		list = append(list, wrap(q, w)...)
	}

	list = append(list, "")
	title := reviewCommandTitle
	if p.command.Template {
		title += " (template)"
	}
	list = append(list, t.Bold.Render(title))
	if p.command.Line == "" {
		list = append(list, wrap(reviewNoCommand, w)...)
	} else {
		lines, ok := result.CommandLines(p.command, p.shell, p.redact, w-2)
		if !ok {
			lines = wrap(reviewTooNarrow, w-2)
		}
		for _, l := range lines {
			list = append(list, "  "+l)
		}
		if p.command.Template {
			for _, l := range wrap(reviewTemplate, w-2) {
				list = append(list, "  "+t.Dim.Render(l))
			}
		}
	}

	tail := []string{""}
	switch {
	case !p.rv.armed:
		for _, l := range wrap(reviewApprovalPending, w) {
			tail = append(tail, t.Dim.Render(l))
		}
	case !p.canContinue():
		for _, l := range wrap(reviewNotApprovable, w) {
			tail = append(tail, t.Fail.Render(l))
		}
	}
	tail = append(tail, p.actionBar([]string{"Back", reviewApproveLabel, "Exit"},
		[]bool{true, p.canApprove(), true}, p.rv.focus, w)...)

	room := rows - len(head) - len(tail)
	return append(append(head, p.reviewWindow(list, room)...), tail...)
}

// reviewWindow is room lines of the plan from the scroll offset (see window).
func (p *prerun) reviewWindow(list []string, room int) []string {
	out, offset := window(list, room, p.rv.offset, p.theme.Glyphs.Ellipsis)
	p.rv.offset = offset
	return out
}

// window is room lines of list from offset, the lines cut off above or below counted
// in words, never silently dropped, and the offset as it was clamped to the list.
func window(list []string, room, offset int, ellipsis string) ([]string, int) {
	if room <= 0 {
		return nil, offset
	}
	if len(list) <= room {
		return list, 0
	}
	offset = min(max(offset, 0), len(list)-room)
	out := append([]string(nil), list[offset:offset+room]...)
	if offset > 0 {
		out[0] = ellipsis + " " + plural(offset+1, "line", "lines") + " above"
	}
	if below := len(list) - (offset + room); below > 0 {
		out[len(out)-1] = ellipsis + " " + plural(below+1, "line", "lines") + " below"
	}
	return out, offset
}

// planShort is the plan in a few words, for the review's title line.
func (p *prerun) planShort() string {
	s := footprintWord(p.facts.Compact) + ", " + topologyWord(p.facts.HA)
	if t := p.targetShort(); t != "" {
		s = t + ", " + s
	}
	return s
}

// approvesWords say which question of the run the approval answers, when it answers
// one: the creation of the managed kind cluster this plan names.
func (p *prerun) approvesWords() string {
	if p.facts.KubeContext != "" || p.facts.Cluster == "" {
		return ""
	}
	return "If no kind cluster " + p.text(p.facts.Cluster) + " exists yet, approving also answers the install's " +
		"question to create it: yes, for this cluster and this plan only."
}

// exitCommand is the review's equivalent command on one line, for the exit summary:
// exact whatever the terminal's width, so it can always be copied. Empty when the
// review was never shown, or there is no command.
func (p *prerun) exitCommand() string {
	if p.rv.gen == 0 || p.command.Line == "" {
		return ""
	}
	lines, ok := result.CommandLines(p.command, p.shell, p.redact, 1<<20)
	if !ok || len(lines) != 1 {
		return ""
	}
	if p.command.Template {
		return lines[0] + " (template: replace each <...> placeholder)"
	}
	return lines[0]
}

// questionApproved reports whether the approval answers q: the install's question to
// create exactly the kind cluster that was approved.
func questionApproved(cluster string, q installflow.Question) bool {
	if q.Kind != installflow.PromptConfirm || q.Question != installflow.QuestionCreateKindCluster {
		return false
	}
	for _, prm := range q.Params {
		if prm.Name == "cluster" {
			return prm.Value.String() == installflow.Text(cluster).String()
		}
	}
	return false
}
