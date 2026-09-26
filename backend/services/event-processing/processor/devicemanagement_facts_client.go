// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// factService names which peer a reconcile query goes to.
type factService int

const (
	factDeviceManagement factService = iota
	factUserManagement
)

// factExec runs one query against a peer for a tenant and decodes its "data" object into out.
//
// It is the ONE thing deviceManagementFactsClient needs from a transport, which is what lets a
// test drive the paging, the page halving and the decoding against device-management's REAL
// schema and resolvers in-process — faking the HTTP hop and nothing else — so a field-name drift
// between the two services fails a test instead of decoding to an empty roster in production. An
// implementation MUST report a response it could not carry as svcclient.ErrResponseTooLarge
// (errors.Is-comparable): that is the signal the rule walk halves its page on.
type factExec func(ctx context.Context, svc factService, tenant, query string, vars map[string]any, out any) error

// deviceManagementFactsClient implements DeviceManagementFacts over service-token GraphQL: the
// tenant list from user-management, the rules, roster and attributes from device-management.
type deviceManagementFactsClient struct {
	client *svcclient.Client
	dmURL  string
	umURL  string
	// listScope is the tenant header the cross-tenant tenantTokens call is made under. The query
	// is not about any one tenant, but the client's calling convention requires one, and the
	// resolver authorizes on the authority's tier rather than on this value.
	listScope string
	// transport, when non-nil, carries a query INSTEAD of the service-token HTTP call (see
	// factExec). Nil in production.
	transport factExec
}

// NewDeviceManagementFactsClient builds the reconcile seam over a service-token client carrying
// tenant:read (the tenant list) and device:read (the three device-management doors).
func NewDeviceManagementFactsClient(client *svcclient.Client, dmURL, umURL, listScope string) DeviceManagementFacts {
	return &deviceManagementFactsClient{client: client, dmURL: dmURL, umURL: umURL, listScope: listScope}
}

func (c *deviceManagementFactsClient) exec(ctx context.Context, svc factService, tenant, query string,
	vars map[string]any, out any) error {
	if c.transport != nil {
		return c.transport(ctx, svc, tenant, query, vars, out)
	}
	url := c.dmURL
	if svc == factUserManagement {
		url = c.umURL
	}
	return c.client.Query(ctx, url, tenant, query, vars, out)
}

// errFactsSkew reports that device-management does not serve the reconcile doors — it runs a
// build from before them, which a rolling upgrade does briefly. It is a distinct error so the log
// says "look at what is deployed" rather than "look at the network"; it is still an ordinary
// failure (counted, retried next sweep), because nothing here should work around it.
var errFactsSkew = errors.New("device-management does not serve the detection reconcile reads (it is running an older build; expected briefly during a rolling upgrade)")

// classifyFactsError wraps a peer error as errFactsSkew when it is the peer not knowing a door.
func classifyFactsError(err error) error {
	if peerLacksQuery(err, "activeProfileRules", "deviceRosterPage", "deviceThresholdAttributePage") {
		return fmt.Errorf("%w: %v", errFactsSkew, err)
	}
	return err
}

const tenantTokensQuery = `query { tenantTokens }`

// Tenants lists every tenant on the instance.
func (c *deviceManagementFactsClient) Tenants(ctx context.Context) ([]string, error) {
	var out struct {
		TenantTokens []string `json:"tenantTokens"`
	}
	if err := c.exec(ctx, factUserManagement, c.listScope, tenantTokensQuery, nil, &out); err != nil {
		return nil, fmt.Errorf("listing tenants for the fact reconcile: %w", err)
	}
	return out.TenantTokens, nil
}

const activeProfileRulesQuery = `query($afterId: String, $limit: Int!) {
  activeProfileRules(afterId: $afterId, limit: $limit) {
    entries {
      profileToken
      versionToken
      activeSince
      rules { token definition entityGroupToken entityGroupVersion }
    }
    nextCursor
  }
}`

// The decoded wire shapes. They are NAMED types so a test can decode a response produced by
// device-management's real schema through exactly the mapping this client uses.
type publishedRulePayload struct {
	Token              string `json:"token"`
	Definition         string `json:"definition"`
	EntityGroupToken   string `json:"entityGroupToken"`
	EntityGroupVersion int32  `json:"entityGroupVersion"`
}

type activeProfilePayload struct {
	ProfileToken string                 `json:"profileToken"`
	VersionToken string                 `json:"versionToken"`
	ActiveSince  string                 `json:"activeSince"`
	Rules        []publishedRulePayload `json:"rules"`
}

type activeProfileRulesResponse struct {
	ActiveProfileRules struct {
		Entries    []activeProfilePayload `json:"entries"`
		NextCursor *string                `json:"nextCursor"`
	} `json:"activeProfileRules"`
}

