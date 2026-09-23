// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests go through NewMicroservice and LoadInstanceConfigurationFrom — the two
// calls a service makes — rather than through config.LoggingConfiguration, because the
// defect they pin was never in parsing a level. It was that nothing on a service's
// startup path set one: zerolog's global level defaults to Debug, so every Debug line
// in every service was always on, and every `log.Debug().Enabled()` guard, the config
// dump's included, was always true.
//
// None of them is parallel. The level is process-wide, and so is the logger.

// keepGlobalLevel restores zerolog's global level when t ends, so a test that moves it
// does not leave the rest of the package running at its level.
func keepGlobalLevel(t *testing.T) {
	t.Helper()
	saved := zerolog.GlobalLevel()
	t.Cleanup(func() { zerolog.SetGlobalLevel(saved) })
}

// withLevel returns a minimal valid instance document that sets
// infrastructure.logging.level.
func withLevel(level string) string {
	return `{"infrastructure":{"nats":{"hostname":"h","port":4222},
	         "userManagement":{"hostname":"u","port":8080},
	         "logging":{"level":"` + level + `"}}}`
}

// A service that has been constructed and has not yet read its instance document runs
// at Info. The level is first set to Debug to reproduce zerolog's own default, which
// is what every service ran at before NewMicroservice set one.
func TestNewMicroserviceDefaultsToInfo(t *testing.T) {
	keepGlobalLevel(t)
	t.Setenv(ENV_MS_FUNCTIONAL_AREA, "log-level-test")
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	require.True(t, log.Debug().Enabled(), "the precondition: Debug on, as zerolog defaults it")

	NewMicroservice(NewNoOpLifecycleCallbacks())

	assert.False(t, log.Debug().Enabled(), "Debug is still on after NewMicroservice")
	// The counterweight: a logger muted entirely would pass the line above.
	assert.True(t, log.Info().Enabled(), "Info is off after NewMicroservice")
}

// The level in the instance document is the level the service then runs at, and an
// invalid one changes nothing.
func TestInstanceConfigurationLevelIsHonoured(t *testing.T) {
	for _, tc := range []struct {
		name     string
		doc      string
		enabled  []zerolog.Level
		disabled []zerolog.Level
	}{
		{name: "absent means info", doc: minimalInstanceDoc,
			enabled: []zerolog.Level{zerolog.InfoLevel}, disabled: []zerolog.Level{zerolog.DebugLevel}},
		{name: "debug", doc: withLevel("debug"),
			enabled: []zerolog.Level{zerolog.DebugLevel}, disabled: []zerolog.Level{zerolog.TraceLevel}},
		{name: "trace", doc: withLevel("trace"),
			enabled: []zerolog.Level{zerolog.TraceLevel}},
		{name: "warn", doc: withLevel("warn"),
			enabled: []zerolog.Level{zerolog.WarnLevel}, disabled: []zerolog.Level{zerolog.InfoLevel}},
		{name: "error", doc: withLevel("error"),
			enabled: []zerolog.Level{zerolog.ErrorLevel}, disabled: []zerolog.Level{zerolog.WarnLevel}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keepGlobalLevel(t)
			t.Setenv(ENV_MS_FUNCTIONAL_AREA, "log-level-test")
			ms := NewMicroservice(NewNoOpLifecycleCallbacks())

			require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, tc.doc)))

			for _, lvl := range tc.enabled {
				assert.True(t, log.WithLevel(lvl).Enabled(), "%v is off", lvl)
			}
			for _, lvl := range tc.disabled {
				assert.False(t, log.WithLevel(lvl).Enabled(), "%v is on", lvl)
			}
		})
	}
}

// An invalid level refuses the load AND leaves the level exactly where it was.
//
// The level is moved to Warn first, deliberately away from the Info NewMicroservice
// sets: left at Info, "unchanged" and "reset to the default by the failed load" would
// read identically, and a load that fell back to info on a bad value would pass.
func TestAnInvalidLevelChangesNothing(t *testing.T) {
	keepGlobalLevel(t)
	t.Setenv(ENV_MS_FUNCTIONAL_AREA, "log-level-test")
	ms := NewMicroservice(NewNoOpLifecycleCallbacks())
	zerolog.SetGlobalLevel(zerolog.WarnLevel)

	err := ms.LoadInstanceConfigurationFrom(instanceDoc(t, withLevel("verbose")))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "infrastructure.logging.level")
	assert.Equal(t, zerolog.WarnLevel, zerolog.GlobalLevel(),
		"a refused document moved the level")
	assert.Empty(t, ms.InstanceConfiguration.Infrastructure.Logging.Level,
		"a refused document was stored on the microservice")
}

