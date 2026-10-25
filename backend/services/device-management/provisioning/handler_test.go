// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Every outcome of the model, as a status ───────────────────────────────

// Each row sets up one state, sends one request and asserts what a device sees (status and
// code), what an operator sees (exactly one result counter moved, and it is this row's) and
// what the database holds. This is the table the endpoint's contract is written from.
func TestEveryOutcomeMapsToItsStatus(t *testing.T) {
	type row struct {
		name   string
		setup  func(f *fixture)
		mutate func(f *fixture, body map[string]any)
		status int
		code   string
		result string
		// outcome is the success body's outcome, for the rows that register.
		outcome string
		// writes is whether the request leaves a new device or credential behind.
		writes bool
	}
	allow := func(f *fixture) { f.profile("pp-1", model.ProvisionAllowNew) }
	check := func(f *fixture) { f.profile("pp-1", model.ProvisionCheckPreProvisioned) }
	rows := []row{
		{name: "created", setup: allow, status: 201, result: ResultCreated, outcome: "CREATED", writes: true},
		{name: "adopted", setup: func(f *fixture) { check(f); f.device("dev-1", "SN-1", "dt-a", "pp-1") },
			status: 200, result: ResultAdopted, outcome: "ADOPTED", writes: true},
		{name: "replayed", setup: func(f *fixture) {
			allow(f)
			require.Equal(t, 201, f.post("acme", f.body("pp-1", "SN-1", 1)).Status)
		}, status: 200, result: ResultReplayed, outcome: "REPLAYED"},

		{name: "wrong secret", setup: allow, mutate: func(f *fixture, b map[string]any) { b["provisionSecret"] = strings.Repeat("x", 43) },
			status: 401, code: "PROVISIONING_FAILED", result: ResultAuthFailed},
		{name: "unknown key", setup: allow, mutate: func(f *fixture, b map[string]any) { b["provisionKey"] = "no-such-key" },
			status: 401, code: "PROVISIONING_FAILED", result: ResultAuthFailed},
		{name: "disabled profile", setup: func(f *fixture) { allow(f); f.sql(`UPDATE provisioning_profiles SET enabled = 0`) },
			status: 401, code: "PROVISIONING_FAILED", result: ResultAuthFailed},
		{name: "expired profile", setup: func(f *fixture) {
			allow(f)
			f.sql(`UPDATE provisioning_profiles SET expires_at = ?`, time.Now().Add(-time.Hour))
		}, status: 401, code: "PROVISIONING_FAILED", result: ResultAuthFailed},
		{name: "profile whose secret the server never issued", setup: func(f *fixture) {
			allow(f)
			f.sql(`UPDATE provisioning_profiles SET provision_secret_issued = 0`)
		}, status: 401, code: "PROVISIONING_FAILED", result: ResultAuthFailed},
		{name: "profile of another tenant", setup: allow, status: 401, code: "PROVISIONING_FAILED", result: ResultAuthFailed},

		{name: "bad material", setup: allow, mutate: func(f *fixture, b map[string]any) { b["credentialSecret"] = "short" },
			status: 400, code: "INVALID_VALUE", result: ResultInvalid},
		{name: "unknown field", setup: allow, mutate: func(f *fixture, b map[string]any) { b["serial"] = "x" },
			status: 400, code: "INVALID_VALUE", result: ResultInvalid},
		{name: "missing secret", setup: allow, mutate: func(f *fixture, b map[string]any) { delete(b, "provisionSecret") },
			status: 400, code: "INVALID_VALUE", result: ResultInvalid},
		{name: "oversized body", setup: allow, mutate: func(f *fixture, b map[string]any) { b["name"] = strings.Repeat("n", 9000) },
			status: 413, code: "INVALID_VALUE", result: ResultInvalid},

		{name: "not registered", setup: check, status: 404, code: "DEVICE_NOT_REGISTERED", result: ResultNotRegistered},
		{name: "not earmarked", setup: func(f *fixture) { allow(f); f.device("dev-1", "SN-1", "dt-a", "") },
			status: 409, code: "DEVICE_NOT_EARMARKED", result: ResultNotEarmarked},
		{name: "device disabled", setup: func(f *fixture) {
			allow(f)
			f.device("dev-1", "SN-1", "dt-a", "pp-1")
			_, err := f.api.UpdateDevice(f.ctx, "dev-1", &model.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
			require.NoError(t, err)
		}, status: 403, code: "DEVICE_DISABLED", result: ResultDisabled},
		{name: "device type mismatch", setup: func(f *fixture) {
			allow(f)
			f.device("dev-1", "SN-1", "dt-a", "pp-1")
			_, err := f.api.UpdateDevice(f.ctx, "dev-1", &model.DeviceUpdateRequest{DeviceTypeToken: dcgraphql.OptionalStringOf("dt-b")})
			require.NoError(t, err)
		}, status: 409, code: "DEVICE_TYPE_MISMATCH", result: ResultTypeMismatch},
		{name: "token collision", setup: func(f *fixture) {
			allow(f)
			_, err := f.api.CreateDevice(f.ctx, &model.DeviceCreateRequest{Token: core.DeriveDeviceToken("SN-1", "pv-"), DeviceTypeToken: "dt-a"})
			require.NoError(t, err)
		}, status: 409, code: "DEVICE_TOKEN_COLLISION", result: ResultTokenCollision},
		{name: "already provisioned", setup: func(f *fixture) {
			allow(f)
			require.Equal(t, 201, f.post("acme", f.body("pp-1", "SN-1", 1)).Status)
		}, mutate: func(f *fixture, b map[string]any) { b["credentialSecret"] = material(2) },
			status: 409, code: "ALREADY_PROVISIONED", result: ResultAlreadyProvisioned},
		{name: "already provisioned, revoked", setup: func(f *fixture) {
			allow(f)
			require.Equal(t, 201, f.post("acme", f.body("pp-1", "SN-1", 1)).Status)
			f.sql(`UPDATE device_credentials SET enabled = 0`)
		}, status: 409, code: "ALREADY_PROVISIONED", result: ResultAlreadyProvisioned},
		{name: "profile at its cap", setup: func(f *fixture) {
			f.profile("pp-1", model.ProvisionAllowNew, func(r *model.ProvisioningProfileCreateRequest) { n := int32(1); r.MaxDevices = &n })
			require.Equal(t, 201, f.post("acme", f.body("pp-1", "SN-0", 9)).Status)
		}, status: 422, code: "LIMIT_EXCEEDED", result: ResultLimitExceeded},

		{name: "profile whose device type is gone", setup: func(f *fixture) {
			allow(f)
			f.sql(`DELETE FROM device_types WHERE token = 'dt-a'`)
		}, status: 500, code: "PROVISIONING_MISCONFIGURED", result: ResultMisconfigured},
		{name: "corrupt strategy", setup: func(f *fixture) { allow(f); f.sql(`UPDATE provisioning_profiles SET strategy = 'allow_new'`) },
			status: 500, code: "PROVISIONING_MISCONFIGURED", result: ResultMisconfigured},
		{name: "credential type this build cannot mint", setup: func(f *fixture) {
			allow(f)
			f.sql(`UPDATE provisioning_profiles SET credential_type = 'MQTT_BASIC'`)
		}, status: 501, code: "CREDENTIAL_TYPE_UNSUPPORTED", result: ResultUnsupportedType},
		{name: "instance without its keys", setup: func(f *fixture) { allow(f); f.api.ProvisioningChecker = nil },
			status: 503, code: "PROVISIONING_UNAVAILABLE", result: ResultUnavailable},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.setup(f)
			// The setup's own requests moved counters; this row measures only its request.
			before := f.counted()
			devices0, creds0 := f.rows()
			body := f.body("pp-1", "SN-1", 1)
			if tc.mutate != nil {
				tc.mutate(f, body)
			}
			tenant := "acme"
			if tc.name == "profile of another tenant" {
				tenant = "globex"
			}

			got := f.post(tenant, body)

			assert.Equal(t, tc.status, got.Status, "body %s", got.Raw)
			if tc.code != "" {
				assert.Equal(t, tc.code, got.Code())
				assert.Contains(t, Codes, got.Code(), "a code outside the closed vocabulary")
			} else {
				assert.Equal(t, tc.outcome, got.JSON()["outcome"])
			}
			after := f.counted()
			for _, r := range Results {
				want := 0.0
				if r == tc.result {
					want = 1
				}
				assert.Equal(t, want, after[r]-before[r], "result %q", r)
			}
			devices1, creds1 := f.rows()
			if tc.writes {
				assert.Greater(t, creds1, creds0, "the registration wrote no credential")
			} else {
				assert.Equal(t, [2]int64{devices0, creds0}, [2]int64{devices1, creds1}, "a request that registers nothing wrote rows")
			}
		})
	}
}

