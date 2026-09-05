// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Exposure names WHAT CLASS OF TENANT DATA a successful call returns.
//
// 🔴 IT IS A READ VOCABULARY, NOT A WRITE ONE. This server is read-only and fronts each
// area's GraphQL under the CALLER'S OWN token — never a service token, which is the
// confused-deputy line ADR-047 draws — so no tool here can create, modify or actuate
// anything, and the MCP spec's own destructive/idempotent hints have nothing to say about
// any of them. What a risk declaration can usefully describe is therefore the shape of
// the disclosure: what someone holding a token for this server learns by calling it.
//
// The values were derived from the tools that exist rather than chosen ahead of them. A
// taxonomy invented first would have produced neat categories that no tool occupies and
// left the one distinction that actually matters — position — as a footnote.
type Exposure string

const (
	// ExposureConfiguration is the tenant's own registry: what exists and how it is set
	// up. The devices, their types, what they can measure and be told to do. It says
	// nothing about what any of them has done.
	ExposureConfiguration Exposure = "configuration"
	// ExposureOperational is the platform's lifecycle record: connectivity, alarms,
	// dispatched commands. It reveals how a fleet is BEHAVING, which is more than the
	// registry says and less than the readings do.
	ExposureOperational Exposure = "operational"
	// ExposureTelemetry is device-reported measurement values, current or historical.
	// For most fleets this is the tenant's actual product data.
	ExposureTelemetry Exposure = "telemetry"
	// ExposurePosition is where a device has been.
	//
	// 🔴 IT IS ITS OWN CLASS BECAUSE IT CAN RE-IDENTIFY A PERSON, NOT A DEVICE. A track
	// of positions describes wherever the thing carrying the device went, and a vehicle,
	// a phone or a wearable is carried by somebody. Every other exposure here is about
	// equipment; this one can be about a life, which is why the platform gives it a
	// separate OAuth scope and why the check below refuses to let it hide inside the
	// general read scope.
	ExposurePosition Exposure = "position"
)

// Scale names HOW MUCH one call can return. It is the second axis because the same class
// of data is a different disclosure at a different volume: reading one device's last
// value is not the same act as summarising a year of every reading it ever sent.
type Scale string

const (
	// ScaleAddressed means the result is bounded by tokens the caller NAMES, so a caller
	// must already know what to ask for. It cannot be used to enumerate a tenant.
	ScaleAddressed Scale = "addressed"
	// ScalePage means one clamped page of a tenant-wide set. Enumerable, a page at a
	// time, at the page ceiling this package pins.
	ScalePage Scale = "page"
	// ScaleWindow means a single call can span the whole RETAINED HISTORY when the caller
	// bounds nothing — an aggregate is not paged, so its ceiling is the retention, not
	// the page size.
	ScaleWindow Scale = "window"
)

// ToolRisk is one tool's declared risk metadata.
//
// 🔑 IT IS A CONSTRUCTOR ARGUMENT, NOT AN ENTRY IN A PARALLEL MAP, and that is the whole
// design. A registry kept beside the catalog is a list a developer can forget, and this
// repository has been bitten by exactly that shape more than once — including by a
// completeness check that a placeholder value satisfied with the compiler's blessing. Here
// the only way to add a tool is `register`, which takes the declaration; there is no
// second list to keep in step because there is no second list.
type ToolRisk struct {
	// Exposure and Scale are the two bounded axes. Both are required and both are
	// validated against the closed vocabularies above.
	Exposure Exposure
	Scale    Scale
	// Discloses is one sentence saying what a successful call reveals, written for
	// someone deciding whether to authorize an agent. Required and non-empty.
	//
	// 🔴 A REQUIRED SENTENCE IS THE PART A PLACEHOLDER CANNOT FAKE CHEAPLY. Two enum
	// fields can be filled in by pattern-matching the tool above; saying out loud what
	// the tool gives away takes the thought the declaration exists to force.
	Discloses string
}

// validate rejects a declaration that does not describe anything.
func (r ToolRisk) validate(tool string) error {
	switch r.Exposure {
	case ExposureConfiguration, ExposureOperational, ExposureTelemetry, ExposurePosition:
	default:
		return fmt.Errorf("tool %q declares exposure %q, which is not one of the read-exposure "+
			"classes", tool, r.Exposure)
	}
	switch r.Scale {
	case ScaleAddressed, ScalePage, ScaleWindow:
	default:
		return fmt.Errorf("tool %q declares scale %q, which is not one of the result-scale "+
			"classes", tool, r.Scale)
	}
	if r.Discloses == "" {
		return fmt.Errorf("tool %q declares no disclosure sentence; say what a successful call "+
			"reveals, in one sentence, for whoever is deciding whether to authorize an agent", tool)
	}
	return nil
}

