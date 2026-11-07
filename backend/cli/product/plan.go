// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"
)

func sortStrings(s []string) { sort.Strings(s) }

// Action is what a plan does to one resource.
type Action string

const (
	ActionCreate  Action = "create"
	ActionUpdate  Action = "update"
	ActionDelete  Action = "delete"
	ActionPublish Action = "publish"
)

// Change is one line of a plan.
type Change struct {
	Kind   Kind
	Token  string
	Parent string // the profile a definition belongs to
	Action Action
	// Fields names what an update changes.
	Fields []string
	Note   string
}

func (c Change) String() string {
	sym := map[Action]string{ActionCreate: "+", ActionUpdate: "~", ActionDelete: "-", ActionPublish: "^"}[c.Action]
	name := c.Token
	if c.Parent != "" {
		name = c.Parent + "/" + c.Token
	}
	s := fmt.Sprintf("%s %-16s %s  %s", sym, c.Kind, name, c.Action)
	if len(c.Fields) > 0 {
		s += ": " + strings.Join(c.Fields, ", ")
	}
	if c.Note != "" {
		s += " (" + c.Note + ")"
	}
	return s
}

// Options tune planning and applying.
type Options struct {
	// Getenv resolves env: credential handles. Defaults to os.Getenv.
	Getenv func(string) string
	// RotateSecrets re-sends every connector credential whose handle resolves, even to a
	// connector that already holds one. The server never reveals a stored credential, so
	// without this a credential is only ever sent to a connector that has none.
	RotateSecrets bool
	// NoPublish converges drafts and stops there.
	NoPublish bool
	// Label is the label on the versions apply publishes.
	Label string
	// Republish publishes every profile, connector and dashboard in the bundle even when
	// nothing seems to have changed. It recovers a publish that was skipped (--no-publish) or
	// failed after the draft was written, for the parts of a profile a published version
	// cannot be read back for (its rules and position declaration).
	Republish bool
}

func (o Options) getenv(k string) string {
	if o.Getenv != nil {
		return o.Getenv(k)
	}
	return os.Getenv(k)
}

func (o Options) label() string {
	if o.Label != "" {
		return o.Label
	}
	return "dcctl product apply"
}

type step struct {
	desc string
	run  func(ctx context.Context) error
}

// Plan is the set of changes that would make the tenant hold a bundle.
type Plan struct {
	Changes []Change
	// Blockers are reasons apply cannot proceed; a plan with any is refused by apply.
	Blockers []string
	// Notes are facts worth reviewing that do not change what happens.
	Notes []string
	steps []step
}

// Empty reports whether the tenant already holds the bundle.
func (p *Plan) Empty() bool { return len(p.Changes) == 0 }

// Fprint writes the plan for a reader.
func (p *Plan) Fprint(w io.Writer) {
	for _, c := range p.Changes {
		fmt.Fprintln(w, c)
	}
	for _, n := range p.Notes {
		fmt.Fprintln(w, "note:", n)
	}
	for _, b := range p.Blockers {
		fmt.Fprintln(w, "blocked:", b)
	}
	if p.Empty() && len(p.Blockers) == 0 {
		fmt.Fprintln(w, "No changes: the tenant already holds this product.")
		return
	}
	if !p.Empty() {
		fmt.Fprintf(w, "%d change(s).\n", len(p.Changes))
	}
}

// Apply runs the plan's steps in order. A failure stops it and says how far it got; apply is
// idempotent, so running it again converges from wherever it stopped.
func (p *Plan) Apply(ctx context.Context, w io.Writer) error {
	if len(p.Blockers) > 0 {
		return fmt.Errorf("refusing to apply: %s", strings.Join(p.Blockers, "; "))
	}
	for i, s := range p.steps {
		if err := s.run(ctx); err != nil {
			return fmt.Errorf("%s: %w (%d of %d step(s) done; apply is idempotent, so fix the cause and run it again)", s.desc, err, i, len(p.steps))
		}
		fmt.Fprintln(w, "done:", s.desc)
	}
	return nil
}

// ---- documents ---------------------------------------------------------------------------