// 🔴 THE DOCUMENT NEVER REACHES THE LOG, at any level. The per-area configuration used
// to be pretty-printed at debug behind a guard that was always true, so every pod start
// wrote it out. The level here is trace — the most verbose there is — so no level can
// be the one that brings the dump back.
//
// The output is read through logWriter, the seam NewMicroservice writes through, so
// what is captured is what a real service writes. The two presence assertions are the
// counterweight: an empty capture would pass every absence check below.
func TestConfigurationDocumentNeverReachesTheLog(t *testing.T) {
	keepGlobalLevel(t)
	t.Setenv(ENV_MS_FUNCTIONAL_AREA, "log-level-test")

	savedWriter := logWriter
	sink := dctest.NewLogSink(io.Discard)
	logWriter = sink
	t.Cleanup(func() {
		// Writer first, then a fresh construction: NewMicroservice is the only sanctioned
		// way to point the global logger anywhere, and without this every later test in
		// the package would keep logging into a discarding sink.
		logWriter = savedWriter
		NewMicroservice(NewNoOpLifecycleCallbacks())
	})
	logs := sink.Capture(t)

	ms := NewMicroservice(NewNoOpLifecycleCallbacks())
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, withLevel("trace"))))
	require.True(t, log.Trace().Enabled(), "the precondition: the most verbose level is in force")

	const marker = "per-area-document-marker-7f3e2a"
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "log-level-test"),
		[]byte(`{"secretish":"`+marker+`"}`), 0o600))
	require.NoError(t, ms.LoadMicroserviceConfigurationFrom(dir))
	assert.Equal(t, `{"secretish":"`+marker+`"}`, string(ms.MicroserviceConfigurationRaw),
		"the document was not the one loaded")

	out := logs.String()
	assert.Contains(t, out, "config_sha256", "the hash line is missing, so nothing below was measured")
	assert.Contains(t, out, "Log level set from instance configuration",
		"the level line is missing, so nothing below was measured")
	assert.NotContains(t, out, marker, "the per-area document reached the log")
	assert.NotContains(t, out, "Microservice configuration:", "the old dump's header reached the log")
	if t.Failed() {
		t.Logf("captured:\n%s", strings.TrimSpace(out))
	}
}

// The level line is written BEFORE the switch, so a service configured at warn or error
// still says, once, which level it was told to run at — the one line that explains why
// every Info line after it is missing. The observability page promises this. Only the
// quieter levels can test it: at info or below the line appears whichever side of the
// switch it is written on, so TestConfigurationDocumentNeverReachesTheLog, at trace,
// cannot see the order at all.
func TestLevelLineSurvivesAQuieterLevel(t *testing.T) {
	for _, level := range []string{"warn", "error"} {
		t.Run(level, func(t *testing.T) {
			keepGlobalLevel(t)
			t.Setenv(ENV_MS_FUNCTIONAL_AREA, "log-level-test")

			savedWriter := logWriter
			sink := dctest.NewLogSink(io.Discard)
			logWriter = sink
			t.Cleanup(func() {
				logWriter = savedWriter
				NewMicroservice(NewNoOpLifecycleCallbacks())
			})
			logs := sink.Capture(t)

			ms := NewMicroservice(NewNoOpLifecycleCallbacks())
			require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, withLevel(level))))
			// The precondition: the switch happened, so the line cannot have got through
			// by being written after it.
			require.False(t, log.Info().Enabled(), "Info is still on at %s", level)

			out := logs.String()
			assert.Contains(t, out, "Log level set from instance configuration",
				"the level line was suppressed by the level it announces")
			assert.Contains(t, out, `"level":"`+level+`"`,
				"the line does not name the configured level")
			if t.Failed() {
				t.Logf("captured:\n%s", strings.TrimSpace(out))
			}
		})
	}
}