// riskMetaKey is the namespaced `_meta` key the declaration is published under, so a
// client or an operator reading the catalog sees the same thing the ratchet checks.
//
// 🔑 PUBLISHING IT IS WHAT KEEPS IT HONEST. A declaration that only a test reads drifts
// into whatever makes the test pass; one that appears in the tool listing is a statement
// to whoever is deciding what to authorize, and is corrected when it is wrong.
const riskMetaKey = "io.devicechain/risk"

// Catalog is the set of risk declarations made AT REGISTRATION. It is filled by register
// and by nothing else, so it cannot describe a tool that was not registered and cannot
// omit one that was — provided every tool goes through register, which is the one
// property the ratchet has to check and the negative control proves it does check.
type Catalog struct {
	risks map[string]ToolRisk
	// decls is the LISTING each tool was declared with — the description, annotations
	// and risk metadata as register stamped them, before the SDK filled in schemas.
	// It is what closes the last gap in the name-based ratchet; see AlteredTools.
	decls map[string]*mcp.Tool
}

// NewCatalog builds an empty catalog.
func NewCatalog() *Catalog {
	return &Catalog{risks: map[string]ToolRisk{}, decls: map[string]*mcp.Tool{}}
}

// Declared returns the listing a tool was registered with.
func (c *Catalog) Declared(name string) (*mcp.Tool, bool) {
	d, ok := c.decls[name]
	return d, ok
}

// Risk returns a tool's declaration.
func (c *Catalog) Risk(name string) (ToolRisk, bool) {
	r, ok := c.risks[name]
	return r, ok
}

