// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/config"
)

type PostgresConfig struct {
	Hostname       string `json:"hostname"`
	MaxConnections int32  `json:"maxConnections"`
	Password       string `json:"password"`
	Port           int32  `json:"port"`
	Username       string `json:"username"`
	// SslMode is the libpq TLS posture for connections to this store. Empty
	// means DefaultSslMode, which is what every install has effectively been
	// running since the DSN hard-coded it (ADR-020 A2.1) — so leaving it unset
	// changes nothing.
	//
	// It became configuration because the A2 database-HA work puts a real
	// replication topology behind these hostnames, and "the DB link is
	// plaintext" is not a thing that should be un-changeable in a deployment
	// whose broker transport ADR-025 already hardened. Turning it ON is a
	// separate decision from making it POSSIBLE; this is the latter.
	SslMode string `json:"sslMode"`
}

// DefaultSslMode preserves the historical hard-coded behaviour. Changing this
// constant changes the posture of every install that has not set `sslMode`,
// which is exactly the kind of silent change this field exists to avoid — so
// treat it as a decision, not a default to tidy.
const DefaultSslMode = "disable"

// validSslModes is libpq's set. It is an ALLOW-LIST rather than a
// pass-it-through, for two reasons that both matter:
//
//   - the DSN is assembled with fmt.Sprintf into a space-separated keyword=value
//     string, so an unvalidated value containing a space injects additional
//     connection parameters. `disable sslrootcert=/tmp/x` is a valid Go string
//     and a valid attack.
//   - a typo like "requrie" would otherwise reach libpq, which rejects it at
//     connect time with an error naming the driver rather than the config —
//     and, because connects are retried, as a crash-loop rather than a verdict.
//
// Fail-closed at startup on a value we do not recognise is the house rule.
var validSslModes = map[string]bool{
	"disable":     true,
	"allow":       true,
	"prefer":      true,
	"require":     true,
	"verify-ca":   true,
	"verify-full": true,
}

// resolveSslMode returns the effective sslmode, or an error naming the offending
// value and the permitted set.
func resolveSslMode(configured string) (string, error) {
	mode := strings.TrimSpace(configured)
	if mode == "" {
		return DefaultSslMode, nil
	}
	if !validSslModes[mode] {
		allowed := make([]string, 0, len(validSslModes))
		for m := range validSslModes {
			allowed = append(allowed, m)
		}
		sort.Strings(allowed)
		return "", fmt.Errorf("invalid sslMode %q for the database connection; expected one of: %s",
			configured, strings.Join(allowed, ", "))
	}
	return mode, nil
}

// Use json marshaling to convert between generic config and strongly-typed.
func convertToPostgresConfig(rdb config.DatastoreConfiguration) (*PostgresConfig, error) {
	bytes, err := json.Marshal(rdb.Configuration)
	if err != nil {
		return nil, err
	}
	pgconf := &PostgresConfig{}
	err = json.Unmarshal(bytes, pgconf)
	if err != nil {
		return nil, err
	}
	return pgconf, nil
}