// A registration that fails inside the model for a reason that is not a refusal is a 500 that
// says nothing about the cause, counted as an error, and a retry is safe.
func TestAnUnexpectedModelErrorIsAGeneric500(t *testing.T) {
	f := newFixture(t)
	f.profile("pp-1", model.ProvisionAllowNew)
	f.registrar.failWith = errors.New("pq: connection refused to 10.1.2.3:5432")
	got := f.post("acme", f.body("pp-1", "SN-1", 1))
	assert.Equal(t, 500, got.Status)
	assert.NotContains(t, string(got.Raw), "10.1.2.3", "an internal error leaked to a stranger")
	assert.Contains(t, Codes, got.Code())
	assert.Equal(t, map[string]float64{ResultError: 1}, f.counted())
}

// An erased tenant answers the generic 401, whichever layer noticed.
func TestAPurgedTenantIsTheGeneric401(t *testing.T) {
	f := newFixture(t)
	f.profile("pp-1", model.ProvisionAllowNew)
	f.registrar.failWith = rdb.ErrTenantPurged
	got := f.post("acme", f.body("pp-1", "SN-1", 1))
	assert.Equal(t, 401, got.Status)
	assert.Equal(t, "PROVISIONING_FAILED", got.Code())
	assert.Equal(t, map[string]float64{ResultAuthFailed: 1}, f.counted())
}

