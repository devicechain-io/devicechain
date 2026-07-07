// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
)

// Provision runs the manifest's whole provisioning chain against
// device-management's tenant GraphQL API: profile (+ metrics) -> publish ->
// device type -> devices (+ credentials). Every step checks-then-creates by
// token (a *ByToken query first, a create only if it comes back empty), so
// re-running Provision against an already-provisioned tenant is a no-op except
// for whatever is genuinely missing — this is what makes `reset` an idempotent
// re-Bootstrap rather than a drop-and-recreate.
//
// On success, rt.Devices holds the manifest's Expand()'d devices (with their
// credential material), ready for Tick to emit against.
func Provision(ctx context.Context, rt *Runtime, manifest SimManifest) error {
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}

	for _, p := range manifest.Profiles {
		if err := ensureProfile(ctx, rt, p); err != nil {
			return fmt.Errorf("provision profile %q: %w", p.Token, err)
		}
	}
	for _, dt := range manifest.DeviceTypes {
		if err := ensureDeviceType(ctx, rt, dt); err != nil {
			return fmt.Errorf("provision device type %q: %w", dt.Token, err)
		}
	}

	devices := manifest.Expand(manifest.Seed)
	for _, d := range devices {
		if err := ensureDevice(ctx, rt, d); err != nil {
			return fmt.Errorf("provision device %q: %w", d.Token, err)
		}
	}
	rt.Devices = devices
	return nil
}

// --- device profile + metrics -------------------------------------------------

type deviceProfileInfo struct {
	Token         string `json:"token"`
	ActiveVersion *int   `json:"activeVersion"`
}

const queryDeviceProfilesByToken = `query($tokens:[String!]!){` +
	`deviceProfilesByToken(tokens:$tokens){token activeVersion}}`

const mutationCreateDeviceProfile = `mutation($request:DeviceProfileCreateRequest){` +
	`createDeviceProfile(request:$request){token activeVersion}}`

const mutationPublishDeviceProfile = `mutation($token:String!){` +
	`publishDeviceProfile(token:$token){version}}`

