// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// Client is the GraphQL seam Apply drives; *userclient.TenantSession satisfies it, so every call
// is authorized as the person who ran the command.
type Client interface {
	Query(ctx context.Context, baseURL, query string, variables map[string]any, out any) error
}

// The documents Apply sends. They are validated against the served schema in the tests.
const (
	createProfileMutation   = `mutation($request:DeviceProfileCreateRequest){createDeviceProfile(request:$request){token}}`
	publishProfileMutation  = `mutation($token:String!,$label:String){publishDeviceProfile(token:$token,label:$label){version}}`
	profileVersionsQuery    = `query($token:String!,$tokens:[String!]!){deviceProfileVersions(token:$token,limit:1){version} deviceProfilesByToken(tokens:$tokens){token metadata}}`
	createTypeMutation      = `mutation($request:DeviceTypeCreateRequest){createDeviceType(request:$request){token}}`
	createAssetTypeMutation = `mutation($request:AssetTypeCreateRequest){createAssetType(request:$request){token}}`
	createAssetMutation     = `mutation($request:AssetCreateRequest){createAsset(request:$request){token}}`
	importDevicesMutation   = `mutation($rows:[DeviceImportRow!]!,$mode:DeviceImportMode!,$dryRun:Boolean!){` +
		`importDevices(rows:$rows,mode:$mode,dryRun:$dryRun){created exists conflict invalid rows{row token outcome code message}}}`
	createRelTypeMutation = `mutation($request:EntityRelationshipTypeCreateRequest!){createEntityRelationshipType(request:$request){token}}`
	setParentMutation     = `mutation($child:String!,$parent:String!){setAssetParent(childToken:$child,parentToken:$parent){token}}`
	createRelMutation     = `mutation($request:EntityRelationshipCreateRequest!){createEntityRelationship(request:$request){token}}`
	setAttrMutation       = `mutation($request:EntityAttributeSetRequest!){setEntityAttribute(request:$request){attrKey}}`
)

// profileVersionsResult is the answer to profileVersionsQuery: the profile's newest version, if any.
type profileVersionsResult struct {
	DeviceProfileVersions []struct {
		Version int `json:"version"`
	} `json:"deviceProfileVersions"`
	DeviceProfilesByToken []struct {
		Token    string  `json:"token"`
		Metadata *string `json:"metadata"`
	} `json:"deviceProfilesByToken"`
}

// importedProfile reports whether the profile named by token carries this importer's provenance:
// the "source" metadata object Build writes, whose id is the token itself. A draft someone made
// by hand under the same token has no such mark and is never published on their behalf.
func (r profileVersionsResult) importedProfile(token string) bool {
	for _, p := range r.DeviceProfilesByToken {
		if p.Token != token || p.Metadata == nil {
			continue
		}
		var m struct {
			Source struct {
				ID string `json:"id"`
			} `json:"source"`
		}
		if json.Unmarshal([]byte(*p.Metadata), &m) == nil && m.Source.ID == token {
			return true
		}
	}
	return false
}

// authGuard stops a run at the first refused credential. A 401 means the token, not the item,
// was refused, so every later call would fail the same way; without this a rejected session turns
// into one failure line per entity and the real cause scrolls away. Every call after the first
// refusal fails at once, without touching the network, and Apply returns the cause.
type authGuard struct {
	Client
	err error
}

func (g *authGuard) Query(ctx context.Context, baseURL, query string, variables map[string]any, out any) error {
	if g.err != nil {
		return g.err
	}
	err := g.Client.Query(ctx, baseURL, query, variables, out)
	if userclient.IsUnauthorized(err) {
		g.err = err
	}
	return err
}

func (g *authGuard) halted() bool { return g.err != nil }

func (g *authGuard) haltError() error {
	return fmt.Errorf("the server refused the session (%v); the run stopped at the first refusal and nothing after it was attempted. Re-run to continue: it only retries what is missing", g.err)
}

// importBatch is the importDevices row ceiling.
const importBatch = 1000

