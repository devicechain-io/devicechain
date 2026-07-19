// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
)

// selfSignedCAPEM returns a PEM-encoded self-signed CA certificate for exercising
// the TLSConfig cert-pool path without pulling in the tls provider's output.
func selfSignedCAPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating cert: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// The shipped default instance configuration applies defaults cleanly and passes
// validation.
func TestDefaultInstanceConfigurationValid(t *testing.T) {
	cfg := NewDefaultInstanceConfiguration()
	cfg.ApplyDefaults()
	if cfg.Infrastructure.Nats.StreamReplicas == 0 {
		t.Fatal("ApplyDefaults should default NATS stream replicas to 1")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default instance configuration should be valid, got %v", err)
	}
}

// The stream size bounds are fail-safe (ADR-023 never-unlimited): an omitted (0)
// or negative value is coerced to the platform default rather than left at 0,
// which JetStream would treat as unlimited; an explicit positive value is honored.
func TestApplyDefaultsStreamBounds(t *testing.T) {
	t.Run("zero and negative coerce to platform default", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			set  func(*NatsConfiguration)
		}{
			{"unset", func(n *NatsConfiguration) {}},
			{"negative", func(n *NatsConfiguration) {
				n.StreamMaxBytes, n.StreamMaxMsgs, n.StreamMaxMsgSize = -1, -1, -1
			}},
		} {
			cfg := &InstanceConfiguration{}
			tc.set(&cfg.Infrastructure.Nats)
			cfg.ApplyDefaults()
			n := cfg.Infrastructure.Nats
			if n.StreamMaxBytes != DefaultStreamMaxBytes || n.StreamMaxMsgs != DefaultStreamMaxMsgs || n.StreamMaxMsgSize != DefaultStreamMaxMsgSize {
				t.Errorf("%s: bounds not defaulted: %d/%d/%d", tc.name, n.StreamMaxBytes, n.StreamMaxMsgs, n.StreamMaxMsgSize)
			}
		}
	})

	t.Run("explicit positive values are honored", func(t *testing.T) {
		cfg := &InstanceConfiguration{}
		cfg.Infrastructure.Nats.StreamMaxBytes = 999
		cfg.Infrastructure.Nats.StreamMaxMsgs = 42
		cfg.Infrastructure.Nats.StreamMaxMsgSize = 7
		cfg.ApplyDefaults()
		n := cfg.Infrastructure.Nats
		if n.StreamMaxBytes != 999 || n.StreamMaxMsgs != 42 || n.StreamMaxMsgSize != 7 {
			t.Errorf("explicit bounds overwritten: %d/%d/%d", n.StreamMaxBytes, n.StreamMaxMsgs, n.StreamMaxMsgSize)
		}
	})
}

// JetStream reserves each stream's MaxBytes UP FRONT at creation, so the disk
// floor is the SUM of the ceilings — not what the streams hold. Overrunning the
// broker's max_file_store is not a soft failure: it crashlooped the last
// stream-creating services with "insufficient storage resources available" when
// 13 × 1 GiB was pointed at an 8 GiB PV. This pins the arithmetic that makes the
// PV size safe, which is the invariant the classification exists to serve.

// The shipped NATS PV and the server-level ceiling that derives from it.
//
// max_file_store is NOT a plain 90% of the PV. The module splits the size into
// magnitude and unit, floors 90% of the MAGNITUDE, and reattaches the unit
// (js_max_file_store in deploy/opentofu/modules/nats/main.tf), so a 12Gi PV
// yields floor(12 * 0.9) = 10 → "10Gi" — verified against a live cluster, which
// reports max_file_store 10Gi against a 12Gi PVC.
//
// Modelling it as an exact 90% (10.8 GiB) overstates the real ceiling by 819 MiB.
// That matters because these tests are the only guard on the reservation: against
// an inflated ceiling a future increase could pass here while leaving a fresh
// install 819 MiB less real headroom than the floor below claims to enforce —
// which, against that floor, is most of it. Integer division floors exactly as the
// module does.
const (
	pvGi               = 12
	pvBytes      int64 = pvGi << 30
	maxFileStore int64 = (pvGi * 9 / 10) << 30
)