const (
	mCreateDeviceType = `mutation ProductCreateDeviceType($request:DeviceTypeCreateRequest!){createDeviceType(request:$request){token}}`
	mUpdateDeviceType = `mutation ProductUpdateDeviceType($token:String!,$request:DeviceTypeUpdateRequest!){updateDeviceType(token:$token,request:$request){token}}`

	mCreateProfile = `mutation ProductCreateProfile($request:DeviceProfileCreateRequest!){createDeviceProfile(request:$request){token}}`
	mUpdateProfile = `mutation ProductUpdateProfile($token:String!,$request:DeviceProfileUpdateRequest!){updateDeviceProfile(token:$token,request:$request){token}}`
	mSetConfigDecl = `mutation ProductSetConfigurationDeclaration($token:String!,$keys:[ConfigurationKeyInput!]!){` +
		`setDeviceProfileConfigurationDeclaration(token:$token,keys:$keys){token}}`
	mSetDecoder     = `mutation ProductSetDecoder($profileToken:String!,$document:String!){setDeviceProfileDecoder(profileToken:$profileToken,document:$document){token}}`
	mClearDecoder   = `mutation ProductClearDecoder($profileToken:String!){clearDeviceProfileDecoder(profileToken:$profileToken){token}}`
	mPublishProfile = `mutation ProductPublishProfile($token:String!,$label:String){publishDeviceProfile(token:$token,label:$label){version}}`

	mCreateMetric = `mutation ProductCreateMetric($request:MetricDefinitionCreateRequest!){createMetricDefinition(request:$request){token}}`
	mUpdateMetric = `mutation ProductUpdateMetric($token:String!,$request:MetricDefinitionUpdateRequest!){updateMetricDefinition(token:$token,request:$request){token}}`
	mDeleteMetric = `mutation ProductDeleteMetric($token:String!){deleteMetricDefinition(token:$token)}`

	mCreateCommand = `mutation ProductCreateCommand($request:CommandDefinitionCreateRequest!){createCommandDefinition(request:$request){token}}`
	mUpdateCommand = `mutation ProductUpdateCommand($token:String!,$request:CommandDefinitionUpdateRequest!){updateCommandDefinition(token:$token,request:$request){token}}`
	mDeleteCommand = `mutation ProductDeleteCommand($token:String!){deleteCommandDefinition(token:$token)}`

	mCreateRule = `mutation ProductCreateRule($request:DetectionRuleCreateRequest!){createDetectionRule(request:$request){token}}`
	mUpdateRule = `mutation ProductUpdateRule($token:String!,$request:DetectionRuleUpdateRequest!){updateDetectionRule(token:$token,request:$request){token}}`
	mDeleteRule = `mutation ProductDeleteRule($token:String!){deleteDetectionRule(token:$token)}`

	// The bearer token the server generates for a new integration is deliberately NOT
	// selected: apply neither receives, prints nor stores it.
	mCreateIntegration = `mutation ProductCreateIntegration($request:IntegrationCreateRequest!){createIntegration(request:$request){integration{token}}}`
	mUpdateIntegration = `mutation ProductUpdateIntegration($token:String!,$request:IntegrationUpdateRequest!){updateIntegration(token:$token,request:$request){token}}`

	mCreateConnector = `mutation ProductCreateConnector($request:ConnectorCreateRequest!){createConnector(request:$request){token updatedAt}}`
	mUpdateConnector = `mutation ProductUpdateConnector($token:String!,$request:ConnectorUpdateRequest!,$expectedUpdatedAt:String){` +
		`updateConnector(token:$token,request:$request,expectedUpdatedAt:$expectedUpdatedAt){token updatedAt}}`
	mPublishConnector = `mutation ProductPublishConnector($token:String!,$label:String,$expectedUpdatedAt:String){` +
		`publishConnector(token:$token,label:$label,expectedUpdatedAt:$expectedUpdatedAt){version}}`

	mCreateDashboard = `mutation ProductCreateDashboard($request:DashboardCreateRequest!){createDashboard(request:$request){token updatedAt}}`
	mUpdateDashboard = `mutation ProductUpdateDashboard($token:String!,$request:DashboardUpdateRequest!,$expectedUpdatedAt:String){` +
		`updateDashboard(token:$token,request:$request,expectedUpdatedAt:$expectedUpdatedAt){token updatedAt}}`
	mPublishDashboard = `mutation ProductPublishDashboard($token:String!,$label:String,$expectedUpdatedAt:String){` +
		`publishDashboard(token:$token,label:$label,expectedUpdatedAt:$expectedUpdatedAt){version{version}}}`
)

// ---- planning ----------------------------------------------------------------------------

type planner struct {
	c  Client
	ep Endpoints
	o  Options
	p  *Plan
	// liveProfiles lets a definition that is moving between profiles be compared with where
	// it is now.
	liveProfiles map[string]*LiveProfile
	// inBundle is the profiles the bundle carries; keyAt is which definition each live profile
	// holds a key under (profile, kind, key), across the whole tenant.
	inBundle map[string]bool
	keyAt    map[[3]string]string
}

