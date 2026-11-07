// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeTenant is an in-memory stand-in for the three tenant GraphQL planes the product spans.
// It dispatches on the NAME of the operation (every document this package sends is a named
// operation), answers in the served schema's shapes, and enforces the rules the real servers
// enforce that the planner depends on: tokens unique, the connector and dashboard version
// stamps, a published version frozen from the draft, a write-only connector credential.
//
// It deliberately returns documents REFORMATTED (indented), as a server that stores text may,
// so every comparison in the tests has to go through canonical form to pass.
type fakeTenant struct {
	mu sync.Mutex
	t  *testing.T

	deviceTypes  map[string]map[string]any
	profiles     map[string]map[string]any
	metrics      map[string]map[string]any
	commands     map[string]map[string]any
	rules        map[string]map[string]any
	integrations map[string]map[string]any
	connectors   map[string]map[string]any
	dashboards   map[string]map[string]any

	// writes counts mutations; a plan or an unchanged apply must leave it alone.
	writes int
	// log records every mutation operation name with its token, in order.
	log []string
	// calls records every (endpoint, document, variables) for schema validation.
	calls []recordedCall
	clock int
	// onMutation, when set, runs before each mutation (to inject a concurrent writer).
	onMutation func(op string)
	// secrets keeps connector credentials, which are write-only.
	secrets map[string]string
	// tokensIssued counts integration tokens the server generated.
	tokensIssued int
}

type recordedCall struct {
	endpoint, query string
	vars            map[string]any
}

func newFake(t *testing.T) *fakeTenant {
	return &fakeTenant{
		t: t, deviceTypes: map[string]map[string]any{}, profiles: map[string]map[string]any{},
		metrics: map[string]map[string]any{}, commands: map[string]map[string]any{}, rules: map[string]map[string]any{},
		integrations: map[string]map[string]any{}, connectors: map[string]map[string]any{},
		dashboards: map[string]map[string]any{}, secrets: map[string]string{},
	}
}

var opRe = regexp.MustCompile(`^\s*(query|mutation)\s+(\w+)`)

func (f *fakeTenant) stamp() string {
	f.clock++
	return fmt.Sprintf("2026-11-07T00:00:%02d.000Z", f.clock%60) + fmt.Sprint(f.clock)
}

func ep() Endpoints {
	return Endpoints{DeviceManagement: "dm", Connectors: "oc", Dashboards: "dash"}
}

