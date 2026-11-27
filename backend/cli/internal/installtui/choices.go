// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// The cluster choices screen's words.
const (
	choicesTitle = "Cluster choices"
	choicesIntro = "Each change re-resolves the plan as the command line would, and runs the read-only checks again for it."
	// choicesRechecking is said while the checks run for a changed plan.
	choicesRechecking = "working: the checks are running for this plan. Continue waits for them."
	choicesNotRun     = "Preflight has not run for this plan. Continue waits for it."
	drawerTitle       = "Advanced settings"
	drawerIntro       = "Editors change the plan at once and re-run the checks. Every other flag from the command line is carried as given."
	drawerEmptyGroup  = "nothing set; the defaults apply"
	pickerTitle       = "Choose a context"
	pickerIntro       = "Contexts from your kubeconfig. Choosing one makes it the target and runs the checks against it."
	pickerEmpty       = "Your kubeconfig has no contexts."
	nameTitle         = "Name the local kind cluster"
	nameEmpty         = "a kind cluster needs a name"
	// nameMax bounds the name editor; kind's own limit is lower still.
	nameMax = 63
)

// drawerGroups is the drawer's sections, in order. A setting whose group is not here is
// drawn after them, under its own heading: no row is ever dropped.
var drawerGroups = []string{
	"Images and developer build",
	"Monitoring and alerts",
	"Database placement, connection budget and WAL",
	"Backups and snapshots",
	"Relational recovery",
	"Gateway, MQTT and legacy ingress",
	"Escape hatches",
}

// chItem is one stop of the choices screen's focus: a field, then an action.
type chItem uint8

const (
	chTarget chItem = iota
	chName
	chFootprint
	chTopology
	chContinue
	chAdvanced
	chPreflight
	chExit
)

// choicesState is the cluster choices screen, its drawer and its editors.
type choicesState struct {
	// focus is the index into items() of the focused stop.
	focus int
	// drawer is the advanced drawer, open over the screen; drawerAt is the index into
	// the settings, in drawer order, of the focused row.
	drawer   bool
	drawerAt int
	// picker is the context picker, open over whichever screen opened it.
	picker *pickerState
	// naming is the kind cluster's name being edited; nil when it is not.
	naming []rune
}

type pickerState struct {
	// names are the contexts as the kubeconfig spells them: the plan takes the raw
	// name, the screen draws it sanitised and redacted.
	names []string
	focus int
	err   string
}

// items is the focus order. Without a replan the fields cannot change, so focus moves
// between the actions only.
func (p *prerun) items() []chItem {
	acts := []chItem{chContinue, chAdvanced, chPreflight, chExit}
	if p.replan == nil {
		return acts
	}
	return append([]chItem{chTarget, chName, chFootprint, chTopology}, acts...)
}

func (p *prerun) focused() chItem {
	items := p.items()
	if p.ch.focus < 0 || p.ch.focus >= len(items) {
		p.ch.focus = 0
	}
	return items[p.ch.focus]
}

func isRadio(i chItem) bool { return i == chTarget || i == chFootprint || i == chTopology }

// ---- keys -------------------------------------------------------------------

func (p *prerun) choicesActive() []key.Binding {
	k := p.keys
	switch {
	case p.ch.naming != nil:
		return []key.Binding{k.apply, k.erase, k.closeOverlay, k.cancel}
	case p.ch.drawer:
		var out []key.Binding
		if len(p.settings) > 1 {
			out = append(out, k.setting)
		}
		if s, ok := p.drawerSetting(); ok && s.Disabled == "" && p.replan != nil {
			out = append(out, k.toggle)
		}
		return append(out, k.closeOverlay, k.cancel)
	}
	var out []key.Binding
	f := p.focused()
	if isRadio(f) {
		out = append(out, k.choose)
	}
	out = append(out, k.nextField)
	if !isRadio(f) {
		out = append(out, k.enter)
	}
	return append(out, k.back, k.exit, k.cancel)
}

