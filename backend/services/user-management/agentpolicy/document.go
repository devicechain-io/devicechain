// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package agentpolicy is a tenant's AGENT POLICY: the published document that decides whether AI
// agents may act in the tenant at all, which OAuth clients may, what each kind of action needs
// before it happens, and who may approve it. It is pure: parsing, validation, the
// loosen/tighten classifier and the decision function, with no I/O. Storage and versioning live in
// iam; the GraphQL surface and the authorization server's use of it live in identity.
//
// 🔴 AN ABSENT, UNREADABLE OR DISABLED POLICY MEANS ACTUATION IS OFF. Every reader treats a
// document it cannot parse as no document, and no document as "refuse"; there is no default that
// lets an agent act.
package agentpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
)

// Mode is what a policy says about one kind of action.
type Mode string

const (
	// ModeRefuse: agents may not do this at all.
	ModeRefuse Mode = "refuse"
	// ModeApprove: an agent may propose it, and a person must approve it.
	ModeApprove Mode = "approve"
	// ModeAuto: it may execute without a person, inside the policy's limits and the code floors.
	ModeAuto Mode = "auto"
)

// rank orders the modes from tightest to loosest, for the classifier.
func (m Mode) rank() int {
	switch m {
	case ModeRefuse:
		return 0
	case ModeApprove:
		return 1
	case ModeAuto:
		return 2
	}
	return -1
}

// Impact is a command definition's declared impact.
type Impact string

const (
	ImpactNormal      Impact = "NORMAL"
	ImpactDisruptive  Impact = "DISRUPTIVE"
	ImpactDestructive Impact = "DESTRUCTIVE"
)

func validImpact(i Impact) bool {
	return i == ImpactNormal || i == ImpactDisruptive || i == ImpactDestructive
}

// Actuation values.
const (
	ActuationEnabled  = "enabled"
	ActuationDisabled = "disabled"
)

// Code-level ceilings no policy can exceed (D-14). A tenant can tighten below them, never loosen
// above them.
const (
	// MaxExpiry is the longest any proposal may wait for approval.
	MaxExpiry = 24 * time.Hour
	// MaxStepUpAge is the oldest second-factor verification any policy may accept for an approval
	// or a loosening publish.
	MaxStepUpAge = time.Hour
	// MaxActRefreshAge is the longest absolute life an act:* grant may have.
	MaxActRefreshAge = 7 * 24 * time.Hour
	// RolloutTwoPersonOver is the rollout size above which two approvers are always required.
	RolloutTwoPersonOver = 100
	// MaxListEntries bounds every list in the document.
	MaxListEntries = 256
	// MaxDocumentBytes bounds a document, as submitted and as stored (its canonical form).
	MaxDocumentBytes = 64 * 1024
	maxDocumentBytes = MaxDocumentBytes
	// maxStoredReadBytes bounds a STORED document as read back (see parse).
	maxStoredReadBytes = 2 * MaxDocumentBytes
)

// Defaults applied where the document is silent.
const (
	DefaultStepUpMaxAge         = 15 * time.Minute
	DefaultExpiry               = time.Hour
	DefaultRolloutExpiry        = 24 * time.Hour
	DefaultActRefreshAge        = 8 * time.Hour
	DefaultTwoPersonOverDevices = 50
)

// Duration is a time.Duration written as a Go duration string ("15m", "24h") in the document.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("a duration must be a string such as \"15m\"")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	*d = Duration(v)
	return nil
}

// D returns the duration, or def when unset.
func (d *Duration) D(def time.Duration) time.Duration {
	if d == nil {
		return def
	}
	return time.Duration(*d)
}

