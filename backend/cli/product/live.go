// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// Client is the GraphQL seam this package drives. *userclient.TenantSession satisfies it,
// which is what carries the caller's own tenant access token: the server authorizes every
// call as the person who ran the command.
type Client interface {
	Query(ctx context.Context, baseURL, query string, variables map[string]any, out any) error
}

// Endpoints are the tenant GraphQL URLs the product spans, one per area.
type Endpoints struct {
	DeviceManagement string
	Connectors       string
	Dashboards       string
}

// AllKinds are the kinds of top-level resource a bundle can carry, as --kinds spells them.
var AllKinds = []string{"device-types", "profiles", "integrations", "connectors", "dashboards"}

// KindSet is a set of the AllKinds names.
type KindSet map[string]bool

// ParseKinds reads a comma-separated --kinds value; empty means all of them.
func ParseKinds(s string) (KindSet, error) {
	set := KindSet{}
	if strings.TrimSpace(s) == "" {
		for _, k := range AllKinds {
			set[k] = true
		}
		return set, nil
	}
	for _, k := range strings.Split(s, ",") {
		k = strings.TrimSpace(k)
		ok := false
		for _, a := range AllKinds {
			ok = ok || a == k
		}
		if !ok {
			return nil, fmt.Errorf("unknown kind %q: expected a comma-separated list of %s", k, strings.Join(AllKinds, ", "))
		}
		set[k] = true
	}
	return set, nil
}

// ErrAreaUnavailable marks a kind whose area this instance does not serve (an opt-in area
// left out of the profile): the endpoint answers 404.
var ErrAreaUnavailable = errors.New("this instance does not serve that area")

func areaErr(kind string, err error) error {
	var se *userclient.StatusError
	if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s: %w (enable the area, or leave %s out of --kinds)", kind, ErrAreaUnavailable, kind)
	}
	return fmt.Errorf("reading %s: %w", kind, err)
}

const pageSize = 500

// paged runs fn for page 1, 2, ... until every record has been seen.
func paged(fn func(page int) (got int, total *int, err error)) error {
	seen := 0
	for page := 1; page < 10000; page++ {
		got, total, err := fn(page)
		if err != nil {
			return err
		}
		seen += got
		if got == 0 || total == nil || seen >= *total {
			return nil
		}
	}
	return errors.New("a listing did not end; refusing to read further")
}

type pagination struct {
	TotalRecords *int `json:"totalRecords"`
}

// ---- documents ---------------------------------------------------------------------------

const (
	qDeviceTypes = `query ProductDeviceTypes($criteria:DeviceTypeSearchCriteria!){` +
		`deviceTypes(criteria:$criteria){results{token name description imageUrl icon backgroundColor ` +
		`foregroundColor borderColor profile{token} maintenanceWindow{token} manufacturer model metadata} ` +
		`pagination{totalRecords}}}`

	qProfiles = `query ProductProfiles($criteria:DeviceProfileSearchCriteria!){` +
		`deviceProfiles(criteria:$criteria){results{token name description category metadata activeVersion ` +
		`location{expectedAccuracyMeters expectedUpdateIntervalSeconds} ` +
		`configurationDeclaration{key valueType description} decoder{document} ` +
		`metricDefinitions{token metricKey name description dataType unit minValue maxValue enum descriptor metadata expression calculated} ` +
		`commandDefinitions{token commandKey name description parameterSchema impact metadata} ` +
		`detectionRules{token name description definition authoringGraph enabled metadata entityGroupToken entityGroupVersion} ` +
		`activeProfileVersion{version configurationDeclaration{key valueType description} decoder{document} ` +
		`metrics{metricKey dataType unit minValue maxValue allowedValues calculated} ` +
		`commands{commandKey name description parameterSchema impact}}} ` +
		`pagination{totalRecords}}}`

	qIntegrations = `query ProductIntegrations($criteria:IntegrationSearchCriteria!){` +
		`integrations(criteria:$criteria){results{token name description metadata type enabled identity ` +
		`generic linkMetrics deviceTypes{token}} pagination{totalRecords}}}`

	qConnectors = `query ProductConnectors($criteria:ConnectorSearchCriteria!){` +
		`connectors(criteria:$criteria){results{token name description type config hasSecret updatedAt} ` +
		`pagination{totalRecords}}}`
	qConnectorVersions = `query ProductConnectorVersions($token:String!){` +
		`connectorVersions(token:$token,limit:1){version}}`

	qDashboards = `query ProductDashboards($criteria:DashboardSearchCriteria!){` +
		`dashboards(criteria:$criteria){results{token publishedVersion} pagination{totalRecords}}}`
	qDashboard = `query ProductDashboard($token:String!){` +
		`dashboard(token:$token){token name description definition updatedAt publishedVersion}}`
	qPublishedDashboard = `query ProductPublishedDashboard($token:String!){` +
		`publishedDashboard(token:$token){version definition}}`
)

// ---- the live snapshot -------------------------------------------------------------------