func (p *prerun) choicesKey(k tea.KeyPressMsg) (tea.Cmd, prerunOutcome) {
	switch {
	case p.ch.naming != nil:
		return p.nameKey(k), stay
	case p.ch.drawer:
		return p.drawerKey(k), stay
	}
	items := p.items()
	f := p.focused()
	switch {
	case isRadio(f) && key.Matches(k, p.keys.choose):
		return p.flip(f), stay
	case key.Matches(k, p.keys.next):
		p.ch.focus = (p.ch.focus + 1) % len(items)
	case key.Matches(k, p.keys.prev):
		p.ch.focus = (p.ch.focus - 1 + len(items)) % len(items)
	case key.Matches(k, p.keys.exit):
		for i, it := range items {
			if it == chExit {
				p.ch.focus = i
			}
		}
	case key.Matches(k, p.keys.back):
		p.screen = screenPreflight
	case key.Matches(k, p.keys.enter):
		// Enter on a choice changes nothing: a queued Enter must neither alter the plan
		// nor walk the focus to Continue.
		switch f {
		case chName:
			if p.facts.KubeContext != "" {
				p.openPicker()
			} else {
				p.refusal = ""
				p.ch.naming = []rune(p.facts.Cluster)
			}
		case chContinue:
			// The review comes next; the run starts only from an approval there.
			if p.canContinue() {
				return p.enterReview(), stay
			}
		case chAdvanced:
			p.ch.drawer, p.ch.drawerAt = true, 0
		case chPreflight:
			p.screen = screenPreflight
		case chExit:
			return nil, leave
		}
	}
	return nil, stay
}

// flip changes a two-way choice to its other value.
func (p *prerun) flip(f chItem) tea.Cmd {
	switch f {
	case chTarget:
		if p.facts.KubeContext == "" {
			// An existing context needs a name: the change is made when one is picked.
			p.openPicker()
			return nil
		}
		cluster := p.facts.Cluster
		return p.applyEdit(func(e *Edits) { e.Target = &Target{Cluster: cluster} })
	case chFootprint:
		want := !p.facts.Compact
		return p.applyEdit(func(e *Edits) { e.Compact = &want })
	case chTopology:
		want := !p.facts.HA
		return p.applyEdit(func(e *Edits) { e.HA = &want })
	}
	return nil
}

func (p *prerun) nameKey(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(k, p.keys.closeOverlay):
		p.ch.naming = nil
	case key.Matches(k, p.keys.erase):
		if n := len(p.ch.naming); n > 0 {
			p.ch.naming = p.ch.naming[:n-1]
		}
	case key.Matches(k, p.keys.apply):
		name := strings.TrimSpace(string(p.ch.naming))
		if name == "" {
			p.refusal = nameEmpty
			return nil
		}
		p.ch.naming = nil
		if name == p.facts.Cluster && p.facts.KubeContext == "" {
			return nil // unchanged: the plan, and its preflight, stand
		}
		return p.applyEdit(func(e *Edits) { e.Target = &Target{Cluster: name} })
	default:
		for _, r := range k.Text {
			if len(p.ch.naming) < nameMax && unicode.IsPrint(r) && !unicode.IsSpace(r) {
				p.ch.naming = append(p.ch.naming, r)
			}
		}
	}
	return nil
}

// ---- the drawer ---------------------------------------------------------------

// ordered is the settings in drawer order: by group, then as the command gave them.
func (p *prerun) ordered() []Setting {
	var out []Setting
	seen := map[string]bool{}
	for _, g := range drawerGroups {
		seen[g] = true
		for _, s := range p.settings {
			if s.Group == g {
				out = append(out, s)
			}
		}
	}
	for _, s := range p.settings {
		if !seen[s.Group] {
			out = append(out, s)
		}
	}
	return out
}

func (p *prerun) drawerSetting() (Setting, bool) {
	o := p.ordered()
	if p.ch.drawerAt < 0 || p.ch.drawerAt >= len(o) || !o[p.ch.drawerAt].Toggle {
		return Setting{}, false
	}
	return o[p.ch.drawerAt], true
}