// ActiveProfiles walks every page of the tenant's published profiles.
//
// Its page is the only one whose byte size is not a function of its row count — each profile
// carries all of its rules — so a response the service client refuses as too large HALVES the
// page and retries the same cursor, down to a single profile; a single profile too large to
// carry is an error naming where the walk stood. The page size returns to the maximum after a
// page that fits.
func (c *deviceManagementFactsClient) ActiveProfiles(ctx context.Context, tenant string) ([]dmmodel.ActiveProfileRules, error) {
	var all []dmmodel.ActiveProfileRules
	var cursor *string
	limit := dmmodel.MaxActiveProfileRulesPageSize
	for {
		var out activeProfileRulesResponse
		err := c.exec(ctx, factDeviceManagement, tenant, activeProfileRulesQuery,
			map[string]any{"afterId": cursor, "limit": limit}, &out)
		if errors.Is(err, svcclient.ErrResponseTooLarge) {
			if limit == 1 {
				return nil, fmt.Errorf("one published profile's rules after cursor %v exceed the service response cap: %w",
					cursorText(cursor), err)
			}
			limit /= 2
			continue
		}
		if err != nil {
			return nil, classifyFactsError(err)
		}
		for _, e := range out.ActiveProfileRules.Entries {
			since, err := time.Parse(time.RFC3339Nano, e.ActiveSince)
			if err != nil {
				return nil, fmt.Errorf("profile %q: unreadable activeSince %q: %w", e.ProfileToken, e.ActiveSince, err)
			}
			rules := make([]dmmodel.PublishedDetectionRule, 0, len(e.Rules))
			for _, r := range e.Rules {
				rules = append(rules, dmmodel.PublishedDetectionRule(r))
			}
			all = append(all, dmmodel.ActiveProfileRules{
				ProfileToken: e.ProfileToken, VersionToken: e.VersionToken, ActiveSince: since, Rules: rules,
			})
		}
		if out.ActiveProfileRules.NextCursor == nil {
			return all, nil
		}
		cursor = out.ActiveProfileRules.NextCursor
		limit = dmmodel.MaxActiveProfileRulesPageSize
	}
}

const deviceRosterPageQuery = `query($afterId: String, $limit: Int!) {
  deviceRosterPage(afterId: $afterId, limit: $limit) {
    entries { deviceToken profileToken expectedSince }
    nextCursor
  }
}`

type rosterEntryPayload struct {
	DeviceToken   string `json:"deviceToken"`
	ProfileToken  string `json:"profileToken"`
	ExpectedSince string `json:"expectedSince"`
}

type deviceRosterPageResponse struct {
	DeviceRosterPage struct {
		Entries    []rosterEntryPayload `json:"entries"`
		NextCursor *string              `json:"nextCursor"`
	} `json:"deviceRosterPage"`
}

// Roster walks every page of the tenant's device roster.
func (c *deviceManagementFactsClient) Roster(ctx context.Context, tenant string) ([]dmmodel.DeviceRosterEntry, error) {
	var all []dmmodel.DeviceRosterEntry
	var cursor *string
	for {
		var out deviceRosterPageResponse
		if err := c.exec(ctx, factDeviceManagement, tenant, deviceRosterPageQuery,
			map[string]any{"afterId": cursor, "limit": dmmodel.MaxRosterPageSize}, &out); err != nil {
			return nil, classifyFactsError(err)
		}
		for _, e := range out.DeviceRosterPage.Entries {
			since, err := time.Parse(time.RFC3339Nano, e.ExpectedSince)
			if err != nil {
				return nil, fmt.Errorf("device %q: unreadable expectedSince %q: %w", e.DeviceToken, e.ExpectedSince, err)
			}
			all = append(all, dmmodel.DeviceRosterEntry{
				DeviceToken: e.DeviceToken, ProfileToken: e.ProfileToken, ExpectedSince: since,
			})
		}
		if out.DeviceRosterPage.NextCursor == nil {
			return all, nil
		}
		cursor = out.DeviceRosterPage.NextCursor
	}
}

const deviceThresholdAttributePageQuery = `query($afterId: String, $limit: Int!) {
  deviceThresholdAttributePage(afterId: $afterId, limit: $limit) {
    entries { deviceToken scope key value updatedAt }
    nextCursor
  }
}`

type thresholdAttributePayload struct {
	DeviceToken string  `json:"deviceToken"`
	Scope       string  `json:"scope"`
	Key         string  `json:"key"`
	Value       float64 `json:"value"`
	UpdatedAt   string  `json:"updatedAt"`
}

type deviceThresholdAttributePageResponse struct {
	DeviceThresholdAttributePage struct {
		Entries    []thresholdAttributePayload `json:"entries"`
		NextCursor *string                     `json:"nextCursor"`
	} `json:"deviceThresholdAttributePage"`
}

// ThresholdAttributes walks every page of the tenant's threshold attributes.
func (c *deviceManagementFactsClient) ThresholdAttributes(ctx context.Context, tenant string) ([]dmmodel.DeviceThresholdAttribute, error) {
	var all []dmmodel.DeviceThresholdAttribute
	var cursor *string
	for {
		var out deviceThresholdAttributePageResponse
		if err := c.exec(ctx, factDeviceManagement, tenant, deviceThresholdAttributePageQuery,
			map[string]any{"afterId": cursor, "limit": dmmodel.MaxThresholdAttributePageSize}, &out); err != nil {
			return nil, classifyFactsError(err)
		}
		for _, e := range out.DeviceThresholdAttributePage.Entries {
			at, err := time.Parse(time.RFC3339Nano, e.UpdatedAt)
			if err != nil {
				return nil, fmt.Errorf("device %q attribute %q: unreadable updatedAt %q: %w", e.DeviceToken, e.Key, e.UpdatedAt, err)
			}
			all = append(all, dmmodel.DeviceThresholdAttribute{
				DeviceToken: e.DeviceToken, Scope: e.Scope, AttrKey: e.Key, Value: e.Value, UpdatedAt: at,
			})
		}
		if out.DeviceThresholdAttributePage.NextCursor == nil {
			return all, nil
		}
		cursor = out.DeviceThresholdAttributePage.NextCursor
	}
}

// cursorText renders a walk position for an error message.
func cursorText(cursor *string) string {
	if cursor == nil {
		return "(start)"
	}
	return *cursor
}