// PublishedView is the part of a profile the active published version freezes and the API
// lets us read back, in the same normalized shape a bundle's profile is projected into. It
// exists so apply publishes when the content devices actually see differs from the bundle,
// not only when this run changed the draft.
type PublishedView struct {
	Config   []ConfigKey
	Decoder  Doc
	Metrics  []pubMetric
	Commands []pubCommand
}

type pubMetric struct {
	Key        string
	DataType   string
	Unit       string
	Min, Max   *float64
	Enum       Doc
	Calculated bool
}

type pubCommand struct {
	Key, Name, Description string
	ParameterSchema        Doc
	Impact                 string
}

// LiveProfile is a profile as the tenant holds it.
type LiveProfile struct {
	Profile
	// Published is nil until the profile's first publish.
	Published *PublishedView
}

// LiveConnector is a connector as the tenant holds it, with what apply needs beyond the
// bundle's shape.
type LiveConnector struct {
	Connector
	HasSecret bool
	// UpdatedAt is the draft's version stamp, sent back as expectedUpdatedAt.
	UpdatedAt string
	Published bool
}

// LiveDashboard is a dashboard as the tenant holds it.
type LiveDashboard struct {
	Dashboard
	UpdatedAt string
	// PublishedDefinition is nil until the dashboard is first published.
	PublishedDefinition Doc
	Published           bool
}

// Live is a read of the tenant's product. Only the kinds asked for are populated.
type Live struct {
	DeviceTypes  map[string]DeviceType
	Profiles     map[string]*LiveProfile
	Integrations map[string]Integration
	Connectors   map[string]*LiveConnector
	Dashboards   map[string]*LiveDashboard
	// Skipped lists kinds left out because the instance does not serve their area.
	Skipped []string
}

// Load reads the requested kinds. With tolerant set, a kind whose area the instance does not
// serve is skipped and recorded in Skipped instead of failing the read.
//
// dashboards names the dashboards whose definitions are read; nil reads every one. A dashboard's
// definition costs a call each, so a plan reads only the ones its bundle carries.
func Load(ctx context.Context, c Client, ep Endpoints, kinds KindSet, tolerant bool, dashboards map[string]bool) (*Live, error) {
	l := &Live{
		DeviceTypes: map[string]DeviceType{}, Profiles: map[string]*LiveProfile{},
		Integrations: map[string]Integration{}, Connectors: map[string]*LiveConnector{},
		Dashboards: map[string]*LiveDashboard{},
	}
	steps := []struct {
		name string
		do   func() error
	}{
		{"device-types", func() error { return l.loadDeviceTypes(ctx, c, ep) }},
		{"profiles", func() error { return l.loadProfiles(ctx, c, ep) }},
		{"integrations", func() error { return l.loadIntegrations(ctx, c, ep) }},
		{"connectors", func() error { return l.loadConnectors(ctx, c, ep) }},
		{"dashboards", func() error { return l.loadDashboards(ctx, c, ep, dashboards) }},
	}
	for _, s := range steps {
		if !kinds[s.name] {
			continue
		}
		if err := s.do(); err != nil {
			if tolerant && errors.Is(err, ErrAreaUnavailable) {
				l.Skipped = append(l.Skipped, s.name)
				continue
			}
			return nil, err
		}
	}
	return l, nil
}

type apiDeviceType struct {
	Token             string                  `json:"token"`
	Name              *string                 `json:"name"`
	Description       *string                 `json:"description"`
	ImageURL          *string                 `json:"imageUrl"`
	Icon              *string                 `json:"icon"`
	BackgroundColor   *string                 `json:"backgroundColor"`
	ForegroundColor   *string                 `json:"foregroundColor"`
	BorderColor       *string                 `json:"borderColor"`
	Profile           *struct{ Token string } `json:"profile"`
	MaintenanceWindow *struct{ Token string } `json:"maintenanceWindow"`
	Manufacturer      *string                 `json:"manufacturer"`
	Model             *string                 `json:"model"`
	Metadata          *string                 `json:"metadata"`
}

func (l *Live) loadDeviceTypes(ctx context.Context, c Client, ep Endpoints) error {
	return areaWrap("device-types", paged(func(page int) (int, *int, error) {
		var out struct {
			R struct {
				Results    []apiDeviceType `json:"results"`
				Pagination pagination      `json:"pagination"`
			} `json:"deviceTypes"`
		}
		vars := map[string]any{"criteria": map[string]any{"pageNumber": page, "pageSize": pageSize}}
		if err := c.Query(ctx, ep.DeviceManagement, qDeviceTypes, vars, &out); err != nil {
			return 0, nil, areaErr("device-types", err)
		}
		for _, a := range out.R.Results {
			md, err := canonText(a.Metadata)
			if err != nil {
				return 0, nil, fmt.Errorf("device type %q metadata: %w", a.Token, err)
			}
			t := DeviceType{
				Kind: string(KindDeviceType), Token: a.Token, Name: deref(a.Name), Description: deref(a.Description),
				ImageURL: deref(a.ImageURL), Icon: deref(a.Icon), BackgroundColor: deref(a.BackgroundColor),
				ForegroundColor: deref(a.ForegroundColor), BorderColor: deref(a.BorderColor),
				Manufacturer: deref(a.Manufacturer), Model: deref(a.Model), Metadata: md,
			}
			if a.Profile != nil {
				t.Profile = a.Profile.Token
			}
			if a.MaintenanceWindow != nil {
				t.MaintenanceWindow = a.MaintenanceWindow.Token
			}
			l.DeviceTypes[t.Token] = t
		}
		return len(out.R.Results), out.R.Pagination.TotalRecords, nil
	}))
}