func (pl *planner) add(ch Change, desc string, run func(ctx context.Context) error) {
	pl.p.Changes = append(pl.p.Changes, ch)
	pl.p.steps = append(pl.p.steps, step{desc: desc, run: run})
}

func (pl *planner) block(format string, a ...any) {
	pl.p.Blockers = append(pl.p.Blockers, fmt.Sprintf(format, a...))
}

// send runs one mutation and discards its result.
func (pl *planner) send(ctx context.Context, endpoint, doc string, vars map[string]any) error {
	var sink json.RawMessage
	return pl.c.Query(ctx, endpoint, doc, vars, &sink)
}

// BuildPlan compares a bundle with a live read and returns what applying it would do. It
// writes nothing. Resources the bundle does not carry are never touched; the exception is the
// definitions UNDER a profile the bundle does carry, which converge to the bundle's.
func BuildPlan(b *Bundle, l *Live, c Client, ep Endpoints, o Options) *Plan {
	pl := &planner{c: c, ep: ep, o: o, p: &Plan{}, liveProfiles: l.Profiles}
	// Connectors first: a rule's publish action may name one.
	for _, d := range b.Connectors {
		pl.connector(d, l.Connectors[d.Token])
	}
	pl.profiles(b, l)
	for _, d := range b.DeviceTypes {
		pl.deviceType(d, l)
	}
	for _, d := range b.Integrations {
		pl.integration(d, l)
	}
	for _, d := range b.Dashboards {
		pl.dashboard(d, l.Dashboards[d.Token])
	}
	pl.unmanaged(b, l)
	return pl.p
}

func (pl *planner) unmanaged(b *Bundle, l *Live) {
	note := func(label string, have []string, want map[string]bool) {
		var extra []string
		for _, h := range have {
			if !want[h] {
				extra = append(extra, h)
			}
		}
		sort.Strings(extra)
		if len(extra) > 0 {
			pl.p.Notes = append(pl.p.Notes, fmt.Sprintf("%d %s in the tenant are not in the bundle and are left alone: %s", len(extra), label, strings.Join(extra, ", ")))
		}
	}
	var tt, pp, ii, cc []string
	for k := range l.DeviceTypes {
		tt = append(tt, k)
	}
	for k := range l.Profiles {
		pp = append(pp, k)
	}
	for k := range l.Integrations {
		ii = append(ii, k)
	}
	for k := range l.Connectors {
		cc = append(cc, k)
	}
	w := func(n int, get func(i int) string) map[string]bool {
		m := map[string]bool{}
		for i := 0; i < n; i++ {
			m[get(i)] = true
		}
		return m
	}
	note("device type(s)", tt, w(len(b.DeviceTypes), func(i int) string { return b.DeviceTypes[i].Token }))
	note("profile(s)", pp, w(len(b.Profiles), func(i int) string { return b.Profiles[i].Token }))
	note("integration(s)", ii, w(len(b.Integrations), func(i int) string { return b.Integrations[i].Token }))
	note("connector(s)", cc, w(len(b.Connectors), func(i int) string { return b.Connectors[i].Token }))
	note("dashboard(s)", l.DashboardTokens, w(len(b.Dashboards), func(i int) string { return b.Dashboards[i].Token }))
}

func (pl *planner) deviceType(d DeviceType, l *Live) {
	live, ok := l.DeviceTypes[d.Token]
	if !ok {
		in := createInput(d.input(), map[string]any{"token": d.Token})
		pl.add(Change{Kind: KindDeviceType, Token: d.Token, Action: ActionCreate}, "create device type "+d.Token,
			func(ctx context.Context) error {
				return pl.send(ctx, pl.ep.DeviceManagement, mCreateDeviceType, map[string]any{"request": in})
			})
		return
	}
	changed, names := diffInputs(d.input(), live.input())
	if len(names) == 0 {
		return
	}
	pl.add(Change{Kind: KindDeviceType, Token: d.Token, Action: ActionUpdate, Fields: names}, "update device type "+d.Token,
		func(ctx context.Context) error {
			return pl.send(ctx, pl.ep.DeviceManagement, mUpdateDeviceType, map[string]any{"token": d.Token, "request": changed})
		})
}

