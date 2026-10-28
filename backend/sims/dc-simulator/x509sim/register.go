// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509sim

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
)

// Querier is the slice of userclient.TenantSession Register needs: one authenticated GraphQL
// call against a tenant-scoped endpoint.
type Querier interface {
	Query(ctx context.Context, baseURL, query string, variables map[string]any, out any) error
}

// credentialTypeX509 is device-management's X509_CERTIFICATE credential type, as a wire literal.
const credentialTypeX509 = "X509_CERTIFICATE"

const (
	queryAnchorsByToken  = `query($tokens:[String!]!){deviceTrustAnchorsByToken(tokens:$tokens){token}}`
	mutationCreateAnchor = `mutation($request:DeviceTrustAnchorCreateRequest!){` +
		`createDeviceTrustAnchor(request:$request){token}}`
	queryDevicesByToken  = `query($tokens:[String!]!){devicesByToken(tokens:$tokens){token}}`
	mutationCreateDevice = `mutation($request:DeviceCreateRequest){createDevice(request:$request){token}}`
	queryCredsByToken    = `query($tokens:[String!]!){deviceCredentialsByToken(tokens:$tokens){token}}`
	mutationCreateCred   = `mutation($request:DeviceCredentialCreateRequest){` +
		`createDeviceCredential(request:$request){token}}`
)

// RegisterConfig addresses the tenant's device-management API.
type RegisterConfig struct {
	// Endpoint is device-management's tenant GraphQL URL.
	Endpoint string
	// DeviceTypeToken is the device type the pinned devices are created under. It must exist.
	DeviceTypeToken string
}

// CredentialToken is the token of the pinned credential of a device.
func CredentialToken(deviceToken string) string { return deviceToken + "-x509" }

// Register creates what a pinned fleet needs: the CA as a trust anchor, and for each pinned
// entry a device and an X509_CERTIFICATE credential pinning its certificate to that anchor. It is
// idempotent by token. JIT entries are deliberately left alone: they must reach the platform as
// devices it has never seen.
func Register(ctx context.Context, q Querier, cfg RegisterConfig, st *State) error {
	if cfg.Endpoint == "" || cfg.DeviceTypeToken == "" {
		return fmt.Errorf("x509sim: registering needs the device-management endpoint and a device type token")
	}
	anchor := st.Manifest.AnchorToken
	var haveAnchor struct {
		Items []struct {
			Token string `json:"token"`
		} `json:"deviceTrustAnchorsByToken"`
	}
	if err := q.Query(ctx, cfg.Endpoint, queryAnchorsByToken, map[string]any{"tokens": []string{anchor}}, &haveAnchor); err != nil {
		return fmt.Errorf("deviceTrustAnchorsByToken: %w", err)
	}
	if len(haveAnchor.Items) == 0 {
		req := map[string]any{
			"token":          anchor,
			"name":           "dc-simulator test CA",
			"certificatePem": st.CAPEM(),
			"enabled":        true,
		}
		if err := q.Query(ctx, cfg.Endpoint, mutationCreateAnchor, map[string]any{"request": req}, &struct{}{}); err != nil {
			return fmt.Errorf("createDeviceTrustAnchor: %w", err)
		}
		log.Info().Str("token", anchor).Msg("created trust anchor")
	}

	for _, e := range st.Entries(KindPinned) {
		if err := registerPinned(ctx, q, cfg, st, e, anchor); err != nil {
			return fmt.Errorf("device %q: %w", e.Token, err)
		}
	}
	return nil
}

func registerPinned(ctx context.Context, q Querier, cfg RegisterConfig, st *State, e Entry, anchor string) error {
	var dev struct {
		Items []struct {
			Token string `json:"token"`
		} `json:"devicesByToken"`
	}
	if err := q.Query(ctx, cfg.Endpoint, queryDevicesByToken, map[string]any{"tokens": []string{e.Token}}, &dev); err != nil {
		return fmt.Errorf("devicesByToken: %w", err)
	}
	if len(dev.Items) == 0 {
		req := map[string]any{"token": e.Token, "externalId": e.ExternalId, "deviceTypeToken": cfg.DeviceTypeToken}
		if err := q.Query(ctx, cfg.Endpoint, mutationCreateDevice, map[string]any{"request": req}, &struct{}{}); err != nil {
			return fmt.Errorf("createDevice: %w", err)
		}
	}
	credToken := CredentialToken(e.Token)
	var cred struct {
		Items []struct {
			Token string `json:"token"`
		} `json:"deviceCredentialsByToken"`
	}
	if err := q.Query(ctx, cfg.Endpoint, queryCredsByToken, map[string]any{"tokens": []string{credToken}}, &cred); err != nil {
		return fmt.Errorf("deviceCredentialsByToken: %w", err)
	}
	if len(cred.Items) > 0 {
		return nil
	}
	leaf, err := st.Leaf(e)
	if err != nil {
		return err
	}
	// credentialId is required by the schema and must be empty here: the server computes the pin
	// (the SHA-256 of the leaf) and refuses a caller-supplied one.
	req := map[string]any{
		"token":            credToken,
		"deviceToken":      e.Token,
		"credentialType":   credentialTypeX509,
		"credentialId":     "",
		"enabled":          true,
		"certificatePem":   string(CertPEM(leaf)),
		"trustAnchorToken": anchor,
	}
	if err := q.Query(ctx, cfg.Endpoint, mutationCreateCred, map[string]any{"request": req}, &struct{}{}); err != nil {
		return fmt.Errorf("createDeviceCredential: %w", err)
	}
	log.Info().Str("device", e.Token).Str("pin", e.Fingerprint).Msg("created pinned X.509 credential")
	return nil
}
