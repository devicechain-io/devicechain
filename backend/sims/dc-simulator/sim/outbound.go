// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// The scenario's destinations OUT of the tenant (a rule's signed webhook and its publish through
// an outbound connector, a notification channel and its policy) and its one way IN besides the
// HTTP ingress (an inbound integration a network server posts LoRaWAN uplinks through).
//
// Each is a static singleton the manifest declares and Provision converges, in the same
// check-then-create-or-update shape as everything in bootstrap.go. None of them carries a
// credential this process keeps: the webhook signing secret is minted by the platform and shown
// once (Provision mints it when the handle has none and discards it; a verifier that needs it
// rotates its own), and the integration's bearer token is likewise shown once, so Provision
// rotates it on every run and keeps it in memory for the network server this process plays.

// TenantPlaceholder is replaced by the sim's tenant id wherever a destination's settings carry it,
// so that several sims sharing one sink can be told apart there (an MQTT topic, a mail recipient).
// A rule definition never carries it: rules are published content gated by a committed fixture,
// and must not vary with the tenant.
const TenantPlaceholder = "{tenant}"

// ConnectorSpec is one outbound connector (outbound-connectors createConnector). Config is the
// connector's settings as the JSON object the platform takes; TenantPlaceholder in it is replaced
// by the tenant id. Provision publishes it, because delivery uses the latest PUBLISHED version and
// a rule naming a never-published connector dead-letters its first delivery.
type ConnectorSpec struct {
	Token  string
	Name   string
	Type   string
	Config string
	Secret string
}

// NotificationChannelSpec is one notification channel (notification-management). Config is the
// channel's settings JSON; TenantPlaceholder in it is replaced by the tenant id. No channel here
// carries a secret.
type NotificationChannelSpec struct {
	Token       string
	Name        string
	ChannelType string
	Config      string
}

// NotificationPolicySpec is one notification policy: which alarm severities page which channel,
// and how long an unacknowledged alarm waits before it is paged again.
type NotificationPolicySpec struct {
	Token string
	Name  string
	// EscalateAfterSeconds re-pages an alarm still ACTIVE and unacknowledged this long after its
	// last page; 0 is no escalation.
	EscalateAfterSeconds int
	// MaxEscalations caps the re-pages; 0 leaves the service's default.
	MaxEscalations int
	Rules          []NotificationRuleSpec
}

// NotificationRuleSpec is one policy rule. Recipients may carry TenantPlaceholder.
type NotificationRuleSpec struct {
	Severity     string
	ChannelToken string
	Recipients   []string
}

// IntegrationSpec is one inbound integration (device-management createIntegration): a network
// server presents its bearer token and posts uplinks of devices whose types it is scoped to.
type IntegrationSpec struct {
	Token            string
	Name             string
	Type             string
	DeviceTypeTokens []string
}

// The wire vocabulary this file sends. Hand mirrors, like the rest of the module's (the owners are
// services this untrusted client cannot import); each is SENT, so a drift fails at the write.
const (
	connectorTypeMQTT          = "mqtt"
	notificationChannelSMTP    = "smtp"
	integrationTypeChirpStack4 = "CHIRPSTACK_V4"
	// AliasSchemeDevEUI is the alias scheme a ChirpStack integration resolves an uplink by.
	AliasSchemeDevEUI = "lorawan.deveui"

	actionTypeHTTPCall = "httpCall"
	actionTypePublish  = "publish"
)

func withTenant(s, tenant string) string { return strings.ReplaceAll(s, TenantPlaceholder, tenant) }

