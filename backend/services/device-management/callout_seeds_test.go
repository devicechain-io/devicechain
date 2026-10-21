// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/stretchr/testify/require"
)

// The callout's seeds come from this pod's own Secret, are checked against the public keys the
// instance configuration records, and a callout that cannot be served REFUSES TO START: a broker
// that delegates every device to a responder that does not exist refuses every device, quietly.

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestTheCalloutSeedsAreReadFromTheEnvironmentAndCheckedAgainstTheirPublicKeys(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	require.NoError(t, err)
	auth := mscfg.NatsAuthConfiguration{CalloutIssuerPublic: creds.IssuerPublic, CalloutXKeyPublic: creds.XKeyPublic}
	env := map[string]string{envCalloutIssuerSeed: creds.IssuerSeed, envCalloutXKeySeed: creds.XKeySeed}

	got, err := resolveCalloutSeeds(auth, envOf(env))
	require.NoError(t, err)
	require.True(t, got.Enabled)
	require.Equal(t, creds.IssuerSeed, got.IssuerSeed)
	require.Equal(t, creds.XKeySeed, got.XKeySeed)
	require.Equal(t, creds.XKeyPublic, got.XKeyPublic)
}

func TestAnInstanceWithNoCalloutNeedsNoSeeds(t *testing.T) {
	got, err := resolveCalloutSeeds(mscfg.NatsAuthConfiguration{}, envOf(nil))
	require.NoError(t, err)
	require.False(t, got.Enabled, "a broker with no callout has nothing to serve")
}

func TestARefusalToStartForEveryWayTheCalloutCannotBeServed(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	require.NoError(t, err)
	other, err := natsauth.GenerateCredentials()
	require.NoError(t, err)
	full := func() (mscfg.NatsAuthConfiguration, map[string]string) {
		return mscfg.NatsAuthConfiguration{CalloutIssuerPublic: creds.IssuerPublic, CalloutXKeyPublic: creds.XKeyPublic},
			map[string]string{envCalloutIssuerSeed: creds.IssuerSeed, envCalloutXKeySeed: creds.XKeySeed}
	}

	// The control: the full set starts. Every refusal below is a deletion or a swap from it.
	auth, env := full()
	_, err = resolveCalloutSeeds(auth, envOf(env))
	require.NoError(t, err)

	for _, tc := range []struct {
		name    string
		mutate  func(*mscfg.NatsAuthConfiguration, map[string]string)
		wantErr string
	}{
		{"the issuer seed is missing", func(_ *mscfg.NatsAuthConfiguration, e map[string]string) { delete(e, envCalloutIssuerSeed) },
			envCalloutIssuerSeed},
		{"the xkey seed is missing while the broker is configured to encrypt", func(_ *mscfg.NatsAuthConfiguration, e map[string]string) { delete(e, envCalloutXKeySeed) },
			envCalloutXKeySeed},
		{"the issuer seed is another mint's", func(_ *mscfg.NatsAuthConfiguration, e map[string]string) { e[envCalloutIssuerSeed] = other.IssuerSeed },
			"different mints"},
		{"the xkey seed is another mint's", func(_ *mscfg.NatsAuthConfiguration, e map[string]string) { e[envCalloutXKeySeed] = other.XKeySeed },
			"different mints"},
		{"the xkey seed is the issuer seed", func(_ *mscfg.NatsAuthConfiguration, e map[string]string) { e[envCalloutXKeySeed] = creds.IssuerSeed },
			envCalloutXKeySeed},
		{"the issuer seed is not a seed", func(_ *mscfg.NatsAuthConfiguration, e map[string]string) { e[envCalloutIssuerSeed] = "garbage" },
			"not a valid account seed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, env := full()
			tc.mutate(&auth, env)
			_, err := resolveCalloutSeeds(auth, envOf(env))
			require.Error(t, err, "the service would have started with a callout it cannot serve")
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// A document still carrying the old in-config seed, with no Secret behind it, is an instance
// that has not been through `dcctl upgrade`. It must say so, not quietly serve from the one place
// every pod can read.
func TestADocumentThatStillCarriesTheSeedIsRefusedWithTheRemedy(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	require.NoError(t, err)
	_, err = resolveCalloutSeeds(mscfg.NatsAuthConfiguration{CalloutIssuerSeed: creds.IssuerSeed}, envOf(nil))
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "dcctl upgrade"), "the refusal names the remedy: %v", err)
}

// An instance whose broker does not encrypt yet (the key is introduced before the broker is told)
// serves with the issuer seed alone.
func TestACalloutWithoutAnXKeyNeedsOnlyTheIssuerSeed(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	require.NoError(t, err)
	got, err := resolveCalloutSeeds(mscfg.NatsAuthConfiguration{CalloutIssuerPublic: creds.IssuerPublic},
		envOf(map[string]string{envCalloutIssuerSeed: creds.IssuerSeed}))
	require.NoError(t, err)
	require.True(t, got.Enabled)
	require.Empty(t, got.XKeySeed)
	// A seed with no public key to check it against is left unused, never used unchecked.
	got, err = resolveCalloutSeeds(mscfg.NatsAuthConfiguration{CalloutIssuerPublic: creds.IssuerPublic},
		envOf(map[string]string{envCalloutIssuerSeed: creds.IssuerSeed, envCalloutXKeySeed: creds.XKeySeed}))
	require.NoError(t, err)
	require.Empty(t, got.XKeySeed, "an xkey seed the configuration names no key for was handed to the responder")
}

// main.go is not run by a test, so the one wiring that matters is read off its source: the start
// hook resolves the seeds (and returns the refusal) BEFORE it builds a responder, and hands the
// xkey seed to the responder. Delete either and a service starts with a callout it cannot serve.
func TestTheStartHookResolvesTheSeedsAndGivesTheResponderItsXKey(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)
	var hook *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "afterMicroserviceStarted" {
			hook = fd
		}
	}
	require.NotNil(t, hook)
	pos := map[string]token.Pos{}
	ast.Inspect(hook.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch f := call.Fun.(type) {
		case *ast.Ident:
			name = f.Name
		case *ast.SelectorExpr:
			name = f.Sel.Name
			if x, ok := f.X.(*ast.Ident); ok && x.Name == "CalloutResponder" {
				name = "CalloutResponder." + f.Sel.Name
			}
		}
		if _, seen := pos[name]; !seen {
			pos[name] = call.Pos()
		}
		return true
	})
	for _, name := range []string{"resolveCalloutSeeds", "NewCalloutResponder", "CalloutResponder.WithRequestEncryption", "CalloutResponder.Start"} {
		require.NotZero(t, pos[name], "afterMicroserviceStarted never calls %s", name)
	}
	require.Less(t, pos["resolveCalloutSeeds"], pos["NewCalloutResponder"], "the seeds must be checked before a responder is built")
	require.Less(t, pos["CalloutResponder.WithRequestEncryption"], pos["CalloutResponder.Start"], "the responder must be able to read requests before it subscribes")
}