func ensureProfile(ctx context.Context, rt *Runtime, p ProfileSpec) error {
	profile, err := deviceProfileByToken(ctx, rt, p.Token)
	if err != nil {
		return err
	}
	if profile == nil {
		var created struct {
			CreateDeviceProfile deviceProfileInfo `json:"createDeviceProfile"`
		}
		req := map[string]any{
			"token":    p.Token,
			"name":     p.Name,
			"category": p.Category,
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateDeviceProfile,
			map[string]any{"request": req}, &created); err != nil {
			return fmt.Errorf("createDeviceProfile: %w", err)
		}
		profile = &created.CreateDeviceProfile
		log.Info().Str("token", p.Token).Msg("created device profile")
	}

	for _, m := range p.Metrics {
		if err := ensureMetricDefinition(ctx, rt, p.Token, m); err != nil {
			return err
		}
	}

	if profile.ActiveVersion == nil {
		var published struct {
			PublishDeviceProfile struct {
				Version int `json:"version"`
			} `json:"publishDeviceProfile"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationPublishDeviceProfile,
			map[string]any{"token": p.Token}, &published); err != nil {
			return fmt.Errorf("publishDeviceProfile: %w", err)
		}
		log.Info().Str("token", p.Token).Int("version", published.PublishDeviceProfile.Version).
			Msg("published device profile")
	}
	return nil
}

func deviceProfileByToken(ctx context.Context, rt *Runtime, token string) (*deviceProfileInfo, error) {
	var out struct {
		DeviceProfilesByToken []deviceProfileInfo `json:"deviceProfilesByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryDeviceProfilesByToken,
		map[string]any{"tokens": []string{token}}, &out); err != nil {
		return nil, fmt.Errorf("deviceProfilesByToken: %w", err)
	}
	if len(out.DeviceProfilesByToken) == 0 {
		return nil, nil
	}
	return &out.DeviceProfilesByToken[0], nil
}

const queryMetricDefinitionsByToken = `query($tokens:[String!]!){` +
	`metricDefinitionsByToken(tokens:$tokens){token}}`

const mutationCreateMetricDefinition = `mutation($request:MetricDefinitionCreateRequest){` +
	`createMetricDefinition(request:$request){token}}`

func ensureMetricDefinition(ctx context.Context, rt *Runtime, profileToken string, m MetricSpec) error {
	metricToken := profileToken + "-" + m.Key
	var existing struct {
		MetricDefinitionsByToken []struct {
			Token string `json:"token"`
		} `json:"metricDefinitionsByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryMetricDefinitionsByToken,
		map[string]any{"tokens": []string{metricToken}}, &existing); err != nil {
		return fmt.Errorf("metricDefinitionsByToken: %w", err)
	}
	if len(existing.MetricDefinitionsByToken) > 0 {
		return nil
	}

	req := map[string]any{
		"token":              metricToken,
		"deviceProfileToken": profileToken,
		"metricKey":          m.Key,
		"name":               m.Name,
		"dataType":           m.DataType,
		"unit":               m.Unit,
	}
	var created struct {
		CreateMetricDefinition struct {
			Token string `json:"token"`
		} `json:"createMetricDefinition"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateMetricDefinition,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createMetricDefinition: %w", err)
	}
	log.Info().Str("token", metricToken).Str("profile", profileToken).Msg("created metric definition")
	return nil
}

// --- device type ---------------------------------------------------------------

const queryDeviceTypesByToken = `query($tokens:[String!]!){deviceTypesByToken(tokens:$tokens){token}}`

const mutationCreateDeviceType = `mutation($request:DeviceTypeCreateRequest){` +
	`createDeviceType(request:$request){token}}`

func ensureDeviceType(ctx context.Context, rt *Runtime, dt DeviceTypeSpec) error {
	var existing struct {
		DeviceTypesByToken []struct {
			Token string `json:"token"`
		} `json:"deviceTypesByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryDeviceTypesByToken,
		map[string]any{"tokens": []string{dt.Token}}, &existing); err != nil {
		return fmt.Errorf("deviceTypesByToken: %w", err)
	}
	if len(existing.DeviceTypesByToken) > 0 {
		return nil
	}

	req := map[string]any{
		"token":        dt.Token,
		"name":         dt.Name,
		"profileToken": dt.ProfileToken,
	}
	var created struct {
		CreateDeviceType struct {
			Token string `json:"token"`
		} `json:"createDeviceType"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateDeviceType,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createDeviceType: %w", err)
	}
	log.Info().Str("token", dt.Token).Msg("created device type")
	return nil
}

// --- device + credential ---------------------------------------------------------

const queryDevicesByToken = `query($tokens:[String!]!){devicesByToken(tokens:$tokens){token}}`

const mutationCreateDevice = `mutation($request:DeviceCreateRequest){createDevice(request:$request){token}}`

const queryDeviceCredentialsByToken = `query($tokens:[String!]!){deviceCredentialsByToken(tokens:$tokens){token}}`

const mutationCreateDeviceCredential = `mutation($request:DeviceCredentialCreateRequest){` +
	`createDeviceCredential(request:$request){token}}`

// credentialTypeAccessToken is device-management's model.CredentialAccessToken
// value (ADR-014); mirrored here as a literal since dc-simulator only ever
// speaks the wire, not the Go model package.
const credentialTypeAccessToken = "ACCESS_TOKEN"

func ensureDevice(ctx context.Context, rt *Runtime, d DeviceInstance) error {
	var existingDevice struct {
		DevicesByToken []struct {
			Token string `json:"token"`
		} `json:"devicesByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryDevicesByToken,
		map[string]any{"tokens": []string{d.Token}}, &existingDevice); err != nil {
		return fmt.Errorf("devicesByToken: %w", err)
	}
	if len(existingDevice.DevicesByToken) == 0 {
		req := map[string]any{
			"token":           d.Token,
			"externalId":      d.ExternalId,
			"deviceTypeToken": d.DeviceTypeToken,
		}
		var created struct {
			CreateDevice struct {
				Token string `json:"token"`
			} `json:"createDevice"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateDevice,
			map[string]any{"request": req}, &created); err != nil {
			return fmt.Errorf("createDevice: %w", err)
		}
		log.Info().Str("token", d.Token).Str("externalId", d.ExternalId).Msg("created device")
	}

	var existingCred struct {
		DeviceCredentialsByToken []struct {
			Token string `json:"token"`
		} `json:"deviceCredentialsByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryDeviceCredentialsByToken,
		map[string]any{"tokens": []string{d.CredentialToken}}, &existingCred); err != nil {
		return fmt.Errorf("deviceCredentialsByToken: %w", err)
	}
	if len(existingCred.DeviceCredentialsByToken) > 0 {
		return nil
	}

	req := map[string]any{
		"token":          d.CredentialToken,
		"deviceToken":    d.Token,
		"credentialType": credentialTypeAccessToken,
		"credentialId":   d.CredentialId,
		"enabled":        true,
	}
	var created struct {
		CreateDeviceCredential struct {
			Token string `json:"token"`
		} `json:"createDeviceCredential"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateDeviceCredential,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createDeviceCredential: %w", err)
	}
	log.Info().Str("token", d.CredentialToken).Str("device", d.Token).Msg("created device credential")
	return nil
}
