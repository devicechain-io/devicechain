// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

// Diagnostic is one node-anchored compile message the console surfaces on the canvas. A
// graph-level problem (a cycle, a dangling edge) carries an empty NodeID.
type Diagnostic struct {
	NodeID   string
	Severity string // "error" | "warning"
	Message  string
}

// LoweredRule is one condition node lowered to a compiled, validated DETECT rule: the
// assembled rules.Rule, its canonical JSON Definition (what device-management stores and
// freezes into the snapshot — it decodes to the same rules.Rule as the equivalent form rule
// and re-marshals to identical canonical bytes, §3.2), and the compiled form (for its cost
// estimate). One canvas condition node ⇒ one LoweredRule (§3.1).
type LoweredRule struct {
	NodeID     string
	Rule       rules.Rule
	Definition string
	Compiled   *rules.CompiledRule
}

// Result is a successful compile: the lowered rules (one per condition node) and any
// non-fatal warnings.
type Result struct {
	Rules       []LoweredRule
	Diagnostics []Diagnostic
}

// CompileError carries the node-anchored diagnostics of a failed compile so the resolver can
// surface each on its node. It fails closed: any structural or semantic defect rejects the
// whole graph (no partial "best-effort" lowering), matching the DETECT compiler's posture.
type CompileError struct {
	Diagnostics []Diagnostic
}

func (e *CompileError) Error() string {
	if len(e.Diagnostics) == 0 {
		return "canvas compile failed"
	}
	d := e.Diagnostics[0]
	if d.NodeID != "" {
		return fmt.Sprintf("node %s: %s", d.NodeID, d.Message)
	}
	return d.Message
}

// errorf builds a single-diagnostic CompileError anchored to a node (empty nodeID for a
// graph-level error).
func errorf(nodeID, format string, args ...interface{}) *CompileError {
	return &CompileError{Diagnostics: []Diagnostic{{NodeID: nodeID, Severity: "error", Message: fmt.Sprintf(format, args...)}}}
}

// typedEdge is an edge resolved against the port catalog: its endpoints and the (single)
// port type it carries.
type typedEdge struct {
	fromNode, fromPort string
	toNode, toPort     string
	ptype              PortType
}