// The success body is the contract: these keys and no others, and nothing secret in it.
func TestTheSuccessBodyIsTheContract(t *testing.T) {
	f := newFixture(t)
	f.profile("pp-1", model.ProvisionAllowNew)
	body := f.body("pp-1", "SN-000123", 1)
	got := f.post("acme", body)
	require.Equal(t, 201, got.Status)
	assert.Equal(t, "application/json", got.Header.Get("Content-Type"))
	assert.Equal(t, "no-store", got.Header.Get("Cache-Control"))
	m := got.JSON()
	token := core.DeriveDeviceToken("SN-000123", "pv-")
	assert.Equal(t, map[string]any{
		"outcome": "CREATED", "tenant": "acme", "deviceToken": token, "externalId": "SN-000123",
		"credentialType": "ACCESS_TOKEN", "mqtt": map[string]any{"clientId": "inst1:acme:" + token},
	}, m)
	for _, secret := range []string{body["credentialSecret"].(string), body["provisionSecret"].(string)} {
		assert.NotContains(t, string(got.Raw), secret)
	}
}

// The body cap is 8 KiB, stated here as a literal so that raising the constant fails a test.
func TestTheBodyCapIsEightKiB(t *testing.T) {
	assert.Equal(t, 8192, MaxBodyBytes)
}

// The closed code vocabulary is pinned.
func TestTheCodeVocabularyIsClosed(t *testing.T) {
	assert.Equal(t, []string{
		"PROVISIONING_FAILED", "INVALID_VALUE", "DEVICE_NOT_REGISTERED", "DEVICE_NOT_EARMARKED", "DEVICE_DISABLED",
		"DEVICE_TYPE_MISMATCH", "DEVICE_TOKEN_COLLISION", "ALREADY_PROVISIONED", "LIMIT_EXCEEDED", "RATE_LIMITED",
		"PROVISIONING_UNAVAILABLE", "PROVISIONING_MISCONFIGURED", "CREDENTIAL_TYPE_UNSUPPORTED",
	}, Codes)
}

// Only POST is routed.
func TestOnlyPostIsRouted(t *testing.T) {
	f := newFixture(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(method, f.srv.URL+"/provisioning/acme/register", nil)
		resp, err := f.srv.Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, method)
	}
}

