// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// 🔑 isCommandWidgetType is a hand-kept list, and the list it has to agree with is the set of
// widget descriptors that declare `interactions: { commandSend: true }` in @devicechain/widgets.
// A control added there and not here would be placed on a board whose devices never answer it,
// and Validate's far-end gate would wave it through. So the set is read from the descriptors
// rather than trusted, in both directions: a type wrongly flagged here would refuse a board that
// sends nothing.
func TestCommandWidgetTypesAreTheDescriptorsThatSendCommands(t *testing.T) {
	const descriptors = "../../../../frontend/packages/widgets/src/builtin-descriptors.ts"
	source, err := os.ReadFile(descriptors)
	if err != nil {
		t.Fatalf("read %s: %v (if the descriptors moved, re-point this check rather than "+
			"deleting it)", descriptors, err)
	}
	// Each descriptor is one `define…Descriptor({ id: '<type>', ... })` block; a block runs to the
	// next one, so the interaction flag inside it belongs to that type. The built-ins are declared
	// through defineRuntimeDescriptor (the doc-free form; their option docs live in
	// builtin-option-docs.ts), the public defineWidgetDescriptor is what an extension uses, and
	// either spelling opens a block -- a rename of the helper must not empty the parse.
	starts := regexp.MustCompile(`define(?:Runtime|Widget)Descriptor\(\{\s*id:\s*'([^']+)'`).FindAllSubmatchIndex(source, -1)
	var all, sending []string
	for i, m := range starts {
		end := len(source)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		id := string(source[m[2]:m[3]])
		all = append(all, id)
		if regexp.MustCompile(`commandSend:\s*true`).Match(source[m[0]:end]) {
			sending = append(sending, id)
		}
	}
	// Negative controls on the parse: an empty or truncated read would make both directions
	// below vacuous. The gallery check proves every type has a descriptor.
	if len(all) < 10 {
		t.Fatalf("parsed %d descriptors out of %s; the pattern has rotted", len(all), descriptors)
	}
	if !slices.Contains(sending, commandWidgetType) {
		t.Fatalf("the parse found no commandSend on %q, which certainly sends; the pattern has rotted",
			commandWidgetType)
	}

	var flagged []string
	for _, id := range all {
		if isCommandWidgetType(id) {
			flagged = append(flagged, id)
		}
	}
	sort.Strings(sending)
	sort.Strings(flagged)
	if !slices.Equal(sending, flagged) {
		t.Errorf("isCommandWidgetType flags %v, but the descriptors that send commands are %v",
			flagged, sending)
	}
}

// Each control ON ITS OWN trips the far-end gate. The widgetlab board carries a command-button
// too, so the whole-manifest test passes even if a control fell out of isCommandWidgetType; only
// a board carrying that one control proves the gate sees it.
func TestValidateRefusesEachCommandControlAloneWithNoFarEnd(t *testing.T) {
	for _, widgetType := range []string{commandWidgetType, commandSliderWidgetType, commandToggleWidgetType} {
		t.Run(widgetType, func(t *testing.T) {
			m := NewWidgetlab(1, Load{}).Manifest()
			if len(m.Dashboards) == 0 {
				t.Fatal("widgetlab publishes no dashboard; nothing to put the control on")
			}
			m.Dashboards = slices.Clone(m.Dashboards[:1])
			m.Dashboards[0].Definition = `{"schemaVersion":1,"widgets":[{"id":"only","type":"` + widgetType + `"}]}`

			m.CommandFarEnd = FarEndInternal
			if err := m.Validate(); err != nil {
				t.Fatalf("the one-control board is invalid even with a far end, so the refusal below "+
					"would be for another reason: %v", err)
			}
			m.CommandFarEnd = FarEndNone
			err := m.Validate()
			if err == nil {
				t.Fatalf("a board carrying only a %s was accepted with no far end", widgetType)
			}
			if !strings.Contains(err.Error(), widgetType) {
				t.Errorf("error %q does not name the %s widget", err, widgetType)
			}
		})
	}
}