// Document is the policy as a tenant writes it. Every field is optional; Validate applies the
// rules and Effective* accessors apply the defaults. Unknown keys are refused at Parse.
type Document struct {
	// Version is the document format, "v1alpha1" in this release (the agent policy is in the
	// preview tier). Absent reads as v1alpha1, and the stored form always carries it. A version
	// this build does not know is refused (ErrUnknownVersion) before any other key is read, so a
	// document written by a newer release is never half-understood by an older one.
	Version string `json:"version,omitempty"`
	// Actuation is "enabled" or "disabled". Anything else, or absent, is disabled.
	Actuation string `json:"actuation,omitempty"`
	// Clients are the OAuth client_ids (and agent service-account client ids) that may receive
	// act:* scopes in this tenant. Absent or empty: none.
	Clients         []string      `json:"clients,omitempty"`
	Approvers       *Approvers    `json:"approvers,omitempty"`
	Expiry          *Expiry       `json:"expiry,omitempty"`
	Session         *Session      `json:"session,omitempty"`
	Taint           *Taint        `json:"taint,omitempty"`
	Auto            *Auto         `json:"auto,omitempty"`
	StepUp          *StepUpPolicy `json:"stepUp,omitempty"`
	ServiceAccounts *ServiceAccts `json:"serviceAccounts,omitempty"`
	Tools           *Tools        `json:"tools,omitempty"`
}

// Approvers says who may approve an agent's proposal.
type Approvers struct {
	// Roles are tenant role tokens whose members may approve.
	Roles []string `json:"roles,omitempty"`
	// StepUpMaxAge is how recently an approver's second factor must have been verified.
	StepUpMaxAge *Duration `json:"stepUpMaxAge,omitempty"`
	// AllowSelfConfirm lets the delegating person approve their own agent's single-approval,
	// normal-impact proposal. Default true; never allowed for two-person proposals.
	AllowSelfConfirm *bool `json:"allowSelfConfirm,omitempty"`
}

// Expiry bounds how long a proposal waits.
type Expiry struct {
	Default *Duration `json:"default,omitempty"`
	Max     *Duration `json:"max,omitempty"`
	Rollout *Duration `json:"rollout,omitempty"`
}

// Session bounds an act:* grant.
type Session struct {
	ActRefreshMaxAge *Duration `json:"actRefreshMaxAge,omitempty"`
}

// Taint is the tightening-only session-taint option.
type Taint struct {
	NeverAutoWhenTaintedOrUnknown bool `json:"neverAutoWhenTaintedOrUnknown,omitempty"`
}

// Auto governs automatic execution.
type Auto struct {
	// AllowAutoForPublicClients lets a public (unauthenticated) client's proposals auto-execute.
	// Default false: a public client's id is a label anyone can claim.
	AllowAutoForPublicClients bool `json:"allowAutoForPublicClients,omitempty"`
}

// StepUpPolicy governs what counts as a second factor.
type StepUpPolicy struct {
	AcceptRecoveryCodes bool `json:"acceptRecoveryCodes,omitempty"`
}

// ServiceAccts governs the tenant's service accounts.
type ServiceAccts struct {
	// AgentFlagRequiredFor: creating a service account that is NOT flagged as an agent and that
	// holds any of these authorities is refused.
	AgentFlagRequiredFor []string `json:"agentFlagRequiredFor,omitempty"`
}

// Tools is the per-kind policy.
type Tools struct {
	Alarms   *AlarmTools   `json:"alarms,omitempty"`
	Commands *CommandTools `json:"commands,omitempty"`
	Rules    *RuleTools    `json:"rules,omitempty"`
	Rollouts *RolloutTools `json:"rollouts,omitempty"`
	Annotate *Mode         `json:"annotate,omitempty"`
}

// AlarmTools governs alarm acknowledge and clear.
type AlarmTools struct {
	Ack         *Mode `json:"ack,omitempty"`
	Clear       *Mode `json:"clear,omitempty"`
	RatePerHour *int  `json:"ratePerHour,omitempty"`
}

// CommandTools governs commands.
type CommandTools struct {
	// Groups restricts targets to these entity groups (ANDed with the person's scope anchors).
	// Absent: no group restriction beyond the person's own scope.
	Groups []string `json:"groups,omitempty"`
	// Commands is the vocabulary allowlist. Absent or empty: no command is allowed.
	Commands  []string     `json:"commands,omitempty"`
	Mode      *Mode        `json:"mode,omitempty"`
	Auto      *CommandAuto `json:"auto,omitempty"`
	TwoPerson *TwoPerson   `json:"twoPerson,omitempty"`
	Caps      *CommandCaps `json:"caps,omitempty"`
}

