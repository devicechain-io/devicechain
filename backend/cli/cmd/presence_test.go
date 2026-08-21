// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// withTerminal swaps both terminal seams for the duration of a test and restores them
// afterwards, so no case can leak a fake terminal into the next one.
func withTerminal(t *testing.T, isTTY bool, answer bool) *int {
	t.Helper()
	prompts := 0
	origTTY, origPrompt := stdinIsTerminal, promptDemotion
	stdinIsTerminal = func() bool { return isTTY }
	promptDemotion = func(string) bool { prompts++; return answer }
	t.Cleanup(func() { stdinIsTerminal, promptDemotion = origTTY, origPrompt })
	return &prompts
}

// 🔴 A RUN WITH NOWHERE TO READ AN ANSWER FROM IS NOT A RUN THAT SAID YES. Piped into
// a script, the demotion is REFUSED and names the flag that means it — the two
// alternatives are worse in opposite directions: blocking hangs a cron job forever,
// and assuming yes makes a fleet-wide write the thing that happens when nobody is
// watching.
//
// Killing mutation: return (true, nil) from the no-terminal branch.
func TestADemotionWillNotProceedWithNoTerminalToConfirmOn(t *testing.T) {
	prompts := withTerminal(t, false, true)
	out := &bytes.Buffer{}
	ok, err := confirmDemotion(out, "mqtt1", "acme", 0, false)
	if err == nil {
		t.Fatal("a non-interactive demotion was allowed to proceed with no confirmation")
	}
	if ok {
		t.Error("confirmDemotion returned ok alongside its refusal")
	}
	if *prompts != 0 {
		t.Errorf("%d prompt(s) were issued with no terminal to read them; that is the hang", *prompts)
	}
	for _, want := range []string{"--yes", "--dry-run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s, so it says no without saying how to say yes: %v", want, err)
		}
	}
}

// --yes is the deliberate way past the prompt, and it works without a terminal — which
// is what makes the command scriptable at all.
//
// Killing mutation: check the terminal before assumeYes — every scripted run breaks.
func TestYesProceedsWithoutATerminalAndWithoutPrompting(t *testing.T) {
	prompts := withTerminal(t, false, false)
	out := &bytes.Buffer{}
	ok, err := confirmDemotion(out, "mqtt1", "acme", 0, true)
	if err != nil || !ok {
		t.Fatalf("--yes did not proceed: ok=%v err=%v", ok, err)
	}
	if *prompts != 0 {
		t.Errorf("--yes still prompted %d time(s)", *prompts)
	}
}

// Answering anything but yes aborts, and aborting is not an error — the operator did
// exactly what the prompt asked.
func TestAnsweringNoAbortsWithoutAnError(t *testing.T) {
	withTerminal(t, true, false)
	out := &bytes.Buffer{}
	ok, err := confirmDemotion(out, "mqtt1", "acme", 0, false)
	if err != nil {
		t.Fatalf("declining the prompt was an error: %v", err)
	}
	if ok {
		t.Error("a declined prompt proceeded anyway")
	}
}

// 🔴 THE PROMPT HAS TO STATE THE BLAST RADIUS, because the command line does not. The
// operator typed a source name; nothing they typed says whether that source holds four
// devices or forty thousand. An unnarrowed run says EVERY, a narrowed one says how
// many were named, and the two must not read alike.
//
// Killing mutation: use the unnarrowed wording in both branches — a scoped repair and
// a fleet-wide one become indistinguishable at the only moment anyone is asked.
func TestThePromptStatesWhetherTheRunIsFleetWideOrNarrowed(t *testing.T) {
	withTerminal(t, true, true)

	wide := &bytes.Buffer{}
	if _, err := confirmDemotion(wide, "mqtt1", "acme", 0, false); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !strings.Contains(wide.String(), "EVERY asserted device") {
		t.Errorf("an unnarrowed run did not say it covers every device:\n%s", wide)
	}
	if !strings.Contains(wide.String(), `"mqtt1"`) || !strings.Contains(wide.String(), `"acme"`) {
		t.Errorf("the prompt names neither the source nor the tenant:\n%s", wide)
	}

	narrow := &bytes.Buffer{}
	if _, err := confirmDemotion(narrow, "mqtt1", "acme", 3, false); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if strings.Contains(narrow.String(), "EVERY") {
		t.Errorf("a run narrowed to 3 devices announced itself as fleet-wide:\n%s", narrow)
	}
	if !strings.Contains(narrow.String(), "3 named device(s)") {
		t.Errorf("a narrowed run did not say how many devices it names:\n%s", narrow)
	}
}

// adrCitation matches this project's inline decision-record references.
var adrCitation = regexp.MustCompile(`ADR-\d`)

// The presence command's operator-facing prose cites no decision records.
//
// WHY THIS IS SCOPED TO ONE SUBTREE. Source comments in this repo cite ADRs by
// convention and should keep doing so, but `--help` is served prose: the records live
// in a repository the person running the command cannot open, so a citation there is a
// dead reference dressed as a source.
//
// Older commands still carry such citations in help text and printed output — this
// grep finds them, and deliberately does not fix them here:
//
//	grep -rn "ADR-" --include=*.go cmd/ bootstrap/ |
//	  grep -iE 'Short:|Long:|Flags\(\)|fmt\.Print|errors\.New|fmt\.Errorf'
//
// Sweeping them in behind an unrelated change is how a rename becomes a review nobody
// can read. No count is given because a number in prose only ever drifts: run the grep.
// This guard covers the surface it was written with, and starts that surface clean.
func TestPresenceHelpCitesNoDecisionRecords(t *testing.T) {
	checked := 0
	walkCommands(presenceCmd, func(c *cobra.Command) {
		for label, text := range map[string]string{"Short": c.Short, "Long": c.Long, "Use": c.Use} {
			checked++
			if adrCitation.MatchString(text) {
				t.Errorf("%s: %s cites a decision record, which whoever runs this command cannot read:\n%s",
					c.CommandPath(), label, text)
			}
		}
		check := func(f *pflag.Flag) {
			checked++
			if adrCitation.MatchString(f.Usage) {
				t.Errorf("%s: --%s's usage cites a decision record: %s", c.CommandPath(), f.Name, f.Usage)
			}
		}
		c.Flags().VisitAll(check)
		c.PersistentFlags().VisitAll(check)
	})
	// A walk that reached nothing would pass this silently.
	if checked < 10 {
		t.Fatalf("the walk checked only %d strings, so it is not covering the presence command tree", checked)
	}
}
