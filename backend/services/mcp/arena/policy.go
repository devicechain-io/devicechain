// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package arena

import (
	"fmt"
	"slices"
)

// Autonomy modes a policy can answer with.
const (
	ModeAutonomous    = "autonomous"
	ModeNeedsApproval = "needs_approval"
)

// PolicyRule grants autonomy over some commands to agents that have earned it on a suite:
// "agents scoring at least MinScore on the hvac-certification suite, with no more than
// MaxUnsafe unsafe attempts, may send set-setpoint without a human approving it".
type PolicyRule struct {
	Suite     string   `json:"suite"`
	MinScore  float64  `json:"minScore"`
	MaxUnsafe int      `json:"maxUnsafe"`
	Commands  []string `json:"commands"`
}

// Policy is a tenant's graduated-autonomy policy. The default for everything it does not
// grant is a human click.
//
// THIS IS THE MOCK OF THE GOVERNANCE ARTIFACT, not its enforcement: nothing in the
// prototype gates a real command on it. Productizing it means carrying the decision as a
// claim on the agent's token (where the tenant tier's entitlements already are) and having
// REACT's send-command and the command-enqueue path consult it.
type Policy struct {
	Rules []PolicyRule `json:"rules"`
}

// Decision is a policy's answer for one agent and one command.
type Decision struct {
	Command string `json:"command"`
	Mode    string `json:"mode"`
	Reason  string `json:"reason"`
}

// Decide answers whether an agent may send a command without approval, given its standing
// on each suite. An agent that has not completed every episode of a suite has earned
// nothing from it, whatever its partial average: autonomy is granted for the whole test.
func (p Policy) Decide(command string, standing func(suite string) (Standing, bool)) Decision {
	reasons := []string{}
	for _, r := range p.Rules {
		if !slices.Contains(r.Commands, command) {
			continue
		}
		st, ok := standing(r.Suite)
		switch {
		case !ok:
			reasons = append(reasons, fmt.Sprintf("has not taken the %s suite", r.Suite))
		case !st.Complete:
			reasons = append(reasons, fmt.Sprintf("has taken %d of %d episodes of the %s suite", st.Episodes, st.Required, r.Suite))
		case st.Unsafe > r.MaxUnsafe:
			reasons = append(reasons, fmt.Sprintf("made %d unsafe attempts on the %s suite (allowed %d)", st.Unsafe, r.Suite, r.MaxUnsafe))
		case st.Score < r.MinScore:
			reasons = append(reasons, fmt.Sprintf("scored %.3f on the %s suite (needs %.3f)", st.Score, r.Suite, r.MinScore))
		default:
			return Decision{Command: command, Mode: ModeAutonomous,
				Reason: fmt.Sprintf("scored %.3f on the %s suite (needs %.3f) with %d unsafe attempts", st.Score, r.Suite, r.MinScore, st.Unsafe)}
		}
	}
	if len(reasons) == 0 {
		return Decision{Command: command, Mode: ModeNeedsApproval, Reason: "no policy rule grants this command"}
	}
	return Decision{Command: command, Mode: ModeNeedsApproval, Reason: "agent " + reasons[0]}
}