func TestStreamReservationFitsBudget(t *testing.T) {
	cfg := &InstanceConfiguration{}
	cfg.ApplyDefaults()
	n := cfg.Infrastructure.Nats

	var total int64
	for _, s := range streams.Suffixes() {
		total += n.StreamMaxBytesFor(s)
	}
	// The MQTT gateway's own streams ($MQTT_msgs, $MQTT_out) are created by
	// nats-server, not by us, and it creates them UNBOUNDED. They share this same
	// max_file_store, so once messaging.ReconcileMqttStores gives them a ceiling
	// that ceiling is reserved up front like any other — and belongs in the sum.
	// Counting them was the point of bounding them: an unbounded stream cannot be
	// budgeted for at all, only hoped about.
	total += n.MqttStoreReservation()
	// The KV buckets are streams too, backed by KV_-prefixed streams in the same
	// account, so a bounded bucket reserves its ceiling up front exactly like the
	// rest. They used to be unbounded and therefore unbudgetable — counted as
	// "headroom" that anything could eat — which is the same hope-not-arithmetic
	// gap the MQTT stores had before they were bounded.
	total += n.KvReservation()

	if total > maxFileStore {
		t.Errorf("stream reservation %d B exceeds max_file_store %d B (PV %d B): "+
			"a fresh bring-up will crashloop once the reservation overruns the store",
			total, maxFileStore, pvBytes)
	}
}

// The budget must have room for the streams the platform does NOT create as well
// as the ones it does, so a future ceiling increase that eats that room fails here
// rather than on someone's fresh install.
//
// What is left over shrank deliberately when the KV buckets were bounded, and the
// floor below shrank with it. That is not a weakening: before, the buckets were
// unbounded, so this headroom was covering an OPEN-ENDED consumer — a floor under
// a quantity nothing capped. Now the buckets reserve a known ceiling that is
// counted in `reserved`, and what remains covers only genuinely unaccounted
// things. Trading a larger floor over an unbounded consumer for a smaller one
// over a bounded set is the entire point of the exercise.
func TestBudgetLeavesHeadroomForUnaccountedStreams(t *testing.T) {
	cfg := &InstanceConfiguration{}
	cfg.ApplyDefaults()
	n := cfg.Infrastructure.Nats

	var reserved int64
	for _, s := range streams.Suffixes() {
		reserved += n.StreamMaxBytesFor(s)
	}
	reserved += n.MqttStoreReservation()
	reserved += n.KvReservation()

	// What is still NOT reserved: the MQTT gateway's $MQTT_sess and $MQTT_rmsgs,
	// which are deliberately left unbounded (both are DiscardOld, so a ceiling
	// would evict live sessions and retained messages rather than protect
	// anything), plus per-consumer state and JetStream's own metadata. None of
	// those scales with fleet size the way the KV caches do, which is why a floor
	// half the previous size still covers strictly more than it used to.
	const headroomFloor = 512 << 20 // 512 MiB
	if got := maxFileStore - reserved; got < headroomFloor {
		t.Errorf("only %d B left unreserved, want at least %d B: $MQTT_sess and "+
			"$MQTT_rmsgs are deliberately unbounded and consume this same store, "+
			"as does per-consumer JetStream state", got, int64(headroomFloor))
	}
}

// The KV reservation must be derived from the declared inventory rather than from
// a hand-maintained count, the way core/streams exists because that exact mirror
// had already gone wrong once for message streams.
//
// Note what this can and cannot catch. Both sides iterate kv.All, so DECLARING a
// bucket raises both in lockstep and cannot fail here — that direction is the
// point, since deriving from the inventory is what makes a new bucket budgeted
// automatically. What it does catch is the reservation drifting away from the
// inventory: a KvReservation that skips a tier, applies the wrong ceiling, or
// stops walking kv.All at all.
func TestKvReservationCoversEveryDeclaredBucket(t *testing.T) {
	cfg := &InstanceConfiguration{}
	cfg.ApplyDefaults()
	n := cfg.Infrastructure.Nats

	want := int64(kv.Count(kv.Cache))*n.KvCacheMaxBytes + int64(kv.Count(kv.State))*n.KvStateMaxBytes
	if got := n.KvReservation(); got != want {
		t.Errorf("KvReservation() = %d, want %d: the reservation has drifted from the "+
			"declared bucket set, so some bucket reserves disk nothing budgets for", got, want)
	}
	if kv.Count(kv.Cache) == 0 || kv.Count(kv.State) == 0 {
		t.Fatal("both tiers must be populated for this test to mean anything")
	}
}

