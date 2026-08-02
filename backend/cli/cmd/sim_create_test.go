// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"testing"

	"github.com/spf13/pflag"
)

// The certificate-verification decision is driven through REAL pflag parsing, not by
// calling the resolver with hand-made booleans.
//
// 🔴 The whole subtlety lives in pflag: `--mqtt-insecure=false` and "not passed" both
// produce the value false, and only Changed() distinguishes them. A resolver written
// against GetBool alone would let the derived default silently beat an operator's
// explicit instruction to verify — the one direction where being wrong is a security
// decision rather than an availability one. Asserting that needs the parser.
func TestResolveMqttInsecureAcrossFlagAndTLS(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		tls  bool
		want bool
	}{
		// Not passed: derived from --tls. A plaintext-HTTP run has already given up
		// more than broker verification protects; a --tls run is a deployment
		// reachable at its certificate's SAN.
		{"unset without --tls skips verification", nil, false, true},
		{"unset with --tls verifies", nil, true, false},

		// Explicit, and it must win in BOTH directions.
		{"explicit --mqtt-insecure beats --tls", []string{"--mqtt-insecure"}, true, true},
		{"explicit =true beats --tls", []string{"--mqtt-insecure=true"}, true, true},
		{"explicit =false beats the no-tls default", []string{"--mqtt-insecure=false"}, false, false},
		{"explicit =false with --tls stays verifying", []string{"--mqtt-insecure=false"}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A FRESH flag set per case: Changed is sticky, so reusing one would
			// leak the previous case's explicitness into this one.
			fs := pflag.NewFlagSet("sim create", pflag.ContinueOnError)
			registerMqttFlags(fs)
			if err := fs.Parse(tc.argv); err != nil {
				t.Fatalf("parse %v: %v", tc.argv, err)
			}
			if got := resolveMqttInsecure(fs, tc.tls); got != tc.want {
				t.Errorf("resolveMqttInsecure(%v, tls=%t) = %t, want %t", tc.argv, tc.tls, got, tc.want)
			}
		})
	}
}

// The resolver and the registration must name the same flags. They are constants for
// that reason, but a constant only helps while both sides use it — so this asserts
// the flags the command actually carries are the ones the resolver reads.
func TestSimCreateRegistersTheMqttFlags(t *testing.T) {
	for _, name := range []string{flagMqttBroker, flagMqttInsecure} {
		if simCreateCmd.Flags().Lookup(name) == nil {
			t.Errorf("`sim create` has no --%s flag, so the far end cannot be addressed", name)
		}
	}
}
