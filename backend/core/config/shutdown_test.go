// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadShutdown decodes an instance document exactly the way a service does —
// json.Unmarshal, then ApplyDefaults, then Validate — and reports what the service
// would have concluded.
//
// It goes through the JSON rather than building the struct because the JSON is what
// an operator actually writes, and half of what these tests are about is the
// difference between a key that is absent and a key that says zero. A struct
// literal cannot express that difference; a document can.
func loadShutdown(t *testing.T, doc string) (*InstanceConfiguration, error) {
	t.Helper()
	cfg := &InstanceConfiguration{}
	if err := json.Unmarshal([]byte(doc), cfg); err != nil {
		return nil, err
	}
	cfg.ApplyDefaults()
	return cfg, cfg.Validate()
}

// withShutdown wraps a shutdown block in the rest of the infrastructure a service
// insists on, so Validate reaches the shutdown rules rather than stopping at a
// missing broker.
func withShutdown(inner string) string {
	return `{"infrastructure":{` +
		`"nats":{"hostname":"dc-nats","port":4222},` +
		`"userManagement":{"hostname":"dc-user-management","port":8080},` +
		`"shutdown":` + inner +
		`}}`
}

// 🔴 THE GATE THIS WHOLE CHANGE EXISTS FOR. A drain window that cannot be parsed
// used to be discovered inside the signal handler, at the moment the process was
// already shutting down — logged as a warning, replaced with the default, and gone
// by the time anyone looked. It is a startup refusal now, and the message names the
// key so an operator can find it in their values.
func TestAnUnparseableDrainWindowIsRefusedAtLoad(t *testing.T) {
	_, err := loadShutdown(t, withShutdown(`{"drainSeconds":"soon"}`))

	require.Error(t, err, "a drain window that is not a number must not load")
	// encoding/json writes the Go field PATH, which differs from the document's key
	// only in its initial letter ("…Shutdown.DrainSeconds" for "shutdown.drainSeconds"),
	// so the comparison is case-insensitive rather than pretending the two spellings
	// match. What matters is that the sentence points at one key out of the whole
	// document, which is what the old shutdown-time warning never did.
	assert.Contains(t, strings.ToLower(err.Error()), "shutdown.drainseconds",
		"the refusal must name the key the operator has to correct")
}

// A negative window is refused rather than coerced. It is the one case the old
// environment reader handled by substituting the default, which meant an operator
// who wrote -1 got 5 and was told so in a log line nobody reads.
func TestANegativeDrainWindowIsRefusedAtLoad(t *testing.T) {
	_, err := loadShutdown(t, withShutdown(`{"drainSeconds":-3,"terminationGracePeriodSeconds":30}`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "infrastructure.shutdown.drainSeconds")
	assert.Contains(t, err.Error(), "-3", "the refusal must quote the value it read")
}

// The misconfiguration this pairing was added to catch: a drain longer than the
// pod's grace period. The process sleeps out the window, the kubelet SIGKILLs it
// partway through, and the teardown that closes the broker consumers and the
// database pool never runs at all.
func TestADrainWindowLongerThanTheGracePeriodIsRefusedAtLoad(t *testing.T) {
	_, err := loadShutdown(t, withShutdown(`{"drainSeconds":60,"terminationGracePeriodSeconds":30}`))

	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "infrastructure.shutdown.drainSeconds")
	assert.Contains(t, msg, "terminationGracePeriodSeconds",
		"the refusal must name the OTHER key too: from inside the process there is no "+
			"way to tell which of the two the operator mistyped")
	assert.Contains(t, msg, "15", "it must say how far drainSeconds would have to come down")
	assert.Contains(t, msg, "120", "and how far terminationGracePeriodSeconds would have to go up")
}

// The rule is not "drain < grace". A 29-second drain inside a 30-second budget
// leaves one second for the phase that actually closes connections, and would be
// accepted by a strict inequality while behaving almost exactly like the case
// above.
func TestADrainWindowThatLeavesNoRoomForTeardownIsRefusedAtLoad(t *testing.T) {
	_, err := loadShutdown(t, withShutdown(`{"drainSeconds":29,"terminationGracePeriodSeconds":30}`))

	require.Error(t, err, "a drain that consumes almost the whole grace period leaves no teardown")
	assert.Contains(t, err.Error(), "infrastructure.shutdown.drainSeconds")
}