// A cache bucket must take a ceiling no larger than a state bucket's. The tier
// split only protects the budget while the fleet-scaling buckets are the smaller
// ones; inverting it would point the larger ceiling at the buckets that grow with
// device count, which is the shape that made the reservation unaffordable before.
func TestCacheTierIsNotLargerThanStateTier(t *testing.T) {
	cfg := &InstanceConfiguration{}
	cfg.ApplyDefaults()
	n := cfg.Infrastructure.Nats

	if n.KvCacheMaxBytes > n.KvStateMaxBytes {
		t.Errorf("cache ceiling %d B exceeds state ceiling %d B: the fleet-scaling "+
			"buckets are the cache ones, so they must not carry the larger bound",
			n.KvCacheMaxBytes, n.KvStateMaxBytes)
	}
}

// Both KV ceilings are fail-safe: a zero or negative value must become the
// platform default rather than reaching JetStream, which reads 0 as UNLIMITED
// (ADR-023 never-unlimited).
func TestKvCeilingsAreNeverUnlimited(t *testing.T) {
	for _, tc := range []int64{0, -1} {
		cfg := &InstanceConfiguration{}
		cfg.Infrastructure.Nats.KvCacheMaxBytes = tc
		cfg.Infrastructure.Nats.KvStateMaxBytes = tc
		cfg.ApplyDefaults()
		n := cfg.Infrastructure.Nats
		if n.KvCacheMaxBytes != DefaultKvCacheMaxBytes || n.KvStateMaxBytes != DefaultKvStateMaxBytes {
			t.Errorf("ceiling %d survived ApplyDefaults as (%d, %d): a non-positive "+
				"MaxBytes means UNLIMITED to JetStream", tc, n.KvCacheMaxBytes, n.KvStateMaxBytes)
		}
	}
}

// The hot/cold split keys on the SUFFIX, and an unclassified suffix must fall to
// the HOT (larger) bound. That direction is the safe one: over-reserving disk is
// cheap and visible, while under-bounding a busy stream silently evicts live data
// via DiscardOld. A regression here is invisible in a dev cluster.
func TestStreamMaxBytesFor(t *testing.T) {
	cfg := &InstanceConfiguration{}
	cfg.ApplyDefaults()
	n := cfg.Infrastructure.Nats

	if got := n.StreamMaxBytesFor("inbound-events"); got != DefaultStreamMaxBytes {
		t.Errorf("hot suffix got %d, want %d", got, DefaultStreamMaxBytes)
	}
	if got := n.StreamMaxBytesFor("device-roster"); got != DefaultStreamMaxBytesCold {
		t.Errorf("cold suffix got %d, want %d", got, DefaultStreamMaxBytesCold)
	}
	// The fail-safe direction: an unknown suffix (a newly added stream nobody
	// classified, or a renamed one) must NOT silently inherit the cold bound.
	if got := n.StreamMaxBytesFor("a-suffix-nobody-classified"); got != DefaultStreamMaxBytes {
		t.Errorf("unknown suffix got %d, want the hot bound %d", got, DefaultStreamMaxBytes)
	}

	// There is deliberately no "is every classified suffix real?" check here any
	// more. That check existed because the tier lived in a separate map keyed by
	// literal, where a typo left an entry inert and silently promoted its stream to
	// the hot bound. The tier now travels WITH the declaration in core/streams, so
	// a suffix cannot be classified without existing — the bug is unrepresentable
	// rather than tested for.
}

