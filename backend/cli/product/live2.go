// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

func (a apiProfile) toLive() (*LiveProfile, error) {
	var errs []error
	doc := func(what string, s *string) Doc {
		d, err := canonText(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", what, err))
		}
		return d
	}
	p := Profile{
		Kind: string(KindProfile), Token: a.Token, Name: deref(a.Name), Description: deref(a.Description),
		Category: deref(a.Category), Metadata: doc("metadata", a.Metadata), Location: a.Location,
		ConfigurationDeclaration: configKeys(a.ConfigDecl),
	}
	if a.Decoder != nil {
		p.Decoder = doc("decoder", &a.Decoder.Document)
	}
	for _, m := range a.MetricDefs {
		w := "metric " + m.Token
		if m.Calculated {
			p.CalculatedMetrics = append(p.CalculatedMetrics, CalculatedMetric{
				Token: m.Token, Key: m.MetricKey, Name: deref(m.Name), Description: deref(m.Description),
				DataType: m.DataType, Unit: deref(m.Unit), Min: m.MinValue, Max: m.MaxValue,
				Enum: doc(w+" enum", m.Enum), Descriptor: deref(m.Descriptor), Metadata: doc(w+" metadata", m.Metadata),
				Expression: deref(m.Expression),
			})
			continue
		}
		p.Metrics = append(p.Metrics, Metric{
			Token: m.Token, Key: m.MetricKey, Name: deref(m.Name), Description: deref(m.Description),
			DataType: m.DataType, Unit: deref(m.Unit), Min: m.MinValue, Max: m.MaxValue,
			Enum: doc(w+" enum", m.Enum), Descriptor: deref(m.Descriptor), Metadata: doc(w+" metadata", m.Metadata),
		})
	}
	for _, c := range a.CommandDefs {
		w := "command " + c.Token
		p.Commands = append(p.Commands, Command{
			Token: c.Token, Key: c.CommandKey, Name: deref(c.Name), Description: deref(c.Description),
			ParameterSchema: doc(w+" parameterSchema", c.ParameterSchema), Impact: c.Impact,
			Metadata: doc(w+" metadata", c.Metadata),
		})
	}
	for _, r := range a.Rules {
		w := "rule " + r.Token
		p.Rules = append(p.Rules, Rule{
			Token: r.Token, Name: deref(r.Name), Description: deref(r.Description),
			Definition: doc(w+" definition", &r.Definition), AuthoringGraph: doc(w+" authoringGraph", r.AuthoringGraph),
			Enabled: r.Enabled, Metadata: doc(w+" metadata", r.Metadata),
			EntityGroupToken: deref(r.EntityGroupToken), EntityGroupVersion: r.EntityGroupVersion,
		})
	}
	lp := &LiveProfile{Profile: p}
	if a.Active != nil {
		v := &PublishedView{Config: configKeys(a.Active.ConfigDecl)}
		if a.Active.Decoder != nil {
			v.Decoder = doc("published decoder", &a.Active.Decoder.Document)
		}
		for _, m := range a.Active.Metrics {
			pm := pubMetric{Key: m.MetricKey, DataType: m.DataType, Unit: deref(m.Unit), Min: m.MinValue, Max: m.MaxValue, Calculated: m.Calculated}
			if len(m.AllowedValues) > 0 {
				raw, _ := json.Marshal(m.AllowedValues)
				pm.Enum, _ = Canon(raw)
			}
			v.Metrics = append(v.Metrics, pm)
		}
		for _, c := range a.Active.Commands {
			v.Commands = append(v.Commands, pubCommand{
				Key: c.CommandKey, Name: deref(c.Name), Description: deref(c.Description),
				ParameterSchema: doc("published parameterSchema", c.ParameterSchema), Impact: c.Impact,
			})
		}
		v.sort()
		lp.Published = v
	}
	if err := joinErrs(errs); err != nil {
		return nil, err
	}
	// The bundle form is ordered; so is this one.
	b := &Bundle{Profiles: []Profile{lp.Profile}}
	if err := b.Normalize(); err != nil {
		return nil, err
	}
	lp.Profile = b.Profiles[0]
	return lp, nil
}

func joinErrs(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%v", errs)
}

func (v *PublishedView) sort() {
	sort.Slice(v.Config, func(i, j int) bool { return v.Config[i].Key < v.Config[j].Key })
	sort.Slice(v.Metrics, func(i, j int) bool { return v.Metrics[i].Key < v.Metrics[j].Key })
	sort.Slice(v.Commands, func(i, j int) bool { return v.Commands[i].Key < v.Commands[j].Key })
}

