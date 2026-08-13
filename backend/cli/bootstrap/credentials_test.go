// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"golang.org/x/crypto/bcrypt"
)

// The root key was not the only credential a bootstrap RE-RUN rotated out from
// under a live instance — it was just the one whose damage was permanent, so it was
// the one that got fixed. These pin the rest (ADR-020/028 A8).
//
// What the others cost is different in kind but not small: the broker-auth material
// lives on both sides of a boundary whose two halves update on different schedules
// (the NATS ConfigMap when its reloader sidecar sees the projected file change; the
// services when Helm rolls them), so rotating it opens an interval in which the
// broker enforces one credential and the pods present another. Found live on
// 2026-07-27: every new pod in CrashLoopBackOff on `nats: Authorization Violation`,
// which is enough to fail the Helm step of the run that caused it.

// deployedInstance builds a config for an instance already running, and hands back
// the credentials it was built from so a test can assert against the exact values
// the cluster holds rather than against a restatement of them.
func deployedInstance(t *testing.T) (*config.InstanceConfiguration, natsauth.Credentials, string) {
	t.Helper()
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatalf("generating credentials for the fake deployed instance: %v", err)
	}
	const serviceAuth = "deployed-service-auth-secret"
	cfg := &config.InstanceConfiguration{}
	cfg.Infrastructure.Nats.Auth.User = natsauth.ServiceUser
	cfg.Infrastructure.Nats.Auth.Password = creds.ServicePassword
	cfg.Infrastructure.Nats.Auth.CalloutIssuerSeed = creds.IssuerSeed
	cfg.Infrastructure.ServiceAuth.Secret = serviceAuth
	cfg.Infrastructure.Secrets.RootKey = testRootKey
	return cfg, creds, serviceAuth
}

// rerun drives stepRenderConfig against an instance that is already deployed.
//
// 🔑 The optional hashes are applied AFTER withDeployedInstance, and the order is
// load-bearing: withDeployedInstance installs its own "no hashes deployed" stub, so a
// caller that set one first would have it silently overwritten and would be testing
// the fallback while believing it tested reuse.
func rerun(t *testing.T, cfg *config.InstanceConfiguration, hashes ...natsauth.DeployedHashes) *State {
	t.Helper()
	withDeployedInstance(t, cfg, nil)
	for _, h := range hashes {
		withDeployedBrokerHashes(t, h)
	}
	st := &State{Instance: "prod", BuildImages: true, Values: map[string]string{}}
	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatalf("stepRenderConfig: %v", err)
	}
	return st
}

// The whole slice in one assertion: a re-run configures the instance with exactly
// what it is already running.
func TestARerunReusesEveryDeployedCredential(t *testing.T) {
	cfg, creds, serviceAuth := deployedInstance(t)
	st := rerun(t, cfg)

	for _, c := range []struct {
		key, want, why string
	}{
		{"natsServicePassword", creds.ServicePassword,
			"every service presents this to the broker; a new one is rejected until the broker's config catches up"},
		{"natsCalloutIssuerSeed", creds.IssuerSeed,
			"device-management signs device user JWTs with this; a new one makes the broker reject every device connect"},
		{"serviceAuthSecret", serviceAuth,
			"user-management verifies cross-service calls against this"},
		{"secretsRootKey", testRootKey,
			"this is the KEK wrapping every stored secret"},
	} {
		if got := st.Values[c.key]; got != c.want {
			t.Errorf("a re-run rotated %s (%q, want the deployed %q).\n  %s", c.key, got, c.want, c.why)
		}
	}
}

// The issuer PUBLIC key is the half that lives in the broker's ConfigMap, and it is
// not stored anywhere dcctl can read — it is derived from the seed. Deriving it is
// what makes the re-run leave the broker config byte-identical, so nothing reloads.
func TestARerunLeavesTheBrokerIssuerUntouched(t *testing.T) {
	cfg, creds, _ := deployedInstance(t)
	st := rerun(t, cfg)

	if got := st.Values["natsCalloutIssuerPublic"]; got != creds.IssuerPublic {
		t.Fatalf("a re-run would rewrite the broker's callout issuer (%q, want %q).\n"+
			"The responder would then be signing with a seed the broker no longer trusts, "+
			"and every device connect would be rejected until the two sides converged.", got, creds.IssuerPublic)
	}
}

