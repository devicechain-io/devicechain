// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// The minted altId is edge:<installId>:<streamEpoch>:<streamSeq>. Its three non-seq
// parts are persisted tokens under StoreDir:
//
//   - installId identifies THIS box across the life of its store. Two agents feeding
//     one cloud Instance have independent capture streams whose sequences can coincide,
//     so without a per-box discriminator their minted keys could collide and the cloud
//     partial index would silently drop one agent's distinct events. Minted once, on
//     first boot.
//   - streamEpoch identifies the current INCARNATION of the capture stream. The stream
//     sequence (the third part) restarts at 1 if the stream is ever deleted and
//     recreated, which would re-issue keys the cloud already consumed — so the epoch
//     changes exactly when the stream is (re)created and stays fixed across restarts
//     otherwise. It is a token WE persist at stream-create time, NOT the server's
//     StreamInfo().Created, which nats-server resets on recovery.
//
// Both MUST be stable across a restart (a regenerated token would re-key still-buffered
// events into cloud duplicates), so both are written atomically and fsync'd — the same
// durability the spool itself gets (SyncAlways) — and read back thereafter.
const (
	installIdFile   = "install-id"
	streamEpochFile = "stream-epoch"
	ackedCountFile  = "acked-count"
)

// loadOrCreateInstallId returns the box install identity, minting it on first boot.
func loadOrCreateInstallId(storeDir string) (string, error) {
	return loadOrMintToken(storeDir, installIdFile, "install id")
}

// mintStreamEpoch mints and persists a FRESH stream-incarnation token, overwriting any
// prior one. Called only when the capture stream is (re)created, so a recreated stream's
// restarted sequences get a new namespace.
func mintStreamEpoch(storeDir string) (string, error) {
	id := uuid.NewString()
	if err := writeTokenAtomic(storeDir, streamEpochFile, id); err != nil {
		return "", fmt.Errorf("persist stream epoch: %w", err)
	}
	return id, nil
}

// readStreamEpoch reads the persisted stream-incarnation token for a stream we are
// adopting. It is an error for the token to be absent while the stream exists: minting a
// fresh one here would re-key the stream's buffered events into cloud duplicates, so we
// fail closed and make the operator reconcile (the store was partially wiped).
func readStreamEpoch(storeDir string) (string, error) {
	path := filepath.Join(storeDir, streamEpochFile)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("capture stream exists but its epoch token %q is missing — refusing to mint a fresh one (it would re-key buffered events into cloud duplicates); the store was partially wiped, clear it fully or restore the token", path)
		}
		return "", fmt.Errorf("read stream epoch %q: %w", path, err)
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", fmt.Errorf("stream epoch token %q is present but empty (corrupt); clear the store fully or restore it", path)
	}
	return id, nil
}

// loadOrSeedAckedCount returns the count of capture-stream messages this agent has
// removed by ack — the durable basis for the drop metric (drops =
// (FirstSeq-1) - ackedCount; see agent.sampleMetrics). It MUST be the agent's own
// persisted token, never derived from the durable consumer's ack floor: nats-server
// drags that floor PAST limit-evicted messages on restore (consumer.go checkAckFloor),
// which would silently erase exactly the drops this metric exists to surface.
//
// When the token is ABSENT it is seeded from the stream's current first sequence:
// everything already removed from the stream at first adoption is assumed acked
// (a pre-E3 store never evicted; a fresh store has removed nothing), so drops start at
// 0 from the adopted baseline forward — a one-time cap-shrink at first adoption is not
// retroactively counted. firstSeq is the live StreamInfo().State.FirstSeq; 0 (a
// brand-new stream) seeds 0.
func loadOrSeedAckedCount(storeDir string, firstSeq uint64) (uint64, error) {
	// The count of messages removed by ack can never exceed the total removed from the
	// stream front, which is exactly firstSeq-1. Clamp to it: a loaded token above this
	// ceiling means the stream is a NEW incarnation (deleted+recreated, or its data dir
	// wiped while the token survived) or the token is corrupt — either way an un-clamped
	// stale-high value would make drops = (firstSeq-1) - ackedCount clamp to 0 and HIDE
	// real evictions until firstSeq caught up. Clamping re-bases onto the live stream.
	var ceiling uint64
	if firstSeq > 0 {
		ceiling = firstSeq - 1
	}
	path := filepath.Join(storeDir, ackedCountFile)
	b, err := os.ReadFile(path)
	if err == nil {
		n, perr := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		if perr != nil {
			return 0, fmt.Errorf("acked-count token %q is corrupt (%q): %w", path, strings.TrimSpace(string(b)), perr)
		}
		if n > ceiling {
			return ceiling, nil
		}
		return n, nil
	}
	if !os.IsNotExist(err) {
		return 0, fmt.Errorf("read acked-count %q: %w", path, err)
	}
	if err := persistAckedCount(storeDir, ceiling); err != nil {
		return 0, err
	}
	return ceiling, nil
}

// persistAckedCount durably writes the acked-count progress token (atomic temp+fsync+
// rename). Called on the sample tick and on graceful shutdown; a crash between writes
// can only leave it stale-LOW, which over-counts drops (biased safe — never hides loss).
func persistAckedCount(storeDir string, count uint64) error {
	return writeTokenAtomic(storeDir, ackedCountFile, strconv.FormatUint(count, 10))
}

// loadOrMintToken reads a persisted token, minting and atomically persisting a fresh
// UUID if none exists yet.
func loadOrMintToken(storeDir, name, label string) (string, error) {
	path := filepath.Join(storeDir, name)
	b, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(b))
		if id == "" {
			return "", fmt.Errorf("%s file %q is present but empty — refusing to mint a fresh one over a corrupt token (it would re-key buffered events); remove it only if the spool is also empty", label, path)
		}
		return id, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read %s %q: %w", label, path, err)
	}
	id := uuid.NewString()
	if err := writeTokenAtomic(storeDir, name, id); err != nil {
		return "", fmt.Errorf("persist %s: %w", label, err)
	}
	return id, nil
}

// writeTokenAtomic writes a token durably: to a temp file, fsync, rename into place, then
// fsync the directory. A crash mid-write leaves either the old token or none (surfaced by
// the not-exist / empty guards on next boot), never a torn value that silently re-keys.
func writeTokenAtomic(storeDir, name, value string) error {
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return fmt.Errorf("create store dir %q: %w", storeDir, err)
	}
	final := filepath.Join(storeDir, name)
	tmp, err := os.CreateTemp(storeDir, name+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.WriteString(value + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, final); err != nil {
		return err
	}
	// fsync the directory so the rename itself survives a power cut (best-effort: not all
	// platforms permit opening a dir, so a failure here is not fatal).
	if d, err := os.Open(storeDir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
