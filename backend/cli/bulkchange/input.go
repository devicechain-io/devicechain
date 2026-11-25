// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bulkchange

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/ledger"
)

type ConfigSet struct {
	Key       string  `json:"key"`
	Value     *string `json:"value,omitempty"`
	ValueType string  `json:"valueType,omitempty"`
	Unset     bool    `json:"unset"`
}

type Request struct {
	Token        string         `json:"token"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	ConfigSet    ConfigSet      `json:"configSet"`
	DeviceTokens []string       `json:"deviceTokens"`
	Safety       map[string]any `json:"safety,omitempty"`
}

// ReadDevices preserves the exact file bytes for idempotency. Files contain one
// token per nonempty line; order and duplicate inputs are left to the server.
func ReadDevices(path string) ([]string, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, ledger.MaxRequestBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > ledger.MaxRequestBytes {
		return nil, nil, fmt.Errorf("device file exceeds %d bytes", ledger.MaxRequestBytes)
	}
	if !utf8.Valid(raw) {
		return nil, nil, fmt.Errorf("device file must be UTF-8")
	}
	var tokens []string
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if token := strings.TrimSpace(string(line)); token != "" {
			tokens = append(tokens, token)
		}
	}
	if len(tokens) == 0 || len(tokens) > ledger.MaxLinesPerOperation {
		return nil, nil, fmt.Errorf("device file must contain 1-%d tokens", ledger.MaxLinesPerOperation)
	}
	return tokens, raw, nil
}

// DefaultToken hashes tenant || exact target bytes || kind || canonical intent.
// This is only the idempotency token. Contract digests always come from the server.
func DefaultToken(tenant string, targetBytes []byte, kind string, intent ConfigSet) (string, error) {
	raw, err := ledger.Canonical(intent)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, _ = io.WriteString(h, tenant)
	_, _ = h.Write(targetBytes)
	_, _ = io.WriteString(h, kind)
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DeviceBytes gives repeated --device values a deterministic ordered encoding.
func DeviceBytes(tokens []string) []byte {
	raw, _ := json.Marshal(tokens)
	return raw
}
