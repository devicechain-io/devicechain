// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-simulator/updater"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var configFixtures = filepath.Join("..", "..", "..", "core", "ota", "testdata", "config")

func fixture(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(configFixtures, rel))
	require.NoError(t, err)
	return string(b)
}

// sink records what the device sent and can be made to fail, like a dropped connection.
type sink struct {
	mu   sync.Mutex
	sent []string
	err  error
}

func (s *sink) Send(_ context.Context, p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, string(p))
	return nil
}

func (s *sink) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.sent
	s.sent = nil
	return out
}

func openAgent(t *testing.T, cfg updater.ConfigAgentConfig) (*updater.ConfigAgent, *sink) {
	t.Helper()
	s := &sink{}
	cfg.Transport = s
	a, err := updater.OpenConfigAgent(cfg)
	require.NoError(t, err)
	return a, s
}

// Byte for byte the golden fixtures: the sim speaks the contract from its own structs, and the
// core package's fixtures are what hold the two together.
func TestAgentSpeaksTheContractFixtures(t *testing.T) {
	desired := fixture(t, "desired/config-revision.json")

	a, s := openAgent(t, updater.ConfigAgentConfig{})
	require.NoError(t, a.Sync(ctx))
	assert.Equal(t, []string{fixture(t, "valid/sync-fresh.json")}, s.take())

	require.NoError(t, a.HandleDesired(ctx, []byte(desired)))
	assert.Equal(t, []string{fixture(t, "valid/status-received.json"), fixture(t, "valid/status-applied.json")}, s.take())

	withBoot, s2 := openAgent(t, updater.ConfigAgentConfig{BootID: "boot-1"})
	require.NoError(t, withBoot.HandleDesired(ctx, []byte(desired)))
	s2.take()
	require.NoError(t, withBoot.Sync(ctx))
	assert.Equal(t, []string{fixture(t, "valid/sync.json")}, s2.take())

	rej, s3 := openAgent(t, updater.ConfigAgentConfig{Validate: func(string) []string {
		return []string{"sampleIntervalSeconds: out of range"}
	}})
	require.NoError(t, rej.HandleDesired(ctx, []byte(desired)))
	assert.Equal(t, []string{fixture(t, "valid/status-received.json"), fixture(t, "valid/status-rejected.json")}, s3.take())
	rev, _, _ := rej.Held()
	assert.Zero(t, rev, "a rejected revision was kept")

	// Everything it sent is something the platform's decoder accepts.
	for _, m := range append(append(s.take(), s2.take()...), s3.take()...) {
		_, err := ota.DecodeDeviceReport([]byte(m))
		assert.NoError(t, err, m)
	}
}

// The device hashes the bytes it was sent. A document that does not hash to its digest is refused
// and never kept.
func TestAgentRefusesADocumentThatDoesNotMatchItsDigest(t *testing.T) {
	a, s := openAgent(t, updater.ConfigAgentConfig{})
	tampered := strings.Replace(fixture(t, "desired/config-revision.json"), `\"sampleIntervalSeconds\":30`, `\"sampleIntervalSeconds\":31`, 1)
	require.NoError(t, a.HandleDesired(ctx, []byte(tampered)))
	sent := s.take()
	require.Len(t, sent, 2)
	r, err := ota.DecodeDeviceReport([]byte(sent[1]))
	require.NoError(t, err)
	assert.Equal(t, ota.ConfigRejected, r.ConfigStatus.Status)
	assert.Contains(t, r.ConfigStatus.Errors[0], "digest")
	rev, _, _ := a.Held()
	assert.Zero(t, rev)
}

func TestAgentIgnoresOlderAndReReportsHeldRevisions(t *testing.T) {
	applied := 0
	a, s := openAgent(t, updater.ConfigAgentConfig{Validate: func(string) []string { applied++; return nil }})
	desired := []byte(fixture(t, "desired/config-revision.json"))
	require.NoError(t, a.HandleDesired(ctx, desired))
	s.take()

	// The same revision again: reported APPLIED again, not re-validated or re-applied.
	require.NoError(t, a.HandleDesired(ctx, desired))
	sent := s.take()
	require.Len(t, sent, 1)
	assert.Equal(t, fixture(t, "valid/status-applied.json"), sent[0])
	assert.Equal(t, 1, applied)

	// An older revision is ignored outright.
	older := strings.Replace(string(desired), `"revision":3`, `"revision":2`, 1)
	require.NoError(t, a.HandleDesired(ctx, []byte(older)))
	assert.Empty(t, s.take())
}

func TestAgentRejectsMessagesItCannotTrust(t *testing.T) {
	a, s := openAgent(t, updater.ConfigAgentConfig{})
	good := fixture(t, "desired/config-revision.json")
	for name, body := range map[string]string{
		"unknown field": strings.Replace(good, `"v":1`, `"v":1,"extra":true`, 1),
		"wrong kind":    strings.Replace(good, "config.revision", "sync", 1),
		"not json":      `{`,
		"trailing":      good + good,
		"wrong version": strings.Replace(good, `"v":1`, `"v":2`, 1),
	} {
		assert.Error(t, a.HandleDesired(ctx, []byte(body)), name)
	}
	assert.Empty(t, s.take(), "a message the device could not trust produced a report")
}

// What a device applied survives a restart, and a send that fails leaves the device able to
// continue: the same message handled again completes the report.
func TestAgentPersistsAndResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	desired := []byte(fixture(t, "desired/config-revision.json"))

	a, s := openAgent(t, updater.ConfigAgentConfig{StatePath: path})
	s.err = errors.New("link down")
	assert.Error(t, a.HandleDesired(ctx, desired), "the RECEIVED report could not be sent")
	rev, _, _ := a.Held()
	assert.Zero(t, rev, "applied before it could say it had received")

	s.err = nil
	require.NoError(t, a.HandleDesired(ctx, desired))
	s.take()

	// Power cycle: a new agent over the same file holds the revision and announces it.
	b, s2 := openAgent(t, updater.ConfigAgentConfig{StatePath: path, BootID: "boot-1"})
	rev, digest, doc := b.Held()
	assert.EqualValues(t, 3, rev)
	assert.True(t, strings.HasPrefix(digest, "sha256:"))
	assert.Equal(t, `{"sampleIntervalSeconds":30}`, doc)
	require.NoError(t, b.Sync(ctx))
	assert.Equal(t, []string{fixture(t, "valid/sync.json")}, s2.take())
}

func TestAgentRefusesACorruptStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"v":1,"revision":2,"digest":"sha256:00","document":"{}"}`), 0o600))
	_, err := updater.OpenConfigAgent(updater.ConfigAgentConfig{StatePath: path, Transport: &sink{}})
	assert.ErrorIs(t, err, updater.ErrCorruptConfigState)
}

func TestAgentBoundsItsRejectionReasons(t *testing.T) {
	long := strings.Repeat("é", 200) // 400 bytes
	var many []string
	for i := 0; i < 40; i++ {
		many = append(many, "reason \x01 with a control character")
	}
	many[0] = long
	a, s := openAgent(t, updater.ConfigAgentConfig{Validate: func(string) []string { return many }})
	require.NoError(t, a.HandleDesired(ctx, []byte(fixture(t, "desired/config-revision.json"))))
	sent := s.take()
	r, err := ota.DecodeDeviceReport([]byte(sent[1]))
	require.NoError(t, err, "a rejection the device bounded must decode")
	assert.Len(t, r.ConfigStatus.Errors, 16)
	assert.LessOrEqual(t, len(r.ConfigStatus.Errors[0]), 256)
}
