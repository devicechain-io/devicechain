// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/nats-io/nkeys"
)

// The environment variables the chart projects the callout Secret into, in this service alone.
// Restated in deploy/helm/devicechain/templates/deployment.yaml and in
// backend/cli/bootstrap/calloutsecret.go; the chart render test and TestTheChartReads... hold
// them together.
const (
	envCalloutIssuerSeed = "DC_CALLOUT_ISSUER_SEED"
	envCalloutXKeySeed   = "DC_CALLOUT_XKEY_SEED"
)

// calloutSeeds is the secret material the device auth callout runs on.
type calloutSeeds struct {
	// Enabled is whether the broker runs the callout at all. When false there is nothing to
	// serve and the other fields are empty.
	Enabled    bool
	IssuerSeed string
	// XKeySeed is empty when the broker is not configured to encrypt its callout.
	XKeySeed string
	// XKeyPublic is the public key the broker encrypts to, from the instance configuration.
	XKeyPublic string
}

// resolveCalloutSeeds reads the callout's seeds from the environment and checks them against
// the PUBLIC keys the instance configuration records.
//
// 🔴 FAIL CLOSED, IN BOTH DIRECTIONS. The configuration says what the broker was told: if it
// names an issuer, the callout runs, and a missing or wrong seed refuses to start the service
// rather than leaving a broker that delegates every device to a responder that does not exist.
// If it names an xkey, the broker encrypts, and a missing xkey seed refuses likewise: a responder
// that could not open a single request would start cleanly and refuse every device connect.
//
// The seeds are checked against the public keys they must derive, so a Secret that came from
// another mint (or the issuer seed in the xkey's place) is named at startup, not discovered as
// a fleet that cannot connect.
//
// A document that still carries the DEPRECATED in-config seed is refused when the Secret does not
// supply it: it is a half-migrated instance, and serving from the config would quietly undo the
// reason the seeds moved.
func resolveCalloutSeeds(auth mscfg.NatsAuthConfiguration, getenv func(string) string) (calloutSeeds, error) {
	if !auth.CalloutEnabled() {
		return calloutSeeds{}, nil
	}
	issuer, xkey := getenv(envCalloutIssuerSeed), getenv(envCalloutXKeySeed)
	out := calloutSeeds{Enabled: true, IssuerSeed: issuer, XKeySeed: xkey, XKeyPublic: auth.CalloutXKeyPublic}

	if issuer == "" {
		if auth.CalloutIssuerSeed != "" {
			return calloutSeeds{}, fmt.Errorf("the instance configuration still carries the callout issuer seed "+
				"and %s is not set: this instance predates the callout Secret. Run `dcctl upgrade`, which moves "+
				"the seed into the Secret the chart mounts here", envCalloutIssuerSeed)
		}
		return calloutSeeds{}, fmt.Errorf("the broker runs the device auth callout but %s is not set, so there "+
			"is nothing to sign device grants with. Refusing to start rather than leave every device "+
			"connect unanswered", envCalloutIssuerSeed)
	}
	if auth.CalloutIssuerPublic != "" {
		kp, err := nkeys.FromSeed([]byte(issuer))
		if err != nil {
			return calloutSeeds{}, fmt.Errorf("%s is not a valid account seed: %w", envCalloutIssuerSeed, err)
		}
		pub, err := kp.PublicKey()
		if err != nil {
			return calloutSeeds{}, err
		}
		if pub != auth.CalloutIssuerPublic {
			return calloutSeeds{}, errors.New("the callout issuer seed does not match the issuer the broker is " +
				"configured to trust; the Secret and the instance configuration came from different mints")
		}
	}

	if auth.CalloutXKeyPublic == "" {
		// No key the broker encrypts to, so nothing to check a seed against: an xkey seed in the
		// environment is left unused rather than used unchecked.
		out.XKeySeed = ""
		return out, nil
	}
	if xkey == "" {
		return calloutSeeds{}, fmt.Errorf("the broker is configured to encrypt its callout (xkey %s) but %s is "+
			"not set, so no request could be read. Refusing to start rather than refuse every device",
			auth.CalloutXKeyPublic, envCalloutXKeySeed)
	}
	pub, err := natsauth.XKeyPublicFromSeed(xkey)
	if err != nil {
		return calloutSeeds{}, fmt.Errorf("%s: %w", envCalloutXKeySeed, err)
	}
	if pub != auth.CalloutXKeyPublic {
		return calloutSeeds{}, errors.New("the callout xkey seed does not match the public key the broker " +
			"encrypts to; the Secret and the instance configuration came from different mints")
	}
	return out, nil
}
