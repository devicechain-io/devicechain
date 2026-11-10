// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Canonical returns the canonical JSON form of v: object keys sorted, no insignificant
// whitespace, numbers as written. It is what a proposal stores and what its digest covers, so two
// requests that mean the same action produce the same bytes whatever order their fields arrived
// in.
//
// The area builds v from the request it has already validated and normalized (for example, alarm
// tokens deduplicated and sorted): canonicalization fixes the encoding, the area fixes the
// meaning.
func Canonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	// encoding/json writes map keys sorted, and a json.Number exactly as it was read.
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}

// Digest is the value an approval binds to: sha256 over the kind, the canonical payload and the
// canonical target set, each length-prefixed so no byte of one can be read as part of another.
// An approver sends the digest the console showed them, and an approval of any other digest is
// refused, so a person approves exactly the stored action and target set and nothing else.
func Digest(kind Kind, payload, targets []byte) string {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(kind), payload, targets} {
		fmt.Fprintf(h, "%d:", len(part))
		h.Write(part)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// DigestEqual compares two digests. An empty digest never matches.
//
// A digest is not a secret: it is shown to every approver and returned to the proposing agent, and
// knowing it grants nothing (the approval still needs a person's own session, the write, an
// approver role and a fresh second factor). So it is compared plainly, not as a credential.
func DigestEqual(a, b string) bool {
	return a != "" && b != "" && a == b
}

// TokenPrefix begins every proposal token.
const TokenPrefix = "agp_"

// NewToken returns a server-generated, unguessable proposal token: 128 random bits. A client
// never chooses one, so no human-choosable name is reserved and no token can be predicted.
func NewToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return TokenPrefix + hex.EncodeToString(b), nil
}