// drawerKey moves over every row, carried ones too, so each can be scrolled to and
// read; only an editor's row can be changed.
func (p *prerun) drawerKey(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(k, p.keys.closeOverlay):
		p.ch.drawer = false
	case key.Matches(k, p.keys.setting) && len(p.settings) > 0:
		d := 1
		if k.String() == "up" {
			d = -1
		}
		p.ch.drawerAt = min(max(p.ch.drawerAt+d, 0), len(p.settings)-1)
	case key.Matches(k, p.keys.toggle):
		s, ok := p.drawerSetting()
		if !ok || s.Disabled != "" || p.replan == nil {
			return nil
		}
		name, want := s.Name, !s.FlagValue
		return p.applyEdit(func(e *Edits) {
			if e.Toggles == nil {
				e.Toggles = map[string]bool{}
			}
			e.Toggles[name] = want
		})
	}
	return nil
}

// ---- the context picker ---------------------------------------------------------

func (p *prerun) openPicker() {
	if p.contexts == nil || p.replan == nil {
		return
	}
	ps := &pickerState{}
	names, err := p.contexts()
	if err != nil {
		ps.err = p.text(err.Error())
	}
	ps.names = names
	for i, n := range names {
		if n == p.facts.KubeContext {
			ps.focus = i
		}
	}
	p.ch.picker = ps
}

func (p *prerun) pickerActive() []key.Binding {
	k := p.keys
	var out []key.Binding
	if len(p.ch.picker.names) > 1 {
		out = append(out, k.pick)
	}
	if len(p.ch.picker.names) > 0 {
		out = append(out, k.apply)
	}
	return append(out, k.closeOverlay, k.cancel)
}

func (p *prerun) pickerKey(k tea.KeyPressMsg) (tea.Cmd, prerunOutcome) {
	ps := p.ch.picker
	switch {
	case key.Matches(k, p.keys.closeOverlay):
		p.ch.picker = nil
	case key.Matches(k, p.keys.pick) && len(ps.names) > 0:
		d := 1
		if k.String() == "up" {
			d = -1
		}
		ps.focus = min(max(ps.focus+d, 0), len(ps.names)-1)
	case key.Matches(k, p.keys.apply) && len(ps.names) > 0:
		name := ps.names[ps.focus]
		p.ch.picker = nil
		if name == p.facts.KubeContext {
			return nil, stay // the target it already is: the plan, and its preflight, stand
		}
		return p.applyEdit(func(e *Edits) { e.Target = &Target{KubeContext: name} }), stay
	}
	return nil, stay
}

// ---- view ---------------------------------------------------------------------

// radio draws a two-way choice: the chosen option marked (*) in every level's text, so
// the choice never depends on colour.
func (p *prerun) radio(options [2]string, second, focused bool) string {
	var parts []string
	for i, o := range options {
		mark := "( ) "
		if (i == 1) == second {
			mark = "(*) "
			if focused {
				o = p.theme.Focus.Render(o)
			}
		}
		parts = append(parts, mark+o)
	}
	return strings.Join(parts, "  ")
}

// field draws one field row: the focus glyph, the label, then the value.
func (p *prerun) field(label, value string, focused bool, w int) []string {
	mark := "  "
	if focused {
		mark = p.theme.Glyphs.Focus + " "
	}
	return hang(mark+p.theme.Dim.Render(padCells(label, 10))+" ", value, w)
}

