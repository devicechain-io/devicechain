// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// Help on every screen of the install and setup journeys: the screens before the run,
// the progress screen, and setup's hand-over, instance choices and instance review.

// norm is a footer entry or a help row as words: runs of spaces are one, so "Esc  back"
// in help's key column and "Esc back" in the footer read the same.
func norm(s string) string { return strings.Join(strings.Fields(s), " ") }

// unframe is a drawn line without an outer border's edges.
func unframe(l string) string {
	l = strings.TrimSpace(l)
	for _, v := range []string{"│", "|"} {
		l = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(l, v), v))
	}
	return l
}

// helpRowsAfter is the key rows a drawn help shows: the lines after the first line
// holding marker and the blank line under it (none for the bootstrap help, whose rows
// follow its line directly), up to the next blank line.
func helpRowsAfter(t *testing.T, view, marker string, blank bool) []string {
	t.Helper()
	lines := strings.Split(view, "\n")
	for i, l := range lines {
		if !strings.Contains(l, marker) {
			continue
		}
		i++
		if blank {
			if i >= len(lines) || unframe(lines[i]) != "" {
				t.Fatalf("no blank line under %q:\n%s", marker, view)
			}
			i++
		}
		var rows []string
		for ; i < len(lines) && unframe(lines[i]) != ""; i++ {
			rows = append(rows, norm(unframe(lines[i])))
		}
		return rows
	}
	t.Fatalf("no %q in:\n%s", marker, view)
	return nil
}

// prerunFooter is the footer entries a bordered screen before the run draws, in order.
func prerunFooter(t *testing.T, view string) []string {
	t.Helper()
	lines := strings.Split(view, "\n")
	at := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "├") {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("no footer rule in:\n%s", view)
	}
	var out []string
	for _, l := range lines[at+1 : len(lines)-1] {
		for _, e := range strings.Split(unframe(l), " · ") {
			if e = norm(e); e != "" {
				out = append(out, e)
			}
		}
	}
	return out
}

func bindingWords(p *prerun) []string {
	var out []string
	for _, b := range p.footerKeys() {
		h := b.Help()
		out = append(out, norm(h.Key+" "+h.Desc))
	}
	return out
}

// helpKeyFor is the key that opens help on the screen: F1 where ? is typed into a name.
func helpKeyFor(p *prerun) string {
	if p.typingName() {
		return "f1"
	}
	return "?"
}

// 🔴 HELP LISTS EXACTLY THE FOOTER'S KEYS, on every screen before the run and in every
// state the goldens draw: every entry the footer draws is a row of help, and every row of
// help is an entry the footer draws, in the same order. Both are read off the drawn
// screens, so neither can drift from the other or from the key map.
func TestPrerunHelpListsExactlyTheFootersKeys(t *testing.T) {
	for _, sc := range preScenarios {
		t.Run(sc.name, func(t *testing.T) {
			p := sc.make(preCaps(terminal.L1, 120, 40))
			view := p.render()
			footer := prerunFooter(t, view)
			if want := bindingWords(p); strings.Join(footer, "|") != strings.Join(want, "|") {
				t.Fatalf("the footer draws %q, its bindings are %q", footer, want)
			}
			if !strings.Contains(strings.Join(footer, "|"), "help") {
				t.Fatalf("the footer offers no help: %q", footer)
			}
			press(p, helpKeyFor(p))
			if !p.help.open {
				t.Fatal("help did not open")
			}
			rows := helpRowsAfter(t, p.render(), "Help · "+p.helpTitle(), true)
			if strings.Join(rows, "|") != strings.Join(footer, "|") {
				t.Fatalf("help lists %q\nthe footer draws %q", rows, footer)
			}
		})
	}
}

// helpBattery is every kind of key the screens under help answer: none of them may act
// on the screen while help is open.
var helpBattery = []string{"enter", "tab", "shift+tab", "space", "q", "y", "n", "backspace", "a", "j", "k",
	"up", "down", "pgdown", "pgup", "up"}