// Compile lowers a validated CanvasDefinition to a set of compiled DETECT rules — the
// server-authoritative graph→schema pass (§3). It partitions the graph at its condition
// nodes (the only stream→signal pivots), synthesizes one rules.Rule per condition from its
// stream-upstream source and its signal-downstream REACT actions, and runs each through the
// SAME rules.Decode→Compile→cost-gate path the form builder uses — so a canvas rule and a
// form rule expressing the same logic compile to byte-identical schema. It fails closed:
// every §3.4 rejected construct (unknown node, cross-typed or dangling edge, cycle, >1
// source per condition, cross-window join) returns a node-anchored CompileError.
//
// profileToken scopes the canvas: every Source node must be profile-scoped to it (the GA
// profile-homed cut, §4.1). limits are the per-tenant compile ceilings, resolved by the
// caller (never uncapped — ADR-023).
func Compile(def CanvasDefinition, profileToken string, limits rules.Limits) (*Result, error) {
	if def.SchemaVersion != SchemaVersion {
		return nil, errorf("", "unsupported canvas schemaVersion %d (this build understands %d)", def.SchemaVersion, SchemaVersion)
	}
	if profileToken == "" {
		return nil, errorf("", "a profile token is required to compile a canvas")
	}
	// Floor the limits once, up front, to the EFFECTIVE per-tenant ceilings — so the up-front branch
	// guard gate below cost-gates against the real ceiling (a zero PredicateCostCeiling would reject
	// every guard) rather than re-deriving the floor. rules.Compile floors again internally (idempotent).
	limits = limits.WithDefaults()

	// Index nodes; reject duplicate ids and unknown types up front (fail closed).
	byID := make(map[string]Node, len(def.Nodes))
	for _, n := range def.Nodes {
		if n.ID == "" {
			return nil, errorf("", "a node has an empty id")
		}
		if _, dup := byID[n.ID]; dup {
			return nil, errorf(n.ID, "duplicate node id %q", n.ID)
		}
		if _, known := catalog[n.Type]; !known {
			return nil, errorf(n.ID, "unknown node type %q", n.Type)
		}
		byID[n.ID] = n
	}
	if len(byID) == 0 {
		return nil, errorf("", "the canvas has no nodes")
	}

	// Validate EVERY source node's config up front, regardless of connectivity — an unwired
	// source still rides along in the stored AuthoringGraph sidecar, so a poisoned one (unknown
	// field, wrong profile, derived-subject scope) must be caught here, not left to surface only
	// when someone later wires it. This is the counterpart to rejecting a dangling action /
	// source-less condition, and it makes the "every Source node is profile-scoped to the
	// canvas" contract (the doc + schema promise) actually hold.
	// A branch node's guard is likewise cost-gated up front regardless of connectivity: an unwired
	// branch rides along in the AuthoringGraph sidecar, and its guard is folded onto an action's Guard
	// (and re-gated by rules.Compile) only once wired — so a poisoned/over-cost guard on an unwired
	// branch must be caught here, not left to surface when someone later wires it.
	for _, n := range def.Nodes {
		switch n.Type {
		case NodeSource:
			if cerr := validateSource(n, profileToken); cerr != nil {
				return nil, cerr
			}
		case NodeBranch:
			if cerr := validateBranch(n, limits); cerr != nil {
				return nil, cerr
			}
		}
	}

	edges, err := typeCheckEdges(def.Edges, byID)
	if err != nil {
		return nil, err
	}
	if err := detectCycle(def.Nodes, edges); err != nil {
		return nil, err
	}

	// Partition: find condition nodes (the stream→signal pivots). Each roots exactly one rule.
	var conditionIDs []string
	for _, n := range def.Nodes {
		if n.Type.isCondition() {
			conditionIDs = append(conditionIDs, n.ID)
		}
	}
	if len(conditionIDs) == 0 {
		return nil, errorf("", "the canvas has no condition node — a rule needs exactly one detection")
	}

	// Global REACT signal-path resolution: bind every action to its owning condition and the composed
	// branch guard on the path between them. An action with no upstream is a stateful node with nothing
	// to react to; an action/branch fed by two signals is a cross-window join (§3.3) — both rejected.
	chains, err := resolveReactChains(def.Nodes, byID, edges)
	if err != nil {
		return nil, err
	}

	var (
		lowered []LoweredRule
		diags   []Diagnostic
	)
	for _, cid := range conditionIDs {
		lr, cerr := lowerCondition(byID[cid], byID, edges, chains, limits)
		if cerr != nil {
			diags = append(diags, cerr.Diagnostics...)
			continue
		}
		lowered = append(lowered, *lr)
	}
	if len(diags) > 0 {
		return nil, &CompileError{Diagnostics: diags}
	}
	return &Result{Rules: lowered}, nil
}

// typeCheckEdges resolves each edge endpoint against the port catalog and requires the two
// ports to carry the same type — the one rule that makes the DETECT↔REACT partition provable
// (§1). A cross-typed edge (e.g. a signal fed back into a stream port) is rejected here.
func typeCheckEdges(edges []Edge, byID map[string]Node) ([]typedEdge, error) {
	out := make([]typedEdge, 0, len(edges))
	seen := make(map[string]struct{}, len(edges))
	for _, e := range edges {
		fn, fp, err := parseEndpoint(e.From)
		if err != nil {
			return nil, errorf("", "edge from %q: %v", e.From, err)
		}
		tn, tp, err := parseEndpoint(e.To)
		if err != nil {
			return nil, errorf("", "edge to %q: %v", e.To, err)
		}
		ftype, err := portType(byID, fn, fp, true)
		if err != nil {
			// Anchor to the node only if it exists; an edge to a phantom node is a graph-level
			// problem (the console has no node to pin the diagnostic to).
			return nil, errorf(anchorFor(byID, fn), "edge from %q: %v", e.From, err)
		}
		ttype, err := portType(byID, tn, tp, false)
		if err != nil {
			return nil, errorf(anchorFor(byID, tn), "edge to %q: %v", e.To, err)
		}
		if ftype != ttype {
			return nil, errorf(tn, "edge %s→%s connects a %s output to a %s input (ports must carry the same type)", e.From, e.To, ftype, ttype)
		}
		key := e.From + "→" + e.To
		if _, dup := seen[key]; dup {
			return nil, errorf(tn, "duplicate edge %s→%s", e.From, e.To)
		}
		seen[key] = struct{}{}
		out = append(out, typedEdge{fromNode: fn, fromPort: fp, toNode: tn, toPort: tp, ptype: ftype})
	}
	return out, nil
}

