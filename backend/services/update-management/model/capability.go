// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/secrets"
)

// MaxCapabilityTTL is the longest a download capability may live. A capability is a bearer URL
// that rides an MQTT message and a device's HTTP client, so it is short on purpose: a device that
// needs more time asks again, and what it gets back names the same artifact.
const MaxCapabilityTTL = 15 * time.Minute

// maxCapabilityLen bounds the cap parameter before anything is parsed.
const maxCapabilityLen = 1024

const (
	capabilityVersion = 1
	capabilityTag     = "dc-update-download-capability-v1"
	capabilityKeyName = "update/download-capability-key"
	capabilityKeyLen  = 32
	keyCacheLimit     = 1024
)

// Capability errors. The download handler maps every one of them to the same refusal, so a caller
// cannot tell a forged capability from a stale one except through ErrCapabilityExpired, which is
// only ever returned for a capability whose signature was genuine.
var (
	// ErrCapabilityMalformed: the string is not a capability at all.
	ErrCapabilityMalformed = errors.New("download capability is malformed")
	// ErrCapabilityInvalid: the signature does not verify, or names a tenant with no key.
	ErrCapabilityInvalid = errors.New("download capability is not valid")
	// ErrCapabilityExpired: a genuine capability past its expiry.
	ErrCapabilityExpired = errors.New("download capability has expired")
)

// Capability is what a verified download capability grants: one artifact, to one device, under one
// assignment, until Expires, within one tenant.
type Capability struct {
	Tenant       string
	DeviceToken  string
	AssignmentID string
	Digest       string
	ArtifactTok  string
	Expires      time.Time
}

// capabilityPayload is the signed content, carried in the capability so the download handler can
// verify the signature before it touches the database. Field names are one letter because the
// whole capability rides a URL.
type capabilityPayload struct {
	V int    `json:"v"`
	T string `json:"t"`
	D string `json:"d"`
	A string `json:"a"`
	G string `json:"g"`
	X string `json:"x"`
	E int64  `json:"e"`
}

// macInput is the exact byte string the signature covers: a domain tag, then every field
// length-prefixed, so no two different field tuples can produce the same input by moving a
// separator between neighbours.
func macInput(p capabilityPayload) []byte {
	var b []byte
	put := func(s string) {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	put(capabilityTag)
	put(p.T)
	put(p.D)
	put(p.A)
	put(p.G)
	put(p.X)
	b = binary.AppendVarint(b, p.E)
	return b
}

func sign(key []byte, p capabilityPayload) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(macInput(p))
	return m.Sum(nil)
}

// capabilityKeys holds each tenant's signing key in memory, so a download does not read the secret
// store. It is bounded: a tenant's key is 32 bytes, and past the limit the cache is dropped and
// refills from the store.
type capabilityKeys struct {
	mu   sync.RWMutex
	keys map[string][]byte
}

func (c *capabilityKeys) get(tenant string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	k, ok := c.keys[tenant]
	return k, ok
}

func (c *capabilityKeys) put(tenant string, key []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys == nil || len(c.keys) >= keyCacheLimit {
		c.keys = make(map[string][]byte, 16)
	}
	c.keys[tenant] = key
}

func (api *Api) keyRef(tenant string) secrets.SecretRef {
	return secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: tenant, Name: capabilityKeyName}
}

// lookupKey returns the tenant's signing key if one exists. It NEVER creates one: verification
// runs on a request nobody has authenticated yet, and a verify path that created keys would let an
// anonymous caller mint a secret row per tenant name it can think of.
func (api *Api) lookupKey(ctx context.Context, tenant string) ([]byte, error) {
	if api.Secrets == nil {
		return nil, errors.New("update-management: the secret store is not wired, so download capabilities cannot be checked")
	}
	if k, ok := api.keys.get(tenant); ok {
		return k, nil
	}
	k, err := api.Secrets.Resolve(core.WithTenant(ctx, tenant), api.keyRef(tenant))
	if err != nil {
		return nil, err
	}
	api.keys.put(tenant, k)
	return k, nil
}