// A path tenant outside the token grammar is a 400 before the body is read.
func TestATenantOutsideTheGrammarIsRefused(t *testing.T) {
	f := newFixture(t)
	f.profile("pp-1", model.ProvisionAllowNew)
	got := f.post("AC%20ME", f.body("pp-1", "SN-1", 1))
	assert.Equal(t, 400, got.Status)
	assert.Equal(t, int64(0), f.compares.Load())
}

// ─── The 401 is one answer ─────────────────────────────────────────────────

// Every way authentication can fail answers byte-identical bodies and headers, and pays
// exactly ONE secret compare: a stranger learns nothing from the content or the work. The
// control is the paired success, and the guard is that the compare count is not zero for the
// classes that would be tempting to answer early (an unknown tenant, a deleted one).
func TestEveryAuthenticationFailureIsTheSameAnswerAtTheSameCost(t *testing.T) {
	f := newFixture(t)
	f.profile("pp-1", model.ProvisionAllowNew)
	f.profile("pp-off", model.ProvisionAllowNew)
	f.profile("pp-old", model.ProvisionAllowNew)
	f.profile("pp-raw", model.ProvisionAllowNew)
	f.sql(`UPDATE provisioning_profiles SET enabled = 0 WHERE token = 'pp-off'`)
	f.sql(`UPDATE provisioning_profiles SET expires_at = ? WHERE token = 'pp-old'`, time.Now().Add(-time.Hour))
	f.sql(`UPDATE provisioning_profiles SET provision_secret_issued = 0 WHERE token = 'pp-raw'`)

	paired := f.post("acme", f.body("pp-1", "SN-ok", 1))
	require.Equal(t, 201, paired.Status, "the control must succeed: %s", paired.Raw)

	classes := map[string]func() reply{
		"wrong secret": func() reply {
			b := f.body("pp-1", "SN-1", 1)
			b["provisionSecret"] = strings.Repeat("y", 43)
			return f.post("acme", b)
		},
		"unknown key": func() reply {
			b := f.body("pp-1", "SN-1", 1)
			b["provisionKey"] = "nobody"
			return f.post("acme", b)
		},
		"unknown tenant": func() reply { return f.post("nowhere", f.body("pp-1", "SN-1", 1)) },
		"disabled":       func() reply { return f.post("acme", f.body("pp-off", "SN-1", 1)) },
		"expired":        func() reply { return f.post("acme", f.body("pp-old", "SN-1", 1)) },
		"unmarked":       func() reply { return f.post("acme", f.body("pp-raw", "SN-1", 1)) },
		"tenant deleted": func() reply {
			f.deleted.Store(true)
			defer f.deleted.Store(false)
			return f.post("acme", f.body("pp-1", "SN-1", 1))
		},
	}
	var first reply
	for name, send := range classes {
		before, readsBefore := f.compares.Load(), f.profileReads.Load()
		got := send()
		assert.Equal(t, 401, got.Status, name)
		assert.Equal(t, int64(1), f.profileReads.Load()-readsBefore, "%s: a stranger could tell this class by its database round trips", name)
		assert.Equal(t, int64(1), f.compares.Load()-before, "%s: a stranger could tell this class by the work it costs", name)
		assert.Equal(t, "PROVISIONING_FAILED", got.Code(), name)
		if first.Raw == nil {
			first = got
			continue
		}
		assert.Equal(t, string(first.Raw), string(got.Raw), "%s: the body differs", name)
		assert.Equal(t, first.Header.Get("Content-Type"), got.Header.Get("Content-Type"), name)
		assert.Equal(t, first.Header.Get("Retry-After"), got.Header.Get("Retry-After"), name)
	}
	// A deleted tenant is refused through the dummy compare AND the same profile lookup.
	assert.Equal(t, int64(1), f.registrar.refuse.Load())
	d, c := f.rows()
	assert.Equal(t, [2]int64{1, 1}, [2]int64{d, c}, "an authentication failure wrote a row")
}

// ─── Limits ────────────────────────────────────────────────────────────────