// 🔴 HELP NEVER CHANGES STATE. On every screen before the run, opening help, pressing any
// key under it and closing it leaves the screen exactly as it was: the same drawing, the
// same plan, no approval, and no outcome but stay.
func TestPrerunHelpChangesNothing(t *testing.T) {
	for _, sc := range preScenarios {
		t.Run(sc.name, func(t *testing.T) {
			p := sc.make(preCaps(terminal.L4, 120, 40))
			before := p.render()
			plan, edits, screen := p.plan, fmt.Sprint(p.edits), p.screen
			if out := press(p, helpKeyFor(p)); out != stay {
				t.Fatalf("opening help gave %v", out)
			}
			for _, k := range helpBattery {
				if out := press(p, k); out != stay {
					t.Fatalf("%s under help gave %v", k, out)
				}
				if !p.help.open {
					t.Fatalf("%s closed help", k)
				}
			}
			if out := press(p, "esc"); out != stay || p.help.open {
				t.Fatalf("Esc under help: %v, open %v", out, p.help.open)
			}
			if p.approval != nil || p.plan != plan || fmt.Sprint(p.edits) != edits || p.screen != screen {
				t.Fatalf("help changed the plan: approval %v, plan %d->%d, edits %s->%v, screen %v->%v",
					p.approval, plan, p.plan, edits, p.edits, screen, p.screen)
			}
			if after := p.render(); after != before {
				t.Fatalf("the screen changed under help:\n--- before\n%s\n--- after\n%s", preStrip(before), preStrip(after))
			}
		})
	}
}

// ? is a character of a kind cluster's name: it is typed, and F1 opens help there.
func TestQuestionMarkIsTypedIntoANameAndF1OpensHelp(t *testing.T) {
	p := goldenChoices(preCaps(terminal.L1, 120, 40), preFacts(), false)
	press(p, "tab", "enter")
	if !p.typingName() {
		t.Fatal("not typing a name")
	}
	press(p, "?")
	if p.help.open || !strings.HasSuffix(string(p.ch.naming), "?") {
		t.Fatalf("? on the name editor: help %v, name %q", p.help.open, string(p.ch.naming))
	}
	press(p, "f1")
	if !p.help.open {
		t.Fatal("F1 did not open help over the name editor")
	}
	press(p, "?")
	if p.help.open || !strings.HasSuffix(string(p.ch.naming), "?") || strings.HasSuffix(string(p.ch.naming), "??") {
		t.Fatalf("? under help: open %v, name %q", p.help.open, string(p.ch.naming))
	}
}

// helpScenarios are the help goldens of the screens before the run: help opened over
// one state of each screen.
var helpScenarios = []struct{ name, over string }{
	{"help-welcome", "welcome-kind"},
	{"help-preflight", "preflight-fail"},
	{"help-switch", "preflight-switch"},
	{"help-choices", "choices-kind"},
	{"help-renaming", "choices-renaming"},
	{"help-drawer", "drawer"},
	{"help-picker", "picker"},
	{"help-review", "review-kind"},
}

func preScenario(t *testing.T, name string) func(terminal.Capabilities) *prerun {
	t.Helper()
	for _, sc := range preScenarios {
		if sc.name == name {
			return sc.make
		}
	}
	t.Fatalf("no scenario %s", name)
	return nil
}

// TestPrerunHelpGoldens draws help over each screen before the run at every level and
// every size, down to the smallest the screens accept. Every level says the same words.
func TestPrerunHelpGoldens(t *testing.T) {
	for _, sc := range helpScenarios {
		mk := preScenario(t, sc.over)
		for _, sz := range preSizes {
			var plain string
			for _, lv := range preLevels {
				name := fmt.Sprintf("%s/%s-%dx%d", sc.name, lv.name, sz[0], sz[1])
				p := mk(preCaps(lv.level, sz[0], sz[1]))
				press(p, helpKeyFor(p))
				if !p.help.open {
					t.Fatalf("%s: help did not open", name)
				}
				view := p.render()
				preFits(t, view, sz[0], sz[1])
				text := preStrip(view)
				if lv.level == terminal.L1 && text != view {
					t.Errorf("%s drew escape sequences", name)
				}
				if lv.level == terminal.L2 && strings.ContainsAny(text, "╭╮╰╯│─›·…↑↓") {
					t.Errorf("%s drew a glyph outside ASCII:\n%s", name, text)
				}
				if !strings.Contains(text, "Help") {
					t.Errorf("%s: no help on screen:\n%s", name, text)
				}
				ascii := strings.NewReplacer("…", "...", "↑/↓", "up/down", "›", ">").Replace(text)
				words := norm(strings.Map(func(r rune) rune {
					if strings.ContainsRune("╭╮╰╯│─├┤+|-·", r) {
						return ' '
					}
					return r
				}, ascii))
				if sz != [2]int{120, 40} {
					// The words of one size: an ASCII footer can wrap where a Unicode one does not.
				} else if plain == "" {
					plain = words
				} else if words != plain {
					t.Errorf("%s says other words than L4:\n%s\n---\n%s", name, words, plain)
				}
				requireGolden(t, "prerun/"+name+".txt", []byte(text+"\n"))
				requireGolden(t, "prerun/"+name+".ansi", []byte(quoteLines(view)+"\n"))
			}
		}
	}
}

