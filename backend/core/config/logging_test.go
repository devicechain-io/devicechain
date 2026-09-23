// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// validLoggingDoc returns a configuration that passes Validate apart from whatever
// the caller sets on Logging, so each case measures only the level.
func validLoggingDoc(level string) *InstanceConfiguration {
	c := &InstanceConfiguration{}
	c.Infrastructure.Nats.Hostname = "h"
	c.Infrastructure.Nats.Port = 4222
	c.Infrastructure.UserManagement.Hostname = "u"
	c.Infrastructure.UserManagement.Port = 8080
	c.Infrastructure.Logging.Level = level
	return c
}

// An omitted level becomes info, and info is Debug-off. The zerolog comparison is
// the half that matters: the string alone would pass with DefaultLogLevel set to a
// name that maps to Debug.
func TestLoggingLevelDefaultsToInfo(t *testing.T) {
	c := validLoggingDoc("")
	c.ApplyDefaults()
	if c.Infrastructure.Logging.Level != "info" {
		t.Fatalf("an omitted level defaulted to %q, want info", c.Infrastructure.Logging.Level)
	}
	lvl, err := c.Infrastructure.Logging.ZerologLevel()
	if err != nil {
		t.Fatalf("the default level does not parse: %v", err)
	}
	if lvl != zerolog.InfoLevel {
		t.Fatalf("the default level is zerolog %v, want %v", lvl, zerolog.InfoLevel)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("a defaulted document does not validate: %v", err)
	}
}

// The built-in default document carries the same level, and validates with it.
func TestDefaultInstanceConfigurationLogsAtInfo(t *testing.T) {
	c := NewDefaultInstanceConfiguration()
	if err := c.Validate(); err != nil {
		t.Fatalf("the default instance configuration does not validate: %v", err)
	}
	lvl, err := c.Infrastructure.Logging.ZerologLevel()
	if err != nil || lvl != zerolog.InfoLevel {
		t.Fatalf("the default instance configuration logs at %v (err %v), want info", lvl, err)
	}
}

// Each accepted name maps to its own zerolog level. Written out rather than read back
// from logLevels, so a transposed entry in that map fails here instead of agreeing
// with itself.
func TestLoggingLevelNamesParse(t *testing.T) {
	want := map[string]zerolog.Level{
		"trace": zerolog.TraceLevel,
		"debug": zerolog.DebugLevel,
		"info":  zerolog.InfoLevel,
		"warn":  zerolog.WarnLevel,
		"error": zerolog.ErrorLevel,
	}
	for name, wantLvl := range want {
		c := validLoggingDoc(name)
		if err := c.Validate(); err != nil {
			t.Errorf("level %q was refused: %v", name, err)
			continue
		}
		got, err := c.Infrastructure.Logging.ZerologLevel()
		if err != nil || got != wantLvl {
			t.Errorf("level %q parsed to %v (err %v), want %v", name, got, err, wantLvl)
		}
	}
	if got := LogLevelNames(); strings.Join(got, ",") != "trace,debug,info,warn,error" {
		t.Errorf("LogLevelNames() = %v; the accepted set or its order changed", got)
	}
}

// Anything else is refused, and the refusal names the key. The cases are the
// near-misses an operator actually writes (wrong case, stray space, zerolog's own
// numeric form) and the three zerolog levels refused on purpose because each one
// silences Error lines.
func TestLoggingLevelRefusesAnythingElse(t *testing.T) {
	for _, bad := range []string{"verbose", "INFO", "Info", "5", "0", "disabled", "fatal", "panic", " info", "info "} {
		c := validLoggingDoc(bad)
		err := c.Validate()
		if err == nil {
			t.Errorf("level %q was accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "infrastructure.logging.level") {
			t.Errorf("the refusal of %q does not name the key: %v", bad, err)
		}
		if !strings.Contains(err.Error(), "trace, debug, info, warn, error") {
			t.Errorf("the refusal of %q does not list the accepted values: %v", bad, err)
		}
	}
}

// Validate runs before ApplyDefaults has filled the level in (LoadConfiguration runs
// them in the other order, but Validate is also callable on its own), and an absent
// key must not be reported as a bad one.
func TestLoggingLevelEmptyIsNotJudgedBeforeDefaults(t *testing.T) {
	if err := validLoggingDoc("").Validate(); err != nil {
		t.Fatalf("an absent level was refused before defaults ran: %v", err)
	}
	// ZerologLevel itself does refuse it: asked for a level, it has none to give.
	if _, err := (LoggingConfiguration{}).ZerologLevel(); err == nil {
		t.Fatal("ZerologLevel invented a level for an empty value")
	}
}