// Over the body cap is a 413 that costs no compare and no slot, and the same body under the
// cap is accepted (the paired control).
func TestTheBodyCapIsEnforcedBeforeAnyWork(t *testing.T) {
	f := newFixture(t)
	f.profile("pp-1", model.ProvisionAllowNew)
	b := f.body("pp-1", "SN-1", 1)
	b["name"] = strings.Repeat("n", 100)
	require.Equal(t, 201, f.post("acme", b).Status, "the control: a body under the cap registers")

	b = f.body("pp-1", "SN-2", 2)
	b["name"] = strings.Repeat("n", 9000)
	before := f.compares.Load()
	got := f.post("acme", b)
	assert.Equal(t, 413, got.Status)
	assert.Equal(t, before, f.compares.Load())
	assert.Equal(t, int64(1), f.registrar.auth.Load(), "an oversized body reached authentication")
}

// The per-source bucket answers 429 with Retry-After BEFORE the compare is paid: a source that
// has spent its allowance gets nothing for its wrong guesses, and the guess is not compared.
func TestThePerSourceBucketIsChargedBeforeTheCompare(t *testing.T) {
	f := newFixture(t, withConfig(func(c *Config) { c.SourceBurst = 2; c.SourceEvery = time.Hour }))
	f.profile("pp-1", model.ProvisionAllowNew)
	wrong := f.body("pp-1", "SN-1", 1)
	wrong["provisionSecret"] = strings.Repeat("z", 43)

	assert.Equal(t, 401, f.post("acme", wrong).Status)
	assert.Equal(t, 401, f.post("acme", wrong).Status)
	compares := f.compares.Load()
	got := f.post("acme", wrong)
	assert.Equal(t, 429, got.Status)
	assert.Equal(t, "RATE_LIMITED", got.Code())
	assert.Equal(t, "3600", got.Header.Get("Retry-After"))
	assert.Equal(t, compares, f.compares.Load(), "a rate-limited request paid for a compare")
	assert.Equal(t, 1.0, f.count(ResultRateLimitedSource))
	// The right secret from the same source is limited too: no exemption for a source that
	// would have succeeded.
	assert.Equal(t, 429, f.post("acme", f.body("pp-1", "SN-1", 1)).Status)
}

// Retry-After is at least a second, whatever the schedule.
func TestRetryAfterIsAtLeastOneSecond(t *testing.T) {
	f := newFixture(t, withConfig(func(c *Config) { c.SourceBurst = 1; c.SourceEvery = 900 * time.Millisecond }))
	f.profile("pp-1", model.ProvisionAllowNew)
	f.post("acme", f.body("pp-1", "SN-1", 1))
	got := f.post("acme", f.body("pp-1", "SN-2", 2))
	require.Equal(t, 429, got.Status)
	assert.Equal(t, "1", got.Header.Get("Retry-After"))
}

// The per-tenant bucket is charged only for an AUTHENTICATED request, after authentication:
// failed guesses cost the tenant's allowance nothing, which is what keeps a stranger from
// draining a tenant's registrations by guessing at its key.
func TestThePerTenantBucketIsChargedAfterAuthentication(t *testing.T) {
	f := newFixture(t)
	f.profile("pp-1", model.ProvisionAllowNew)
	wrong := f.body("pp-1", "SN-1", 1)
	wrong["provisionSecret"] = strings.Repeat("z", 43)
	for i := 0; i < 5; i++ {
		require.Equal(t, 401, f.post("acme", wrong).Status)
	}
	assert.Equal(t, int64(0), f.stub.charged.Load(), "a failed authentication spent the tenant's allowance")

	require.Equal(t, 201, f.post("acme", f.body("pp-1", "SN-1", 1)).Status)
	assert.Equal(t, int64(1), f.stub.charged.Load())

	f.stub.deny.Store(true)
	got := f.post("acme", f.body("pp-1", "SN-2", 2))
	assert.Equal(t, 429, got.Status)
	assert.Equal(t, "RATE_LIMITED", got.Code())
	assert.Equal(t, "1", got.Header.Get("Retry-After"))
	assert.Equal(t, 1.0, f.count(ResultRateLimitedTenant))
	assert.Equal(t, int64(1), f.registrar.provision.Load(), "a tenant-limited request registered")
	d, _ := f.rows()
	assert.Equal(t, int64(1), d)
}