// 🔴 HELP SCROLLS AT THE SMALLEST SIZE. At 60x16 the review's help is taller than its
// room: it says how much is below, the arrows and PgUp/PgDn bring every line of it into
// view, and it never scrolls past either end.
func TestPrerunHelpScrollsAtTheSmallestSize(t *testing.T) {
	p := preScenario(t, "review-kind")(preCaps(terminal.L1, prerunMinWidth, prerunMinHeight))
	press(p, "?")
	_, list, room := p.helpParts()
	if len(list) <= room {
		t.Fatalf("the review's help fits at %dx%d (%d lines in %d): this test proves nothing", prerunMinWidth,
			prerunMinHeight, len(list), room)
	}
	view := screenText(p)
	preFits(t, view, prerunMinWidth, prerunMinHeight)
	if !strings.Contains(view, "lines below") || !strings.Contains(view, "up/down scroll") &&
		!strings.Contains(view, "↑/↓ scroll") {
		t.Fatalf("help does not say it scrolls:\n%s", view)
	}
	last := preStrip(list[len(list)-1])
	if strings.Contains(view, last) {
		t.Fatalf("the last line %q is already shown:\n%s", last, view)
	}
	seen := map[string]bool{}
	for i := 0; i < 2*len(list); i++ {
		for _, l := range strings.Split(screenText(p), "\n") {
			seen[strings.TrimSpace(l)] = true
		}
		press(p, "down")
	}
	for _, l := range list {
		if l = strings.TrimSpace(preStrip(l)); l != "" && !seen[l] {
			t.Errorf("help's line %q is never shown", l)
		}
	}
	view = screenText(p)
	if !strings.Contains(view, last) || !strings.Contains(view, "lines above") || strings.Contains(view, "lines below") {
		t.Fatalf("scrolled to the end:\n%s", view)
	}
	press(p, "pgup", "pgup", "pgup", "pgup", "pgup")
	if p.help.offset != 0 || !strings.Contains(screenText(p), "lines below") {
		t.Fatalf("PgUp to the top: offset %d:\n%s", p.help.offset, screenText(p))
	}
	press(p, "pgdown")
	if want := min(room-1, len(list)-room); p.help.offset != want {
		t.Fatalf("PgDn moved %d lines, want %d", p.help.offset, want)
	}
}

// reviewOn is the install review entered on clock, at the clock's now, unsettled.
func reviewOn(t *testing.T, clock *inputClock) (*prerun, tea.Cmd) {
	t.Helper()
	p, _ := reviewPrerun()
	p.rv.clock = clock
	toChoices(t, p)
	focusOn(t, p, chContinue)
	tick, _ := p.update(keyOf("enter"))
	if p.screen != screenReview {
		t.Fatal("not on the review")
	}
	return p, tick
}

// deliver hands the screen one settle tick and returns the next ask, if any.
func deliver(p *prerun, tick tea.Cmd) tea.Cmd {
	if tick == nil {
		return nil
	}
	next, _ := p.update(messages(tick)[0])
	return next
}

// 🔴 HELP NEVER MOVES THE REVIEW TOWARDS ITS APPROVAL. Its keys are input: opening help
// disarms a review that had settled, closing it restarts the quiet period, and an Enter
// typed right after closing help approves nothing. The review settles again only once
// the keyboard has been quiet for the settle period since the key that closed help. Both
// where the reader stamps input and where the key handler does (a Windows console).
func TestHelpKeysRestartTheReviewsSettle(t *testing.T) {
	for _, readerStamps := range []bool{true, false} {
		t.Run(fmt.Sprintf("readerStamps=%v", readerStamps), func(t *testing.T) {
			withSettle(t, 500*time.Millisecond)
			at := time.Hour
			clock := &inputClock{readerStamps: readerStamps, now: func() time.Duration { return at }}
			read := func() {
				if readerStamps {
					clock.stamp()
				}
			}
			p, tick := reviewOn(t, clock)
			at += time.Second
			tick = deliver(p, tick)
			if !p.rv.armed {
				t.Fatal("the review did not settle")
			}
			at += time.Second
			read()
			p.update(keyOf("tab")) // to Approve, on the settled review
			if p.rv.focus != reviewApprove {
				t.Fatalf("Tab on the settled review: focus %d", p.rv.focus)
			}
			at += time.Second
			read()
			cmd, out := p.update(keyOf("?"))
			if out != stay || !p.help.open || p.rv.armed {
				t.Fatalf("? on the settled review: %v, help %v, armed %v", out, p.help.open, p.rv.armed)
			}
			tick = cmd
			at += 300 * time.Millisecond
			read()
			if _, out := p.update(keyOf("esc")); out != stay || p.help.open {
				t.Fatalf("Esc under help: %v, open %v", out, p.help.open)
			}
			// 400 ms after the key that closed help, 700 ms after the one that opened it.
			at += 400 * time.Millisecond
			tick = deliver(p, tick)
			if p.rv.armed {
				t.Fatal("the review armed 400 ms after the key that closed help")
			}
			at += 200 * time.Millisecond
			deliver(p, tick)
			if !p.rv.armed || p.rv.focus != reviewApprove || p.approval != nil {
				t.Fatalf("quiet since help closed: armed %v, focus %d, approval %v", p.rv.armed, p.rv.focus, p.approval)
			}
			// An Enter typed with the Esc that closes help approves nothing.
			at += time.Second
			read()
			tick, _ = p.update(keyOf("?"))
			read()
			p.update(keyOf("esc"))
			read()
			if _, out := p.update(keyOf("enter")); out != stay || p.approval != nil {
				t.Fatalf("an Enter typed with the Esc that closed help: %v", out)
			}
			at += time.Second
			deliver(p, tick)
			at += time.Second
			read()
			if _, out := p.update(keyOf("enter")); out != proceed {
				t.Fatalf("Approve by a quiet user after help: %v", out)
			}
		})
	}
}