// ApplyDefaults selects the zero-infra secret-store default (envelope-in-Postgres,
// instance KEK) when omitted, but never synthesizes a root key.
func TestApplyDefaultsSecrets(t *testing.T) {
	cfg := &InstanceConfiguration{}
	cfg.ApplyDefaults()
	s := cfg.Infrastructure.Secrets
	if s.Backend != DefaultSecretsBackend || s.KEKProvider != DefaultSecretsKEKProvider {
		t.Fatalf("secret-store defaults not applied: backend=%q kek=%q", s.Backend, s.KEKProvider)
	}
	if s.RootKey != "" {
		t.Fatalf("ApplyDefaults must not synthesize a root key, got %q", s.RootKey)
	}

	// An explicit selection is honored.
	cfg2 := &InstanceConfiguration{}
	cfg2.Infrastructure.Secrets.Backend = "vault"
	cfg2.Infrastructure.Secrets.KEKProvider = "gcpkms"
	cfg2.ApplyDefaults()
	if cfg2.Infrastructure.Secrets.Backend != "vault" || cfg2.Infrastructure.Secrets.KEKProvider != "gcpkms" {
		t.Fatal("explicit secret-store selection overwritten by defaults")
	}
}

// DecodedRootKey fails closed on an absent, malformed, or wrong-length key and
// returns the 32-byte key when well-formed.
func TestDecodedRootKey(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(make([]byte, 32))
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))

	if _, err := (SecretsConfiguration{RootKey: ""}).DecodedRootKey(); err == nil {
		t.Fatal("absent root key must fail closed")
	}
	if _, err := (SecretsConfiguration{RootKey: "not*base64"}).DecodedRootKey(); err == nil {
		t.Fatal("malformed base64 must fail closed")
	}
	if _, err := (SecretsConfiguration{RootKey: short}).DecodedRootKey(); err == nil {
		t.Fatal("a 16-byte key must fail closed (want 256-bit)")
	}
	raw, err := (SecretsConfiguration{RootKey: good}).DecodedRootKey()
	if err != nil {
		t.Fatalf("well-formed root key: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("decoded key is %d bytes, want 32", len(raw))
	}
}

// Validate rejects a present-but-malformed root key (so a misrendered key surfaces
// at startup) while allowing an absent one (only a consuming service requires it).
func TestValidateSecretsRootKey(t *testing.T) {
	cfg := NewDefaultInstanceConfiguration()
	cfg.Infrastructure.Secrets.RootKey = "not-valid-base64-!!"
	if err := cfg.Validate(); err == nil {
		t.Fatal("a malformed root key must fail validation")
	}

	cfg2 := NewDefaultInstanceConfiguration()
	cfg2.Infrastructure.Secrets.RootKey = "" // absent is allowed
	if err := cfg2.Validate(); err != nil {
		t.Fatalf("an absent root key must pass validation, got %v", err)
	}

	cfg3 := NewDefaultInstanceConfiguration()
	cfg3.Infrastructure.Secrets.RootKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := cfg3.Validate(); err != nil {
		t.Fatalf("a well-formed root key must pass validation, got %v", err)
	}
}

// Validate fails closed when the NATS backbone or the user-management endpoint
// (which every service validates tokens against) is missing.
func TestInstanceConfigurationValidateFailsClosed(t *testing.T) {
	cases := map[string]func(*InstanceConfiguration){
		"missing nats host": func(c *InstanceConfiguration) { c.Infrastructure.Nats.Hostname = "" },
		"missing nats port": func(c *InstanceConfiguration) { c.Infrastructure.Nats.Port = 0 },
		"missing um host":   func(c *InstanceConfiguration) { c.Infrastructure.UserManagement.Hostname = "" },
		"missing um port":   func(c *InstanceConfiguration) { c.Infrastructure.UserManagement.Port = 0 },
	}
	for name, mutate := range cases {
		cfg := NewDefaultInstanceConfiguration()
		mutate(cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
}

// Validate rejects a ceiling pair whose tiers have been inverted.
//
// Both splits put the SMALLER ceiling on the tier that grows, which is the only
// reason the reservation is affordable at all. Inverting one keeps the sum almost
// unchanged, so the budget tests above stay green while the bound now sits on the
// side that cannot grow — the failure is invisible everywhere else, which is why
// it is worth a startup error.
func TestValidateRejectsInvertedTierCeilings(t *testing.T) {
	cases := map[string]struct {
		mutate func(*NatsConfiguration)
		want   string
	}{
		"cold stream bound above hot": {
			mutate: func(n *NatsConfiguration) {
				n.StreamMaxBytes = 64 << 20
				n.StreamMaxBytesCold = 128 << 20
			},
			want: "streamMaxBytesCold",
		},
		"kv cache ceiling above state": {
			mutate: func(n *NatsConfiguration) {
				n.KvCacheMaxBytes = 256 << 20
				n.KvStateMaxBytes = 128 << 20
			},
			want: "kvCacheMaxBytes",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := NewDefaultInstanceConfiguration()
			cfg.ApplyDefaults()
			tc.mutate(&cfg.Infrastructure.Nats)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("inverted ceilings validated cleanly: the bound is now on the "+
					"tier that grows and nothing else reports it (%s)", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %s, so it cannot tell an operator "+
					"which of the two numbers to change", err, tc.want)
			}
		})
	}
}

