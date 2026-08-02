// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Proving a scenario's detection rules are LIVE, rather than inferring it from a
// publish that returned no error.
//
// 🔴 A silent publish is not evidence. The ADR-044 validation gate compiles a
// profile's enabled rules at publish and fails closed — but ONLY when a validator is
// wired: with the service secret unset it returns nil having validated nothing. On
// such an instance a scenario bootstraps entirely green, event-processing drops the
// rule at load with a log line nobody is reading, and both alarm widgets are
// permanently empty while every CI gate stays green. "The publish did not error" and
// "the rule is running" are different claims, and the first was standing in for the
// second.
//
// So the claim is checked where it is true or false: event-processing's own
// ruleHealth, which reads ITS OWN projection of the active version.
//
// 🔴🔴 And it is checked PER VERSION, which is the whole subtlety. ruleHealth does not
// read device-management — it answers from a projection an async NATS consumer
// writes, so for a window after a publish it is still serving the PREVIOUS version.
// Rule tokens are stable authoring ids that survive every definition change, so that
// previous version reports the very same tokens, all ACTIVE: a check matching on
// token alone passes on the first read and confirms a version the engine has not
// loaded. That is not a corner case — it is the ordinary "ship a corrected rule and
// re-bootstrap" flow, which is exactly the flow the reconciler exists for and the one
// where a false green costs the most. A rule id embeds "{profileToken}@{version}", so
// the version is right there to be checked, and rows from any other version are
// treated as "not settled yet" rather than as an answer.
const queryRuleHealth = `query($profileToken:String!){` +
	`ruleHealth(profileToken:$profileToken){ruleId ruleToken status message}}`

// ruleStatusActive is event-processing's RuleStatus for a rule that compiles and is
// running. Any other value — COMPILE_ERROR today, whatever is added later — is a
// failure here, so the comparison is against ACTIVE rather than against a list of
// known-bad statuses that a new one would silently join.
//
// It aliases the single mirrored copy in wirevocabulary.go, which is what the
// authored-rules fixture publishes and event-processing holds to its real RuleStatus.
// A second inline "ACTIVE" here would be a copy no gate can see.
const ruleStatusActive = RuleStatusActiveWire

// The settle window. The projection is asynchronous, so a check with no wait would
// fail on timing rather than on correctness — the fastest way to get a real gate
// deleted for being flaky. Transport failures are retried inside the same window for
// the same reason: a rolling restart of event-processing is the same asynchrony
// wearing a different hat.
//
// 🔴 These are vars, not consts, ONLY so a test can shorten them — a negative case
// has to outlast the window, and 30s per case is how a suite gets skipped. They are
// mutated by the shared fake-server constructor, so THIS PACKAGE MUST STAY SERIAL:
// adding t.Parallel() to any test here races the swap, and the loudest symptom would
// be a 30s hang rather than a failure.
var (
	ruleHealthSettleTimeout = 30 * time.Second
	ruleHealthPollInterval  = time.Second
)

// verifyDetectionRulesAreLive fails the bootstrap unless every ENABLED rule the
// profile declares is ACTIVE in event-processing's live rule set FOR activeVersion.
//
// Only enabled rules: a disabled rule is inert by design (the runtime skips it at the
// fact-emit boundary and the publish gate does not submit it), so requiring it to be
// live would refuse a scenario that deliberately parks one.
//
// The declared rules must be a SUBSET of what is live, not equal to it. A tenant may
// legitimately author its own rules on the same profile through the console, and
// refusing to bootstrap because someone added one would make the sim hostile to the
// instance it runs on. The cost is that a rule this scenario REMOVED but which is
// still running goes unreported here; its symptom is a spurious alarm rather than a
// silent absence, which is the failure direction that shows itself.
func verifyDetectionRulesAreLive(ctx context.Context, rt *Runtime, p ProfileSpec, activeVersion int) error {
	want := make([]string, 0, len(p.DetectionRules))
	for _, r := range p.DetectionRules {
		if r.Enabled {
			want = append(want, r.Token)
		}
	}
	if len(want) == 0 {
		return nil
	}
	sort.Strings(want)

	if strings.TrimSpace(rt.Endpoints.EventProcessingGraphQL) == "" {
		return fmt.Errorf("profile %q declares %d enabled detection rule(s) but the handshake "+
			"carries no endpoints.eventProcessingGraphQL, so nothing can confirm they are running: "+
			"a rule dropped at load leaves the alarm widgets permanently empty with every step "+
			"reporting success. Re-create the sim with a current dcctl", p.Token, len(want))
	}

	deadline := time.Now().Add(ruleHealthSettleTimeout)
	var problem string
	for {
		health, err := fetchRuleHealth(ctx, rt, p.Token)
		if err != nil {
			// Retried, not fatal. event-processing gates its data plane on an auth
			// readiness check and is restarted by ordinary rollouts, so a single 5xx
			// during a bootstrap is the asynchronous seam this loop exists to absorb.
			// Only the deadline decides.
			problem = fmt.Sprintf("event-processing could not be reached: %v", err)
		} else {
			problem = firstUnhealthyRule(want, health, p.Token, activeVersion)
			if problem == "" {
				log.Info().Str("token", p.Token).Int("version", activeVersion).Int("rules", len(want)).
					Msg("detection rules confirmed live in event-processing")
				return nil
			}
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(ruleHealthPollInterval):
		}
	}
	return fmt.Errorf("profile %q version %d published, but after %s %s.\n"+
		"Three things produce this, in rough order of likelihood: event-processing is not "+
		"deployed or not reachable on this instance (the chart's telemetry and ingest-only "+
		"profiles do not include it, and this scenario's alarm path cannot work without it); "+
		"it is unhealthy or badly backed up; or the publish-time rule validator was SKIPPED "+
		"(device-management validates rules at publish only when a validator is wired), the "+
		"rule never reached the engine, and the scenario's alarm widgets would stay empty "+
		"forever", p.Token, activeVersion, ruleHealthSettleTimeout, problem)
}