// The real core limiter, fed a governance-shaped ceiling: a burst of two admits two and
// refuses the third, while another tenant's allowance is its own.
func TestThePerTenantBucketIsTheCoreLimiter(t *testing.T) {
	f := newFixture(t, withTenants(core.NewTenantRateLimiter(core.StaticCeiling(0.001, 2))))
	f.profile("pp-1", model.ProvisionAllowNew)
	assert.Equal(t, 201, f.post("acme", f.body("pp-1", "SN-1", 1)).Status)
	assert.Equal(t, 201, f.post("acme", f.body("pp-1", "SN-2", 2)).Status)
	assert.Equal(t, 429, f.post("acme", f.body("pp-1", "SN-3", 3)).Status)
	// A replay is a request like any other.
	assert.Equal(t, 429, f.post("acme", f.body("pp-1", "SN-1", 1)).Status)
}

// A busy replica refuses with 503 + Retry-After instead of queueing, and it does so without
// running a compare: the slot is taken before authentication.
func TestNoFreeSlotIsA503(t *testing.T) {
	f := newFixture(t, withConfig(func(c *Config) { c.MaxInFlight = 1 }))
	f.profile("pp-1", model.ProvisionAllowNew)
	f.registrar.hold = make(chan struct{})
	f.registrar.entered = make(chan struct{}, 1)
	held := make(chan reply, 1)
	go func() { held <- f.post("acme", f.body("pp-1", "SN-1", 1)) }()
	<-f.registrar.entered

	// The slot is occupied: the gauge says so, and the next request is refused.
	assert.Equal(t, 1.0, testutil.ToFloat64(f.metrics.InFlight))
	got := f.post("acme", f.body("pp-1", "SN-2", 2))
	assert.Equal(t, 503, got.Status)
	assert.Equal(t, "PROVISIONING_UNAVAILABLE", got.Code())
	assert.Equal(t, "1", got.Header.Get("Retry-After"))
	assert.Equal(t, 1.0, f.count(ResultBusy))
	assert.Equal(t, int64(1), f.registrar.auth.Load(), "a refused request reached authentication")

	close(f.registrar.hold)
	assert.Equal(t, 201, (<-held).Status)
	assert.Equal(t, 0.0, testutil.ToFloat64(f.metrics.InFlight), "the slot was not released")
	// And it is free again.
	assert.Equal(t, 201, f.post("acme", f.body("pp-1", "SN-2", 2)).Status)
}

// A rejected-before-the-slot request never moves the in-flight gauge, which is what makes
// the gauge a measure of work and not of connections.
func TestAnInvalidRequestTakesNoSlot(t *testing.T) {
	f := newFixture(t, withConfig(func(c *Config) { c.MaxInFlight = 1 }))
	f.profile("pp-1", model.ProvisionAllowNew)
	f.registrar.hold = make(chan struct{})
	f.registrar.entered = make(chan struct{}, 1)
	held := make(chan reply, 1)
	go func() { held <- f.post("acme", f.body("pp-1", "SN-1", 1)) }()
	<-f.registrar.entered

	// With the only slot taken, a malformed body is still answered 400 (not 503): it was
	// refused before the slot.
	bad := f.body("pp-1", "SN-2", 2)
	bad["credentialSecret"] = "short"
	assert.Equal(t, 400, f.post("acme", bad).Status)
	assert.Equal(t, 413, f.post("acme", strings.Repeat("x", 9000)).Status)
	assert.Equal(t, 400, f.post("acme", "{not json").Status)
	assert.Equal(t, 0.0, f.count(ResultBusy))
	close(f.registrar.hold)
	<-held
}

// ─── The source address ────────────────────────────────────────────────────

// With no trusted proxy, X-Forwarded-For is a header any client can write and is ignored: a
// sender that rotates it is still one source.
func TestASpoofedForwardedForDoesNotBuyAFreshBucket(t *testing.T) {
	f := newFixture(t, withConfig(func(c *Config) { c.SourceBurst = 2; c.SourceEvery = time.Hour }))
	f.profile("pp-1", model.ProvisionAllowNew)
	var statuses []int
	for i, xff := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"} {
		statuses = append(statuses, f.post("acme", f.body("pp-1", "SN-x", i+1), "X-Forwarded-For", xff).Status)
	}
	assert.NotEqual(t, 429, statuses[0])
	assert.NotEqual(t, 429, statuses[1])
	assert.Equal(t, 429, statuses[2], "the spoofed addresses were believed")
	assert.Equal(t, 429, statuses[3], "the spoofed addresses were believed")
}

