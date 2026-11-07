// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"context"
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
func Apply(ctx context.Context, c Client, endpoint string, p *Plan) ([]Tally, error) {
	var out []Tally
	tally := func(kind string) *Tally { out = append(out, Tally{Kind: kind}); return &out[len(out)-1] }
	create := func(t *Tally, ref, q string, vars map[string]any) bool {
		err := c.Query(ctx, endpoint, q, vars, nil)
		switch {
		case err == nil:
			t.Created++
			return true
		case conflict(err):
			t.Exists++
			return false
		default:
			t.fail(ref, err)
			return false
		}
	}

	t := tally("device profiles")
	for _, pr := range p.Profiles {
		req := map[string]any{"token": pr.Token, "name": pr.Name, "description": pr.Description, "metadata": pr.Metadata}
		// Publish only a profile this run created, so a re-run never mints another version.
		if create(t, pr.Token, createProfileMutation, map[string]any{"request": req}) {
			if err := c.Query(ctx, endpoint, publishProfileMutation, map[string]any{"token": pr.Token, "label": "imported"}, nil); err != nil {
				t.fail(pr.Token+" (publish)", err)
			}
		}
	}
	t = tally("device types")
	for _, pr := range p.Profiles {
		req := map[string]any{"token": pr.Token, "name": pr.Name, "description": pr.Description, "profileToken": pr.Token}
		create(t, pr.Token, createTypeMutation, map[string]any{"request": req})
	}
	t = tally("asset types")
	for _, a := range p.AssetTypes {
		req := map[string]any{"token": a.Token, "name": a.Name, "description": a.Description, "metadata": a.Metadata}
		create(t, a.Token, createAssetTypeMutation, map[string]any{"request": req})
	}
	t = tally("assets")
	for _, a := range p.Assets {
		req := map[string]any{"token": a.Token, "name": a.Name, "description": a.Description,
			"assetTypeToken": a.TypeToken, "metadata": a.Metadata}
		create(t, a.Token, createAssetMutation, map[string]any{"request": req})
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

	t = tally("relationship types")
	for _, r := range p.RelTypes {
		req := map[string]any{"token": r.Token, "name": r.Name, "tracked": false}
		create(t, r.Token, createRelTypeMutation, map[string]any{"request": req})
	}
	t = tally("asset hierarchy")
	for _, pp := range p.Parents {
		// setAssetParent replaces the parent, so it is already idempotent: count it as created.
		if err := c.Query(ctx, endpoint, setParentMutation, map[string]any{"child": pp.Child, "parent": pp.Parent}, nil); err != nil {
			t.fail(pp.Child, err)
		} else {
			t.Created++
		}
	}
	t = tally("relationships")
	for _, r := range p.Relations {
		req := map[string]any{"token": r.Token, "sourceType": r.SourceType, "source": r.Source,
			"targetType": r.TargetType, "target": r.Target, "relationshipType": r.Type}
		create(t, r.Token, createRelMutation, map[string]any{"request": req})
	}
	t = tally("attributes")
	for _, a := range p.Attributes {
		req := map[string]any{"entityType": a.EntityType, "entity": a.Entity, "scope": a.Scope,
			"attrKey": a.Key, "valueType": a.ValueType, "value": a.Value}
		// An upsert: the same write twice is one row, so it counts as written either way.
		if err := c.Query(ctx, endpoint, setAttrMutation, map[string]any{"request": req}, nil); err != nil {
			t.fail(a.Entity+"/"+a.Key, err)
		} else {
			t.Created++
		}
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