// Tally counts what happened to one kind of thing.
type Tally struct {
	Kind    string   `json:"kind"`
	Created int      `json:"created"`
	Exists  int      `json:"exists"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

func (t *Tally) fail(ref string, err error) {
	t.Failed++
	if len(t.Errors) < 20 {
		t.Errors = append(t.Errors, fmt.Sprintf("%s: %v", ref, err))
	}
}

// conflict reports whether err is the server refusing a create because the token is taken,
// which on a re-run means "already there".
func conflict(err error) bool {
	var g *userclient.GraphQLError
	if asGraphQL(err, &g) {
		for _, c := range g.Codes {
			if c == "CONFLICT" {
				return true
			}
		}
	}
	return false
}

func asGraphQL(err error, target **userclient.GraphQLError) bool {
	for err != nil {
		if g, ok := err.(*userclient.GraphQLError); ok {
			*target = g
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Apply sends the plan to endpoint (the device-management GraphQL URL) in dependency order. It
// is create-only and idempotent: a thing that already exists counts as existing and is never
// updated. A failure on one item does not stop the rest, because every item is independent of
// its siblings; items that depend on a failed one fail in turn and say so.
func Apply(ctx context.Context, client Client, endpoint string, p *Plan) ([]Tally, error) {
	var out []Tally
	tally := func(kind string) *Tally { out = append(out, Tally{Kind: kind}); return &out[len(out)-1] }
	c := &authGuard{Client: client}
	// createOutcome says whether the write made the thing, found it already there, or failed.
	createOutcome := func(t *Tally, ref, q string, vars map[string]any) (created, existed bool) {
		err := c.Query(ctx, endpoint, q, vars, nil)
		switch {
		case err == nil:
			t.Created++
			return true, false
		case conflict(err):
			t.Exists++
			return false, true
		default:
			t.fail(ref, err)
			return false, false
		}
	}
	create := func(t *Tally, ref, q string, vars map[string]any) bool {
		created, _ := createOutcome(t, ref, q, vars)
		return created
	}

	t := tally("device profiles")
	for _, pr := range p.Profiles {
		req := map[string]any{"token": pr.Token, "name": pr.Name, "description": pr.Description, "metadata": pr.Metadata}
		// Publish a profile this run created, and one an earlier run created but never published
		// (the publish failed after the create). Never publish one that already has a version, so
		// a re-run does not mint another.
		created, existed := createOutcome(t, pr.Token, createProfileMutation, map[string]any{"request": req})
		publish := created
		if existed {
			var vs profileVersionsResult
			if err := c.Query(ctx, endpoint, profileVersionsQuery, map[string]any{"token": pr.Token, "tokens": []string{pr.Token}}, &vs); err != nil {
				t.fail(pr.Token+" (versions)", err)
			} else {
				publish = len(vs.DeviceProfileVersions) == 0 && vs.importedProfile(pr.Token)
			}
		}
		if publish {
			if err := c.Query(ctx, endpoint, publishProfileMutation, map[string]any{"token": pr.Token, "label": "imported"}, nil); err != nil {
				t.fail(pr.Token+" (publish)", err)
			}
		}
		if c.halted() {
			break
		}
	}
	if c.halted() {
		return out, c.haltError()
	}
	t = tally("device types")
	for _, pr := range p.Profiles {
		if c.halted() {
			break
		}
		req := map[string]any{"token": pr.Token, "name": pr.Name, "description": pr.Description, "profileToken": pr.Token}
		create(t, pr.Token, createTypeMutation, map[string]any{"request": req})
	}
	if c.halted() {
		return out, c.haltError()
	}
	t = tally("asset types")
	for _, a := range p.AssetTypes {
		if c.halted() {
			break
		}
		req := map[string]any{"token": a.Token, "name": a.Name, "description": a.Description, "metadata": a.Metadata}
		create(t, a.Token, createAssetTypeMutation, map[string]any{"request": req})
	}
	if c.halted() {
		return out, c.haltError()
	}
	t = tally("assets")
	for _, a := range p.Assets {
		if c.halted() {
			break
		}
		req := map[string]any{"token": a.Token, "name": a.Name, "description": a.Description,
			"assetTypeToken": a.TypeToken, "metadata": a.Metadata}
		create(t, a.Token, createAssetMutation, map[string]any{"request": req})
	}

	if c.halted() {
		return out, c.haltError()
	}
	t = tally("devices")
	var plain, carried []DeviceRow
	for _, r := range p.Devices {
		if r.CredentialID != "" {
			carried = append(carried, r)
		} else {
			plain = append(plain, r)
		}
	}
	importRows(ctx, c, endpoint, t, plain, "NONE")
	importRows(ctx, c, endpoint, t, carried, "FACTORY_CREDENTIALS")

	if c.halted() {
		return out, c.haltError()
	}
	t = tally("relationship types")
	for _, r := range p.RelTypes {
		if c.halted() {
			break
		}
		req := map[string]any{"token": r.Token, "name": r.Name, "tracked": false}
		create(t, r.Token, createRelTypeMutation, map[string]any{"request": req})
	}
	if c.halted() {
		return out, c.haltError()
	}
	t = tally("asset hierarchy")
	for _, pp := range p.Parents {
		if c.halted() {
			break
		}
		// setAssetParent replaces the parent, so it is already idempotent: count it as created.
		if err := c.Query(ctx, endpoint, setParentMutation, map[string]any{"child": pp.Child, "parent": pp.Parent}, nil); err != nil {
			t.fail(pp.Child, err)
		} else {
			t.Created++
		}
	}
	if c.halted() {
		return out, c.haltError()
	}
	t = tally("relationships")
	for _, r := range p.Relations {
		if c.halted() {
			break
		}
		req := map[string]any{"token": r.Token, "sourceType": r.SourceType, "source": r.Source,
			"targetType": r.TargetType, "target": r.Target, "relationshipType": r.Type}
		create(t, r.Token, createRelMutation, map[string]any{"request": req})
	}
	if c.halted() {
		return out, c.haltError()
	}
	t = tally("attributes")
	for _, a := range p.Attributes {
		if c.halted() {
			break
		}
		req := map[string]any{"entityType": a.EntityType, "entity": a.Entity, "scope": a.Scope,
			"attrKey": a.Key, "valueType": a.ValueType, "value": a.Value}
		// An upsert: the same write twice is one row, so it counts as written either way.
		if err := c.Query(ctx, endpoint, setAttrMutation, map[string]any{"request": req}, nil); err != nil {
			t.fail(a.Entity+"/"+a.Key, err)
		} else {
			t.Created++
		}
	}

	if c.halted() {
		return out, c.haltError()
	}
	failed := 0
	for _, x := range out {
		failed += x.Failed
	}
	if failed > 0 {
		return out, fmt.Errorf("%d item(s) failed; the rest were applied and a re-run retries only what is missing", failed)
	}
	return out, nil
}

type importResult struct {
	ImportDevices struct {
		Rows []struct {
			Row     int     `json:"row"`
			Token   string  `json:"token"`
			Outcome string  `json:"outcome"`
			Code    *string `json:"code"`
			Message *string `json:"message"`
		} `json:"rows"`
	} `json:"importDevices"`
}

func importRows(ctx context.Context, c Client, endpoint string, t *Tally, rows []DeviceRow, mode string) {
	for start := 0; start < len(rows); start += importBatch {
		if g, ok := c.(*authGuard); ok && g.halted() {
			return
		}
		end := min(start+importBatch, len(rows))
		batch := rows[start:end]
		in := make([]any, 0, len(batch))
		for _, r := range batch {
			m := map[string]any{"token": r.Token, "deviceTypeToken": r.DeviceTypeToken}
			set := func(k, v string) {
				if v != "" {
					m[k] = v
				}
			}
			set("externalId", r.ExternalID)
			set("name", r.Name)
			set("description", r.Description)
			set("metadata", r.Metadata)
			if mode == "FACTORY_CREDENTIALS" {
				m["credentialType"] = r.CredentialType
				m["credentialId"] = r.CredentialID
			}
			in = append(in, m)
		}
		var res importResult
		err := c.Query(ctx, endpoint, importDevicesMutation, map[string]any{"rows": in, "mode": mode, "dryRun": false}, &res)
		if err != nil {
			if userclient.IsUnauthorized(err) {
				// The session was refused, not the rows: one failure says so, and the caller's
				// guard stops the run.
				t.fail(fmt.Sprintf("%d device row(s) from %s", len(batch), batch[0].Token), err)
				return
			}
			for _, r := range batch {
				t.fail(r.Token, err)
			}
			continue
		}
		for _, r := range res.ImportDevices.Rows {
			switch r.Outcome {
			case "CREATED":
				t.Created++
			case "EXISTS":
				t.Exists++
			default:
				msg := r.Outcome
				if r.Code != nil {
					msg += " " + *r.Code
				}
				if r.Message != nil {
					msg += ": " + *r.Message
				}
				t.fail(r.Token, fmt.Errorf("%s", msg))
			}
		}
	}
}

// PrintReport writes the plan's size and the report, grouped by class.
func PrintReport(w io.Writer, p *Plan) {
	fmt.Fprintf(w, "Plan: %d profiles/types, %d asset types, %d assets, %d devices (%d with credential), %d asset parents, %d relationships (%d new types), %d attributes\n",
		len(p.Profiles), len(p.AssetTypes), len(p.Assets), len(p.Devices), carried(p.Devices),
		len(p.Parents), len(p.Relations), len(p.RelTypes), len(p.Attributes))
	sum := Summary(p.Report)
	fmt.Fprintf(w, "Report: %d direct, %d transformed, %d partial, %d unsupported\n",
		sum[Direct], sum[Transformed], sum[Partial], sum[Unsupported])
	for _, c := range []Class{Unsupported, Partial, Transformed, Direct} {
		var lines []Line
		for _, l := range p.Report {
			if l.Class == c {
				lines = append(lines, l)
			}
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s\n", strings.ToUpper(string(c)))
		// A fleet repeats one message per entity ("2 platform-maintained attributes not copied",
		// once for each of 5,000 devices). Print each distinct message once with its count and
		// a first reference; the JSON report keeps every line.
		type group struct {
			Line
			n int
		}
		var groups []*group
		index := map[string]*group{}
		for _, l := range lines {
			k := l.Kind + "\x00" + l.Message
			if g, ok := index[k]; ok {
				g.n++
				continue
			}
			g := &group{Line: l, n: 1}
			index[k] = g
			groups = append(groups, g)
		}
		for _, g := range groups {
			ref := ""
			if g.Ref != "" {
				ref = " " + g.Ref
			}
			more := ""
			if g.n > 1 {
				more = fmt.Sprintf(" (and %d more like it)", g.n-1)
			}
			fmt.Fprintf(w, "  [%s%s] %s%s\n", g.Kind, ref, g.Message, more)
		}
	}
}

func carried(rows []DeviceRow) int {
	n := 0
	for _, r := range rows {
		if r.CredentialID != "" {
			n++
		}
	}
	return n
}