// The guard must not fire on a legitimately small deployment: lowering BOTH
// ceilings together — which is what the compact preset does — is the supported way
// to shrink the reservation, and equal ceilings are fine too. This is the
// counterweight to the test above; a guard that rejects valid configurations is a
// worse bug than the one it prevents, because it fails a bring-up that would have
// worked.
func TestValidateAcceptsProportionallyLoweredCeilings(t *testing.T) {
	for _, tc := range []struct {
		name             string
		cold, hot        int64
		kvCache, kvState int64
	}{
		{"lowered together", 16 << 20, 64 << 20, 8 << 20, 16 << 20},
		{"equal ceilings", 64 << 20, 64 << 20, 16 << 20, 16 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewDefaultInstanceConfiguration()
			cfg.ApplyDefaults()
			n := &cfg.Infrastructure.Nats
			n.StreamMaxBytesCold, n.StreamMaxBytes = tc.cold, tc.hot
			n.KvCacheMaxBytes, n.KvStateMaxBytes = tc.kvCache, tc.kvState
			if err := cfg.Validate(); err != nil {
				t.Errorf("valid small-footprint sizing rejected: %v", err)
			}
		})
	}
}

// TLSConfig returns nil (plaintext) when TLS is disabled, fails closed on an
// enabled-but-unusable CA, and otherwise builds a pool-backed config carrying the
// requested serverName (ADR-025).
func TestNatsTLSConfig(t *testing.T) {
	t.Run("disabled returns nil", func(t *testing.T) {
		c := NatsConfiguration{Hostname: "dc-nats.dc-system"}
		tc, err := c.TLSConfig("dc-nats.dc-system")
		if err != nil || tc != nil {
			t.Fatalf("disabled TLS should yield (nil, nil), got (%v, %v)", tc, err)
		}
	})

	t.Run("enabled with empty CA fails closed", func(t *testing.T) {
		c := NatsConfiguration{Tls: NatsTlsConfiguration{Enabled: true}}
		if _, err := c.TLSConfig("dc-nats.dc-system"); err == nil {
			t.Fatal("enabled TLS with empty CA should error")
		}
	})

	t.Run("enabled with junk CA fails closed", func(t *testing.T) {
		c := NatsConfiguration{Tls: NatsTlsConfiguration{Enabled: true, Ca: "not a pem"}}
		if _, err := c.TLSConfig("dc-nats.dc-system"); err == nil {
			t.Fatal("enabled TLS with an unparseable CA should error")
		}
	})

	t.Run("enabled with valid CA builds config", func(t *testing.T) {
		c := NatsConfiguration{Tls: NatsTlsConfiguration{Enabled: true, Ca: selfSignedCAPEM(t)}}
		tc, err := c.TLSConfig("dc-nats.dc-system")
		if err != nil {
			t.Fatalf("valid CA should not error: %v", err)
		}
		if tc == nil || tc.RootCAs == nil {
			t.Fatal("expected a tls.Config with a populated RootCAs pool")
		}
		if tc.ServerName != "dc-nats.dc-system" {
			t.Errorf("ServerName = %q, want dc-nats.dc-system", tc.ServerName)
		}
		if tc.MinVersion != tls.VersionTLS12 {
			t.Errorf("MinVersion = %d, want TLS 1.2", tc.MinVersion)
		}
	})
}