func (p *prerun) choicesLines(w, rows int) []string {
	if p.ch.drawer {
		return p.drawerLines(w, rows)
	}
	t := p.theme
	f := p.facts
	editable := p.replan != nil
	cur := p.focused()
	head := []string{t.Bold.Render(choicesTitle), ""}

	var list []string
	from, to := -1, -1
	add := func(it chItem, lines []string) {
		if it == cur {
			from, to = len(list), len(list)+len(lines)-1
		}
		list = append(list, lines...)
	}
	add(chTarget, p.field("Target", p.radio([2]string{"Managed local kind", "Existing context"}, f.KubeContext != "",
		cur == chTarget && editable)+p.origin(f.TargetFlag, p.edits.Target != nil), editable && cur == chTarget, w))
	switch {
	case p.ch.naming != nil:
		add(chName, p.field("Cluster", p.text(string(p.ch.naming))+"_", true, w))
	case f.KubeContext != "":
		v := p.text(f.KubeContext)
		if editable && p.contexts != nil {
			v += t.Dim.Render(" (Enter to choose another)")
		}
		add(chName, p.field("Context", v, editable && cur == chName, w))
	default:
		v := p.text(f.Cluster) + ", created if it does not exist"
		if editable {
			v += t.Dim.Render(" (Enter to rename)")
		}
		add(chName, p.field("Cluster", v, editable && cur == chName, w))
	}
	add(chFootprint, p.field("Footprint", p.radio([2]string{"Standard", "Compact"}, f.Compact,
		cur == chFootprint && editable)+p.origin(f.FootprintFlag, p.edits.Compact != nil), editable && cur == chFootprint, w))
	if f.Compact {
		// Compact's consequences, inline whenever it is the footprint, as the plan now
		// stands: what it leaves out, and what an explicit choice kept.
		indent := strings.Repeat(" ", 13)
		for _, l := range wrap(compactConsequences(f), w-13) {
			list = append(list, indent+t.Dim.Render(l))
		}
	}
	add(chTopology, p.field("Topology", p.radio([2]string{"Single", "HA"}, f.HA,
		cur == chTopology && editable)+p.origin(f.TopologyFlag, p.edits.HA != nil), editable && cur == chTopology, w))
	if editable && p.ch.naming == nil {
		list = append(list, "")
		for _, l := range wrap(choicesIntro, w) {
			list = append(list, t.Dim.Render(l))
		}
	}

	tail := append([]string{""}, p.preflightStatus(w)...)
	tail = append(tail, p.refusalLines(w)...)
	tail = append(tail, "")
	if p.ch.naming != nil {
		tail = append(tail, t.Bold.Render(nameTitle))
	} else {
		labels := []string{"Continue", "Advanced", "Preflight", "Exit"}
		enabled := []bool{p.canContinue(), true, true, true}
		focus := -1
		if !isRadio(cur) && cur != chName {
			focus = int(cur - chContinue)
		}
		tail = append(tail, p.actionBar(labels, enabled, focus, w)...)
	}
	return stack(head, list, tail, rows, from, to, t.Glyphs.Ellipsis)
}

// compactConsequences is what Compact means for the plan as it stands. Each part is
// read from the settled plan's facts: a monitoring stack or cert-manager kept by an
// explicit choice is said to stay, never said to be gone.
func compactConsequences(f Facts) string {
	parts := []string{"smaller volumes", "lowered JetStream/KV ceilings and scheduling requests"}
	if f.Monitoring {
		parts = append(parts, "the monitoring stack kept (chosen explicitly)")
	} else {
		parts = append(parts, "no monitoring stack")
	}
	if f.TLS {
		parts = append(parts, "cert-manager kept (chosen explicitly)")
	} else {
		parts = append(parts, "no cert-manager")
	}
	if f.Backups {
		parts = append(parts, "database backups kept")
	} else {
		parts = append(parts, "no database backups")
	}
	return "Compact is the small-footprint preset for the cluster and every instance on it: " +
		strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1] + "."
}

// preflightStatus is the one line the choices and the drawer give the checks for the
// plan as it stands.
func (p *prerun) preflightStatus(w int) []string {
	t := p.theme
	s := &p.pf
	var msg string
	st := t.Dim
	switch {
	case p.facts.SkipPreflight:
		msg, st = "Preflight: not checked (--skip-preflight supplied).", t.Warn
	case s.running:
		msg = choicesRechecking
	case s.err != "":
		msg, st = "Preflight could not finish: "+s.err+". "+preflightBlocked, t.Fail
	case s.finished:
		msg, st = p.tally()
	default:
		msg = choicesNotRun
	}
	var out []string
	for _, l := range wrap(msg, w) {
		out = append(out, st.Render(l))
	}
	return out
}