func (pl *planner) integration(d Integration, l *Live) {
	live, ok := l.Integrations[d.Token]
	if !ok {
		in := createInput(d.input(), map[string]any{"token": d.Token, "type": d.Type})
		pl.add(Change{Kind: KindIntegration, Token: d.Token, Action: ActionCreate,
			Note: "the server generates its bearer token and apply does not capture it; rotate it to issue one"},
			"create integration "+d.Token, func(ctx context.Context) error {
				return pl.send(ctx, pl.ep.DeviceManagement, mCreateIntegration, map[string]any{"request": in})
			})
		return
	}
	if live.Type != d.Type {
		pl.block("integration %q is type %s in the tenant and %s in the bundle; a type cannot be changed after creation", d.Token, live.Type, d.Type)
		return
	}
	changed, names := diffInputs(d.input(), live.input())
	if len(names) == 0 {
		return
	}
	pl.add(Change{Kind: KindIntegration, Token: d.Token, Action: ActionUpdate, Fields: names}, "update integration "+d.Token,
		func(ctx context.Context) error {
			return pl.send(ctx, pl.ep.DeviceManagement, mUpdateIntegration, map[string]any{"token": d.Token, "request": changed})
		})
}

// secretFor resolves a connector's credential handle. ok is false when the handle names an
// environment variable that is not set.
func (pl *planner) secretFor(d Connector) (value string, ok bool) {
	name, err := parseHandle(d.Credential.Handle)
	if err != nil {
		return "", false
	}
	v := pl.o.getenv(name)
	return v, v != ""
}

func (pl *planner) connector(d Connector, live *LiveConnector) {
	var secret *string // nil: leave alone; "": delete; otherwise set
	if live == nil || d.Credential != nil {
		if d.Credential != nil && (live == nil || !live.HasSecret || pl.o.RotateSecrets) {
			v, ok := pl.secretFor(d)
			if !ok {
				if live == nil || !live.HasSecret {
					pl.block("connector %q needs a credential and %s is not set", d.Token, d.Credential.Handle)
					return
				}
				pl.p.Notes = append(pl.p.Notes, fmt.Sprintf("connector %q: --rotate-secrets was given but %s is not set; its stored credential is kept", d.Token, d.Credential.Handle))
			} else {
				secret = &v
			}
		}
	} else if live.HasSecret {
		empty := ""
		secret = &empty
	}

	publish := func(ctx context.Context, updatedAt string) error {
		return pl.send(ctx, pl.ep.Connectors, mPublishConnector,
			map[string]any{"token": d.Token, "label": pl.o.label(), "expectedUpdatedAt": updatedAt})
	}
	if live == nil {
		in := createInput(d.input(), map[string]any{"token": d.Token})
		if secret != nil {
			in["secret"] = *secret
		}
		pl.add(Change{Kind: KindConnector, Token: d.Token, Action: ActionCreate}, "create connector "+d.Token,
			func(ctx context.Context) error {
				var out struct {
					C struct {
						UpdatedAt *string `json:"updatedAt"`
					} `json:"createConnector"`
				}
				if err := pl.c.Query(ctx, pl.ep.Connectors, mCreateConnector, map[string]any{"request": in}, &out); err != nil {
					return err
				}
				return pl.maybePublish(ctx, publish, deref(out.C.UpdatedAt))
			})
		return
	}
	changed, names := diffInputs(d.input(), live.input())
	if secret != nil {
		changed["secret"] = *secret
		names = append(names, "credential")
	}
	stale := !live.Published || draftNewer(live.UpdatedAt, live.PublishedAt) || pl.o.Republish
	needPublish := len(names) > 0 || stale
	if len(names) > 0 {
		pl.add(Change{Kind: KindConnector, Token: d.Token, Action: ActionUpdate, Fields: names}, "update connector "+d.Token,
			func(ctx context.Context) error {
				var out struct {
					C struct {
						UpdatedAt *string `json:"updatedAt"`
					} `json:"updateConnector"`
				}
				vars := map[string]any{"token": d.Token, "request": changed, "expectedUpdatedAt": live.UpdatedAt}
				if err := pl.c.Query(ctx, pl.ep.Connectors, mUpdateConnector, vars, &out); err != nil {
					return err
				}
				return pl.maybePublish(ctx, publish, deref(out.C.UpdatedAt))
			})
		return
	}
	if needPublish && !pl.o.NoPublish {
		note := "the draft is newer than the published version"
		if !live.Published {
			note = "never published"
		} else if pl.o.Republish && !draftNewer(live.UpdatedAt, live.PublishedAt) {
			note = "--republish"
		}
		pl.add(Change{Kind: KindConnector, Token: d.Token, Action: ActionPublish, Note: note}, "publish connector "+d.Token,
			func(ctx context.Context) error { return publish(ctx, live.UpdatedAt) })
	}
}

