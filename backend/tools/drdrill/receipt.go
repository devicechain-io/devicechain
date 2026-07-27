// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Receipt is what seed hands to verify: everything needed to find the secret
// again in a cluster that has no memory of the run that created it, plus the
// expected plaintext.
//
// It carries the plaintext deliberately. The alternative — a fixed literal both
// halves know — would make the drill pass against a leftover row from an earlier
// run, which is exactly the false pass a restore drill must not be able to
// produce. The value is freshly random per run, so a match means THIS run's
// ciphertext was opened. The file is written 0600 and is throwaway drill
// material; it is not a place to put anything real.
type Receipt struct {
	// Instance is the instance the secret was seeded into, recorded so verify can
	// say so when the operator points it at the wrong dump.
	Instance string `json:"instance"`
	// Tenant, Schema and SecretName locate the row: the store's handle is
	// (tenant, scope=tenant, name), and the row lives in the seeding service's
	// functional-area schema.
	Tenant     string `json:"tenant"`
	Schema     string `json:"schema"`
	SecretName string `json:"secretName"`
	// ChannelToken/ChannelID identify the notification channel the secret hangs
	// off. The token is what the API query needs; the ID is what the secret
	// handle is keyed by (ChannelSecretName — the ID, because a token is mutable).
	ChannelToken string `json:"channelToken"`
	ChannelID    uint   `json:"channelId"`
	// Identity/Password is the scoped identity seed minted, so verify can ask the
	// restored instance whether it still sees the channel.
	Identity string `json:"identity"`
	Password string `json:"password"`
	// Secret is the expected plaintext. See the type comment.
	Secret string `json:"secret"`
}

// Validate rejects a receipt that cannot drive a verify. It is checked on READ
// rather than only on write, because the file crosses a cluster's lifetime and a
// truncated or hand-edited one must not degrade into a confusing decrypt error.
func (r Receipt) Validate() error {
	missing := []string{}
	for name, value := range map[string]string{
		"instance":   r.Instance,
		"tenant":     r.Tenant,
		"schema":     r.Schema,
		"secretName": r.SecretName,
		"secret":     r.Secret,
		// The API precheck needs these three. Without them here, a receipt that
		// lacked channelToken reached that check and reported "the restored
		// instance does not have channel \"\"" — blaming the restore for a
		// malformed file.
		"channelToken": r.ChannelToken,
		"identity":     r.Identity,
		"password":     r.Password,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if r.ChannelID == 0 {
		missing = append(missing, "channelId")
	}
	if len(missing) > 0 {
		// Sorted so the message is stable across map iteration order.
		return fmt.Errorf("receipt is missing %s", strings.Join(sortedStrings(missing), ", "))
	}
	return nil
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// WriteReceipt persists the receipt at 0600.
func WriteReceipt(path string, r Receipt) error {
	if err := r.Validate(); err != nil {
		return fmt.Errorf("refusing to write an unusable receipt: %w", err)
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o600)
}

// ReadReceipt loads and validates a receipt.
func ReadReceipt(path string) (Receipt, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Receipt{}, err
	}
	var r Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		return Receipt{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := r.Validate(); err != nil {
		return Receipt{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}