// areaWrap leaves an error already classified by areaErr alone.
func areaWrap(_ string, err error) error { return err }

type apiConfigKey struct {
	Key         string  `json:"key"`
	ValueType   string  `json:"valueType"`
	Description *string `json:"description"`
}

type apiDecoder struct {
	Document string `json:"document"`
}

func configKeys(in []apiConfigKey) []ConfigKey {
	var out []ConfigKey
	for _, k := range in {
		out = append(out, ConfigKey{Key: k.Key, ValueType: k.ValueType, Description: deref(k.Description)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

type apiProfile struct {
	Token         string         `json:"token"`
	Name          *string        `json:"name"`
	Description   *string        `json:"description"`
	Category      *string        `json:"category"`
	Metadata      *string        `json:"metadata"`
	ActiveVersion *int           `json:"activeVersion"`
	Location      *Location      `json:"location"`
	ConfigDecl    []apiConfigKey `json:"configurationDeclaration"`
	Decoder       *apiDecoder    `json:"decoder"`
	MetricDefs    []struct {
		Token       string   `json:"token"`
		MetricKey   string   `json:"metricKey"`
		Name        *string  `json:"name"`
		Description *string  `json:"description"`
		DataType    string   `json:"dataType"`
		Unit        *string  `json:"unit"`
		MinValue    *float64 `json:"minValue"`
		MaxValue    *float64 `json:"maxValue"`
		Enum        *string  `json:"enum"`
		Descriptor  *string  `json:"descriptor"`
		Metadata    *string  `json:"metadata"`
		Expression  *string  `json:"expression"`
		Calculated  bool     `json:"calculated"`
	} `json:"metricDefinitions"`
	CommandDefs []struct {
		Token           string  `json:"token"`
		CommandKey      string  `json:"commandKey"`
		Name            *string `json:"name"`
		Description     *string `json:"description"`
		ParameterSchema *string `json:"parameterSchema"`
		Impact          string  `json:"impact"`
		Metadata        *string `json:"metadata"`
	} `json:"commandDefinitions"`
	Rules []struct {
		Token              string  `json:"token"`
		Name               *string `json:"name"`
		Description        *string `json:"description"`
		Definition         string  `json:"definition"`
		AuthoringGraph     *string `json:"authoringGraph"`
		Enabled            bool    `json:"enabled"`
		Metadata           *string `json:"metadata"`
		EntityGroupToken   *string `json:"entityGroupToken"`
		EntityGroupVersion *int    `json:"entityGroupVersion"`
	} `json:"detectionRules"`
	Active *struct {
		Version    int            `json:"version"`
		ConfigDecl []apiConfigKey `json:"configurationDeclaration"`
		Decoder    *apiDecoder    `json:"decoder"`
		Metrics    []struct {
			MetricKey     string   `json:"metricKey"`
			DataType      string   `json:"dataType"`
			Unit          *string  `json:"unit"`
			MinValue      *float64 `json:"minValue"`
			MaxValue      *float64 `json:"maxValue"`
			AllowedValues []string `json:"allowedValues"`
			Calculated    bool     `json:"calculated"`
		} `json:"metrics"`
		Commands []struct {
			CommandKey      string  `json:"commandKey"`
			Name            *string `json:"name"`
			Description     *string `json:"description"`
			ParameterSchema *string `json:"parameterSchema"`
			Impact          string  `json:"impact"`
		} `json:"commands"`
	} `json:"activeProfileVersion"`
}

func (l *Live) loadProfiles(ctx context.Context, c Client, ep Endpoints) error {
	return paged(func(page int) (int, *int, error) {
		var out struct {
			R struct {
				Results    []apiProfile `json:"results"`
				Pagination pagination   `json:"pagination"`
			} `json:"deviceProfiles"`
		}
		vars := map[string]any{"criteria": map[string]any{"pageNumber": page, "pageSize": pageSize}}
		if err := c.Query(ctx, ep.DeviceManagement, qProfiles, vars, &out); err != nil {
			return 0, nil, areaErr("profiles", err)
		}
		for _, a := range out.R.Results {
			p, err := a.toLive()
			if err != nil {
				return 0, nil, fmt.Errorf("profile %q: %w", a.Token, err)
			}
			l.Profiles[p.Token] = p
		}
		return len(out.R.Results), out.R.Pagination.TotalRecords, nil
	})
}