// draftNewer reports whether a draft was last written after its latest version was published.
// A timestamp that does not parse is treated as "not newer", so a server that formats them
// differently costs a missed recovery rather than a publish on every run.
func draftNewer(updatedAt, publishedAt string) bool {
	u, err1 := time.Parse(time.RFC3339Nano, updatedAt)
	p, err2 := time.Parse(time.RFC3339Nano, publishedAt)
	return err1 == nil && err2 == nil && u.After(p)
}

func (pl *planner) maybePublish(ctx context.Context, publish func(context.Context, string) error, updatedAt string) error {
	if pl.o.NoPublish {
		return nil
	}
	return publish(ctx, updatedAt)
}

func (pl *planner) dashboard(d Dashboard, live *LiveDashboard) {
	publish := func(ctx context.Context, updatedAt string) error {
		if pl.o.NoPublish {
			return nil
		}
		return pl.send(ctx, pl.ep.Dashboards, mPublishDashboard,
			map[string]any{"token": d.Token, "label": pl.o.label(), "expectedUpdatedAt": updatedAt})
	}
	if live == nil {
		in := createInput(d.input(), map[string]any{"token": d.Token})
		pl.add(Change{Kind: KindDashboard, Token: d.Token, Action: ActionCreate}, "create dashboard "+d.Token,
			func(ctx context.Context) error {
				var out struct {
					D struct {
						UpdatedAt *string `json:"updatedAt"`
					} `json:"createDashboard"`
				}
				if err := pl.c.Query(ctx, pl.ep.Dashboards, mCreateDashboard, map[string]any{"request": in}, &out); err != nil {
					return err
				}
				return publish(ctx, deref(out.D.UpdatedAt))
			})
		return
	}
	changed, names := diffInputs(d.input(), live.input())
	stale := !live.Published || !eqDoc(d.Definition, live.PublishedDefinition) || pl.o.Republish
	switch {
	case len(names) > 0:
		pl.add(Change{Kind: KindDashboard, Token: d.Token, Action: ActionUpdate, Fields: names}, "update dashboard "+d.Token,
			func(ctx context.Context) error {
				var out struct {
					D struct {
						UpdatedAt *string `json:"updatedAt"`
					} `json:"updateDashboard"`
				}
				vars := map[string]any{"token": d.Token, "request": changed, "expectedUpdatedAt": live.UpdatedAt}
				if err := pl.c.Query(ctx, pl.ep.Dashboards, mUpdateDashboard, vars, &out); err != nil {
					return err
				}
				return publish(ctx, deref(out.D.UpdatedAt))
			})
	case stale && !pl.o.NoPublish:
		note := "the draft differs from the published version"
		if !live.Published {
			note = "never published"
		} else if eqDoc(d.Definition, live.PublishedDefinition) {
			note = "--republish"
		}
		pl.add(Change{Kind: KindDashboard, Token: d.Token, Action: ActionPublish, Note: note}, "publish dashboard "+d.Token,
			func(ctx context.Context) error { return publish(ctx, live.UpdatedAt) })
	}
}

// ---- profiles ----------------------------------------------------------------------------

// owner records which live profile holds a definition and what family it is in.
type owner struct {
	profile    string
	calculated bool
}