// ruleDestinations reads the signing handles a rule's httpCall actions sign with and the connector
// tokens its publish actions name, at the depth this package looks (see ruleSendCommandKeys).
func ruleDestinations(definition string) (handles, connectors []string) {
	var rule struct {
		Actions []struct {
			Type     string `json:"type"`
			HTTPCall struct {
				SigningSecretRef string `json:"signingSecretRef"`
			} `json:"httpCall"`
			Publish struct {
				ConnectorRef string `json:"connectorRef"`
			} `json:"publish"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(definition), &rule); err != nil {
		return nil, nil
	}
	for _, a := range rule.Actions {
		switch a.Type {
		case actionTypeHTTPCall:
			if a.HTTPCall.SigningSecretRef != "" {
				handles = append(handles, a.HTTPCall.SigningSecretRef)
			}
		case actionTypePublish:
			connectors = append(connectors, a.Publish.ConnectorRef)
		}
	}
	return handles, connectors
}

// validateRuleDestinations refuses a rule whose httpCall signs with a handle, or whose publish
// names a connector, that the manifest does not provision. The platform accepts both at publish
// (it checks neither), so the first delivery is where they would fail: a terminal dead letter
// several services away, which a tenant cannot read.
func (m SimManifest) validateRuleDestinations(r DetectionRuleSpec) error {
	handles, connectors := ruleDestinations(r.Definition)
	for _, h := range handles {
		if !sliceContains(m.WebhookSigningHandles, h) {
			return fmt.Errorf("detection rule %q signs its webhook with handle %q, which the manifest does not "+
				"provision: every delivery would be refused rather than sent unsigned", r.Token, h)
		}
	}
	for _, c := range connectors {
		found := false
		for _, spec := range m.Connectors {
			found = found || spec.Token == c
		}
		if !found {
			return fmt.Errorf("detection rule %q publishes through connector %q, which the manifest does not "+
				"provision: its first delivery would be dead-lettered", r.Token, c)
		}
	}
	return nil
}

// validateOutbound checks the destinations and integrations the manifest declares.
func (m SimManifest) validateOutbound(typeTokens map[string]bool) error {
	devices := map[string]bool{}
	for _, d := range m.Expand(m.Seed) {
		devices[d.Token] = true
	}
	attributes := map[string]bool{}
	for _, a := range m.Attributes {
		key := a.Device + "/" + a.Key
		if !devices[a.Device] || attributes[key] {
			return fmt.Errorf("attribute %s has unknown device or duplicate natural key", key)
		}
		if err := core.ValidateToken(a.Key); err != nil {
			return err
		}
		if math.IsNaN(a.Value) || math.IsInf(a.Value, 0) {
			return fmt.Errorf("attribute %s is not finite", key)
		}
		attributes[key] = true
	}
	seen := map[string]bool{}
	for _, h := range m.WebhookSigningHandles {
		if h == "" || strings.HasPrefix(h, "/") || seen["handle/"+h] {
			return fmt.Errorf("webhook signing handle %q is empty, absolute or declared twice", h)
		}
		seen["handle/"+h] = true
	}
	for _, c := range m.Connectors {
		if err := validateTokenOnce(seen, "connector", c.Token); err != nil {
			return err
		}
		if c.Type != connectorTypeMQTT && c.Type != "lorawan_downlink" {
			return fmt.Errorf("connector %q has type %q; this package provisions %q and lorawan_downlink", c.Token, c.Type, connectorTypeMQTT)
		}
		if err := jsonObject(c.Config); err != nil {
			return fmt.Errorf("connector %q config: %w", c.Token, err)
		}
	}
	channels := map[string]bool{}
	for _, c := range m.NotificationChannels {
		if err := validateTokenOnce(seen, "notification channel", c.Token); err != nil {
			return err
		}
		if c.ChannelType != notificationChannelSMTP {
			return fmt.Errorf("notification channel %q has type %q; this package provisions only %q", c.Token, c.ChannelType, notificationChannelSMTP)
		}
		if err := jsonObject(c.Config); err != nil {
			return fmt.Errorf("notification channel %q config: %w", c.Token, err)
		}
		channels[c.Token] = true
	}
	for _, p := range m.NotificationPolicies {
		if err := validateTokenOnce(seen, "notification policy", p.Token); err != nil {
			return err
		}
		if len(p.Rules) == 0 {
			return fmt.Errorf("notification policy %q has no rules, so it pages nobody", p.Token)
		}
		if p.EscalateAfterSeconds < 0 || p.MaxEscalations < 0 {
			return fmt.Errorf("notification policy %q has a negative escalation setting", p.Token)
		}
		for _, r := range p.Rules {
			if !channels[r.ChannelToken] {
				return fmt.Errorf("notification policy %q pages channel %q, which the manifest does not provision", p.Token, r.ChannelToken)
			}
			if len(r.Recipients) == 0 {
				return fmt.Errorf("notification policy %q pages channel %q with no recipients; a mail channel refuses that", p.Token, r.ChannelToken)
			}
		}
	}
	for _, in := range m.Integrations {
		if err := validateTokenOnce(seen, "integration", in.Token); err != nil {
			return err
		}
		if in.Type != integrationTypeChirpStack4 {
			return fmt.Errorf("integration %q has type %q; this package provisions only %q", in.Token, in.Type, integrationTypeChirpStack4)
		}
		if len(in.DeviceTypeTokens) == 0 {
			return fmt.Errorf("integration %q is scoped to no device type, so it resolves nothing", in.Token)
		}
		for _, t := range in.DeviceTypeTokens {
			if !typeTokens[t] {
				return fmt.Errorf("integration %q is scoped to unknown device type %q", in.Token, t)
			}
		}
	}
	return nil
}

func validateTokenOnce(seen map[string]bool, kind, token string) error {
	if err := core.ValidateToken(token); err != nil {
		return fmt.Errorf("%s token: %w", kind, err)
	}
	if seen[kind+"/"+token] {
		return fmt.Errorf("%s %q is declared twice", kind, token)
	}
	seen[kind+"/"+token] = true
	return nil
}

func jsonObject(s string) error {
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err != nil || obj == nil {
		return fmt.Errorf("not a JSON object")
	}
	return nil
}

// validateAliases refuses a device alias no integration can resolve: a device whose type is in no
// integration's scope is refused whatever the sender claims, so its uplinks would be counted as
// unknown and stored nowhere, with every provisioning step green.
func (m SimManifest) validateAliases(d DeviceInstance) error {
	schemes := map[string]bool{}
	for _, a := range d.Aliases {
		if a.Scheme != AliasSchemeDevEUI {
			return fmt.Errorf("device %q alias scheme %q: this package provisions only %q", d.Token, a.Scheme, AliasSchemeDevEUI)
		}
		if schemes[a.Scheme] {
			return fmt.Errorf("device %q carries two %s aliases; the platform holds one per scheme", d.Token, a.Scheme)
		}
		schemes[a.Scheme] = true
		if len(a.Value) != 16 || strings.Trim(strings.ToUpper(a.Value), "0123456789ABCDEF") != "" {
			return fmt.Errorf("device %q DevEUI %q is not 16 hex digits", d.Token, a.Value)
		}
		scoped := false
		for _, in := range m.Integrations {
			scoped = scoped || sliceContains(in.DeviceTypeTokens, d.DeviceTypeToken)
		}
		if !scoped {
			return fmt.Errorf("device %q carries a DevEUI but its type %q is in no integration's scope, so no uplink "+
				"could ever resolve to it", d.Token, d.DeviceTypeToken)
		}
	}
	return nil
}

// ---- provisioning ------------------------------------------------------------------------------

const (
	queryWebhookSigningSecret = `query($handle:String!){webhookSigningSecret(handle:$handle){handle}}`

	mutationGenerateWebhookSigningSecret = `mutation($handle:String!){` +
		`generateWebhookSigningSecret(handle:$handle){handle}}`

	queryConnector = `query($token:String!){connector(token:$token){token type config}}`

	queryConnectorVersions = `query($token:String!){connectorVersions(token:$token,limit:1){version label}}`

	mutationCreateConnector = `mutation($request:ConnectorCreateRequest!){createConnector(request:$request){token}}`

	mutationUpdateConnector = `mutation($token:String!,$request:ConnectorUpdateRequest!){` +
		`updateConnector(token:$token,request:$request){token}}`

	mutationPublishConnector = `mutation($token:String!,$label:String){publishConnector(token:$token,label:$label){version}}`

	queryNotificationChannelsByToken = `query($tokens:[String!]!){notificationChannelsByToken(tokens:$tokens){token}}`

	mutationCreateNotificationChannel = `mutation($request:NotificationChannelCreateRequest!){` +
		`createNotificationChannel(request:$request){token}}`

	mutationUpdateNotificationChannel = `mutation($token:String!,$request:NotificationChannelUpdateRequest!){` +
		`updateNotificationChannel(token:$token,request:$request){token}}`

	queryNotificationPoliciesByToken = `query($tokens:[String!]!){notificationPoliciesByToken(tokens:$tokens){token}}`

	mutationCreateNotificationPolicy = `mutation($request:NotificationPolicyCreateRequest!){` +
		`createNotificationPolicy(request:$request){token}}`

	mutationUpdateNotificationPolicy = `mutation($token:String!,$request:NotificationPolicyUpdateRequest!){` +
		`updateNotificationPolicy(token:$token,request:$request){token}}`

	queryIntegration = `query($token:String!){integration(token:$token){token}}`

	mutationCreateIntegration = `mutation($request:IntegrationCreateRequest!){` +
		`createIntegration(request:$request){issuedToken}}`

	mutationUpdateIntegration = `mutation($token:String!,$request:IntegrationUpdateRequest!){` +
		`updateIntegration(token:$token,request:$request){token}}`

	mutationRotateIntegrationToken = `mutation($token:String!){rotateIntegrationToken(token:$token){issuedToken}}`

	mutationSetDeviceProfileDecoder = `mutation($profileToken:String!,$document:String!){` +
		`setDeviceProfileDecoder(profileToken:$profileToken,document:$document){token}}`
)

// requireEndpoints fails before any network call when the manifest declares a destination whose
// service the handshake names no endpoint for (a record written by an older dcctl).
func requireEndpoints(m SimManifest, e Endpoints) error {
	need := func(have string, what, field string) error {
		if strings.TrimSpace(have) == "" {
			return fmt.Errorf("manifest %q declares %s but the handshake has no endpoints.%s: re-create the sim with a current dcctl",
				m.Name, what, field)
		}
		return nil
	}
	if len(m.WebhookSigningHandles)+len(m.Connectors) > 0 {
		if err := need(e.OutboundConnectorsGraphQL, "outbound connectors or webhook signing handles", "outboundConnectorsGraphQL"); err != nil {
			return err
		}
	}
	if len(m.NotificationChannels)+len(m.NotificationPolicies) > 0 {
		if err := need(e.NotificationMgmtGraphQL, "notification channels or policies", "notificationMgmtGraphQL"); err != nil {
			return err
		}
	}
	if len(m.Integrations) > 0 {
		if err := need(e.Integrations, "inbound integrations", "integrations"); err != nil {
			return err
		}
	}
	return nil
}

// areaHint wraps an error from an opt-in area's endpoint with what usually causes it.
func areaHint(err error, area string) error {
	return fmt.Errorf("%w (is %s deployed? an install adds it with `dcctl bootstrap --enable-area %s`)", err, area, area)
}

// ensureWebhookSigningHandle mints a signing secret for a handle that has none. The secret is
// shown once and this process discards it: nothing here signs or verifies, and a verifier that
// must check a signature rotates the handle to a secret of its own.
func ensureWebhookSigningHandle(ctx context.Context, rt *Runtime, handle string) error {
	var existing struct {
		WebhookSigningSecret *struct {
			Handle string `json:"handle"`
		} `json:"webhookSigningSecret"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.OutboundConnectorsGraphQL, queryWebhookSigningSecret,
		map[string]any{"handle": handle}, &existing); err != nil {
		return areaHint(fmt.Errorf("webhookSigningSecret: %w", err), "outbound-connectors")
	}
	if existing.WebhookSigningSecret != nil {
		return nil
	}
	var out struct {
		GenerateWebhookSigningSecret struct {
			Handle string `json:"handle"`
		} `json:"generateWebhookSigningSecret"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.OutboundConnectorsGraphQL, mutationGenerateWebhookSigningSecret,
		map[string]any{"handle": handle}, &out); err != nil {
		return fmt.Errorf("generateWebhookSigningSecret: %w", err)
	}
	log.Info().Str("handle", handle).Msg("minted webhook signing secret")
	return nil
}

// connectorMarker is the label of the published version carrying exactly this type and config, so
// "is this content published?" is answered by the tenant's own state, as a profile's marker is.
func connectorMarker(typ, config string) string {
	sum := sha256.Sum256([]byte(typ + "\x00" + config))
	return markerPrefix + hex.EncodeToString(sum[:])[:16]
}

// ensureConnector converges one connector's draft to the manifest and publishes it when the latest
// published version is not this content.
func ensureConnector(ctx context.Context, rt *Runtime, c ConnectorSpec) error {
	url := rt.Endpoints.OutboundConnectorsGraphQL
	config := withTenant(c.Config, rt.Tenant)
	var existing struct {
		Connector *struct {
			Token  string `json:"token"`
			Type   string `json:"type"`
			Config string `json:"config"`
		} `json:"connector"`
	}
	if err := rt.Session.Query(ctx, url, queryConnector, map[string]any{"token": c.Token}, &existing); err != nil {
		return areaHint(fmt.Errorf("connector: %w", err), "outbound-connectors")
	}
	switch {
	case existing.Connector == nil:
		var out struct {
			CreateConnector struct {
				Token string `json:"token"`
			} `json:"createConnector"`
		}
		req := map[string]any{"token": c.Token, "name": c.Name, "type": c.Type, "config": config}
		if c.Secret != "" {
			req["secret"] = withTenant(c.Secret, rt.Tenant)
		}
		if err := rt.Session.Query(ctx, url, mutationCreateConnector, map[string]any{"request": req}, &out); err != nil {
			return fmt.Errorf("createConnector: %w", err)
		}
		log.Info().Str("token", c.Token).Msg("created connector")
	case existing.Connector.Type != c.Type || !sameJSON(existing.Connector.Config, config) || c.Secret != "":
		var out struct {
			UpdateConnector struct {
				Token string `json:"token"`
			} `json:"updateConnector"`
		}
		req := map[string]any{"name": c.Name, "type": c.Type, "config": config}
		if c.Secret != "" {
			req["secret"] = withTenant(c.Secret, rt.Tenant)
		}
		if err := rt.Session.Query(ctx, url, mutationUpdateConnector, map[string]any{"token": c.Token, "request": req}, &out); err != nil {
			return fmt.Errorf("updateConnector: %w", err)
		}
		log.Info().Str("token", c.Token).Msg("updated connector")
	}
	marker := connectorMarker(c.Type, config)
	var versions struct {
		ConnectorVersions []struct {
			Version int     `json:"version"`
			Label   *string `json:"label"`
		} `json:"connectorVersions"`
	}
	if err := rt.Session.Query(ctx, url, queryConnectorVersions, map[string]any{"token": c.Token}, &versions); err != nil {
		return fmt.Errorf("connectorVersions: %w", err)
	}
	if len(versions.ConnectorVersions) > 0 && versions.ConnectorVersions[0].Label != nil && *versions.ConnectorVersions[0].Label == marker {
		return nil
	}
	var out struct {
		PublishConnector struct {
			Version int `json:"version"`
		} `json:"publishConnector"`
	}
	if err := rt.Session.Query(ctx, url, mutationPublishConnector, map[string]any{"token": c.Token, "label": marker}, &out); err != nil {
		return fmt.Errorf("publishConnector: %w", err)
	}
	log.Info().Str("token", c.Token).Int("version", out.PublishConnector.Version).Msg("published connector")
	return nil
}

// ensureNotificationChannel creates the channel, or rewrites an existing one to the manifest.
func ensureNotificationChannel(ctx context.Context, rt *Runtime, c NotificationChannelSpec) error {
	url := rt.Endpoints.NotificationMgmtGraphQL
	var existing struct {
		NotificationChannelsByToken []struct {
			Token string `json:"token"`
		} `json:"notificationChannelsByToken"`
	}
	if err := rt.Session.Query(ctx, url, queryNotificationChannelsByToken, map[string]any{"tokens": []string{c.Token}}, &existing); err != nil {
		return areaHint(fmt.Errorf("notificationChannelsByToken: %w", err), "notification-management")
	}
	config := withTenant(c.Config, rt.Tenant)
	if len(existing.NotificationChannelsByToken) == 0 {
		var out struct {
			CreateNotificationChannel struct {
				Token string `json:"token"`
			} `json:"createNotificationChannel"`
		}
		req := map[string]any{"token": c.Token, "name": c.Name, "channelType": c.ChannelType, "config": config, "enabled": true}
		if err := rt.Session.Query(ctx, url, mutationCreateNotificationChannel, map[string]any{"request": req}, &out); err != nil {
			return fmt.Errorf("createNotificationChannel: %w", err)
		}
		log.Info().Str("token", c.Token).Msg("created notification channel")
		return nil
	}
	var out struct {
		UpdateNotificationChannel struct {
			Token string `json:"token"`
		} `json:"updateNotificationChannel"`
	}
	req := map[string]any{"name": c.Name, "channelType": c.ChannelType, "config": config, "enabled": true}
	if err := rt.Session.Query(ctx, url, mutationUpdateNotificationChannel, map[string]any{"token": c.Token, "request": req}, &out); err != nil {
		return fmt.Errorf("updateNotificationChannel: %w", err)
	}
	return nil
}

// notificationRules renders a policy's rules as the request takes them: recipients as a JSON array
// in a string.
func notificationRules(p NotificationPolicySpec, tenant string) []map[string]any {
	out := make([]map[string]any, 0, len(p.Rules))
	for _, r := range p.Rules {
		recipients := make([]string, len(r.Recipients))
		for i, x := range r.Recipients {
			recipients[i] = withTenant(x, tenant)
		}
		raw, _ := json.Marshal(recipients)
		out = append(out, map[string]any{"severity": r.Severity, "channelToken": r.ChannelToken, "recipients": string(raw)})
	}
	return out
}

// ensureNotificationPolicy creates the policy, or rewrites an existing one to the manifest. The
// whole request goes as one variable: an update whose rules were null would delete every rule.
func ensureNotificationPolicy(ctx context.Context, rt *Runtime, p NotificationPolicySpec) error {
	url := rt.Endpoints.NotificationMgmtGraphQL
	var existing struct {
		NotificationPoliciesByToken []struct {
			Token string `json:"token"`
		} `json:"notificationPoliciesByToken"`
	}
	if err := rt.Session.Query(ctx, url, queryNotificationPoliciesByToken, map[string]any{"tokens": []string{p.Token}}, &existing); err != nil {
		return areaHint(fmt.Errorf("notificationPoliciesByToken: %w", err), "notification-management")
	}
	req := map[string]any{
		"name": p.Name, "enabled": true, "escalateAfterSeconds": p.EscalateAfterSeconds,
		"rules": notificationRules(p, rt.Tenant),
	}
	if p.MaxEscalations > 0 {
		req["maxEscalations"] = p.MaxEscalations
	}
	if len(existing.NotificationPoliciesByToken) == 0 {
		req["token"] = p.Token
		var out struct {
			CreateNotificationPolicy struct {
				Token string `json:"token"`
			} `json:"createNotificationPolicy"`
		}
		if err := rt.Session.Query(ctx, url, mutationCreateNotificationPolicy, map[string]any{"request": req}, &out); err != nil {
			return fmt.Errorf("createNotificationPolicy: %w", err)
		}
		log.Info().Str("token", p.Token).Msg("created notification policy")
		return nil
	}
	var out struct {
		UpdateNotificationPolicy struct {
			Token string `json:"token"`
		} `json:"updateNotificationPolicy"`
	}
	if err := rt.Session.Query(ctx, url, mutationUpdateNotificationPolicy, map[string]any{"token": p.Token, "request": req}, &out); err != nil {
		return fmt.Errorf("updateNotificationPolicy: %w", err)
	}
	return nil
}

// ensureIntegration creates the integration, or converges an existing one's scope and rotates its
// token, and returns the bearer token. The platform keeps only a digest, so the token of an
// existing integration cannot be read back: rotating is the only way to hold one, and it costs a
// network server that is not this process nothing, because no other sender uses it.
func ensureIntegration(ctx context.Context, rt *Runtime, in IntegrationSpec) (string, error) {
	url := rt.Endpoints.DeviceMgmtGraphQL
	types := append([]string(nil), in.DeviceTypeTokens...)
	sort.Strings(types)
	var existing struct {
		Integration *struct {
			Token string `json:"token"`
		} `json:"integration"`
	}
	if err := rt.Session.Query(ctx, url, queryIntegration, map[string]any{"token": in.Token}, &existing); err != nil {
		return "", fmt.Errorf("integration: %w", err)
	}
	if existing.Integration == nil {
		var out struct {
			CreateIntegration struct {
				IssuedToken string `json:"issuedToken"`
			} `json:"createIntegration"`
		}
		req := map[string]any{"token": in.Token, "name": in.Name, "type": in.Type, "enabled": true, "deviceTypeTokens": types}
		if err := rt.Session.Query(ctx, url, mutationCreateIntegration, map[string]any{"request": req}, &out); err != nil {
			return "", fmt.Errorf("createIntegration: %w", err)
		}
		log.Info().Str("token", in.Token).Msg("created integration")
		return out.CreateIntegration.IssuedToken, nil
	}
	var updated struct {
		UpdateIntegration struct {
			Token string `json:"token"`
		} `json:"updateIntegration"`
	}
	req := map[string]any{"name": in.Name, "enabled": true, "deviceTypeTokens": types}
	if err := rt.Session.Query(ctx, url, mutationUpdateIntegration, map[string]any{"token": in.Token, "request": req}, &updated); err != nil {
		return "", fmt.Errorf("updateIntegration: %w", err)
	}
	var out struct {
		RotateIntegrationToken struct {
			IssuedToken string `json:"issuedToken"`
		} `json:"rotateIntegrationToken"`
	}
	if err := rt.Session.Query(ctx, url, mutationRotateIntegrationToken, map[string]any{"token": in.Token}, &out); err != nil {
		return "", fmt.Errorf("rotateIntegrationToken: %w", err)
	}
	log.Info().Str("token", in.Token).Msg("rotated integration token")
	return out.RotateIntegrationToken.IssuedToken, nil
}

// setProfileDecoder saves the profile's draft decoder. It is set on every run, before the publish
// check: the content marker covers the decoder, so a run that changed it republishes.
func setProfileDecoder(ctx context.Context, rt *Runtime, profileToken, document string) error {
	var out struct {
		SetDeviceProfileDecoder struct {
			Token string `json:"token"`
		} `json:"setDeviceProfileDecoder"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationSetDeviceProfileDecoder,
		map[string]any{"profileToken": profileToken, "document": document}, &out); err != nil {
		return fmt.Errorf("setDeviceProfileDecoder: %w", err)
	}
	return nil
}

// aliasInputs renders a device's aliases as the request takes them.
func aliasInputs(aliases []AliasSpec) []map[string]any {
	out := make([]map[string]any, 0, len(aliases))
	for _, a := range aliases {
		out = append(out, map[string]any{"scheme": a.Scheme, "value": strings.ToUpper(a.Value)})
	}
	return out
}

// sameAliases reports whether the device holds exactly these aliases (values compared upper case,
// as the platform stores a DevEUI).
func sameAliases(have []AliasSpec, want []AliasSpec) bool {
	key := func(as []AliasSpec) []string {
		out := make([]string, 0, len(as))
		for _, a := range as {
			out = append(out, a.Scheme+"="+strings.ToUpper(a.Value))
		}
		sort.Strings(out)
		return out
	}
	h, w := key(have), key(want)
	if len(h) != len(w) {
		return false
	}
	for i := range h {
		if h[i] != w[i] {
			return false
		}
	}
	return true
}
