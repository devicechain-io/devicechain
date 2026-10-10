// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// The wire literals. Deliberately NOT the core update contract's types: the simulator is the
// device side of that contract and speaks it from its own structs, the way cmdreceiver mirrors
// the command envelope. A shared struct would let a drift in the contract compile on both
// sides and stay invisible; with separate structs the golden fixtures and the mirror test are
// what hold the two together.
const (
	wireVersion = 1

	kindProgress  = "update.progress"
	kindInventory = "update.inventory"

	// The device-reported stages, in the device's own words.
	stageReceived    = "RECEIVED"
	stageDownloading = "DOWNLOADING"
	stageDownloaded  = "DOWNLOADED"
	stageVerified    = "VERIFIED"
	stageInstalling  = "INSTALLING"
	stageRebooting   = "REBOOTING"
	stageRunning     = "RUNNING"
	stageFailed      = "FAILED"
	stageAbandoned   = "ABANDONED"

	// maxDetailBytes is the contract's bound on a failure detail.
	maxDetailBytes = 512
)

type wireProgress struct {
	Bytes int64 `json:"bytes"`
	Total int64 `json:"total"`
}

type wireResult struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type wireRunning struct {
	Version string `json:"version"`
	Digest  string `json:"digest,omitempty"`
}

// wireReport is the one envelope both device-to-platform kinds share. Field order is the
// golden fixtures' byte order, which is what lets the mirror test compare bytes.
type wireReport struct {
	V              int           `json:"v"`
	Kind           string        `json:"kind"`
	AttemptID      string        `json:"attemptId,omitempty"`
	AssignmentID   string        `json:"assignmentId,omitempty"`
	Component      string        `json:"component"`
	ArtifactDigest string        `json:"artifactDigest,omitempty"`
	Seq            uint64        `json:"seq,omitempty"`
	Stage          string        `json:"stage,omitempty"`
	Progress       *wireProgress `json:"progress,omitempty"`
	Result         *wireResult   `json:"result,omitempty"`
	Running        *wireRunning  `json:"running,omitempty"`
	BootID         string        `json:"bootId,omitempty"`
}

func (r wireReport) encode() ([]byte, error) { return json.Marshal(r) }

// truncateDetail bounds a failure detail to the contract's byte limit without cutting a
// multi-byte character in half (the contract rejects invalid UTF-8).
func truncateDetail(s string) string {
	s = strings.ToValidUTF8(s, "?")
	if len(s) <= maxDetailBytes {
		return s
	}
	s = s[:maxDetailBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