// Whatever hash a re-run ships, it must verify the password the services present.
// This is the invariant that outranks every optimisation below it: get it wrong and
// the broker rejects every service connection the moment it loads the config.
func TestTheServicePasswordHashAlwaysVerifies(t *testing.T) {
	cfg, creds, _ := deployedInstance(t)
	st := rerun(t, cfg)

	hash := st.Values["natsServicePasswordBcrypt"]
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(creds.ServicePassword)); err != nil {
		t.Fatalf("the re-run's hash does not verify the password the services present: %v.\n"+
			"The broker would reject every service connection once it loaded this config.", err)
	}
}

// 🔴 THE HASH MUST NOT MOVE WHEN NOTHING MOVED, and this is not tidiness. The nats
// module stamps a checksum of the rendered ConfigMap onto the broker's pod template
// so that config changes are actually adopted (nats-server cannot hot-reload the
// auth_callout block, and refuses the whole reload when it changes). A hash that is
// re-salted on every re-run therefore rolls the broker every time — dropping every
// MQTT device and stalling ingest for ~a minute — for a credential nobody changed.
//
// Measured on a live cluster before this was fixed: two identical applies in a row,
// differing only in bcrypt's salt, produced two different pod-template checksums.
func TestARerunKeepsTheBrokersExistingHashes(t *testing.T) {
	cfg, creds, _ := deployedInstance(t)
	cfg.Infrastructure.Nats.Auth.SysPassword = creds.SysPassword
	st := rerun(t, cfg, natsauth.DeployedHashes{
		Service: creds.ServicePasswordBcrypt,
		Sys:     creds.SysPasswordBcrypt,
	})

	for _, c := range []struct{ key, want string }{
		{"natsServicePasswordBcrypt", creds.ServicePasswordBcrypt},
		{"natsSysPasswordBcrypt", creds.SysPasswordBcrypt},
	} {
		if got := st.Values[c.key]; got != c.want {
			t.Errorf("a re-run rewrote %s even though the password did not change.\n"+
				"  got  %q\n  want %q\n"+
				"That is a changed ConfigMap, which is a changed pod-template checksum, "+
				"which is a broker restart on an idempotent re-run.", c.key, got, c.want)
		}
	}
}

// The counterweight, and the thing that makes reading a hash out of a live cluster
// safe at all: a supplied hash that does NOT verify is discarded, not shipped. Drives
// the real step with a well-formed bcrypt hash of the wrong password — the shape a
// stale ConfigMap or a mis-parse would produce.
func TestARerunDiscardsABrokerHashThatDoesNotVerify(t *testing.T) {
	cfg, creds, _ := deployedInstance(t)
	wrong, err := bcrypt.GenerateFromPassword([]byte("not-the-deployed-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	st := rerun(t, cfg, natsauth.DeployedHashes{Service: string(wrong)})

	hash := st.Values["natsServicePasswordBcrypt"]
	if hash == string(wrong) {
		t.Fatal("a re-run shipped a hash that does not verify the deployed password. " +
			"The broker would load it and then reject every service connection — the exact " +
			"failure the reuse path exists to prevent, now caused by the reuse path.")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(creds.ServicePassword)); err != nil {
		t.Fatalf("after discarding the bad hash the re-run did not produce a working one: %v", err)
	}
}

// The reuse branch must not have swallowed the fresh-install path: a first bootstrap
// still mints, and mints something different every time.
func TestAFreshInstallStillMintsBrokerCredentials(t *testing.T) {
	first := rerun(t, nil)
	second := rerun(t, nil)

	for _, key := range []string{
		"natsServicePassword", "natsServicePasswordBcrypt",
		"natsSysPassword", "natsSysPasswordBcrypt",
		"natsCalloutIssuerSeed", "natsCalloutIssuerPublic", "serviceAuthSecret",
	} {
		if first.Values[key] == "" {
			t.Errorf("a fresh install minted no %s", key)
		}
		if first.Values[key] == second.Values[key] {
			t.Errorf("two fresh installs got the same %s, so nothing is being generated", key)
		}
	}
	// The seed and the public key must belong to each other, or the broker trusts an
	// issuer that nothing signs with.
	derived, err := natsauth.CredentialsFromDeployed(
		first.Values["natsCalloutIssuerSeed"], first.Values["natsServicePassword"],
		first.Values["natsSysPassword"], natsauth.DeployedHashes{})
	if err != nil {
		t.Fatalf("the minted seed is not usable: %v", err)
	}
	if derived.IssuerPublic != first.Values["natsCalloutIssuerPublic"] {
		t.Error("the minted issuer public key does not belong to the minted seed")
	}
}

// A live instance whose config is missing the broker credentials is a state nothing
// should produce, so the run must stop rather than fall back to minting — the
// fallback IS the rotation this slice exists to prevent.
func TestARerunOverAnInstanceMissingItsBrokerCredentialsFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*config.InstanceConfiguration)
	}{
		{"no issuer seed", func(c *config.InstanceConfiguration) {
			c.Infrastructure.Nats.Auth.CalloutIssuerSeed = ""
		}},
		{"no service password", func(c *config.InstanceConfiguration) {
			c.Infrastructure.Nats.Auth.Password = ""
		}},
		{"unusable issuer seed", func(c *config.InstanceConfiguration) {
			c.Infrastructure.Nats.Auth.CalloutIssuerSeed = "not-an-nkey-seed"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _, _ := deployedInstance(t)
			tc.mut(cfg)
			withDeployedInstance(t, cfg, nil)
			st := &State{Instance: "prod", BuildImages: true, Values: map[string]string{}}

			if err := stepRenderConfig(t.Context(), st); err == nil {
				t.Fatal("a re-run continued past a live instance whose broker credentials could not be reused")
			}
			if st.Values["natsServicePassword"] != "" {
				t.Errorf("fresh broker credentials were configured anyway: %q", st.Values["natsServicePassword"])
			}
		})
	}
}

