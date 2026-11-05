// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/otafleet"
)

// otaFlags are the --ota family: simulated devices that take over-the-air updates from the
// instance. Every flag is additive; a sim started without --ota is exactly what it was.
type otaFlags struct {
	enabled       *bool
	version       *string
	component     *string
	trustedKeys   *string
	allowUnsigned *bool
	stateDir      *string
	chunk         *int
	stepDelay     *time.Duration
	badHash       *int
	interrupted   *int
	badSignature  *int
	offline       *int
	offlineFor    *time.Duration
}

func defineOTAFlags(fs *flag.FlagSet) otaFlags {
	return otaFlags{
		enabled: fs.Bool("ota", envBoolOr("DC_SIM_OTA", false),
			"give every device of the scenario an over-the-air update client against the instance's MQTT "+
				"broker: it receives the desired update, downloads it from the capability URL with Range "+
				"resume, verifies size, SHA-256 and signature, reports every stage, swaps its version string "+
				"and reports the new version. Needs a trusted key (--ota-trusted-key) or --ota-allow-unsigned"),
		version: fs.String("ota-version", envOr("DC_SIM_OTA_VERSION", "1.0.0"),
			"with --ota: the version a fresh simulated device runs"),
		component: fs.String("ota-component", envOr("DC_SIM_OTA_COMPONENT", otafleet.DefaultComponent),
			"with --ota: the component each device manages (an assignment for another component is refused)"),
		trustedKeys: fs.String("ota-trusted-key", envOr("DC_SIM_OTA_TRUSTED_KEY", ""),
			"with --ota: comma-separated paths of PEM public keys (ECDSA P-256 or Ed25519) the devices trust. "+
				"A signed update is accepted only if it verifies against one of them"),
		allowUnsigned: fs.Bool("ota-allow-unsigned", envBoolOr("DC_SIM_OTA_ALLOW_UNSIGNED", false),
			"with --ota: let devices install an UNVERIFIED (unsigned) update. Off by default, as on the reference device"),
		stateDir: fs.String("ota-state-dir", envOr("DC_SIM_OTA_STATE_DIR", ""),
			"with --ota: keep each device's local update state here so a restarted simulator continues the "+
				"same attempts (default: a temporary directory, removed on exit)"),
		chunk: fs.Int("ota-chunk", envIntOr("DC_SIM_OTA_CHUNK", 32768),
			"with --ota: bytes fetched per ranged download request; smaller makes progress visible and gives an interruption something to resume"),
		stepDelay: fs.Duration("ota-step-delay", envDurationOr("DC_SIM_OTA_STEP_DELAY", 0),
			"with --ota: pause between a device's steps (e.g. 300ms), to make a rollout watchable"),
		badHash: fs.Int("ota-bad-hash", envIntOr("DC_SIM_OTA_BAD_HASH", 0),
			"with --ota: this many devices (the first ones) are served an image with a flipped byte and must report FAILED VERIFICATION_FAILED"),
		interrupted: fs.Int("ota-interrupted", envIntOr("DC_SIM_OTA_INTERRUPTED", 0),
			"with --ota: this many devices (after the bad-hash ones) lose the download part-way once and resume from the bytes they hold"),
		badSignature: fs.Int("ota-bad-signature", envIntOr("DC_SIM_OTA_BAD_SIGNATURE", 0),
			"with --ota: this many devices (after the interrupted ones) see a corrupted signature and must report FAILED SIGNATURE_INVALID"),
		offline: fs.Int("ota-offline", envIntOr("DC_SIM_OTA_OFFLINE", 0),
			"with --ota: this many devices (after the bad-signature ones) never connect to the broker"),
		offlineFor: fs.Duration("ota-offline-for", envDurationOr("DC_SIM_OTA_OFFLINE_FOR", 0),
			"with --ota-offline: bring those devices online after this long (they sync and catch up); 0 keeps them offline"),
	}
}

// options turns the flags into fleet options, or nil when --ota is off. It refuses a fault or
// tuning flag given without --ota: it would otherwise be accepted and do nothing, which reads as a
// fleet that was asked to misbehave and chose not to.
func (o otaFlags) options(fs *flag.FlagSet) (*otafleet.Options, error) {
	if !*o.enabled {
		set := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		var stray []string
		for _, n := range []string{"ota-version", "ota-component", "ota-trusted-key", "ota-allow-unsigned", "ota-state-dir",
			"ota-chunk", "ota-step-delay", "ota-bad-hash", "ota-interrupted", "ota-bad-signature", "ota-offline", "ota-offline-for"} {
			if set[n] {
				stray = append(stray, "--"+n)
			}
		}
		for _, n := range []struct {
			env string
			bad bool
		}{{"DC_SIM_OTA_BAD_HASH", *o.badHash != 0}, {"DC_SIM_OTA_INTERRUPTED", *o.interrupted != 0},
			{"DC_SIM_OTA_BAD_SIGNATURE", *o.badSignature != 0}, {"DC_SIM_OTA_OFFLINE", *o.offline != 0},
			{"DC_SIM_OTA_TRUSTED_KEY", *o.trustedKeys != ""}} {
			if n.bad {
				stray = append(stray, n.env)
			}
		}
		if len(stray) > 0 {
			return nil, fmt.Errorf("%s given without --ota (or DC_SIM_OTA=1): they configure the update devices, which are not running",
				strings.Join(stray, ", "))
		}
		return nil, nil
	}

	var keys []string
	for _, p := range strings.Split(*o.trustedKeys, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("--ota-trusted-key: %w", err)
		}
		keys = append(keys, string(raw))
	}
	if len(keys) == 0 && !*o.allowUnsigned {
		return nil, errors.New("--ota needs --ota-trusted-key <public key PEM> so the devices can verify signed updates, " +
			"or --ota-allow-unsigned; without either every update would be refused")
	}
	opts := &otafleet.Options{
		Component: *o.component, InitialVersion: *o.version, StateDir: *o.stateDir,
		TrustedKeys: keys, AllowUnsigned: *o.allowUnsigned, ChunkSize: int64(*o.chunk), StepDelay: *o.stepDelay,
		Faults: otafleet.Faults{BadHash: *o.badHash, Interrupted: *o.interrupted, BadSignature: *o.badSignature,
			Offline: *o.offline, OfflineFor: *o.offlineFor},
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if *o.offlineFor > 0 && *o.offline == 0 {
		return nil, errors.New("--ota-offline-for brings --ota-offline devices online, and none were asked for")
	}
	return opts, nil
}