// publishedViewOf projects what publishing this profile would freeze, in the shape the live
// active version is read back in.
func publishedViewOf(p Profile) *PublishedView {
	v := &PublishedView{Config: append([]ConfigKey(nil), p.ConfigurationDeclaration...), Decoder: p.Decoder}
	for _, m := range p.Metrics {
		v.Metrics = append(v.Metrics, pubMetric{Key: m.Key, DataType: m.DataType, Unit: m.Unit, Min: m.Min, Max: m.Max, Enum: m.Enum})
	}
	for _, m := range p.CalculatedMetrics {
		v.Metrics = append(v.Metrics, pubMetric{Key: m.Key, DataType: m.DataType, Unit: m.Unit, Min: m.Min, Max: m.Max, Enum: m.Enum, Calculated: true})
	}
	for _, c := range p.Commands {
		v.Commands = append(v.Commands, pubCommand{Key: c.Key, Name: c.Name, Description: c.Description, ParameterSchema: c.ParameterSchema, Impact: c.Impact})
	}
	v.sort()
	return v
}

func (l *Live) loadIntegrations(ctx context.Context, c Client, ep Endpoints) error {
	type apiIntegration struct {
		Token       string  `json:"token"`
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Metadata    *string `json:"metadata"`
		Type        string  `json:"type"`
		Enabled     bool    `json:"enabled"`
		Identity    string  `json:"identity"`
		Generic     *string `json:"generic"`
		LinkMetrics bool    `json:"linkMetrics"`
		DeviceTypes []struct {
			Token string `json:"token"`
		} `json:"deviceTypes"`
	}
	return paged(func(page int) (int, *int, error) {
		var out struct {
			R struct {
				Results    []apiIntegration `json:"results"`
				Pagination pagination       `json:"pagination"`
			} `json:"integrations"`
		}
		vars := map[string]any{"criteria": map[string]any{"pageNumber": page, "pageSize": pageSize}}
		if err := c.Query(ctx, ep.DeviceManagement, qIntegrations, vars, &out); err != nil {
			return 0, nil, areaErr("integrations", err)
		}
		for _, a := range out.R.Results {
			md, err := canonText(a.Metadata)
			if err != nil {
				return 0, nil, fmt.Errorf("integration %q metadata: %w", a.Token, err)
			}
			gen, err := canonText(a.Generic)
			if err != nil {
				return 0, nil, fmt.Errorf("integration %q generic: %w", a.Token, err)
			}
			in := Integration{
				Kind: string(KindIntegration), Token: a.Token, Name: deref(a.Name), Description: deref(a.Description),
				Type: a.Type, Enabled: a.Enabled, Identity: a.Identity, Generic: gen, LinkMetrics: a.LinkMetrics, Metadata: md,
			}
			for _, d := range a.DeviceTypes {
				in.DeviceTypes = append(in.DeviceTypes, d.Token)
			}
			sort.Strings(in.DeviceTypes)
			l.Integrations[in.Token] = in
		}
		return len(out.R.Results), out.R.Pagination.TotalRecords, nil
	})
}

func (l *Live) loadConnectors(ctx context.Context, c Client, ep Endpoints) error {
	type apiConnector struct {
		Token       string  `json:"token"`
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Type        string  `json:"type"`
		Config      string  `json:"config"`
		HasSecret   bool    `json:"hasSecret"`
		UpdatedAt   *string `json:"updatedAt"`
	}
	return paged(func(page int) (int, *int, error) {
		var out struct {
			R struct {
				Results    []apiConnector `json:"results"`
				Pagination pagination     `json:"pagination"`
			} `json:"connectors"`
		}
		vars := map[string]any{"criteria": map[string]any{"pageNumber": page, "pageSize": pageSize}}
		if err := c.Query(ctx, ep.Connectors, qConnectors, vars, &out); err != nil {
			return 0, nil, areaErr("connectors", err)
		}
		for _, a := range out.R.Results {
			cfg, err := Canon([]byte(a.Config))
			if err != nil {
				return 0, nil, fmt.Errorf("connector %q config: %w", a.Token, err)
			}
			lc := &LiveConnector{
				Connector: Connector{
					Kind: string(KindConnector), Token: a.Token, Name: deref(a.Name), Description: deref(a.Description),
					Type: a.Type, Config: cfg,
				},
				HasSecret: a.HasSecret, UpdatedAt: deref(a.UpdatedAt),
			}
			if a.HasSecret {
				lc.Credential = &Credential{Handle: "env:" + envName(a.Token)}
			}
			var vout struct {
				V []struct {
					Version     int    `json:"version"`
					PublishedAt string `json:"publishedAt"`
				} `json:"connectorVersions"`
			}
			if err := c.Query(ctx, ep.Connectors, qConnectorVersions, map[string]any{"token": a.Token}, &vout); err != nil {
				return 0, nil, fmt.Errorf("reading connector %q versions: %w", a.Token, err)
			}
			lc.Published = len(vout.V) > 0
			if lc.Published {
				lc.PublishedAt = vout.V[0].PublishedAt
			}
			l.Connectors[lc.Token] = lc
		}
		return len(out.R.Results), out.R.Pagination.TotalRecords, nil
	})
}