// 🔴 THE COUNTERWEIGHT. Refusing bad budgets is only worth anything while the
// chart's own defaults, and a deliberately larger window an operator might choose,
// still load untouched.
func TestAValidShutdownBudgetLoadsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		doc   string
		drain int
		grace int
	}{
		{"the chart's defaults", `{"drainSeconds":5,"terminationGracePeriodSeconds":30}`, 5, 30},
		{"exactly half the budget", `{"drainSeconds":15,"terminationGracePeriodSeconds":30}`, 15, 30},
		{"a longer window in a longer budget", `{"drainSeconds":45,"terminationGracePeriodSeconds":120}`, 45, 120},
		{"no drain at all", `{"drainSeconds":0,"terminationGracePeriodSeconds":30}`, 0, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadShutdown(t, withShutdown(tc.doc))

			require.NoError(t, err)
			require.NotNil(t, cfg.Infrastructure.Shutdown.DrainSeconds)
			assert.Equal(t, tc.drain, *cfg.Infrastructure.Shutdown.DrainSeconds,
				"the window the operator wrote must be the window that survives the load")
			assert.Equal(t, tc.grace, cfg.Infrastructure.Shutdown.TerminationGracePeriodSeconds)
			assert.Equal(t, time.Duration(tc.drain)*time.Second, cfg.Infrastructure.Shutdown.DrainWindow())
		})
	}
}

// 🔑 THE DISTINCTION THE POINTER BUYS, and the reason DrainSeconds is not a plain
// int like every other number in this file. An omitted key means "whatever the
// platform thinks is right"; an explicit 0 means "do not drain", which a local
// single-instance run wants because there is no Service to be pulled out of. A
// plain int reads both as 0, so an instance document that simply left the key out
// would stop draining and sever in-flight requests without anyone having asked for
// it.
func TestAnAbsentDrainWindowIsNotTheSameAsAnExplicitZero(t *testing.T) {
	absent, err := loadShutdown(t, withShutdown(`{"terminationGracePeriodSeconds":30}`))
	require.NoError(t, err)
	require.NotNil(t, absent.Infrastructure.Shutdown.DrainSeconds)
	assert.Equal(t, DefaultShutdownDrainSeconds, *absent.Infrastructure.Shutdown.DrainSeconds,
		"an omitted key must take the platform default, not zero")
	assert.Equal(t, DefaultShutdownDrainSeconds*time.Second, absent.Infrastructure.Shutdown.DrainWindow())

	zero, err := loadShutdown(t, withShutdown(`{"drainSeconds":0,"terminationGracePeriodSeconds":30}`))
	require.NoError(t, err)
	require.NotNil(t, zero.Infrastructure.Shutdown.DrainSeconds)
	assert.Equal(t, 0, *zero.Infrastructure.Shutdown.DrainSeconds)
	assert.Equal(t, time.Duration(0), zero.Infrastructure.Shutdown.DrainWindow(),
		"an operator who wrote 0 asked for no drain and must get no drain")
}

// An omitted grace period is judged against the one the pod will really be given,
// which is Kubernetes' own default rather than a number this platform invented.
func TestAnOmittedGracePeriodTakesKubernetesOwnDefault(t *testing.T) {
	cfg, err := loadShutdown(t, withShutdown(`{"drainSeconds":5}`))
	require.NoError(t, err)
	assert.Equal(t, DefaultTerminationGracePeriodSeconds,
		cfg.Infrastructure.Shutdown.TerminationGracePeriodSeconds)

	// ...and it is a real ceiling, not decoration: the same document with a large
	// window is refused against it.
	_, err = loadShutdown(t, withShutdown(`{"drainSeconds":40}`))
	require.Error(t, err, "an omitted grace period must still bound the drain")
}

// The window is defaulted at the point of use as well as in ApplyDefaults. A
// Microservice built as a struct literal never loads an instance document, so this
// method is read from a zero value in a good deal of this tree — and a zero there
// must not read as "sever every in-flight request".
func TestDrainWindowDefaultsOnAConfigurationThatWasNeverLoaded(t *testing.T) {
	var never ShutdownConfiguration
	assert.Equal(t, DefaultShutdownDrainSeconds*time.Second, never.DrainWindow())

	// A negative that somehow reached the point of use — validation refuses one, so
	// this is the belt to that pair of braces — is the default, never a negative
	// sleep, and never zero.
	negative := -1
	assert.Equal(t, DefaultShutdownDrainSeconds*time.Second,
		ShutdownConfiguration{DrainSeconds: &negative}.DrainWindow())
}

// The platform's own default configuration must satisfy the platform's own rules.
// It is built by hand rather than by ApplyDefaults, so nothing else would notice a
// budget written into it that a service would then refuse to start on.
func TestTheDefaultInstanceConfigurationHasAValidShutdownBudget(t *testing.T) {
	cfg := NewDefaultInstanceConfiguration()

	require.NoError(t, cfg.Validate())
	require.NotNil(t, cfg.Infrastructure.Shutdown.DrainSeconds)
	assert.Equal(t, DefaultShutdownDrainSeconds, *cfg.Infrastructure.Shutdown.DrainSeconds)
	assert.Equal(t, DefaultTerminationGracePeriodSeconds,
		cfg.Infrastructure.Shutdown.TerminationGracePeriodSeconds)
}