func (pl *planner) profiles(b *Bundle, l *Live) {
	// A definition token is unique across the tenant, so a definition may have to MOVE
	// between profiles; index where every live one is, and which are wanted anywhere.
	liveAt := map[Kind]map[string]owner{KindMetric: {}, KindCommand: {}, KindRule: {}}
	for _, lp := range l.Profiles {
		for _, m := range lp.Metrics {
			liveAt[KindMetric][m.Token] = owner{profile: lp.Token}
		}
		for _, m := range lp.CalculatedMetrics {
			liveAt[KindMetric][m.Token] = owner{profile: lp.Token, calculated: true}
		}
		for _, c := range lp.Commands {
			liveAt[KindCommand][c.Token] = owner{profile: lp.Token}
		}
		for _, r := range lp.Rules {
			liveAt[KindRule][r.Token] = owner{profile: lp.Token}
		}
	}
	wanted := map[Kind]map[string]bool{KindMetric: {}, KindCommand: {}, KindRule: {}}
	for _, p := range b.Profiles {
		for _, m := range p.Metrics {
			wanted[KindMetric][m.Token] = true
		}
		for _, m := range p.CalculatedMetrics {
			wanted[KindMetric][m.Token] = true
		}
		for _, c := range p.Commands {
			wanted[KindCommand][c.Token] = true
		}
		for _, r := range p.Rules {
			wanted[KindRule][r.Token] = true
		}
	}
	// Publishing waits until every profile's definitions have converged: a definition that
	// moves between profiles changes both, and publishing the source before the move would
	// freeze it into the old version.
	// A definition moving out of a profile changes that profile too, and its published version
	// still holds the definition, so the source is republished as well as the destination.
	movedFrom := map[string]bool{}
	inBundle := map[string]bool{}
	for _, p := range b.Profiles {
		inBundle[p.Token] = true
	}
	pl.inBundle = inBundle
	pl.keyAt = map[[3]string]string{}
	for _, lp := range l.Profiles {
		for _, m := range lp.Metrics {
			pl.keyAt[[3]string{lp.Token, string(KindMetric), m.Key}] = m.Token
		}
		for _, m := range lp.CalculatedMetrics {
			pl.keyAt[[3]string{lp.Token, string(KindMetric), m.Key}] = m.Token
		}
		for _, c := range lp.Commands {
			pl.keyAt[[3]string{lp.Token, string(KindCommand), c.Key}] = c.Token
		}
	}
	for _, p := range b.Profiles {
		for _, t := range desiredTokens(p) {
			if ow, ok := liveAt[t.kind][t.token]; ok && ow.profile != p.Token && inBundle[ow.profile] {
				movedFrom[ow.profile] = true
			}
		}
	}
	var publishes []func()
	for _, p := range b.Profiles {
		if pub := pl.profile(p, l.Profiles[p.Token], liveAt, wanted, movedFrom[p.Token]); pub != nil {
			publishes = append(publishes, pub)
		}
	}
	for _, pub := range publishes {
		pub()
	}
}

