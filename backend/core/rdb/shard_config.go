// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"
	"sort"
	"strings"
)

// ShardConfig is one shard database: a full, independent Postgres/Timescale cluster that owns
// one shard group's tenants. The database name and the functional-area schema are the same
// ones the manager's own database uses (the instance id and the service's area), so only the
// coordinates and credentials differ.
//
// 🔴 THIS LIST IS MOUNTED CONFIGURATION, NOT DATABASE STATE. Which cluster a shard group lives on
// decides where a tenant's rows are written and read; a value that could be edited in a table
// the service itself reads would let a tenant-scoped statement be re-aimed at runtime. The list
// is fixed for the life of the process and changes only with a deploy.
type ShardConfig struct {
	// Group is the shard group this database is. Group 0 is the manager's own database and is
	// never listed; the list names groups 1..N and must be exactly that run.
	Group    int    `json:"group"`
	Hostname string `json:"hostname"`
	Port     int32  `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	// SslMode is as PostgresConfig.SslMode; empty means DefaultSslMode.
	SslMode string `json:"sslMode"`
}

// validateShards is the fail-closed gate on the shard list, run once by
// convertToPostgresConfig before any connection is attempted, so a malformed list is a startup
// verdict and not a retry loop. It also normalises each entry's sslMode and orders the list by
// group.
//
// An absent list is valid and leaves pg untouched: that is the unconfigured, byte-identical case.
//
// Refused: a group below 1 (0 is home), a repeated group, a gap in the run 1..N (a hole would
// leave a placement that names a group no database serves), an entry without a hostname,
// username or a usable port, an unusable sslMode, and two entries - or an entry and the home
// database - on the same host and port. The database name is the instance id on every cluster,
// so equal coordinates are the same database and would give two shard groups one set of rows.
func validateShards(pg *PostgresConfig) error {
	if len(pg.Shards) == 0 {
		pg.Shards = nil
		return nil
	}
	seenGroup := make(map[int]bool, len(pg.Shards))
	seenAddr := map[string]string{endpoint(pg.Hostname, pg.Port): "the home database (shard group 0)"}
	for i := range pg.Shards {
		s := &pg.Shards[i]
		label := fmt.Sprintf("shards[%d]", i)
		switch {
		case s.Group < 1:
			return fmt.Errorf("invalid database configuration: %s: group %d is not allowed; "+
				"shard group 0 is the home database and is not listed", label, s.Group)
		case seenGroup[s.Group]:
			return fmt.Errorf("invalid database configuration: %s: shard group %d is listed twice", label, s.Group)
		case strings.TrimSpace(s.Hostname) == "":
			return fmt.Errorf("invalid database configuration: %s (group %d): hostname is required", label, s.Group)
		case strings.TrimSpace(s.Username) == "":
			return fmt.Errorf("invalid database configuration: %s (group %d): username is required", label, s.Group)
		case s.Port < 1 || s.Port > 65535:
			return fmt.Errorf("invalid database configuration: %s (group %d): port %d is not in 1..65535",
				label, s.Group, s.Port)
		}
		seenGroup[s.Group] = true
		mode, err := resolveSslMode(s.SslMode)
		if err != nil {
			return fmt.Errorf("%s (group %d): %w", label, s.Group, err)
		}
		s.SslMode = mode
		addr := endpoint(s.Hostname, s.Port)
		if other, dup := seenAddr[addr]; dup {
			return fmt.Errorf("invalid database configuration: %s (group %d) is on %s, the same as %s; "+
				"every shard group needs its own database", label, s.Group, addr, other)
		}
		seenAddr[addr] = fmt.Sprintf("shard group %d", s.Group)
	}
	sort.Slice(pg.Shards, func(i, j int) bool { return pg.Shards[i].Group < pg.Shards[j].Group })
	for i, s := range pg.Shards {
		if s.Group != i+1 {
			return fmt.Errorf("invalid database configuration: shard groups must be exactly 1..%d with no gap; "+
				"group %d is missing", len(pg.Shards), i+1)
		}
	}
	return nil
}

// endpoint is the comparison key for "the same server": host names are case-insensitive.
func endpoint(host string, port int32) string {
	return fmt.Sprintf("%s:%d", strings.ToLower(strings.TrimSpace(host)), port)
}

// postgresConfigOf is the single-database view of a shard, so a shard's DSN is built by the
// same code, with the same sslMode and search_path rules, as the home database's.
func (s ShardConfig) postgresConfigOf() *PostgresConfig {
	return &PostgresConfig{
		Hostname: s.Hostname, Port: s.Port, Username: s.Username,
		Password: s.Password, SslMode: s.SslMode,
	}
}