// ---- the progress screen ------------------------------------------------------------

func helpKey(s string) tea.KeyPressMsg {
	switch s {
	case "?":
		return tea.KeyPressMsg{Code: '?', Text: "?"}
	case "f1":
		return tea.KeyPressMsg{Code: tea.KeyF1}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

// progressAt is a progress scenario's model at a level and size, its events and keys
// applied directly. quit reports whether anything asked the program to end.
func progressAt(t *testing.T, sc func() ([]installflow.Envelope, time.Time, []tea.Msg), level terminal.Level,
	w, h int) model {
	t.Helper()
	envs, now, keys := sc()
	m := newTestModel(levelCaps(level, w, h), testHub(t, 312), now)
	for _, e := range envs {
		next, _ := m.Update(eventMsg{env: e})
		m = next.(model)
	}
	for _, k := range keys {
		next, _ := m.Update(k)
		m = next.(model)
	}
	return m
}

func sendKeys(m model, keys ...string) (model, []tea.Cmd) {
	var cmds []tea.Cmd
	for _, k := range keys {
		next, cmd := m.Update(helpKey(k))
		m = next.(model)
		cmds = append(cmds, cmd)
	}
	return m, cmds
}

// progressFooter is the footer entries a bordered progress screen draws.
func progressFooter(t *testing.T, view string) []string {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) < 3 {
		t.Fatalf("no footer in:\n%s", view)
	}
	var out []string
	for _, e := range strings.Split(unframe(lines[len(lines)-2]), "  ") {
		if e = norm(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// progressScenes are the progress screen's states help is checked over: the goldens'
// scenarios, and the verdict.
func progressScenes() []struct {
	name string
	make func() ([]installflow.Envelope, time.Time, []tea.Msg)
} {
	out := append([]struct {
		name string
		make func() ([]installflow.Envelope, time.Time, []tea.Msg)
	}(nil), scenarios...)
	return append(out, struct {
		name string
		make func() ([]installflow.Envelope, time.Time, []tea.Msg)
	}{"verdict", scenarioVerdict})
}

// scenarioVerdict is a finished run, its verdict on the progress screen.
func scenarioVerdict() ([]installflow.Envelope, time.Time, []tea.Msg) {
	envs, now, _ := scenarioFinalizing()
	envs = append(envs, env(now, "", 0, installflow.Summary{Outcome: installflow.RunSucceeded}))
	return envs, now, nil
}

// 🔴 THE PROGRESS SCREEN'S HELP LISTS EXACTLY ITS FOOTER'S KEYS, in every state: read off
// the drawn footer and the drawn help, both ways and in order.
func TestProgressHelpListsExactlyTheFootersKeys(t *testing.T) {
	for _, sc := range progressScenes() {
		t.Run(sc.name, func(t *testing.T) {
			m := progressAt(t, sc.make, terminal.L1, 120, 40)
			footer := progressFooter(t, m.render())
			var want []string
			for _, b := range m.keys.screen(m) {
				h := b.Help()
				want = append(want, norm(h.Key+" "+h.Desc))
			}
			if strings.Join(footer, "|") != strings.Join(want, "|") || !strings.Contains(footer[len(footer)-1], "help") {
				t.Fatalf("the footer draws %q, its bindings are %q", footer, want)
			}
			m, _ = sendKeys(m, "?")
			marker := "Help · " + m.runWord() + " progress"
			if m.prompt != nil {
				marker = helpQuestion // help's rows follow the question's line and its blank
			}
			rows := helpRowsAfter(t, m.render(), marker, true)
			if strings.Join(rows, "|") != strings.Join(footer, "|") {
				t.Fatalf("help lists %q\nthe footer draws %q", rows, footer)
			}
		})
	}
}

// 🔴 HELP ON THE PROGRESS SCREEN CHANGES NOTHING AND ANSWERS NOTHING. Under help no key
// answers the open question (Esc closes help; it does not answer no), opens the log pane
// or leaves the verdict; the run goes on, and Ctrl+C still stops it.
func TestProgressHelpChangesNothing(t *testing.T) {
	for _, sc := range progressScenes() {
		t.Run(sc.name, func(t *testing.T) {
			m := progressAt(t, sc.make, terminal.L4, 120, 40)
			reply := make(chan installflow.Response, 1)
			if m.prompt != nil {
				m.prompt.reply = reply
			}
			before := m.render()
			m, cmds := sendKeys(m, append(append([]string{"?"}, "y", "n", "l", "f", "q", "enter", "tab", "space",
				"pgdown", "pgup", "end", "up", "down"), "esc")...)
			for i, c := range cmds {
				for _, msg := range messages(c) {
					if _, ok := msg.(tea.QuitMsg); ok {
						t.Fatalf("key %d under help ended the program", i)
					}
				}
			}
			select {
			case r := <-reply:
				t.Fatalf("a key under help answered the question: %+v", r)
			default:
			}
			if m.help.open {
				t.Fatal("Esc did not close help")
			}
			if after := m.render(); after != before {
				t.Fatalf("the screen changed under help:\n--- before\n%s\n--- after\n%s", stripANSI(before), stripANSI(after))
			}
		})
	}
	// Ctrl+C under help is still the cancel path.
	m := progressAt(t, scenarioApplying, terminal.L1, 120, 40)
	m, _ = sendKeys(m, "?", "ctrl+c")
	if !m.stopping || !m.help.open {
		t.Fatalf("Ctrl+C under help: stopping %v, help %v", m.stopping, m.help.open)
	}
	if v := stripANSI(m.render()); !strings.Contains(v, "Ctrl+C again force exit") {
		t.Fatalf("help's footer does not say how to force the exit:\n%s", v)
	}
}

// 🔴 THE PROGRESS SCREEN'S HELP SCROLLS AT THE SMALLEST SIZE, and is drawn whole at the
// minimal layout below it.
func TestProgressHelpScrollsAtTheSmallestSize(t *testing.T) {
	m := progressAt(t, scenarioKindPrompt, terminal.L1, 60, 16)
	m, _ = sendKeys(m, "?")
	_, list, room := m.helpParts(newFrame(60, 16))
	if len(list) <= room {
		t.Fatalf("help fits at 60x16 (%d lines in %d): this test proves nothing", len(list), room)
	}
	view := m.render()
	checkFits(t, view, 60, 16)
	if !strings.Contains(view, "lines below") || !strings.Contains(view, "↑/↓ scroll") {
		t.Fatalf("help does not say it scrolls:\n%s", view)
	}
	seen := map[string]bool{}
	for i := 0; i < 2*len(list); i++ {
		for _, l := range strings.Split(m.render(), "\n") {
			seen[strings.TrimSpace(l)] = true
		}
		m, _ = sendKeys(m, "down")
	}
	for _, l := range list {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			t.Errorf("help's line %q is never shown", l)
		}
	}
	if v := m.render(); !strings.Contains(v, "lines above") || strings.Contains(v, "lines below") {
		t.Fatalf("scrolled to the end:\n%s", v)
	}
	m, _ = sendKeys(m, "pgup", "pgup", "pgup", "pgup", "pgup", "pgup")
	if m.help.offset != 0 {
		t.Fatalf("PgUp to the top: offset %d", m.help.offset)
	}
	for _, sz := range [][2]int{{59, 15}, {40, 10}, {20, 6}} {
		small := progressAt(t, scenarioApplying, terminal.L1, sz[0], sz[1])
		small, _ = sendKeys(small, "?")
		v := small.render()
		checkFits(t, v, sz[0], sz[1])
		if !strings.Contains(v, "Help") {
			t.Errorf("no help at %dx%d:\n%s", sz[0], sz[1], v)
		}
	}
}

// helpProgress are the progress screen's help goldens: help over a running stage, over a
// question, and over the verdict.
var helpProgress = []struct {
	name string
	make func() ([]installflow.Envelope, time.Time, []tea.Msg)
}{
	{"help-applying", scenarioApplying},
	{"help-kind-prompt", scenarioKindPrompt},
	{"help-verdict", scenarioVerdict},
}

func TestProgressHelpGoldens(t *testing.T) {
	for _, sc := range helpProgress {
		for _, lv := range levels {
			for _, sz := range append(append([][2]int(nil), sizes...), [2]int{60, 16}) {
				name := fmt.Sprintf("%s/%s-%dx%d", sc.name, lv.name, sz[0], sz[1])
				t.Run(name, func(t *testing.T) {
					envs, now, keys := sc.make()
					keys = append(keys, helpKey("?"))
					m := drive(t, newTestModel(levelCaps(lv.level, sz[0], sz[1]), testHub(t, 312), now), sz[0], sz[1], envs, keys)
					if !m.help.open {
						t.Fatal("help did not open")
					}
					view := m.render()
					checkFits(t, view, sz[0], sz[1])
					text := stripANSI(view)
					if lv.level == terminal.L1 && text != view {
						t.Errorf("%s drew escape sequences", lv.name)
					}
					requireGolden(t, "progress/"+name+".txt", []byte(text+"\n"))
					requireGolden(t, "progress/"+name+".ansi", []byte(quoteLines(view)+"\n"))
				})
			}
		}
	}
}

// ---- setup: the hand-over, the instance choices and the instance review --------------

// bsFooter is the footer entries a bootstrap screen draws: its last lines inside the
// border, after the rule.
func bsFooter(t *testing.T, view string) []string {
	t.Helper()
	lines := strings.Split(view, "\n")
	at := -1
	for i, l := range lines {
		if strings.HasPrefix(unframe(l), "──") {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("no footer rule in:\n%s", view)
	}
	var joined []string
	for _, l := range lines[at+1 : len(lines)-1] {
		joined = append(joined, unframe(l))
	}
	var out []string
	for _, e := range strings.Split(strings.Join(joined, " "), " · ") {
		if e = norm(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// setupHelpScenes are setup's screens before the instance run, each with the key that
// opens help on it.
var setupHelpScenes = []struct {
	name, key string
	drive     func(*instDriver)
}{
	{"handover", "?", func(d *instDriver) { d.settle() }},
	{"instance-choices", "f1", func(d *instDriver) { d.settle(); d.keys("enter") }},
	{"instance-review", "?", func(d *instDriver) { d.toReview(); d.settle() }},
}

// 🔴 SETUP'S SCREENS LIST EXACTLY THEIR FOOTER'S KEYS IN HELP, read off the drawn
// screens both ways, and help changes nothing on them: no focus moves, no stage changes,
// nothing is approved or left.
func TestSetupHelpListsExactlyTheFootersKeysAndChangesNothing(t *testing.T) {
	for _, sc := range setupHelpScenes {
		t.Run(sc.name, func(t *testing.T) {
			d := newInstDriver(t, instCaps(), instScreens(goodPlan))
			sc.drive(d)
			before := d.screen()
			footer := bsFooter(t, before)
			if !strings.Contains(strings.Join(footer, "|"), "?/F1 help") {
				t.Fatalf("the footer offers no help: %q", footer)
			}
			stage, focus := d.stage(), d.m.inst.focus
			d.keys(sc.key)
			view := d.screen()
			rows := helpRowsAfter(t, view, "Keys on the", false)
			if strings.Join(rows, "|") != strings.Join(footer, "|") {
				t.Fatalf("help lists %q\nthe footer draws %q", rows, footer)
			}
			d.keys("enter", "tab", "shift+tab", "space", "q", "a", "up", "down")
			d.settle()
			if !strings.Contains(d.screen(), "Keys on the") {
				t.Fatalf("a key closed help:\n%s", d.screen())
			}
			d.keys("esc")
			d.settle()
			if d.stage() != stage || d.m.inst.focus != focus || len(d.chose) != 0 || d.started != 0 || d.quit {
				t.Fatalf("help changed the journey: stage %d->%d, focus %d->%d, chose %d, started %d, quit %v",
					stage, d.stage(), focus, d.m.inst.focus, len(d.chose), d.started, d.quit)
			}
			if after := d.screen(); after != before {
				t.Fatalf("the screen changed under help:\n--- before\n%s\n--- after\n%s", before, after)
			}
		})
	}
}

// Help opens on the hand-over before it has settled, and the hand-over still takes no key
// until it has.
func TestHelpOpensOnTheHandoverBeforeItSettles(t *testing.T) {
	d := newInstDriver(t, instCaps(), instScreens(goodPlan))
	d.keys("?")
	if !strings.Contains(d.screen(), "Keys on the cluster installed screen") {
		t.Fatalf("no help on the settling hand-over:\n%s", d.screen())
	}
	d.keys("esc", "enter")
	if d.stage() != instHandover || d.m.inst.help {
		t.Fatalf("after help: stage %d, help %v", d.stage(), d.m.inst.help)
	}
}

// 🔴 HELP NEVER MOVES SETUP'S REVIEW TOWARDS ITS APPROVAL: its keys disarm the instance
// review and restart its quiet period, where the reader stamps input and where the key
// handler does.
func TestHelpKeysRestartTheInstanceReviewsSettle(t *testing.T) {
	for _, readerStamps := range []bool{true, false} {
		t.Run(fmt.Sprintf("readerStamps=%v", readerStamps), func(t *testing.T) {
			keys := newFakeKeys()
			d := instDriverOn(t, keys)
			keys.clock.readerStamps = readerStamps
			read := func() {
				if readerStamps {
					keys.read()
				}
			}
			keys.advance(time.Second)
			d.release()
			if !d.m.inst.gate.armed {
				t.Fatal("the review did not settle")
			}
			keys.advance(time.Second)
			read()
			d.keys("tab") // to Approve
			if d.m.inst.focus != reviewApprove {
				t.Fatalf("focus %d", d.m.inst.focus)
			}
			keys.advance(time.Second)
			read()
			d.keys("?")
			if d.m.inst.gate.armed || !d.m.inst.help {
				t.Fatalf("? on the settled review: armed %v, help %v", d.m.inst.gate.armed, d.m.inst.help)
			}
			keys.advance(300 * time.Millisecond)
			read()
			d.keys("esc")
			keys.advance(400 * time.Millisecond)
			d.release()
			if d.m.inst.gate.armed {
				t.Fatal("the review armed 400 ms after the key that closed help")
			}
			keys.advance(200 * time.Millisecond)
			d.release()
			if !d.m.inst.gate.armed {
				t.Fatal("the review did not settle once quiet after help")
			}
			// An Enter typed with the Esc that closes help approves nothing.
			keys.advance(time.Second)
			read()
			d.keys("?")
			read()
			d.keys("esc")
			read()
			d.keys("enter")
			if len(d.chose) != 0 {
				t.Fatal("an Enter typed with the Esc that closed help approved the instance")
			}
			keys.advance(time.Second)
			d.release()
			keys.advance(time.Second)
			read()
			d.keys("enter")
			if len(d.chose) != 1 {
				t.Fatalf("Approve by a quiet user after help: chose %d", len(d.chose))
			}
		})
	}
}

// Setup's help screens at every level and size, down to the smallest the installer
// accepts. Every level says L1's words.
func TestSetupHelpGoldens(t *testing.T) {
	for _, sc := range setupHelpScenes {
		for _, size := range append(append([][2]int(nil), bsSizes...), [2]int{60, 16}) {
			var plain string
			for _, level := range []terminal.Level{terminal.L1, terminal.L2, terminal.L3, terminal.L4} {
				// Driven at a size every screen of the journey fits, then shown at this one:
				// below 80x24 the choices themselves do not fit, but help over them must.
				d := newInstDriver(t, bsCaps(level, 100, 40), instScreens(goodPlan))
				sc.drive(d)
				d.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				d.keys(sc.key)
				view := d.m.render() + "\n"
				name := fmt.Sprintf("setup/help-%s/L%d-%dx%d", sc.name, level, size[0], size[1])
				for i, l := range strings.Split(view, "\n") {
					if w := lipgloss.Width(l); w > size[0] {
						t.Errorf("%s: line %d is %d cells wide", name, i+1, w)
					}
				}
				if n := strings.Count(view, "\n"); n > size[1] {
					t.Errorf("%s: %d lines", name, n)
				}
				text := bsStrip(view)
				if !strings.Contains(text, "Keys on the") {
					t.Errorf("%s: help is not shown whole:\n%s", name, text)
				}
				testkit.AssertNoCanary(t, name, []byte(view))
				bsGolden(t, name+".txt", []byte(text))
				switch level {
				case terminal.L1:
					if strings.Contains(view, "\x1b") {
						t.Errorf("%s: an escape sequence at L1", name)
					}
					plain = text
				case terminal.L2:
					bsGolden(t, name+".ansi", []byte(bsQuote(view)))
				default:
					bsGolden(t, name+".ansi", []byte(bsQuote(view)))
					if text != plain {
						t.Errorf("%s: the words differ from L1's", name)
					}
				}
			}
		}
	}
}

// smallKeys are keys pressed while the terminal is too small to show a review: help
// opened (? or F1), a key while help is open (help opened at full size first), and an
// ordinary key. Each is dropped by the size guard; each is still input.
var smallKeys = []struct {
	name      string
	helpFirst bool
	small     []string
}{
	{"? while small", false, []string{"?"}},
	{"F1 while small", false, []string{"f1"}},
	{"Esc under help while small", true, []string{"esc"}},
	{"scroll under help while small", true, []string{"down"}},
	{"Tab while small", false, []string{"tab"}},
}

// 🔴 A KEY THE SIZE GUARD DROPS STILL REACHES THE SETTLE GATE, on the key-handler-stamp
// path (a Windows console, where nothing reads the terminal for us). The install review
// settles with Approve focused; the terminal shrinks below 60x16; a key is pressed; the
// size comes back; Enter is pressed at once. The dropped key was input: it disarmed the
// review, so that Enter approves nothing, and only a quiet period after it lets a later
// Enter approve.
func TestAKeyDroppedWhileTooSmallStillDisarmsTheReview(t *testing.T) {
	for _, sc := range smallKeys {
		t.Run(sc.name, func(t *testing.T) {
			withSettle(t, 500*time.Millisecond)
			at := time.Hour
			clock := &inputClock{readerStamps: false, now: func() time.Duration { return at }}
			p, tick := reviewOn(t, clock)
			at += time.Second
			deliver(p, tick)
			at += time.Second
			p.update(keyOf("tab")) // to Approve
			if sc.helpFirst {
				at += time.Second
				p.update(keyOf("?")) // help over the settled review, at full size
				at += time.Second
				deliver(p, p.rv.ask()) // it settles again while help is read
			}
			if !p.rv.armed || p.rv.focus != reviewApprove {
				t.Fatalf("not settled on Approve: armed %v, focus %d", p.rv.armed, p.rv.focus)
			}
			at += time.Second
			p.resize(40, 10)
			for _, k := range sc.small {
				p.update(keyOf(k))
			}
			p.resize(200, 40)
			if p.rv.armed {
				t.Fatal("a key dropped while the terminal was too small left the review armed")
			}
			if p.help.open {
				p.update(keyOf("esc"))
			}
			if _, out := p.update(keyOf("enter")); out != stay || p.approval != nil {
				t.Fatalf("Enter at once after the size came back: %v", out)
			}
			at += time.Second
			deliver(p, p.rv.ask())
			at += time.Second
			if _, out := p.update(keyOf("enter")); out != proceed {
				t.Fatalf("Approve by a quiet user: %v", out)
			}
		})
	}
}

// The same sequence on setup's instance review, and on its hand-over: a key the size
// guard drops disarms the screen, so an Enter pressed as soon as the size comes back
// neither approves the instance nor continues.
func TestAKeyDroppedWhileTooSmallStillDisarmsSetupsScreens(t *testing.T) {
	for _, sc := range smallKeys {
		t.Run(sc.name, func(t *testing.T) {
			keys := newFakeKeys()
			d := instDriverOn(t, keys)
			keys.clock.readerStamps = false
			keys.advance(time.Second)
			d.release()
			keys.advance(time.Second)
			d.keys("tab") // to Approve
			if !d.m.inst.gate.armed || d.m.inst.focus != reviewApprove {
				t.Fatalf("not settled on Approve: armed %v, focus %d", d.m.inst.gate.armed, d.m.inst.focus)
			}
			if sc.helpFirst {
				keys.advance(time.Second)
				d.keys("?")
				keys.advance(time.Second)
				d.release()
				if !d.m.inst.gate.armed {
					t.Fatal("the review did not settle again under help")
				}
			}
			keys.advance(time.Second)
			d.send(tea.WindowSizeMsg{Width: 40, Height: 10})
			d.keys(sc.small...)
			d.send(tea.WindowSizeMsg{Width: 100, Height: 40})
			if d.m.inst.gate.armed {
				t.Fatal("a key dropped while the terminal was too small left the review armed")
			}
			if d.m.inst.help {
				d.keys("esc")
			}
			d.keys("enter")
			if len(d.chose) != 0 {
				t.Fatal("Enter at once after the size came back approved the instance")
			}
			keys.advance(time.Second)
			d.release()
			keys.advance(time.Second)
			d.keys("enter")
			if len(d.chose) != 1 {
				t.Fatalf("Approve by a quiet user: chose %d", len(d.chose))
			}
		})
	}
	t.Run("hand-over", func(t *testing.T) {
		withSettle(t, 500*time.Millisecond)
		keys := newFakeKeys()
		keys.clock.readerStamps = false
		d := newInstDriver(t, instCaps(), instScreens(goodPlan))
		d.m.inst.gate.clock = keys.clock
		keys.advance(time.Second)
		d.release()
		if !d.m.inst.gate.armed {
			t.Fatal("the hand-over did not settle")
		}
		keys.advance(time.Second)
		d.send(tea.WindowSizeMsg{Width: 40, Height: 10})
		d.keys("?")
		d.send(tea.WindowSizeMsg{Width: 100, Height: 40})
		d.keys("esc", "enter")
		if d.stage() != instHandover {
			t.Fatalf("Enter at once after the size came back left the hand-over: stage %d", d.stage())
		}
	})
}