// Query implements Client.
func (f *fakeTenant) Query(_ context.Context, endpoint, query string, vars map[string]any, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := map[string]any{}
	if vars != nil {
		raw, _ := json.Marshal(vars)
		_ = json.Unmarshal(raw, &cp)
	}
	f.calls = append(f.calls, recordedCall{endpoint, query, vars})
	m := opRe.FindStringSubmatch(query)
	if m == nil {
		return fmt.Errorf("fake: unnamed operation %q", query)
	}
	kind, op := m[1], m[2]
	if kind == "mutation" {
		if f.onMutation != nil {
			f.onMutation(op)
		}
		f.writes++
	}
	res, err := f.dispatch(op, cp)
	if err != nil {
		return err
	}
	if kind == "mutation" {
		f.log = append(f.log, op+" "+logToken(cp))
	}
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func logToken(v map[string]any) string {
	if t, ok := v["token"].(string); ok {
		return t
	}
	if r, ok := v["request"].(map[string]any); ok {
		if t, ok := r["token"].(string); ok {
			return t
		}
	}
	if t, ok := v["profileToken"].(string); ok {
		return t
	}
	return ""
}

func sorted(m map[string]map[string]any) []map[string]any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

func page(rows []map[string]any, field string) map[string]any {
	return map[string]any{field: map[string]any{
		"results": rows, "pagination": map[string]any{"totalRecords": len(rows)},
	}}
}

// pretty re-indents a JSON text the way a server that stores it verbatim-but-formatted might.
func pretty(s any) any {
	str, ok := s.(string)
	if !ok || str == "" {
		return s
	}
	var buf bytes.Buffer
	if json.Indent(&buf, []byte(str), "", "  ") != nil {
		return s
	}
	return buf.String()
}

var docFields = map[string]bool{"metadata": true, "enum": true, "parameterSchema": true, "definition": true,
	"authoringGraph": true, "generic": true, "config": true, "document": true}

func clone(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (f *fakeTenant) conflict(kind, tok string) error {
	return fmt.Errorf("fake: %s %q already exists (CONFLICT)", kind, tok)
}

// merge applies a partial update: a key present in req is set, an explicit null clears.
func merge(dst, req map[string]any, rename map[string]string) {
	for k, v := range req {
		if r, ok := rename[k]; ok {
			k = r
		}
		if v == nil {
			delete(dst, k)
			continue
		}
		dst[k] = v
	}
}

func (f *fakeTenant) dispatch(op string, v map[string]any) (any, error) {
	req, _ := v["request"].(map[string]any)
	tok, _ := v["token"].(string)
	switch op {
	// ---- reads ----
	case "ProductDeviceTypes":
		var rows []map[string]any
		for _, d := range sorted(f.deviceTypes) {
			r := clone(d)
			if pt, ok := r["profileToken"].(string); ok {
				r["profile"] = map[string]any{"token": pt}
				delete(r, "profileToken")
			}
			if mt, ok := r["maintenanceWindowToken"].(string); ok {
				r["maintenanceWindow"] = map[string]any{"token": mt}
				delete(r, "maintenanceWindowToken")
			}
			for k := range docFields {
				if x, ok := r[k]; ok {
					r[k] = pretty(x)
				}
			}
			rows = append(rows, r)
		}
		return page(rows, "deviceTypes"), nil
	case "ProductProfiles":
		var rows []map[string]any
		for _, p := range sorted(f.profiles) {
			rows = append(rows, f.profileView(p))
		}
		return page(rows, "deviceProfiles"), nil
	case "ProductIntegrations":
		var rows []map[string]any
		for _, d := range sorted(f.integrations) {
			r := clone(d)
			var dts []map[string]any
			for _, t := range r["deviceTypeTokens"].([]any) {
				dts = append(dts, map[string]any{"token": t})
			}
			r["deviceTypes"] = dts
			delete(r, "deviceTypeTokens")
			delete(r, "_secretDigest")
			for k := range docFields {
				if x, ok := r[k]; ok {
					r[k] = pretty(x)
				}
			}
			rows = append(rows, r)
		}
		return page(rows, "integrations"), nil
	case "ProductConnectors":
		var rows []map[string]any
		for _, d := range sorted(f.connectors) {
			r := clone(d)
			_, has := f.secrets[d["token"].(string)]
			r["hasSecret"] = has
			delete(r, "_versions")
			r["config"] = pretty(r["config"])
			rows = append(rows, r)
		}
		return page(rows, "connectors"), nil
	case "ProductConnectorVersions":
		c := f.connectors[tok]
		if c == nil {
			return nil, fmt.Errorf("fake: no connector %q", tok)
		}
		var vs []map[string]any
		if n, _ := c["_versions"].(int); n > 0 {
			vs = append(vs, map[string]any{"version": n})
		}
		return map[string]any{"connectorVersions": vs}, nil
	case "ProductDashboards":
		var rows []map[string]any
		for _, d := range sorted(f.dashboards) {
			rows = append(rows, map[string]any{"token": d["token"]})
		}
		return page(rows, "dashboards"), nil
	case "ProductDashboard":
		d := f.dashboards[tok]
		if d == nil {
			return map[string]any{"dashboard": nil}, nil
		}
		r := clone(d)
		delete(r, "_published")
		r["definition"] = pretty(r["definition"])
		if pv, ok := d["_published"].(map[string]any); ok {
			r["publishedVersion"] = pv["version"]
		}
		return map[string]any{"dashboard": r}, nil
	case "ProductPublishedDashboard":
		d := f.dashboards[tok]
		if d == nil {
			return map[string]any{"publishedDashboard": nil}, nil
		}
		pv, ok := d["_published"].(map[string]any)
		if !ok {
			return map[string]any{"publishedDashboard": nil}, nil
		}
		return map[string]any{"publishedDashboard": map[string]any{"version": pv["version"], "definition": pretty(pv["definition"])}}, nil

	// ---- device types / integrations ----
	case "ProductCreateDeviceType":
		t := req["token"].(string)
		if _, dup := f.deviceTypes[t]; dup {
			return nil, f.conflict("device type", t)
		}
		f.deviceTypes[t] = clone(req)
		return map[string]any{"createDeviceType": map[string]any{"token": t}}, nil
	case "ProductUpdateDeviceType":
		d := f.deviceTypes[tok]
		if d == nil {
			return nil, fmt.Errorf("fake: no device type %q", tok)
		}
		merge(d, req, nil)
		return map[string]any{"updateDeviceType": map[string]any{"token": tok}}, nil
	case "ProductCreateIntegration":
		t := req["token"].(string)
		if _, dup := f.integrations[t]; dup {
			return nil, f.conflict("integration", t)
		}
		r := clone(req)
		if _, ok := r["identity"]; !ok {
			r["identity"] = "DEV_EUI"
		}
		if _, ok := r["linkMetrics"]; !ok {
			r["linkMetrics"] = false
		}
		f.integrations[t] = r
		f.tokensIssued++
		return map[string]any{"createIntegration": map[string]any{"integration": map[string]any{"token": t}}}, nil
	case "ProductUpdateIntegration":
		d := f.integrations[tok]
		if d == nil {
			return nil, fmt.Errorf("fake: no integration %q", tok)
		}
		merge(d, req, nil)
		return map[string]any{"updateIntegration": map[string]any{"token": tok}}, nil

	// ---- profiles ----
	case "ProductCreateProfile":
		t := req["token"].(string)
		if _, dup := f.profiles[t]; dup {
			return nil, f.conflict("profile", t)
		}
		p := clone(req)
		p["_draftVersion"] = 0
		f.profiles[t] = p
		return map[string]any{"createDeviceProfile": map[string]any{"token": t}}, nil
	case "ProductUpdateProfile":
		p := f.profiles[tok]
		if p == nil {
			return nil, fmt.Errorf("fake: no profile %q", tok)
		}
		merge(p, req, nil)
		return map[string]any{"updateDeviceProfile": map[string]any{"token": tok}}, nil
	case "ProductSetConfigurationDeclaration":
		p := f.profiles[tok]
		if p == nil {
			return nil, fmt.Errorf("fake: no profile %q", tok)
		}
		p["configurationDeclaration"] = v["keys"]
		return map[string]any{"setDeviceProfileConfigurationDeclaration": map[string]any{"token": tok}}, nil
	case "ProductSetDecoder":
		pt := v["profileToken"].(string)
		p := f.profiles[pt]
		if p == nil {
			return nil, fmt.Errorf("fake: no profile %q", pt)
		}
		p["decoder"] = map[string]any{"document": v["document"]}
		return map[string]any{"setDeviceProfileDecoder": map[string]any{"token": pt}}, nil
	case "ProductClearDecoder":
		pt := v["profileToken"].(string)
		delete(f.profiles[pt], "decoder")
		return map[string]any{"clearDeviceProfileDecoder": map[string]any{"token": pt}}, nil
	case "ProductPublishProfile":
		p := f.profiles[tok]
		if p == nil {
			return nil, fmt.Errorf("fake: no profile %q", tok)
		}
		n, _ := p["_active"].(map[string]any)
		ver := 1
		if n != nil {
			ver = int(n["version"].(float64)) + 1
		}
		snap := f.activeSnapshot(tok)
		snap["version"] = float64(ver)
		p["_active"] = snap
		return map[string]any{"publishDeviceProfile": map[string]any{"version": ver}}, nil

	case "ProductCreateMetric", "ProductCreateCommand", "ProductCreateRule":
		return f.createChild(op, req)
	case "ProductUpdateMetric", "ProductUpdateCommand", "ProductUpdateRule":
		store := f.childStore(op)
		d := store[tok]
		if d == nil {
			return nil, fmt.Errorf("fake: no definition %q", tok)
		}
		merge(d, req, map[string]string{"deviceProfileToken": "_profile"})
		return map[string]any{"update": map[string]any{"token": tok}}, nil
	case "ProductDeleteMetric", "ProductDeleteCommand", "ProductDeleteRule":
		store := f.childStore(op)
		if _, ok := store[tok]; !ok {
			return nil, fmt.Errorf("fake: no definition %q", tok)
		}
		delete(store, tok)
		return map[string]any{"delete": true}, nil

	// ---- connectors ----
	case "ProductCreateConnector":
		t := req["token"].(string)
		if _, dup := f.connectors[t]; dup {
			return nil, f.conflict("connector", t)
		}
		c := clone(req)
		if s, ok := c["secret"].(string); ok {
			if s != "" {
				f.secrets[t] = s
			}
			delete(c, "secret")
		}
		c["updatedAt"] = f.stamp()
		c["_versions"] = 0
		f.connectors[t] = c
		return map[string]any{"createConnector": map[string]any{"token": t, "updatedAt": c["updatedAt"]}}, nil
	case "ProductUpdateConnector":
		c := f.connectors[tok]
		if c == nil {
			return nil, fmt.Errorf("fake: no connector %q", tok)
		}
		if exp, _ := v["expectedUpdatedAt"].(string); exp != "" && exp != c["updatedAt"] {
			return nil, fmt.Errorf("connector was modified by another writer; reload and try again")
		}
		r := clone(req)
		if s, ok := r["secret"]; ok {
			if s == nil || s == "" {
				delete(f.secrets, tok)
			} else {
				f.secrets[tok] = s.(string)
			}
			delete(r, "secret")
		}
		merge(c, r, nil)
		c["updatedAt"] = f.stamp()
		return map[string]any{"updateConnector": map[string]any{"token": tok, "updatedAt": c["updatedAt"]}}, nil
	case "ProductPublishConnector":
		c := f.connectors[tok]
		if c == nil {
			return nil, fmt.Errorf("fake: no connector %q", tok)
		}
		if exp, _ := v["expectedUpdatedAt"].(string); exp != "" && exp != c["updatedAt"] {
			return nil, fmt.Errorf("connector was modified by another writer; reload and try again")
		}
		n, _ := c["_versions"].(int)
		c["_versions"] = n + 1
		return map[string]any{"publishConnector": map[string]any{"version": n + 1}}, nil

	// ---- dashboards ----
	case "ProductCreateDashboard":
		t := req["token"].(string)
		if _, dup := f.dashboards[t]; dup {
			return nil, f.conflict("dashboard", t)
		}
		d := clone(req)
		d["updatedAt"] = f.stamp()
		f.dashboards[t] = d
		return map[string]any{"createDashboard": map[string]any{"token": t, "updatedAt": d["updatedAt"]}}, nil
	case "ProductUpdateDashboard":
		d := f.dashboards[tok]
		if d == nil {
			return nil, fmt.Errorf("fake: no dashboard %q", tok)
		}
		if exp, _ := v["expectedUpdatedAt"].(string); exp != "" && exp != d["updatedAt"] {
			return nil, fmt.Errorf("dashboard was modified by another writer; reload and try again")
		}
		merge(d, req, nil)
		d["updatedAt"] = f.stamp()
		return map[string]any{"updateDashboard": map[string]any{"token": tok, "updatedAt": d["updatedAt"]}}, nil
	case "ProductPublishDashboard":
		d := f.dashboards[tok]
		if d == nil {
			return nil, fmt.Errorf("fake: no dashboard %q", tok)
		}
		if exp, _ := v["expectedUpdatedAt"].(string); exp != "" && exp != d["updatedAt"] {
			return nil, fmt.Errorf("dashboard was modified by another writer; reload and try again")
		}
		ver := 1.0
		if pv, ok := d["_published"].(map[string]any); ok {
			ver = pv["version"].(float64) + 1
		}
		d["_published"] = map[string]any{"version": ver, "definition": d["definition"]}
		return map[string]any{"publishDashboard": map[string]any{"version": map[string]any{"version": ver}}}, nil
	}
	return nil, fmt.Errorf("fake: unhandled operation %s", op)
}

func (f *fakeTenant) childStore(op string) map[string]map[string]any {
	switch {
	case strings.HasSuffix(op, "Metric"):
		return f.metrics
	case strings.HasSuffix(op, "Command"):
		return f.commands
	}
	return f.rules
}

func (f *fakeTenant) createChild(op string, req map[string]any) (any, error) {
	store := f.childStore(op)
	t := req["token"].(string)
	for _, s := range []map[string]map[string]any{f.metrics, f.commands, f.rules} {
		if _, dup := s[t]; dup {
			return nil, f.conflict("definition", t)
		}
	}
	pt, _ := req["deviceProfileToken"].(string)
	if f.profiles[pt] == nil {
		return nil, fmt.Errorf("fake: no profile %q", pt)
	}
	d := clone(req)
	delete(d, "deviceProfileToken")
	d["_profile"] = pt
	if strings.HasSuffix(op, "Rule") {
		if _, ok := d["enabled"]; !ok {
			d["enabled"] = false
		}
	}
	if strings.HasSuffix(op, "Command") {
		if _, ok := d["impact"]; !ok {
			d["impact"] = "NORMAL"
		}
	}
	store[t] = d
	return map[string]any{"create": map[string]any{"token": t}}, nil
}

func (f *fakeTenant) childrenOf(store map[string]map[string]any, profile string) []map[string]any {
	var out []map[string]any
	for _, d := range sorted(store) {
		if d["_profile"] == profile {
			r := clone(d)
			delete(r, "_profile")
			for k := range docFields {
				if x, ok := r[k]; ok {
					r[k] = pretty(x)
				}
			}
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeTenant) profileView(p map[string]any) map[string]any {
	tok := p["token"].(string)
	r := clone(p)
	for k := range r {
		if strings.HasPrefix(k, "_") {
			delete(r, k)
		}
	}
	if _, ok := r["configurationDeclaration"]; !ok {
		r["configurationDeclaration"] = []any{}
	}
	if d, ok := r["decoder"].(map[string]any); ok {
		r["decoder"] = map[string]any{"document": pretty(d["document"])}
	}
	if x, ok := r["metadata"]; ok {
		r["metadata"] = pretty(x)
	}
	var metrics []map[string]any
	for _, m := range f.childrenOf(f.metrics, tok) {
		_, calc := m["expression"]
		m["calculated"] = calc && m["expression"] != nil
		metrics = append(metrics, m)
	}
	r["metricDefinitions"] = nonNil(metrics)
	r["commandDefinitions"] = nonNil(f.childrenOf(f.commands, tok))
	r["detectionRules"] = nonNil(f.childrenOf(f.rules, tok))
	if a, ok := p["_active"].(map[string]any); ok {
		r["activeVersion"] = a["version"]
		r["activeProfileVersion"] = a
	}
	return r
}

func nonNil(s []map[string]any) []map[string]any {
	if s == nil {
		return []map[string]any{}
	}
	return s
}

// activeSnapshot freezes the draft the way a publish does: only what a version carries.
func (f *fakeTenant) activeSnapshot(tok string) map[string]any {
	p := f.profiles[tok]
	var metrics []map[string]any
	for _, m := range f.childrenOf(f.metrics, tok) {
		pm := map[string]any{"metricKey": m["metricKey"], "dataType": m["dataType"], "calculated": m["expression"] != nil}
		for _, k := range []string{"unit", "minValue", "maxValue"} {
			if x, ok := m[k]; ok {
				pm[k] = x
			}
		}
		if e, ok := m["enum"].(string); ok {
			var vals []string
			_ = json.Unmarshal([]byte(e), &vals)
			pm["allowedValues"] = vals
		}
		metrics = append(metrics, pm)
	}
	var commands []map[string]any
	for _, c := range f.childrenOf(f.commands, tok) {
		pc := map[string]any{"commandKey": c["commandKey"], "impact": c["impact"]}
		for _, k := range []string{"name", "description", "parameterSchema"} {
			if x, ok := c[k]; ok {
				pc[k] = x
			}
		}
		commands = append(commands, pc)
	}
	snap := map[string]any{"metrics": nonNil(metrics), "commands": nonNil(commands)}
	if x, ok := p["configurationDeclaration"]; ok {
		snap["configurationDeclaration"] = x
	} else {
		snap["configurationDeclaration"] = []any{}
	}
	if d, ok := p["decoder"]; ok {
		snap["decoder"] = d
	}
	return snap
}
