// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package webhooksig signs and verifies outbound webhook deliveries per the Standard Webhooks
// specification (https://www.standardwebhooks.com). It is the one implementation behind every
// signed delivery the platform makes: the REACT httpCall action and the notification webhook
// channel both sign through core/httpsink, which calls Sign here. It carries the receiver side
// too (Verify), so the verification helper a consumer copies is the code the golden vectors pin.
//
// A delivery carries three headers:
//
//	webhook-id          a message id, stable across the retries of one message (a receiver
//	                    deduplicates on it)
//	webhook-timestamp   integer unix seconds, refreshed on every attempt (a receiver rejects a
//	                    stale one, which is what bounds replay)
//	webhook-signature   one or more space-delimited "v1,<base64 HMAC-SHA256>" entries
//
// The signed content is "<id>.<timestamp>.<body>" over the EXACT bytes sent, keyed by the
// base64-decoded secret (the "whsec_" prefix is a label, not part of the key). During a key
// rotation a delivery carries one entry per key, so a receiver holding either key verifies it.
package webhooksig

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// HeaderID, HeaderTimestamp and HeaderSignature are the three Standard Webhooks headers.
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"

	// SecretPrefix labels a secret as a signing secret. It is not part of the key.
	SecretPrefix = "whsec_"

	// DefaultTolerance is the clock skew a receiver should accept in either direction.
	DefaultTolerance = 5 * time.Minute

	// secretBytes is the generated key length. The spec allows 24 to 64 bytes.
	secretBytes = 32

	// minKeyBytes and maxKeyBytes bound an accepted key to the spec's range, so a secret that was
	// truncated or pasted wrong is refused instead of quietly producing signatures nobody can
	// verify.
	minKeyBytes = 24
	maxKeyBytes = 64

	sigVersion = "v1"
)

var (
	// ErrInvalidSecret reports a signing secret that is not "whsec_" plus base64 of 24 to 64 bytes.
	ErrInvalidSecret = errors.New("webhooksig: invalid signing secret")
	// ErrNoSecrets reports a Sign or Verify call that was given no key.
	ErrNoSecrets = errors.New("webhooksig: no signing secret supplied")
	// ErrMissingHeaders reports a delivery lacking webhook-id, webhook-timestamp or
	// webhook-signature.
	ErrMissingHeaders = errors.New("webhooksig: missing webhook-id, webhook-timestamp or webhook-signature header")
	// ErrBadTimestamp reports a webhook-timestamp that is not integer unix seconds.
	ErrBadTimestamp = errors.New("webhooksig: malformed webhook-timestamp")
	// ErrStale reports a timestamp outside the tolerance window, in either direction.
	ErrStale = errors.New("webhooksig: webhook-timestamp is outside the tolerance window")
	// ErrBadSignature reports that no signature on the delivery matches the body under any key.
	ErrBadSignature = errors.New("webhooksig: no valid signature")
)

// GenerateSecret returns a new random signing secret in the form the spec and every receiver
// library expects: "whsec_" followed by the base64 of 32 random bytes.
func GenerateSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("webhooksig: generate secret: %w", err)
	}
	return SecretPrefix + base64.StdEncoding.EncodeToString(b), nil
}

// key decodes a secret to its HMAC key.
func key(secret string) ([]byte, error) {
	raw := strings.TrimPrefix(secret, SecretPrefix)
	k, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: not base64", ErrInvalidSecret)
	}
	if len(k) < minKeyBytes || len(k) > maxKeyBytes {
		return nil, fmt.Errorf("%w: key must be %d to %d bytes", ErrInvalidSecret, minKeyBytes, maxKeyBytes)
	}
	return k, nil
}

// ValidateSecret reports whether secret is usable as a signing secret.
func ValidateSecret(secret string) error {
	_, err := key(secret)
	return err
}

func sign(k []byte, id string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, k)
	mac.Write([]byte(id))
	mac.Write([]byte{'.'})
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return sigVersion + "," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Sign returns the webhook-signature header value for a delivery: one "v1,<signature>" entry per
// secret, in the order given, space-delimited. A secret that is not a valid signing secret is an
// error: a delivery is never silently signed with fewer keys than were asked for.
func Sign(id string, at time.Time, body []byte, secrets ...string) (string, error) {
	if len(secrets) == 0 {
		return "", ErrNoSecrets
	}
	entries := make([]string, 0, len(secrets))
	for _, s := range secrets {
		k, err := key(s)
		if err != nil {
			return "", err
		}
		entries = append(entries, sign(k, id, at.Unix(), body))
	}
	return strings.Join(entries, " "), nil
}

// Timestamp formats a time as the webhook-timestamp header value.
func Timestamp(at time.Time) string { return strconv.FormatInt(at.Unix(), 10) }

// Verify checks a received delivery against one or more secrets (a receiver mid-rotation holds
// two). It requires all three headers, a timestamp within tolerance of now in either direction
// (tolerance <= 0 means DefaultTolerance), and at least one "v1" entry that matches the body under
// at least one secret, compared in constant time. Entries of an unknown version are skipped, as
// the spec requires, so a future version does not break a v1 receiver.
//
// body MUST be the raw bytes received, before any JSON decoding: re-serializing a parsed body
// changes the bytes and so the signature.
func Verify(h http.Header, body []byte, now time.Time, tolerance time.Duration, secrets ...string) error {
	if len(secrets) == 0 {
		return ErrNoSecrets
	}
	id := h.Get(HeaderID)
	tsRaw := h.Get(HeaderTimestamp)
	sigRaw := h.Get(HeaderSignature)
	if id == "" || tsRaw == "" || sigRaw == "" {
		return ErrMissingHeaders
	}
	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return ErrBadTimestamp
	}
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	skew := now.Sub(time.Unix(ts, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > tolerance {
		return ErrStale
	}
	keys := make([][]byte, 0, len(secrets))
	for _, s := range secrets {
		k, err := key(s)
		if err != nil {
			return err
		}
		keys = append(keys, k)
	}
	for _, entry := range strings.Fields(sigRaw) {
		version, _, ok := strings.Cut(entry, ",")
		if !ok || version != sigVersion {
			continue
		}
		for _, k := range keys {
			if hmac.Equal([]byte(entry), []byte(sign(k, id, ts, body))) {
				return nil
			}
		}
	}
	return ErrBadSignature
}
