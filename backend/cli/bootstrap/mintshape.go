// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// How a minted credential is shaped, and why it is shaped that way.
//
// 🔴 THE ALPHABET IS A DELIBERATE INTEROPERABILITY CHOICE, NOT A WORKAROUND. These
// values cross into systems whose quoting rules this project does not own — the
// database, the object store, the dashboard stack — and each parses credentials its
// own way. A character class that needs no quoting in a URL, a shell word, a libpq
// keyword string or a YAML scalar removes that whole class of question rather than
// answering it once per consumer and hoping the next consumer is asked too.
//
// It is NOT here because a consumer is known to mishandle punctuation. The one place
// in this tree that did — the bootstrap's own database check — was repaired rather
// than accommodated, because accommodating it would have left the defect live for
// every credential an operator supplies by hand.
//
// base64url without padding (RFC 4648 §5): 64 symbols over [A-Za-z0-9-_], six bits a
// character, and no `=` — which is itself syntax in a keyword DSN and in an
// environment file.
const (
	// mintedPasswordBytes is the entropy behind a minted password. 32 bytes is 256
	// bits, rendered as 43 characters.
	mintedPasswordBytes = 32

	// minObjectStoreSecretLen is MinIO's floor. 🔴 IT IS ENFORCED TODAY ONLY BY AN
	// OpenTofu VARIABLE VALIDATION, WHICH A SECRET WRITTEN BY dcctl BYPASSES
	// ENTIRELY. Below it MinIO crash-loops, so the check has to move with the value.
	minObjectStoreSecretLen = 8
)

// mintPassword returns a fresh credential in the shape described above.
func mintPassword() (string, error) {
	return mintPasswordOfBytes(mintedPasswordBytes)
}

func mintPasswordOfBytes(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("a credential needs a positive entropy budget, got %d bytes", n)
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("reading randomness for a credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// validateObjectStoreSecret enforces MinIO's length floor.
//
// 🔴 IT IS A PROPERTY OF THE VALUE, NOT OF THE MINT, AND THAT IS WHY IT IS ITS OWN
// FUNCTION. Written inside the generator it would be unreachable — the minted length
// is a constant comfortably above the floor — and an unreachable check is one that
// cannot fail, which is the same as not having it. The reachable caller is the
// SUPPLIED path: an operator pointing at their own object store can hand over
// anything, and a short value takes MinIO into a crash loop whose first visible
// symptom is that the write-ahead log stopped being archived.
//
// The floor used to live in an OpenTofu variable validation, which a Secret written
// by dcctl does not pass through.
func validateObjectStoreSecret(v string) error {
	if len(v) < minObjectStoreSecretLen {
		return fmt.Errorf("an object-store credential must be at least %d characters and this one "+
			"is %d; the object store refuses shorter ones and crash-loops, which surfaces as "+
			"backups silently no longer being archived",
			minObjectStoreSecretLen, len(v))
	}
	return nil
}

// mintObjectStoreSecret returns a credential for the in-cluster object store, held to
// the same floor a supplied one is.
func mintObjectStoreSecret() (string, error) {
	v, err := mintPassword()
	if err != nil {
		return "", err
	}
	if err := validateObjectStoreSecret(v); err != nil {
		return "", fmt.Errorf("the minted object-store credential is unusable, so the entropy "+
			"budget has been tuned below what the object store accepts: %w", err)
	}
	return v, nil
}
