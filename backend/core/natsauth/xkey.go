// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"errors"
	"fmt"

	"github.com/nats-io/nkeys"
)

// AuthRequestXKeyHeader is the header the broker stamps on an auth-callout request it has
// encrypted. Its value is the SERVER's curve public key, which the responder needs to open the
// request and to seal its reply. It spells nats-server's own constant (server.AuthRequestXKeyHeader);
// TestXKeyHeaderMatchesTheServer holds the two together, so a rename upstream fails here.
const AuthRequestXKeyHeader = "Nats-Server-Xkey"

// 🔴 WHY THE CALLOUT IS ENCRYPTED.
//
// The authorization request is a JWT carrying the connecting device's CONNECT username and
// password, published on `$SYS.REQ.USER.AUTH` inside the APP account. Every platform service holds
// a login in that account, so without encryption any of them can subscribe there (or capture the
// subject into a JetStream stream) and read every device credential as it connects.
//
// With auth_callout.xkey set, the broker seals each request to the responder's curve key and
// stamps its own public key in AuthRequestXKeyHeader. Only the holder of the responder's curve SEED
// can open it. That seed lives in device-management's own Secret, mounted nowhere else.

// ErrNotAnXKeySeed is returned for a seed that is not a curve (X) seed. An account or user seed
// is a valid nkey and would fail later and obscurely, inside the box, on first use.
var ErrNotAnXKeySeed = errors.New("not a curve (xkey) seed")

// GenerateXKey mints the curve key pair the callout encrypts to: the seed is the responder's,
// the public key goes in the broker's auth_callout.xkey.
func GenerateXKey() (seed, public string, err error) {
	kp, err := nkeys.CreateCurveKeys()
	if err != nil {
		return "", "", fmt.Errorf("creating callout xkey: %w", err)
	}
	s, err := kp.Seed()
	if err != nil {
		return "", "", fmt.Errorf("reading callout xkey seed: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return "", "", fmt.Errorf("reading callout xkey public key: %w", err)
	}
	return string(s), pub, nil
}

// XKeyPublicFromSeed derives the public key of a curve seed, refusing anything else.
func XKeyPublicFromSeed(seed string) (string, error) {
	kp, err := nkeys.FromCurveSeed([]byte(seed))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotAnXKeySeed, err)
	}
	defer kp.Wipe()
	return kp.PublicKey()
}

// IssuerPublicFromSeed derives the public account key from the callout issuer's seed: the value
// the broker's auth_callout.issuer holds and the instance configuration records.
func IssuerPublicFromSeed(seed string) (string, error) {
	kp, err := nkeys.FromSeed([]byte(seed))
	if err != nil {
		return "", fmt.Errorf("loading the callout issuer seed: %w", err)
	}
	defer kp.Wipe()
	return kp.PublicKey()
}

// XKeyCodec opens broker-sealed callout requests and seals replies, with one curve seed.
type XKeyCodec struct {
	kp nkeys.KeyPair
}

// NewXKeyCodec builds the codec for a curve seed. It refuses a seed that is not one.
func NewXKeyCodec(seed string) (*XKeyCodec, error) {
	kp, err := nkeys.FromCurveSeed([]byte(seed))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotAnXKeySeed, err)
	}
	return &XKeyCodec{kp: kp}, nil
}

// Public is the codec's public key, the value the broker is configured with.
func (c *XKeyCodec) Public() (string, error) { return c.kp.PublicKey() }

// Open decrypts a request sealed by the server whose curve public key is serverXKey.
func (c *XKeyCodec) Open(sealed []byte, serverXKey string) ([]byte, error) {
	return c.kp.Open(sealed, serverXKey)
}

// Seal encrypts a reply to the server whose curve public key is serverXKey.
func (c *XKeyCodec) Seal(plain []byte, serverXKey string) ([]byte, error) {
	return c.kp.Seal(plain, serverXKey)
}

// WithXKey completes a credential set with the callout's curve key. A non-empty existingSeed is
// REUSED (the key is derived from it, never replaced); an empty one is MINTED.
//
// 🔑 INTRODUCED, NOT EVOLVED. Every other broker credential is already load-bearing when a
// running instance is evolved, so replacing one breaks it while reporting success. The xkey is
// absent from the starting state: nothing authenticates with it until the broker is told, so
// minting it replaces nothing. That is the only reason an upgrade may mint it. A seed that is
// present but not a curve seed is an ERROR, not a reason to mint: it names a corrupted store, and
// minting over it would hide that.
func (c Credentials) WithXKey(existingSeed string) (Credentials, error) {
	if existingSeed == "" {
		seed, pub, err := GenerateXKey()
		if err != nil {
			return Credentials{}, err
		}
		c.XKeySeed, c.XKeyPublic = seed, pub
		return c, nil
	}
	pub, err := XKeyPublicFromSeed(existingSeed)
	if err != nil {
		return Credentials{}, fmt.Errorf("the running instance's callout xkey seed is unusable: %w", err)
	}
	c.XKeySeed, c.XKeyPublic = existingSeed, pub
	return c, nil
}