// An instance that predates the service-auth secret gets one; it is additive, not a
// credential whose absence should stop a run.
func TestARerunMintsAServiceAuthSecretTheInstanceNeverHad(t *testing.T) {
	cfg, _, _ := deployedInstance(t)
	cfg.Infrastructure.ServiceAuth.Secret = ""
	st := rerun(t, cfg)

	if st.Values["serviceAuthSecret"] == "" {
		t.Fatal("no service-auth secret was minted for an instance that has none")
	}
}

// A dry run must not need a reachable cluster: it deploys nothing, so it has nothing
// to put at risk, and requiring an API server would make `--dry-run` useless for
// exactly the person checking a plan before they have one.
func TestADryRunNeverConsultsTheCluster(t *testing.T) {
	called := false
	withDeployedInstance(t, nil, nil)
	orig := lookupDeployedInstance
	t.Cleanup(func() { lookupDeployedInstance = orig })
	lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
		called = true
		return nil, fmt.Errorf("the cluster was consulted")
	}

	st := &State{Instance: "prod", BuildImages: true, DryRun: true, Values: map[string]string{}}
	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatalf("a dry run failed: %v", err)
	}
	if called {
		t.Error("a dry run asked the cluster what is deployed")
	}
}

// The lookup's two refusals, tested where they can be reached without a cluster.
//
// A configuration Secret that exists with nothing usable in it is the shape that
// matters: the Secret being there means the INSTANCE is there, so reading it as
// "no instance" would take the mint branch against something live.
func TestParseDeployedConfigRefusesAnUnreadableLiveInstance(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"no payload at all", nil},
		{"an empty payload", []byte{}},
		{"a payload that is not JSON", []byte("{{{")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDeployedConfig(tc.raw, "prod", "dci-prod-config")
			if err == nil {
				t.Fatalf("an unreadable live instance was reported as configuration: %+v", got)
			}
			if got != nil {
				t.Errorf("a configuration was returned alongside the error: %+v", got)
			}
		})
	}
}

// ...and it still reads a well-formed one, or the refusal above would just be a
// bootstrap that never works.
func TestParseDeployedConfigReadsAWellFormedInstance(t *testing.T) {
	cfg, _, _ := deployedInstance(t)
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	got, err := parseDeployedConfig(raw, "prod", "dci-prod-config")
	if err != nil {
		t.Fatalf("parseDeployedConfig: %v", err)
	}
	if got.Infrastructure.Secrets.RootKey != cfg.Infrastructure.Secrets.RootKey ||
		got.Infrastructure.Nats.Auth.CalloutIssuerSeed != cfg.Infrastructure.Nats.Auth.CalloutIssuerSeed {
		t.Errorf("the round trip lost credentials: %+v", got.Infrastructure)
	}
}