// signingKey returns the tenant's key, creating it on first use. Only the mint path calls it, and
// the mint path is reached only for an authenticated device's current assignment.
//
// Two replicas can both find no key and both try to create one. The store's unique handle index
// makes one of the writes fail; the loser reads the winner's key instead of keeping its own, so
// every replica ends up signing and verifying with the same key.
func (api *Api) signingKey(ctx context.Context, tenant string) ([]byte, error) {
	k, err := api.lookupKey(ctx, tenant)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		return nil, err
	}
	fresh := make([]byte, capabilityKeyLen)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	tctx := core.WithTenant(ctx, tenant)
	if putErr := api.Secrets.Put(tctx, api.keyRef(tenant), fresh); putErr != nil {
		k, err := api.Secrets.Resolve(tctx, api.keyRef(tenant))
		if err != nil {
			return nil, fmt.Errorf("creating the download capability key: %w", putErr)
		}
		api.keys.put(tenant, k)
		return k, nil
	}
	// Read back what is stored, not what was generated: if a concurrent writer's row won the
	// upsert, the stored value is the one every other replica will verify with.
	k, err = api.Secrets.Resolve(tctx, api.keyRef(tenant))
	if err != nil {
		return nil, err
	}
	api.keys.put(tenant, k)
	return k, nil
}

// MintCapability signs a capability for one (device, assignment, artifact). The digest is the
// artifact's, so a capability for one build of a component can never fetch another.
func (api *Api) MintCapability(ctx context.Context, c Capability) (string, error) {
	if err := core.ValidateToken(c.Tenant); err != nil {
		return "", err
	}
	key, err := api.signingKey(ctx, c.Tenant)
	if err != nil {
		return "", err
	}
	p := capabilityPayload{V: capabilityVersion, T: c.Tenant, D: c.DeviceToken, A: c.AssignmentID,
		G: c.Digest, X: c.ArtifactTok, E: c.Expires.Unix()}
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(body) + "." + enc.EncodeToString(sign(key, p)), nil
}

// VerifyCapability checks a capability presented for the artifact named in the URL path. Order is
// the design: shape, then the signature, and only for a genuine signature the expiry and the path
// binding. Nothing about the tenant, the device or the assignment is looked up here; the caller
// checks the database state the capability refers to after this returns.
func (api *Api) VerifyCapability(ctx context.Context, raw, artifactToken string) (*Capability, error) {
	if len(raw) < 3 || len(raw) > maxCapabilityLen {
		return nil, ErrCapabilityMalformed
	}
	body, mac, ok := strings.Cut(raw, ".")
	if !ok {
		return nil, ErrCapabilityMalformed
	}
	enc := base64.RawURLEncoding
	payload, err := enc.DecodeString(body)
	if err != nil {
		return nil, ErrCapabilityMalformed
	}
	gotMAC, err := enc.DecodeString(mac)
	if err != nil {
		return nil, ErrCapabilityMalformed
	}
	var p capabilityPayload
	if err := json.Unmarshal(payload, &p); err != nil || p.V != capabilityVersion {
		return nil, ErrCapabilityMalformed
	}
	if core.ValidateToken(p.T) != nil {
		return nil, ErrCapabilityMalformed
	}
	key, err := api.lookupKey(ctx, p.T)
	if err != nil {
		if errors.Is(err, secrets.ErrSecretNotFound) {
			// A tenant that never minted a capability has no key. Indistinguishable from a
			// bad signature, so the response does not enumerate tenants.
			return nil, ErrCapabilityInvalid
		}
		return nil, err
	}
	if !hmac.Equal(gotMAC, sign(key, p)) {
		return nil, ErrCapabilityInvalid
	}
	// The signature is genuine from here on.
	if p.X != artifactToken {
		return nil, ErrCapabilityInvalid
	}
	exp := time.Unix(p.E, 0).UTC()
	if !api.now().Before(exp) {
		return nil, ErrCapabilityExpired
	}
	return &Capability{Tenant: p.T, DeviceToken: p.D, AssignmentID: p.A, Digest: p.G, ArtifactTok: p.X, Expires: exp}, nil
}

// DownloadURL is the capability URL a device fetches: the configured public base, the artifact
// token in the path, and the capability as a query parameter.
func DownloadURL(base, artifactToken, capability string) string {
	return strings.TrimRight(base, "/") + "/updates/download/" + url.PathEscape(artifactToken) +
		"?cap=" + url.QueryEscape(capability)
}