// CommandAuto is when a command may auto-execute.
type CommandAuto struct {
	Impact         []Impact `json:"impact,omitempty"`
	MaxDevices     *int     `json:"maxDevices,omitempty"`
	DevicesPerHour *int     `json:"devicesPerHour,omitempty"`
}

// TwoPerson adds two-approver triggers on top of the code floor.
type TwoPerson struct {
	Impact      []Impact `json:"impact,omitempty"`
	OverDevices *int     `json:"overDevices,omitempty"`
}

// CommandCaps are hard limits.
type CommandCaps struct {
	PerAction      *int `json:"perAction,omitempty"`
	DevicesPerHour *int `json:"devicesPerHour,omitempty"`
}

// RuleTools governs rule changesets.
type RuleTools struct {
	// ReactActions is the REACT-action allowlist. The code floor admits only raiseAlarm in this
	// release, whatever is listed.
	ReactActions []string `json:"reactActions,omitempty"`
	Mode         *Mode    `json:"mode,omitempty"`
}

// RolloutTools governs rollouts.
type RolloutTools struct {
	MaxGroupSize *int  `json:"maxGroupSize,omitempty"`
	MaxInFlight  *int  `json:"maxInFlight,omitempty"`
	Mode         *Mode `json:"mode,omitempty"`
}

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("invalid agent policy")

// DocumentVersion is the one document format this build reads and writes.
const DocumentVersion = "v1alpha1"

// ErrUnknownVersion is a document whose format this build does not know: one written by a newer
// release, read on an older pod during a rolling upgrade. It is NOT ErrInvalid. Every reader that
// decides whether agents may act treats it as "no policy" (actuation off); see each caller for
// what else it does.
var ErrUnknownVersion = errors.New("agent policy document version not known to this release")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Parse reads a policy document strictly: unknown keys, trailing data, and any rule violation are
// refused. It never returns a partially valid document. It is for what a PERSON submits.
func Parse(raw []byte) (*Document, error) { return parse(raw, true) }

// ParseStored reads a document that is already STORED: every rule Parse applies, except that an
// entry of serviceAccounts.agentFlagRequiredFor need not be an authority THIS build knows. A stored
// entry this build does not know may have been written by a newer release. It requires nothing
// here, because no role here can hold it; preserving it keeps the rule for releases that know it.
func ParseStored(raw []byte) (*Document, error) { return parse(raw, false) }