// profile plans one profile and returns the step that publishes it, or nil for none.
func (pl *planner) profile(d Profile, live *LiveProfile, liveAt map[Kind]map[string]owner, wanted map[Kind]map[string]bool, movedFrom bool) func() {
	touched := false
	have := Profile{Token: d.Token}
	if live != nil {
		have = live.Profile
	}
	mark := func(ch Change, desc string, run func(ctx context.Context) error) {
		touched = true
		pl.add(ch, desc, run)
	}

	if live == nil {
		in := createInput(d.input(), map[string]any{"token": d.Token})
		mark(Change{Kind: KindProfile, Token: d.Token, Action: ActionCreate}, "create profile "+d.Token, func(ctx context.Context) error {
			return pl.send(ctx, pl.ep.DeviceManagement, mCreateProfile, map[string]any{"request": in})
		})
	} else if changed, names := diffInputs(d.input(), have.input()); len(names) > 0 {
		mark(Change{Kind: KindProfile, Token: d.Token, Action: ActionUpdate, Fields: names}, "update profile "+d.Token, func(ctx context.Context) error {
			return pl.send(ctx, pl.ep.DeviceManagement, mUpdateProfile, map[string]any{"token": d.Token, "request": changed})
		})
	}

	if !reflect.DeepEqual(nilIfEmpty(d.ConfigurationDeclaration), nilIfEmpty(have.ConfigurationDeclaration)) {
		keys := make([]map[string]any, 0, len(d.ConfigurationDeclaration))
		for _, k := range d.ConfigurationDeclaration {
			e := map[string]any{"key": k.Key, "valueType": k.ValueType}
			if k.Description != "" {
				e["description"] = k.Description
			}
			keys = append(keys, e)
		}
		mark(Change{Kind: KindProfile, Token: d.Token, Action: ActionUpdate, Fields: []string{"configurationDeclaration"}},
			"set the configuration declaration of profile "+d.Token, func(ctx context.Context) error {
				return pl.send(ctx, pl.ep.DeviceManagement, mSetConfigDecl, map[string]any{"token": d.Token, "keys": keys})
			})
	}

	if !eqDoc(d.Decoder, have.Decoder) {
		if len(d.Decoder) == 0 {
			mark(Change{Kind: KindProfile, Token: d.Token, Action: ActionUpdate, Fields: []string{"decoder"}, Note: "cleared"},
				"clear the decoder of profile "+d.Token, func(ctx context.Context) error {
					return pl.send(ctx, pl.ep.DeviceManagement, mClearDecoder, map[string]any{"profileToken": d.Token})
				})
		} else {
			doc := string(d.Decoder)
			mark(Change{Kind: KindProfile, Token: d.Token, Action: ActionUpdate, Fields: []string{"decoder"}},
				"set the decoder of profile "+d.Token, func(ctx context.Context) error {
					return pl.send(ctx, pl.ep.DeviceManagement, mSetDecoder, map[string]any{"profileToken": d.Token, "document": doc})
				})
		}
	}

	// Definitions: removals first (a key freed by a removal may be wanted by a create), then
	// updates, then creates.
	type def struct {
		kind       Kind
		token      string
		input      map[string]any
		calculated bool
	}
	var wantDefs []def
	for _, m := range d.Metrics {
		wantDefs = append(wantDefs, def{KindMetric, m.Token, m.input(), false})
	}
	for _, m := range d.CalculatedMetrics {
		wantDefs = append(wantDefs, def{KindMetric, m.Token, m.input(), true})
	}
	for _, c := range d.Commands {
		wantDefs = append(wantDefs, def{KindCommand, c.Token, c.input(), false})
	}
	for _, r := range d.Rules {
		wantDefs = append(wantDefs, def{KindRule, r.Token, r.input(), false})
	}
	haveIn := map[Kind]map[string]map[string]any{KindMetric: {}, KindCommand: {}, KindRule: {}}
	for _, m := range have.Metrics {
		haveIn[KindMetric][m.Token] = m.input()
	}
	for _, m := range have.CalculatedMetrics {
		haveIn[KindMetric][m.Token] = m.input()
	}
	for _, c := range have.Commands {
		haveIn[KindCommand][c.Token] = c.input()
	}
	for _, r := range have.Rules {
		haveIn[KindRule][r.Token] = r.input()
	}

	docs := map[Kind][3]string{
		KindMetric:  {mCreateMetric, mUpdateMetric, mDeleteMetric},
		KindCommand: {mCreateCommand, mUpdateCommand, mDeleteCommand},
		KindRule:    {mCreateRule, mUpdateRule, mDeleteRule},
	}
	label := map[Kind]string{KindMetric: "metric", KindCommand: "command", KindRule: "detection rule"}

	for _, k := range []Kind{KindMetric, KindCommand, KindRule} {
		var gone []string
		for tok := range haveIn[k] {
			if !wanted[k][tok] {
				gone = append(gone, tok)
			}
		}
		sort.Strings(gone)
		for _, tok := range gone {
			tok := tok
			dd := docs[k][2]
			mark(Change{Kind: kindOf(k, liveAt, tok), Token: tok, Parent: d.Token, Action: ActionDelete, Note: "not in the bundle"},
				"delete "+label[k]+" "+tok, func(ctx context.Context) error {
					return pl.send(ctx, pl.ep.DeviceManagement, dd, map[string]any{"token": tok})
				})
		}
	}

	for _, pass := range []string{"update", "create"} {
		for _, w := range wantDefs {
			w := w
			ow, exists := liveAt[w.kind][w.token]
			kind := w.kind
			if w.kind == KindMetric && w.calculated {
				kind = KindCalculated
			}
			if pass == "update" && exists {
				if w.kind == KindMetric && ow.calculated != w.calculated {
					pl.block("metric %q is %s in the tenant and %s in the bundle; which of the two a metric is cannot be changed", w.token, calcWord(ow.calculated), calcWord(w.calculated))
					continue
				}
				var cur map[string]any
				if ow.profile == d.Token {
					cur = haveIn[w.kind][w.token]
				} else {
					cur = pl.liveInputOf(w.kind, w.token, ow.profile)
				}
				changed, names := diffInputs(w.input, cur)
				if pl.blockKeyConflict(d.Token, w.kind, w.token, w.input, wanted, label[w.kind]) {
					continue
				}
				note := ""
				if ow.profile != d.Token {
					changed["deviceProfileToken"] = d.Token
					names = append([]string{"profile"}, names...)
					if !pl.inBundle[ow.profile] {
						note = "moved out of profile " + ow.profile + ", which is not in the bundle"
					}
				}
				if len(names) == 0 {
					continue
				}
				mark(Change{Kind: kind, Token: w.token, Parent: d.Token, Action: ActionUpdate, Fields: names, Note: note},
					"update "+label[w.kind]+" "+w.token, func(ctx context.Context) error {
						return pl.send(ctx, pl.ep.DeviceManagement, docs[w.kind][1], map[string]any{"token": w.token, "request": changed})
					})
			}
			if pass == "create" && !exists {
				if pl.blockKeyConflict(d.Token, w.kind, w.token, w.input, wanted, label[w.kind]) {
					continue
				}
				in := createInput(w.input, map[string]any{"token": w.token, "deviceProfileToken": d.Token})
				mark(Change{Kind: kind, Token: w.token, Parent: d.Token, Action: ActionCreate},
					"create "+label[w.kind]+" "+w.token, func(ctx context.Context) error {
						return pl.send(ctx, pl.ep.DeviceManagement, docs[w.kind][0], map[string]any{"request": in})
					})
			}
		}
	}

	// Publish when this run changed the draft, the profile was never published, or the
	// content devices actually see (metrics, commands, decoder, configuration) differs from
	// the bundle. Rules and the position declaration are not readable from a published
	// version, so only a draft change made BY this run, or a first publish, republishes them.
	if pl.o.NoPublish {
		return nil
	}
	published := live != nil && live.Published != nil
	stale := !published
	note := "never published"
	if published {
		switch {
		case !reflect.DeepEqual(publishedViewOf(d), live.Published):
			stale, note = true, "the published version differs from the bundle"
		case movedFrom:
			stale, note = true, "a definition moved out of it"
		case pl.o.Republish:
			stale, note = true, "--republish"
		}
		if touched {
			note = "the draft changed"
		}
	}
	if !touched && !stale {
		return nil
	}
	return func() {
		pl.add(Change{Kind: KindProfile, Token: d.Token, Action: ActionPublish, Note: note}, "publish profile "+d.Token,
			func(ctx context.Context) error {
				return pl.send(ctx, pl.ep.DeviceManagement, mPublishProfile, map[string]any{"token": d.Token, "label": pl.o.label()})
			})
	}
}