// ruleHealthEntry is one rule's live status, as event-processing reports it. RuleId
// is "{tenant}/{profileToken}@{version}/{ruleToken}" — the only field that says WHICH
// version the engine is answering for.
type ruleHealthEntry struct {
	RuleId    string `json:"ruleId"`
	RuleToken string `json:"ruleToken"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

func fetchRuleHealth(ctx context.Context, rt *Runtime, profileToken string) ([]ruleHealthEntry, error) {
	var out struct {
		RuleHealth []ruleHealthEntry `json:"ruleHealth"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.EventProcessingGraphQL, queryRuleHealth,
		map[string]any{"profileToken": profileToken}, &out); err != nil {
		return nil, fmt.Errorf("ruleHealth(%q): %w", profileToken, err)
	}
	return out.RuleHealth, nil
}

// ruleIdVersion extracts the profile version from a minted rule id
// "{tenant}/{profileToken}@{version}/{ruleToken}". Returns ok=false for an id that
// does not parse into that shape, which is treated as "cannot confirm" rather than as
// a match — an unparseable id must never be read as agreement.
func ruleIdVersion(id, profileToken string) (int, bool) {
	_, rest, ok := strings.Cut(id, "/") // drop the tenant prefix
	if !ok {
		return 0, false
	}
	pvt, _, ok := strings.Cut(rest, "/") // pvt = "{profileToken}@{version}"
	if !ok {
		return 0, false
	}
	token, version, ok := strings.Cut(pvt, "@")
	if !ok || token != profileToken {
		return 0, false
	}
	n, err := strconv.Atoi(version)
	if err != nil {
		return 0, false
	}
	return n, true
}

// firstUnhealthyRule returns a human-readable description of the first thing standing
// between the declared rules and "running under activeVersion", or "" when every one
// of them is.
//
// It is a pure function over the two lists so each distinct failure — the engine
// serving an older version, a rule absent from it, a rule that does not compile — is
// unit-testable without event-processing. The wording differs per case deliberately:
// they point at different causes, and a gate that says only "not healthy" sends the
// reader to the wrong place.
func firstUnhealthyRule(want []string, health []ruleHealthEntry, profileToken string, activeVersion int) string {
	live := make(map[string]ruleHealthEntry, len(health))
	stale := 0
	for _, h := range health {
		version, ok := ruleIdVersion(h.RuleId, profileToken)
		if !ok {
			return fmt.Sprintf("event-processing reported rule id %q, which does not carry a "+
				"%q version — the check cannot tell which version it is answering for", h.RuleId, profileToken)
		}
		if version != activeVersion {
			stale++
			continue
		}
		live[h.RuleToken] = h
	}
	// Nothing for this version yet. Reported as the engine lagging rather than as a
	// missing rule, because it is: the tokens in those stale rows are the SAME tokens,
	// and reading them as an answer is the false green this check exists to prevent.
	if len(live) == 0 && stale > 0 {
		return fmt.Sprintf("event-processing is still serving an older version of profile %q "+
			"(%d rule(s) from another version, none from version %d) — its projection has not "+
			"caught up with the publish", profileToken, stale, activeVersion)
	}
	for _, token := range want {
		h, ok := live[token]
		if !ok {
			return fmt.Sprintf("detection rule %q is absent from event-processing's live rule set "+
				"for version %d (it was dropped at load, or the engine has not loaded that version)",
				token, activeVersion)
		}
		if h.Status != ruleStatusActive {
			detail := h.Message
			if detail == "" {
				detail = "no diagnostic reported"
			}
			return fmt.Sprintf("detection rule %q is %s, not %s: %s", token, h.Status, ruleStatusActive, detail)
		}
	}
	return ""
}