// anchorFor returns nodeID if it names a real node, else "" (a graph-level anchor) — so a
// diagnostic never points the console at a node id it cannot find.
func anchorFor(byID map[string]Node, nodeID string) string {
	if _, ok := byID[nodeID]; ok {
		return nodeID
	}
	return ""
}

// parseEndpoint splits a "nodeId:port" endpoint on its last colon (a node id may contain a
// colon; a port name never does), rejecting a malformed endpoint.
func parseEndpoint(s string) (nodeID, port string, err error) {
	i := strings.LastIndex(s, ":")
	if i <= 0 || i == len(s)-1 {
		return "", "", fmt.Errorf("must be \"nodeId:port\"")
	}
	return s[:i], s[i+1:], nil
}

// portType resolves a port's type on a node, requiring the node to exist and the port to be
// a declared input (want output=false) or output (want output=true) of that node's type.
func portType(byID map[string]Node, nodeID, port string, output bool) (PortType, error) {
	n, ok := byID[nodeID]
	if !ok {
		return "", fmt.Errorf("no node %q", nodeID)
	}
	spec := catalog[n.Type].ports
	m := spec.in
	dir := "input"
	if output {
		m = spec.out
		dir = "output"
	}
	pt, ok := m[port]
	if !ok {
		return "", fmt.Errorf("node type %q has no %s port %q", n.Type, dir, port)
	}
	return pt, nil
}

// detectCycle rejects any directed cycle over the node graph (edges point from→to). The
// canvas is a DAG-with-branches; a cycle has no lowering (§3.4).
func detectCycle(nodes []Node, edges []typedEdge) error {
	adj := make(map[string][]string, len(nodes))
	for _, e := range edges {
		adj[e.fromNode] = append(adj[e.fromNode], e.toNode)
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(nodes))
	var visit func(string) bool
	visit = func(u string) bool {
		color[u] = gray
		for _, v := range adj[u] {
			switch color[v] {
			case gray:
				return true
			case white:
				if visit(v) {
					return true
				}
			}
		}
		color[u] = black
		return false
	}
	for _, n := range nodes {
		if color[n.ID] == white {
			if visit(n.ID) {
				return errorf(n.ID, "the canvas has a cycle — automation graphs must be acyclic")
			}
		}
	}
	return nil
}

// reactBinding is where one action node lowers: the condition whose signal ultimately feeds it, and
// the composed guard — the conjunction of every branch predicate on the signal path from that
// condition to the action (empty when the action wires straight to the condition, the pre-9c case).
type reactBinding struct {
	conditionID string
	guard       string
}