// blockKeyConflict refuses, before anything is written, a definition whose key in its
// DESTINATION profile is held by another definition the bundle keeps. Keys are unique per
// profile and the operations are not ordered across profiles, so a swap (or a move onto a
// key the occupant has not yet vacated) would fail at the server on every retry, after some
// of the run had already been applied. It reports whether it blocked.
func (pl *planner) blockKeyConflict(profile string, k Kind, token string, input map[string]any, wanted map[Kind]map[string]bool, label string) bool {
	f := keyField(k)
	if f == "" {
		return false
	}
	key, _ := input[f].(string)
	other := pl.keyAt[[3]string{profile, string(k), key}]
	if key == "" || other == "" || other == token || !wanted[k][other] {
		return false
	}
	pl.block("%s %q takes the key %q in profile %q that %q holds in the tenant; free the key in a separate apply first (the operations are not ordered across definitions)", label, token, key, profile, other)
	return true
}

// liveInputOf is the input rendering of a definition that currently sits under another
// profile (a move). It is filled by Plan construction from the live snapshot.
func (pl *planner) liveInputOf(k Kind, token, profile string) map[string]any {
	if pl.liveProfiles == nil {
		return nil
	}
	lp := pl.liveProfiles[profile]
	if lp == nil {
		return nil
	}
	switch k {
	case KindMetric:
		for _, m := range lp.Metrics {
			if m.Token == token {
				return m.input()
			}
		}
		for _, m := range lp.CalculatedMetrics {
			if m.Token == token {
				return m.input()
			}
		}
	case KindCommand:
		for _, c := range lp.Commands {
			if c.Token == token {
				return c.input()
			}
		}
	case KindRule:
		for _, r := range lp.Rules {
			if r.Token == token {
				return r.input()
			}
		}
	}
	return nil
}

type defRef struct {
	kind  Kind
	token string
}

// desiredTokens lists every definition token a profile in the bundle carries.
func desiredTokens(p Profile) []defRef {
	var out []defRef
	for _, m := range p.Metrics {
		out = append(out, defRef{KindMetric, m.Token})
	}
	for _, m := range p.CalculatedMetrics {
		out = append(out, defRef{KindMetric, m.Token})
	}
	for _, c := range p.Commands {
		out = append(out, defRef{KindCommand, c.Token})
	}
	for _, r := range p.Rules {
		out = append(out, defRef{KindRule, r.Token})
	}
	return out
}

// keyField is the update field that carries a definition's key, or "" for a kind that has none.
func keyField(k Kind) string {
	switch k {
	case KindMetric:
		return "metricKey"
	case KindCommand:
		return "commandKey"
	}
	return ""
}

func calcWord(c bool) string {
	if c {
		return "calculated"
	}
	return "measured"
}

func kindOf(k Kind, liveAt map[Kind]map[string]owner, tok string) Kind {
	if k == KindMetric && liveAt[k][tok].calculated {
		return KindCalculated
	}
	return k
}

func nilIfEmpty[T any](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	return s
}