// refusalLines say why the last change was refused, when it was.
func (p *prerun) refusalLines(w int) []string {
	if p.refusal == "" {
		return nil
	}
	var out []string
	for _, l := range wrap("The plan was not changed: "+p.refusal, w) {
		out = append(out, p.theme.Fail.Render(l))
	}
	return out
}

func (p *prerun) drawerLines(w, rows int) []string {
	t := p.theme
	head := []string{t.Bold.Render(drawerTitle)}
	head = append(head, wrap(drawerIntro, w)...)
	var list []string
	from, to := -1, -1
	o := p.ordered()
	groups := append([]string(nil), drawerGroups...)
	for _, s := range o {
		known := false
		for _, g := range groups {
			known = known || g == s.Group
		}
		if !known {
			groups = append(groups, s.Group)
		}
	}
	for _, g := range groups {
		list = append(list, "", t.Bold.Render(truncCells(p.text(g), w, t.Glyphs.Ellipsis)))
		n := 0
		for i, s := range o {
			if s.Group != g {
				continue
			}
			n++
			lines := p.settingLines(s, i == p.ch.drawerAt, w)
			if i == p.ch.drawerAt {
				from, to = len(list), len(list)+len(lines)-1
			}
			list = append(list, lines...)
		}
		if n == 0 {
			list = append(list, "  "+t.Dim.Render(drawerEmptyGroup))
		}
	}
	tail := append([]string{""}, p.preflightStatus(w)...)
	tail = append(tail, p.refusalLines(w)...)
	return stack(head, list, tail, rows, from, to, t.Glyphs.Ellipsis)
}

// settingLines draws one drawer row: an editor with its label, flag, value and where
// the value came from; or a carried flag, by name, as the command line gave it.
func (p *prerun) settingLines(s Setting, focused bool, w int) []string {
	t := p.theme
	mark := "  "
	if focused {
		mark = t.Glyphs.Focus + " "
	}
	value := p.text(s.Value)
	if focused {
		value = t.Focus.Render(value)
	}
	var text string
	switch {
	case s.Toggle:
		text = p.text(s.Label) + " (" + p.text(s.Flag) + "): " + value
	case s.Flag == "":
		// A consequence of other settings, named by what it is.
		text = p.text(s.Label) + ": " + value
	default:
		text = p.text(s.Flag) + " " + value + " " + t.Dim.Render("carried")
	}
	if s.Source != "" {
		text += " " + t.Dim.Render("("+p.text(s.Source)+")")
	}
	if s.Disabled != "" {
		text += " " + t.Dim.Render("unavailable: "+p.text(s.Disabled))
	}
	return hang(mark, text, w)
}

func (p *prerun) pickerLines(w, rows int) []string {
	t := p.theme
	ps := p.ch.picker
	head := []string{t.Bold.Render(pickerTitle)}
	head = append(head, wrap(pickerIntro, w)...)
	head = append(head, "")
	var list []string
	from := -1
	switch {
	case ps.err != "":
		for _, l := range wrap("The kubeconfig could not be read: "+ps.err, w) {
			list = append(list, t.Fail.Render(l))
		}
	case len(ps.names) == 0:
		list = append(list, wrap(pickerEmpty, w)...)
	}
	for i, n := range ps.names {
		mark := "  "
		name := truncCells(p.text(n), w-2, t.Glyphs.Ellipsis)
		if i == ps.focus {
			mark, name, from = t.Glyphs.Focus+" ", t.Focus.Render(name), len(list)
		}
		if n == p.facts.KubeContext {
			name += t.Dim.Render(" (the current target)")
		}
		list = append(list, mark+name)
	}
	return stack(head, list, nil, rows, from, from, t.Glyphs.Ellipsis)
}