func parse(raw []byte, vocabulary bool) (*Document, error) {
	limit := maxDocumentBytes
	if !vocabulary {
		// A STORED document was size-checked in its canonical form when it was written, but it is
		// read back through a jsonb column, whose text output adds a space after every ':' and ','.
		// That can never double it, so twice the limit admits every document a writer stored and
		// still bounds the read.
		limit = maxStoredReadBytes
	}
	if len(raw) > limit {
		return nil, invalid("the document is larger than %d bytes", limit)
	}
	// The version first, read loosely: a newer format may carry keys this build would refuse,
	// and the right answer to it is "unknown version", not "invalid".
	var head struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, invalid("%v", err)
	}
	if head.Version != "" && head.Version != DocumentVersion {
		return nil, fmt.Errorf("%w: %q (this release reads %q)", ErrUnknownVersion, head.Version, DocumentVersion)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d Document
	if err := dec.Decode(&d); err != nil {
		return nil, invalid("%v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, invalid("trailing data after the document")
	}
	if err := d.validate(vocabulary); err != nil {
		return nil, err
	}
	return &d, nil
}

// Canonical is the document's stored form: Go's struct order, defaults NOT filled in, so what is
// stored is what the tenant wrote.
//
// 🔴 EVERY WRITER STORES Canonical() OF A PARSED DOCUMENT, AND NOTHING ELSE. The forward-compat
// rules lean on it: a later release tells this format from its own by the version Canonical always
// writes, and ExtractAgentFlagRequirement reads the one frozen key from the stored bytes. A writer
// that stored raw input would let a document with no version, or with this key spelt differently,
// reach a release that cannot tell what it is. TestEveryWriterStoresTheCanonicalForm pins it.
//
// It always carries the version, so a later release can tell this format from its own.
func (d *Document) Canonical() ([]byte, error) {
	c := *d
	c.Version = DocumentVersion
	return json.Marshal(&c)
}

// Validate applies every rule to the document.
func (d *Document) Validate() error { return d.validate(true) }

func (d *Document) validate(vocabulary bool) error {
	switch d.Actuation {
	case "", ActuationEnabled, ActuationDisabled:
	default:
		return invalid("actuation must be %q or %q", ActuationEnabled, ActuationDisabled)
	}
	if err := validList("clients", d.Clients); err != nil {
		return err
	}
	if a := d.Approvers; a != nil {
		if err := validList("approvers.roles", a.Roles); err != nil {
			return err
		}
		if err := validDuration("approvers.stepUpMaxAge", a.StepUpMaxAge, MaxStepUpAge); err != nil {
			return err
		}
	}
	if e := d.Expiry; e != nil {
		for name, v := range map[string]*Duration{"expiry.default": e.Default, "expiry.max": e.Max, "expiry.rollout": e.Rollout} {
			if err := validDuration(name, v, MaxExpiry); err != nil {
				return err
			}
		}
		if e.Default != nil && e.Max != nil && *e.Default > *e.Max {
			return invalid("expiry.default exceeds expiry.max")
		}
	}
	if s := d.Session; s != nil {
		if err := validDuration("session.actRefreshMaxAge", s.ActRefreshMaxAge, MaxActRefreshAge); err != nil {
			return err
		}
	}
	if sa := d.ServiceAccounts; sa != nil {
		if err := validList("serviceAccounts.agentFlagRequiredFor", sa.AgentFlagRequiredFor); err != nil {
			return err
		}
		for _, a := range sa.AgentFlagRequiredFor {
			// A misspelt authority would require nothing and look like a rule.
			if a == string(auth.AuthorityAll) || (vocabulary && !auth.ValidAuthority(a)) {
				return invalid("serviceAccounts.agentFlagRequiredFor: %q is not a grantable authority", a)
			}
		}
	}
	if t := d.Tools; t != nil {
		if err := t.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tools) validate() error {
	if a := t.Alarms; a != nil {
		if err := validMode("tools.alarms.ack", a.Ack); err != nil {
			return err
		}
		if err := validMode("tools.alarms.clear", a.Clear); err != nil {
			return err
		}
		if err := validCount("tools.alarms.ratePerHour", a.RatePerHour); err != nil {
			return err
		}
	}
	if c := t.Commands; c != nil {
		if err := validList("tools.commands.groups", c.Groups); err != nil {
			return err
		}
		if c.Groups != nil && len(c.Groups) == 0 {
			// An empty list would be stored as absent, and absent means NO group restriction:
			// the opposite of what an empty allowlist says. Refused rather than reinterpreted.
			return invalid("tools.commands.groups may not be empty: omit it for no group restriction, or list the groups")
		}
		if err := validList("tools.commands.commands", c.Commands); err != nil {
			return err
		}
		if err := validMode("tools.commands.mode", c.Mode); err != nil {
			return err
		}
		if c.Mode != nil && *c.Mode == ModeAuto {
			return invalid("tools.commands.mode may be refuse or approve; automatic execution is configured under tools.commands.auto")
		}
		if a := c.Auto; a != nil {
			if err := validImpacts("tools.commands.auto.impact", a.Impact); err != nil {
				return err
			}
			if slices.Contains(a.Impact, ImpactDestructive) {
				return invalid("tools.commands.auto.impact may not include DESTRUCTIVE: a destructive command is never automatic")
			}
			if err := validCount("tools.commands.auto.maxDevices", a.MaxDevices); err != nil {
				return err
			}
			if err := validCount("tools.commands.auto.devicesPerHour", a.DevicesPerHour); err != nil {
				return err
			}
		}
		if tp := c.TwoPerson; tp != nil {
			if err := validImpacts("tools.commands.twoPerson.impact", tp.Impact); err != nil {
				return err
			}
			if err := validCount("tools.commands.twoPerson.overDevices", tp.OverDevices); err != nil {
				return err
			}
		}
		if cp := c.Caps; cp != nil {
			if err := validCount("tools.commands.caps.perAction", cp.PerAction); err != nil {
				return err
			}
			if err := validCount("tools.commands.caps.devicesPerHour", cp.DevicesPerHour); err != nil {
				return err
			}
		}
	}
	if r := t.Rules; r != nil {
		if err := validList("tools.rules.reactActions", r.ReactActions); err != nil {
			return err
		}
		for _, a := range r.ReactActions {
			if a != "raiseAlarm" {
				return invalid("tools.rules.reactActions may list only raiseAlarm in this release (got %q)", a)
			}
		}
		if err := validMode("tools.rules.mode", r.Mode); err != nil {
			return err
		}
		if r.Mode != nil && *r.Mode == ModeAuto {
			return invalid("tools.rules.mode may not be auto: a rule changeset always needs a person")
		}
	}
	if r := t.Rollouts; r != nil {
		if err := validCount("tools.rollouts.maxGroupSize", r.MaxGroupSize); err != nil {
			return err
		}
		if err := validCount("tools.rollouts.maxInFlight", r.MaxInFlight); err != nil {
			return err
		}
		if err := validMode("tools.rollouts.mode", r.Mode); err != nil {
			return err
		}
		if r.Mode != nil && *r.Mode == ModeAuto {
			return invalid("tools.rollouts.mode may not be auto: a rollout always needs a person to start it")
		}
	}
	if err := validMode("tools.annotate", t.Annotate); err != nil {
		return err
	}
	if t.Annotate != nil && *t.Annotate == ModeApprove {
		return invalid("tools.annotate may be refuse or auto: an annotation never waits for approval")
	}
	return nil
}

func validMode(name string, m *Mode) error {
	if m != nil && m.rank() < 0 {
		return invalid("%s must be refuse, approve or auto", name)
	}
	return nil
}

func validCount(name string, n *int) error {
	if n != nil && (*n < 0 || *n > 1_000_000) {
		return invalid("%s must be between 0 and 1000000", name)
	}
	return nil
}

func validDuration(name string, d *Duration, max time.Duration) error {
	if d == nil {
		return nil
	}
	if *d <= 0 || time.Duration(*d) > max {
		return invalid("%s must be positive and at most %s", name, max)
	}
	return nil
}

func validImpacts(name string, in []Impact) error {
	if len(in) > 3 {
		return invalid("%s lists too many impacts", name)
	}
	for _, i := range in {
		if !validImpact(i) {
			return invalid("%s: unknown impact %q", name, i)
		}
	}
	return nil
}

// validListEntry is the grammar every list entry is held to: letters, digits and . _ : -. It
// covers everything a list legitimately holds (OAuth and service-account client ids, role, group
// and command tokens, authorities such as command:write, REACT action names).
//
// 🔴 IT IS ALSO WHAT KEEPS THE CANONICAL FORM THE SIZE OF THE INPUT. Canonical() is JSON, which
// writes < > & as six-byte escapes; an entry allowed to hold them could make a document that is
// under the size limit as submitted several times over it as stored, and a stored document over
// the limit is one no reader can parse. None of those characters is in this grammar.
func validListEntry(v string) bool {
	if v == "" || len(v) > 256 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}

// CheckCanonicalSize refuses a canonical form over MaxDocumentBytes. Every writer calls it on the
// bytes it is about to store, so nothing is stored that a reader would then refuse to parse.
func CheckCanonicalSize(canonical []byte) error {
	if len(canonical) > MaxDocumentBytes {
		return invalid("the stored form of the document is %d bytes, over the limit of %d", len(canonical), MaxDocumentBytes)
	}
	return nil
}

func validList(name string, in []string) error {
	if len(in) > MaxListEntries {
		return invalid("%s may hold at most %d entries", name, MaxListEntries)
	}
	seen := map[string]bool{}
	for _, v := range in {
		if !validListEntry(v) {
			return invalid("%s: %q is not a valid entry (letters, digits and . _ : - only, at most 256)", name, v)
		}
		if seen[v] {
			return invalid("%s lists %q twice", name, v)
		}
		seen[v] = true
	}
	return nil
}

// ---- effective values -------------------------------------------------------------------------

// ActuationOn reports whether the document enables actuation. Absent is off.
func (d *Document) ActuationOn() bool { return d != nil && d.Actuation == ActuationEnabled }

// ClientAllowed reports whether clientID is listed. An empty id is never listed.
func (d *Document) ClientAllowed(clientID string) bool {
	return d != nil && clientID != "" && slices.Contains(d.Clients, clientID)
}

// StepUpMaxAge is the effective approver step-up age.
func (d *Document) StepUpMaxAge() time.Duration {
	if d == nil || d.Approvers == nil {
		return DefaultStepUpMaxAge
	}
	return d.Approvers.StepUpMaxAge.D(DefaultStepUpMaxAge)
}

// AcceptRecoveryCodes is the effective recovery-code rule.
func (d *Document) AcceptRecoveryCodes() bool {
	return d != nil && d.StepUp != nil && d.StepUp.AcceptRecoveryCodes
}

// ActRefreshMaxAge is the effective absolute cap on an act:* grant.
func (d *Document) ActRefreshMaxAge() time.Duration {
	if d == nil || d.Session == nil {
		return DefaultActRefreshAge
	}
	return d.Session.ActRefreshMaxAge.D(DefaultActRefreshAge)
}

// AgentFlagRequiredFor is the effective service-account rule, sorted.
func (d *Document) AgentFlagRequiredFor() []string {
	if d == nil || d.ServiceAccounts == nil {
		return nil
	}
	out := append([]string(nil), d.ServiceAccounts.AgentFlagRequiredFor...)
	sort.Strings(out)
	return out
}

func modeOr(m *Mode, def Mode) Mode {
	if m == nil {
		return def
	}
	return *m
}

func intOr(n *int, def int) int {
	if n == nil {
		return def
	}
	return *n
}

// ExtractAgentFlagRequirement reads ONLY serviceAccounts.agentFlagRequiredFor out of a stored
// document of ANY version, ignoring every other key. ok is false when raw is not a JSON object or
// that one key is malformed.
//
// 🔴 IT EXISTS SO THE SERVICE-ACCOUNT RULE NEVER LAPSES ON AN OLDER POD. A document written by a
// newer release is refused by Parse (ErrUnknownVersion), which is right for every decision about
// agents: those fail closed to "off". But the service-account rule restricts ORDINARY accounts,
// and reading it as "no requirement" would let an older pod mint raw writes the newer pods refuse.
// So the key's spelling and meaning are FROZEN across document versions: a later format may add
// keys, and must keep this one.
func ExtractAgentFlagRequirement(raw []byte) (required []string, ok bool) {
	var probe struct {
		ServiceAccounts *struct {
			AgentFlagRequiredFor []string `json:"agentFlagRequiredFor"`
		} `json:"serviceAccounts"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, false
	}
	if probe.ServiceAccounts == nil {
		return nil, true
	}
	return append([]string(nil), probe.ServiceAccounts.AgentFlagRequiredFor...), true
}

// AgentFlagRequirement is the service-account rule of a stored document, for the paths that
// enforce it (account creation, access changes, every mint). A document of this release's format
// must parse in full: a damaged one is an error, so the rule does not lapse. A document of a newer
// format has the one frozen key extracted (ExtractAgentFlagRequirement); if even that fails, it is
// an error too.
func AgentFlagRequirement(raw []byte) ([]string, error) {
	doc, err := ParseStored(raw)
	if err == nil {
		return doc.AgentFlagRequiredFor(), nil
	}
	if !errors.Is(err, ErrUnknownVersion) {
		return nil, err
	}
	req, ok := ExtractAgentFlagRequirement(raw)
	if !ok {
		return nil, fmt.Errorf("%w; and its serviceAccounts.agentFlagRequiredFor cannot be read", err)
	}
	return req, nil
}