// resolveReactChains walks the signal-downstream subgraph to bind every action node to its owning
// condition and its composed branch guard (slice 9c). It enforces that every branch and action node
// has EXACTLY ONE incoming signal edge: zero is a dangling node with nothing to react to; more than
// one is a cross-signal join (two conditions/branches converging on one node), which has no
// single-guard lowering and is the cross-window-correlation construct rejected at compile (§3.3 /
// §3.4). A condition may fan its signal out to many branches/actions. Because each branch/action has
// a unique signal predecessor, an action's walk back is a single chain terminating at exactly one
// condition — so the old "fed by more than one condition" check is subsumed by the one-incoming rule.
func resolveReactChains(nodes []Node, byID map[string]Node, edges []typedEdge) (map[string]reactBinding, error) {
	// The signal predecessors of each branch/action node. Signal edges only ever target a branch or
	// an action (a condition has no signal INPUT port, a source none at all — type-checked already).
	incoming := make(map[string][]string) // toNode → []fromNode
	for _, e := range edges {
		if e.ptype != PortSignal {
			continue
		}
		if t := byID[e.toNode].Type; t == NodeBranch || t == NodeAction {
			incoming[e.toNode] = append(incoming[e.toNode], e.fromNode)
		}
	}
	// Enforce exactly-one-incoming for every branch and action, in node declaration order so the
	// first-reported diagnostic is deterministic run-to-run.
	pred := make(map[string]string, len(incoming))
	for _, n := range nodes {
		if n.Type != NodeBranch && n.Type != NodeAction {
			continue
		}
		fs := incoming[n.ID]
		if len(fs) == 0 {
			if n.Type == NodeAction {
				return nil, errorf(n.ID, "this action has no detection feeding it — wire a condition's signal into it")
			}
			return nil, errorf(n.ID, "this branch has no input — wire a condition's or branch's signal into it")
		}
		if len(fs) > 1 {
			return nil, errorf(n.ID, "this %s is fed by more than one signal — combine conditions within a single windowed rule, or split into two rules; cross-window correlation is post-GA (ADR-052)", nodeKind(n.Type))
		}
		pred[n.ID] = fs[0]
	}

	out := make(map[string]reactBinding)
	for _, n := range nodes {
		if n.Type != NodeAction {
			continue
		}
		var guards []string // nearest-action-first; composeGuards reverses to condition→action order
		cur := n.ID
		// Bound the walk by the node count — a cycle was rejected upstream (detectCycle), so this is a
		// belt-and-braces guard against a walk that never reaches a condition.
		for steps := 0; steps <= len(nodes); steps++ {
			f, ok := pred[cur]
			if !ok {
				// cur is a branch/action with no recorded predecessor — unreachable (the loop above
				// errored on zero-incoming), but fail closed rather than bind to an empty condition.
				return nil, errorf(n.ID, "this action's signal path does not terminate at a condition")
			}
			ft := byID[f].Type
			switch {
			case ft.isCondition():
				out[n.ID] = reactBinding{conditionID: f, guard: composeGuards(guards)}
				cur = "" // sentinel: done
			case ft.isBranch():
				when, berr := branchWhen(byID[f])
				if berr != nil {
					return nil, errorf(f, "%v", berr)
				}
				guards = append(guards, when)
				cur = f
			default:
				// A signal edge can only originate at a condition or a branch (type-checked), so this
				// is unreachable; fail closed against a future signal-emitting node type.
				return nil, errorf(n.ID, "this action's signal path passes through a %q node that cannot emit a signal", ft)
			}
			if cur == "" {
				break
			}
		}
		if _, done := out[n.ID]; !done {
			return nil, errorf(n.ID, "this action's signal path does not terminate at a condition")
		}
	}
	return out, nil
}

// nodeKind names a node type for a diagnostic ("branch"/"action"); it is the human word, not the
// wire token, and only used where those two categories appear.
func nodeKind(t NodeType) string {
	if t == NodeBranch {
		return "branch"
	}
	return "action"
}

// branchWhen decodes a branch node's config and returns its (validated non-empty) guard predicate.
func branchWhen(n Node) (string, error) {
	var c branchConfig
	if err := decodeConfig(n.Config, &c); err != nil {
		return "", fmt.Errorf("branch config: %v", err)
	}
	return c.When, nil
}

// composeGuards conjoins the branch predicates on a signal path into one guard CEL string, in
// condition→action order (the walk collects them action→condition, so this reverses). One predicate
// is emitted verbatim; several are parenthesized and &&-joined. The result is what a form builder
// setting the same Action.Guard would emit, keeping the byte-identity contract (§3.2) intact for
// guarded rules once the form grows a guard editor.
func composeGuards(guards []string) string {
	if len(guards) == 0 {
		return ""
	}
	rev := make([]string, len(guards))
	for i, g := range guards {
		rev[len(guards)-1-i] = g
	}
	if len(rev) == 1 {
		return rev[0]
	}
	parts := make([]string, len(rev))
	for i, g := range rev {
		parts[i] = "(" + g + ")"
	}
	return strings.Join(parts, " && ")
}

// validateBranch cost-gates a branch node's guard up front (regardless of connectivity), rejecting an
// empty predicate (a branch that gates nothing) and a parse/type/over-cost guard — the same
// fail-closed posture validateSource takes for an unwired source. limits must already be floored to
// the effective ceiling (Compile floors before this runs).
func validateBranch(n Node, limits rules.Limits) *CompileError {
	var c branchConfig
	if err := decodeConfig(n.Config, &c); err != nil {
		return errorf(n.ID, "branch config: %v", err)
	}
	if strings.TrimSpace(c.When) == "" {
		return errorf(n.ID, "a branch needs a condition — its \"when\" predicate is empty")
	}
	if _, err := rules.CompileGuard(c.When, limits.PredicateCostCeiling); err != nil {
		return errorf(n.ID, "branch condition: %v", err)
	}
	return nil
}

