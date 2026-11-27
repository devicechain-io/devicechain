// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"strings"
)

// The save-door read of verify (--authored-via).
//
// A device profile version records the doors its rules were saved through (authoredVia), frozen at
// publish. A release that recorded no doors published versions the column did not exist for, and
// those must read UNKNOWN after the upgrade: not empty, not API, not whatever a backfill guessed. The
// seeded profile was published by the baseline, so when the baseline recorded no doors the drill
// passes --authored-via UNKNOWN, and its control passes a door no seeded version can carry.

// authoredViaQuery reads every version of one profile with its doors.
const authoredViaQuery = `query($token:String!){deviceProfileVersions(token:$token){version authoredVia}}`

// profileVersionEntity is the receipt entry whose token is the published profile's.
const profileVersionEntity = "device-profile-version"

type versionDoors struct {
	Version     int      `json:"version"`
	AuthoredVia []string `json:"authoredVia"`
}

// authoredViaVerdict is nil when every version reads exactly [want]. Pure, so the tests hand it the
// states it must tell apart.
func authoredViaVerdict(token string, versions []versionDoors, want string) error {
	if len(versions) == 0 {
		return failWith(exitMissing, "device profile %q has no published version to read authoredVia from", token)
	}
	for _, v := range versions {
		if len(v.AuthoredVia) != 1 || v.AuthoredVia[0] != want {
			return failWith(exitMismatch, "device profile %q version %d reads authoredVia [%s], want [%s]",
				token, v.Version, strings.Join(v.AuthoredVia, " "), want)
		}
	}
	return nil
}

// checkAuthoredVia reads the seeded profile's versions and judges them, returning how many it read.
func checkAuthoredVia(ctx context.Context, q querier, url string, rec Receipt, want string) (int, error) {
	token := ""
	for _, r := range rec.Entities {
		if r.Name == profileVersionEntity {
			token = r.Token
			break
		}
	}
	if token == "" {
		return 0, failWith(exitSetup, "--authored-via was given but the receipt records no %s, so there is nothing to read", profileVersionEntity)
	}
	var out struct {
		Versions []versionDoors `json:"deviceProfileVersions"`
	}
	if err := q.Query(ctx, url, authoredViaQuery, map[string]any{"token": token}, &out); err != nil {
		var decided *codedError
		if errors.As(err, &decided) {
			return 0, err
		}
		return 0, failWith(exitShape, "read the versions of device profile %q with authoredVia: %w", token, err)
	}
	if err := authoredViaVerdict(token, out.Versions, want); err != nil {
		return 0, err
	}
	return len(out.Versions), nil
}