// Names returns every declared tool name, sorted.
func (c *Catalog) Names() []string {
	out := make([]string, 0, len(c.risks))
	for name := range c.risks {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// register adds a tool to the server together with its risk declaration.
//
// 🔴 IT PANICS ON A DECLARATION THAT DOES NOT DESCRIBE ANYTHING, and the loudness is the
// point. This runs during server construction, at process start, so the failure mode is a
// pod that will not start and a message naming the tool — not a server that answers with
// a tool nobody classified. There is no error to return here that a caller would not
// simply have to panic on anyway: a catalog is either complete or the server should not
// be serving it.
//
// The declaration is also stamped onto the tool itself: the read-only annotation (true of
// every tool here, and worth saying to a client rather than leaving implicit) and the
// declaration under a namespaced `_meta` key.
func register[In, Out any](s *mcp.Server, c *Catalog, tool *mcp.Tool, risk ToolRisk,
	h mcp.ToolHandlerFor[In, Out]) {
	if err := risk.validate(tool.Name); err != nil {
		panic("mcp: " + err.Error())
	}
	if _, duplicate := c.risks[tool.Name]; duplicate {
		panic(fmt.Sprintf("mcp: tool %q is registered twice; the second declaration would "+
			"silently replace the first", tool.Name))
	}
	c.risks[tool.Name] = risk

	// Every tool on this server is a read, and both hints are true of all of them: nothing
	// here modifies anything, and calling one twice returns the same answer rather than
	// doing anything twice.
	//
	// 🔴 THIS LINE IS A STATEMENT TO CLIENTS, NOT A CHECK. It used to claim that "the day
	// a tool that is NOT a read is proposed, the person adding it has to change this
	// line" — which nothing makes true. A write handler passed to register is stamped
	// ReadOnlyHint:true exactly like every other, validate() only checks that the two
	// enums are members of their vocabularies, and no test could tell the difference: an
	// MCP tool annotation is advisory metadata for the client, not an authorization
	// decision, so the server would happily serve a write behind it. The one thing that
	// is enforced here is upstream of the stamp — Exposure is a closed READ vocabulary
	// (configuration / operational / telemetry / position) with no write class in it, so
	// a write tool has nothing honest to declare and validate() refuses whatever it
	// invents.
	//
	// 🔑 WHAT ACTUALLY MAKES THIS SERVER READ-ONLY LIVES IN core/auth (scopeAllowances
	// and IntersectAuthorities), AND IT IS STRONGER THAN THIS COMMENT EVER CLAIMED. The
	// `read-only` scope carries an authority CEILING — device/event/state/command/alarm
	// and dashboard reads — and every OAuth token is minted through it by intersecting
	// what the subject holds against that ceiling. A superuser holding the `*`
	// super-authority is capped by the same intersection: `*` is skipped rather than
	// expanded, so the token that reaches this server carries reads and nothing else.
	// The middleware in New requires that scope on every request. So a write tool added
	// here would not become dangerous by being mis-annotated; it would be REFUSED
	// downstream, by the caller's own token, because no token this server accepts carries
	// a write authority to run it with. Changing that takes a new scope with a new
	// ceiling and a new consent string, which is the conversation this comment wanted.
	tool.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	if tool.Meta == nil {
		tool.Meta = mcp.Meta{}
	}
	tool.Meta[riskMetaKey] = map[string]any{
		"exposure":  string(risk.Exposure),
		"scale":     string(risk.Scale),
		"discloses": risk.Discloses,
	}

	// Keep the listing as declared, so AlteredTools can compare it against what the
	// server ends up serving. Schemas are deliberately not kept: mcp.AddTool derives
	// them from the handler's types, so a declared copy would differ from the served
	// one for a reason that is not a defect.
	meta := mcp.Meta{}
	for k, v := range tool.Meta {
		meta[k] = v
	}
	annotations := *tool.Annotations
	c.decls[tool.Name] = &mcp.Tool{
		Name:        tool.Name,
		Title:       tool.Title,
		Description: tool.Description,
		Annotations: &annotations,
		Meta:        meta,
	}

	mcp.AddTool(s, tool, h)
}

// UndeclaredTools reports every tool the server exposes that carries no declaration in c.
//
// 🔴 IT IS THE RATCHET, AND IT IS WRITTEN AS A FUNCTION RATHER THAN INSIDE A TEST SO THAT
// THE NEGATIVE CONTROL CAN RUN IT. A completeness check that has only ever been run
// against a complete catalog has not been shown to fail, and a check that has not been
// shown to fail is worth nothing; risk_test.go registers a tool the long way round —
// straight through mcp.AddTool, bypassing register — and asserts this reports it.
//
// It takes the names the SERVER actually lists rather than reading registerTools' source,
// because the catalog a client sees is the only thing that matters.
func UndeclaredTools(catalogNames []string, c *Catalog) []string {
	missing := []string{}
	for _, name := range catalogNames {
		if _, declared := c.Risk(name); !declared {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// AlteredTools reports every served tool whose listing is not the one register declared
// for that name.
//
// 🔴 IT CLOSES THE HOLE THE NAME-BASED RATCHET STRUCTURALLY CANNOT SEE. Server.AddTool
// REPLACES a tool of the same name rather than refusing it ("add replaces existing
// tools", in the SDK's own words), so a bare mcp.AddTool reusing a name this package
// already declared swaps the HANDLER while leaving everything the other checks look at
// identical: register's duplicate panic never runs, because register was not called;
// the catalog still holds one entry per name; and the served name list is unchanged, so
// UndeclaredTools has nothing to report. The one place a swap shows is the listing
// itself — a replacement carries its own description, its own annotations and no risk
// metadata — so that is what this compares, field for field, against the declaration.
//
// 🔑 ITS RESIDUAL, STATED PRECISELY BECAUSE IT IS LARGER THAN IT FIRST LOOKS.
// listingFingerprint compares the title, description, annotations and `_meta` — and
// NOTHING ELSE that reaches the wire. It deliberately excludes InputSchema and
// OutputSchema (mcp.AddTool derives those from the handler's own types, so a declared
// copy would differ for a reason that is not a defect), and it does not reach Icons or
// any field a future SDK release adds. So an invisible replacement does not have to be
// a careful forgery: it has to copy four public strings, all of which are readable in
// this file.
//
// Worse, the most LIKELY replacement shape is invisible by construction rather than by
// difficulty. A handler taking a different input type is exactly what someone
// substituting behaviour would write, and its schema is the field this comparison
// cannot use — so the served tool advertises different arguments under a declared
// name, and this reports nothing.
//
// What bounds that is not here and could not be: no token this server accepts carries a
// write authority, so a swapped-in handler has nothing to act with (see register). This
// function closes the ACCIDENT — someone reaching for mcp.AddTool because it is the API
// the SDK documents — and says plainly that it does not close the substitution.
//
// It takes the SERVED listings, not names, for the same reason UndeclaredTools takes
// served names: the catalog a client is offered is the only one that matters.
func AlteredTools(served []*mcp.Tool, c *Catalog) []string {
	altered := []string{}
	for _, tool := range served {
		declared, ok := c.Declared(tool.Name)
		if !ok {
			continue // an undeclared tool is UndeclaredTools' report to make
		}
		if listingFingerprint(tool) != listingFingerprint(declared) {
			altered = append(altered, tool.Name)
		}
	}
	sort.Strings(altered)
	return altered
}

// listingFingerprint reduces a tool to the parts a client is shown and register
// controls. Schemas are excluded deliberately (see register); everything else that
// reaches the wire is compared. JSON is the comparison medium because the served
// listing has been through it, so a declared map[string]any and a decoded one compare
// equal instead of differing by their Go types.
func listingFingerprint(t *mcp.Tool) string {
	b, err := json.Marshal(struct {
		Title       string               `json:"title"`
		Description string               `json:"description"`
		Annotations *mcp.ToolAnnotations `json:"annotations"`
		Meta        mcp.Meta             `json:"_meta"`
	}{t.Title, t.Description, t.Annotations, t.Meta})
	if err != nil {
		// Unreachable for these field types, and a panic would take the process down
		// for a comparison. An error string cannot equal a marshalled listing, so the
		// tool is reported as altered — which fails closed.
		return "unmarshalable: " + err.Error()
	}
	return string(b)
}