// lowerCondition synthesizes one compiled rule from a condition node: its single upstream
// source (validated against the profile scope), its own predicate/temporal config, and the
// REACT actions its signal fans out to. It runs the assembled rule through the exact
// rules.Compile path the form builder uses.
func lowerCondition(c Node, byID map[string]Node, edges []typedEdge, chains map[string]reactBinding, limits rules.Limits) (*LoweredRule, *CompileError) {
	// Exactly one source must feed the condition's stream input (§3.4: >1 source rejected;
	// 0 sources means the detection has no telemetry to run against).
	var sources []string
	for _, e := range edges {
		if e.toNode == c.ID && e.ptype == PortStream {
			sources = append(sources, e.fromNode)
		}
	}
	if len(sources) == 0 {
		return nil, errorf(c.ID, "this condition has no source — wire a source's stream into it")
	}
	if len(sources) > 1 {
		return nil, errorf(c.ID, "this condition is fed by more than one source — a rule reads from a single source")
	}
	// The source's config/scope was already validated up front (Compile validates every source
	// node regardless of connectivity), so by here sources[0] is a known-good profile source.

	rule, err := buildRule(c)
	if err != nil {
		return nil, errorf(c.ID, "%v", err)
	}

	// Attach the REACT actions this condition's signal fans out to, ordered by the action
	// node's id so the chain is deterministic (byte-identity does not depend on map order). Each
	// action carries the composed guard of the branch path between this condition and it (empty when
	// wired straight through); rules.Compile cost-gates the guard as part of validateReact.
	var actionIDs []string
	for aid, b := range chains {
		if b.conditionID == c.ID {
			actionIDs = append(actionIDs, aid)
		}
	}
	sort.Strings(actionIDs)
	for _, aid := range actionIDs {
		a, aerr := buildAction(byID[aid])
		if aerr != nil {
			return nil, errorf(aid, "%v", aerr)
		}
		a.Guard = chains[aid].guard
		rule.Actions = append(rule.Actions, a)
	}

	// Compile against a copy carrying the node id as a placeholder — Compile requires a
	// non-empty id and uses it only to anchor its error messages. The STORED rule keeps id
	// empty: the definition device-management persists carries no transient id (the runtime
	// id is composed at publish), exactly as the form builder emits it — so the two decode to
	// the same rules.Rule.
	toCompile := rule
	toCompile.ID = c.ID
	compiled, err := rules.Compile(toCompile, limits)
	if err != nil {
		return nil, errorf(c.ID, "%v", err)
	}
	// Canonicalize the id-free rule to the definition device-management stores. Marshalling a
	// rules.Rule is deterministic (struct field order + Duration's canonical string form), so
	// the canvas rule and the equivalent form rule re-marshal to identical bytes (§3.2).
	defBytes, err := json.Marshal(rule)
	if err != nil {
		return nil, errorf(c.ID, "marshal compiled rule: %v", err)
	}
	return &LoweredRule{NodeID: c.ID, Rule: rule, Definition: string(defBytes), Compiled: compiled}, nil
}

// validateSource requires a Source node to be profile-scoped to the canvas's profile (the GA
// profile-homed cut, §4.1). A derived-subject source (§4.2) is a deferred fast-follow and is
// rejected for now.
func validateSource(n Node, profileToken string) *CompileError {
	var c sourceConfig
	if err := decodeConfig(n.Config, &c); err != nil {
		return errorf(n.ID, "source config: %v", err)
	}
	switch c.Scope.Kind {
	case "profile":
		if c.Scope.ProfileToken != profileToken {
			return errorf(n.ID, "source is scoped to profile %q but the canvas is being compiled against %q", c.Scope.ProfileToken, profileToken)
		}
		return nil
	case "derivedSubject":
		return errorf(n.ID, "a derived-subject source is not supported in the GA profile-homed canvas (ADR-053 §4.2, deferred)")
	case "":
		return errorf(n.ID, "source requires a scope kind")
	default:
		return errorf(n.ID, "unknown source scope kind %q", c.Scope.Kind)
	}
}