// loadDashboards reads the tenant's dashboards: all of them, or only the tokens named.
func (l *Live) loadDashboards(ctx context.Context, c Client, ep Endpoints, only map[string]bool) error {
	var tokens []string
	err := paged(func(page int) (int, *int, error) {
		var out struct {
			R struct {
				Results []struct {
					Token string `json:"token"`
				} `json:"results"`
				Pagination pagination `json:"pagination"`
			} `json:"dashboards"`
		}
		vars := map[string]any{"criteria": map[string]any{"pageNumber": page, "pageSize": pageSize}}
		if err := c.Query(ctx, ep.Dashboards, qDashboards, vars, &out); err != nil {
			return 0, nil, areaErr("dashboards", err)
		}
		for _, r := range out.R.Results {
			tokens = append(tokens, r.Token)
		}
		return len(out.R.Results), out.R.Pagination.TotalRecords, nil
	})
	if err != nil {
		return err
	}
	l.DashboardTokens = tokens
	for _, tok := range tokens {
		if only != nil && !only[tok] {
			continue
		}
		var d struct {
			D *struct {
				Token            string  `json:"token"`
				Name             *string `json:"name"`
				Description      *string `json:"description"`
				Definition       string  `json:"definition"`
				UpdatedAt        *string `json:"updatedAt"`
				PublishedVersion *int    `json:"publishedVersion"`
			} `json:"dashboard"`
		}
		if err := c.Query(ctx, ep.Dashboards, qDashboard, map[string]any{"token": tok}, &d); err != nil {
			return fmt.Errorf("reading dashboard %q: %w", tok, err)
		}
		if d.D == nil {
			continue
		}
		def, err := Canon([]byte(d.D.Definition))
		if err != nil {
			return fmt.Errorf("dashboard %q definition: %w", tok, err)
		}
		ld := &LiveDashboard{
			Dashboard: Dashboard{Kind: string(KindDashboard), Token: tok, Name: deref(d.D.Name), Description: deref(d.D.Description), Definition: def},
			UpdatedAt: deref(d.D.UpdatedAt),
		}
		if d.D.PublishedVersion != nil {
			var p struct {
				P *struct {
					Definition string `json:"definition"`
				} `json:"publishedDashboard"`
			}
			if err := c.Query(ctx, ep.Dashboards, qPublishedDashboard, map[string]any{"token": tok}, &p); err != nil {
				return fmt.Errorf("reading dashboard %q published version: %w", tok, err)
			}
			if p.P != nil {
				ld.Published = true
				ld.PublishedDefinition, err = Canon([]byte(p.P.Definition))
				if err != nil {
					return fmt.Errorf("dashboard %q published definition: %w", tok, err)
				}
			}
		}
		l.Dashboards[tok] = ld
	}
	return nil
}

// Bundle projects the live snapshot into a bundle -- the export.
func (l *Live) Bundle() (*Bundle, error) {
	b := &Bundle{}
	for _, t := range l.DeviceTypes {
		b.DeviceTypes = append(b.DeviceTypes, t)
	}
	for _, p := range l.Profiles {
		b.Profiles = append(b.Profiles, p.Profile)
	}
	for _, in := range l.Integrations {
		b.Integrations = append(b.Integrations, in)
	}
	for _, c := range l.Connectors {
		b.Connectors = append(b.Connectors, c.Connector)
	}
	for _, d := range l.Dashboards {
		b.Dashboards = append(b.Dashboards, d.Dashboard)
	}
	if err := b.Normalize(); err != nil {
		return nil, err
	}
	return b, nil
}