// Behind a trusted proxy each client address is its own source, and a client that prefixes
// the header with addresses of its own gains nothing: the right-most entry the trusted
// proxy wrote is the one used.
func TestBehindATrustedProxyTheClientAddressIsTheSource(t *testing.T) {
	f := newFixture(t, withTrusted("127.0.0.0/8", "::1/128"),
		withConfig(func(c *Config) { c.SourceBurst = 1; c.SourceEvery = time.Hour }))
	f.profile("pp-1", model.ProvisionAllowNew)
	send := func(xff string) int {
		return f.post("acme", f.body("pp-1", "SN-"+xff, 1), "X-Forwarded-For", xff).Status
	}

	assert.Equal(t, 201, send("203.0.113.1"), "a first client")
	assert.Equal(t, 201, send("203.0.113.2"), "a second client has a bucket of its own")
	assert.Equal(t, 429, send("203.0.113.1"), "the first client's allowance is spent")
	// An attacker prepends fresh addresses; the proxy appended the real one, which is unchanged.
	for _, spoof := range []string{"9.9.9.1, 203.0.113.1", "9.9.9.2, 203.0.113.1", "10.0.0.1, 9.9.9.3, 203.0.113.1"} {
		assert.Equal(t, 429, send(spoof), "a prefixed header bought a fresh bucket: %q", spoof)
	}
}

// IPv6 sources are bucketed by their /64: walking addresses inside one subscriber's block
// buys nothing.
func TestAnIPv6BlockIsOneSource(t *testing.T) {
	f := newFixture(t, withTrusted("127.0.0.0/8"),
		withConfig(func(c *Config) { c.SourceBurst = 1; c.SourceEvery = time.Hour }))
	f.profile("pp-1", model.ProvisionAllowNew)
	send := func(xff string) int { return f.post("acme", f.body("pp-1", "SN-1", 1), "X-Forwarded-For", xff).Status }
	assert.Equal(t, 201, send("2001:db8:1:2::1"))
	assert.Equal(t, 429, send("2001:db8:1:2:ffff::9"))
	assert.Equal(t, 200, send("2001:db8:1:3::1"), "another /64 is another source")
}

// A source address behind NO believed proxy is the connection's peer, even when the header
// is well formed and names a private address.
func TestNoTrustedProxiesMeansThePeer(t *testing.T) {
	f := newFixture(t, withConfig(func(c *Config) { c.SourceBurst = 1; c.SourceEvery = time.Hour }))
	f.profile("pp-1", model.ProvisionAllowNew)
	assert.Equal(t, 201, f.post("acme", f.body("pp-1", "SN-1", 1)).Status)
	assert.Equal(t, 429, f.post("acme", f.body("pp-1", "SN-1", 1), "X-Forwarded-For", "10.9.8.7").Status)
}

// ─── Construction fails closed ─────────────────────────────────────────────

func TestNewRefusesAnythingThatLeavesALayerOff(t *testing.T) {
	f := newFixture(t)
	for name, mutate := range map[string]func(*Config, *Deps){
		"no read deadline":   func(c *Config, d *Deps) { c.ReadDeadline = 0 },
		"no slots":           func(c *Config, d *Deps) { c.MaxInFlight = 0 },
		"no db places":       func(c *Config, d *Deps) { c.MaxConcurrentRegistrations = 0 },
		"no source burst":    func(c *Config, d *Deps) { c.SourceBurst = 0 },
		"no source schedule": func(c *Config, d *Deps) { c.SourceEvery = 0 },
		"no registrar":       func(c *Config, d *Deps) { d.Registrar = nil },
		"no tenant limiter":  func(c *Config, d *Deps) { d.Tenants = nil },
		"no metrics":         func(c *Config, d *Deps) { d.Metrics = nil },
		"no instance id":     func(c *Config, d *Deps) { d.InstanceId = "" },
	} {
		cfg, deps := f.cfg, f.deps
		mutate(&cfg, &deps)
		_, err := New(cfg, deps)
		assert.Error(t, err, name)
	}
	_, err := New(f.cfg, f.deps)
	assert.NoError(t, err, "the unmutated control must construct")
}
