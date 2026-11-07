// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package webhooksig

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/devicechain-io/dc-microservice/secrets"
)

// A signing secret lives in the secret store as TWO slots under a base name: "<base>/current" and
// "<base>/previous". Rotation is what the second slot is for. Rotate demotes the current key to
// previous and installs a new current, and while previous exists every delivery carries a
// signature under each, so a receiver can swap keys at its own pace without dropping a delivery.
// EndRotation drops previous once the receiver has moved.
//
// The slot is always the LAST path segment, so the mapping from (base, slot) to a store name is
// injective: no base can make one slot's name equal another's.
const (
	slotCurrent  = "/current"
	slotPrevious = "/previous"
)

var (
	// ErrNoSigningSecret reports a base with no current key. A delivery that is meant to be signed
	// and finds this is refused, never sent unsigned.
	ErrNoSigningSecret = errors.New("webhooksig: no signing secret is stored for this handle")
	// ErrSigningSecretExists reports a Create over a base that already has a current key.
	ErrSigningSecretExists = errors.New("webhooksig: a signing secret already exists for this handle")
)

// ReservedPrefix is the namespace every signing secret is stored under, ahead of the handle. The
// secret store is shared with the Bearer credentials a rule names by handle, so a rule that named
// a signing key as its Bearer credential would send the key, which is never meant to be readable
// after it is issued, to its own endpoint. The connectors service refuses a credential handle in
// this namespace when it delivers, and the rule compiler refuses it when the rule is saved.
const ReservedPrefix = "webhook-signing/"

// InReservedNamespace reports whether a credential handle names something in the signing keyspace.
func InReservedNamespace(handle string) bool { return strings.HasPrefix(handle, ReservedPrefix) }

// KeyNames returns the secret-store names of a base's two slots.
func KeyNames(base string) (current, previous string) { return base + slotCurrent, base + slotPrevious }

// Resolve reads a secret value by store name. It is the seam both services use: the connectors
// service passes its cached resolver, the notification service passes its store.
type Resolve func(ctx context.Context, name string) (string, error)

// ResolveKeys returns the keys a delivery under base is signed with: the current key first, then
// the previous one while a rotation is open. A missing current key is ErrNoSigningSecret; a
// missing previous key is simply no rotation. Any other store error is returned as is, so the
// caller can tell a transient failure from a missing key. A stored value that is not a valid
// secret is an error rather than being skipped, so a corrupt slot is loud.
func ResolveKeys(ctx context.Context, base string, resolve Resolve) ([]string, error) {
	cur, prev := KeyNames(base)
	current, err := resolve(ctx, cur)
	if errors.Is(err, secrets.ErrSecretNotFound) {
		return nil, ErrNoSigningSecret
	}
	if err != nil {
		return nil, err
	}
	if err := ValidateSecret(current); err != nil {
		return nil, err
	}
	keys := []string{current}
	previous, err := resolve(ctx, prev)
	switch {
	case errors.Is(err, secrets.ErrSecretNotFound):
		return keys, nil
	case err != nil:
		return nil, err
	}
	if err := ValidateSecret(previous); err != nil {
		return nil, err
	}
	return append(keys, previous), nil
}

// Keyring manages the write side of a base's two slots in one tenant's scope. The secrets it
// generates are returned to the caller exactly once and are otherwise unreadable through any API.
//
// Rotations of one base are serialized within a process. Across replicas they are not: the secret
// store offers no compare-and-swap, so two operators rotating the same base at the same instant
// on different replicas can leave the first one's new key already replaced, and a receiver
// configured with it would stop verifying. The key that is current after both calls is the one
// the last writer returned, so rotating once more resolves it.
type Keyring struct {
	Store secrets.SecretStore
}

var rotating sync.Map // base key -> *sync.Mutex

func lockRotation(tenant, base string) func() {
	m, _ := rotating.LoadOrStore(tenant+"\n"+base, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (k Keyring) ref(tenant, name string) secrets.SecretRef {
	return secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: tenant, Name: name}
}

// Create generates the first key for base and returns it. It never replaces an existing key
// (ErrSigningSecretExists), so two racing creates cannot hand out two different "first" secrets;
// replacing is Rotate.
func (k Keyring) Create(ctx context.Context, tenant, base string) (string, error) {
	secret, err := GenerateSecret()
	if err != nil {
		return "", err
	}
	cur, _ := KeyNames(base)
	created, err := secrets.CreateIfAbsent(ctx, k.Store, k.ref(tenant, cur), []byte(secret))
	if err != nil {
		return "", err
	}
	if !created {
		return "", ErrSigningSecretExists
	}
	return secret, nil
}

// Rotate installs a new current key, keeps the old one as previous, and returns the new one.
// The previous slot is written first: a failure between the two writes leaves previous equal to
// current, which signs twice with one key and is harmless, where the opposite order could lose
// the key a receiver still holds. Rotating again while a rotation is open replaces previous with
// the then-current key, so a key two rotations old stops being signed with.
func (k Keyring) Rotate(ctx context.Context, tenant, base string) (string, error) {
	defer lockRotation(tenant, base)()
	cur, prev := KeyNames(base)
	old, err := k.Store.Resolve(ctx, k.ref(tenant, cur))
	if errors.Is(err, secrets.ErrSecretNotFound) {
		return "", ErrNoSigningSecret
	}
	if err != nil {
		return "", err
	}
	secret, err := GenerateSecret()
	if err != nil {
		return "", err
	}
	if err := k.Store.Put(ctx, k.ref(tenant, prev), old); err != nil {
		return "", fmt.Errorf("webhooksig: keep previous key: %w", err)
	}
	if err := k.Store.Put(ctx, k.ref(tenant, cur), []byte(secret)); err != nil {
		return "", fmt.Errorf("webhooksig: install new key: %w", err)
	}
	return secret, nil
}

// EndRotation drops the previous key, so deliveries carry one signature again. Idempotent.
func (k Keyring) EndRotation(ctx context.Context, tenant, base string) error {
	_, prev := KeyNames(base)
	return k.Store.Delete(ctx, k.ref(tenant, prev))
}

// Delete removes both slots. Idempotent.
func (k Keyring) Delete(ctx context.Context, tenant, base string) error {
	cur, prev := KeyNames(base)
	if err := k.Store.Delete(ctx, k.ref(tenant, prev)); err != nil {
		return err
	}
	return k.Store.Delete(ctx, k.ref(tenant, cur))
}

// Status reports whether base has a current key and whether a rotation is open.
func (k Keyring) Status(ctx context.Context, tenant, base string) (exists, rotating bool, err error) {
	cur, prev := KeyNames(base)
	if exists, err = k.Store.Exists(ctx, k.ref(tenant, cur)); err != nil || !exists {
		return false, false, err
	}
	rotating, err = k.Store.Exists(ctx, k.ref(tenant, prev))
	return exists, rotating, err
}

// MaxHandleLen bounds a signing handle.
const MaxHandleLen = 256

// ValidateHandle checks the name a tenant gives a signing secret, the value a rule's
// signingSecretRef carries and a mutation's handle argument takes. It is a bounded path of
// letters, digits and - _ . / with no leading slash and no empty or dot segment, the same shape
// as every other secret handle a rule authors, so it can never carry an injection or an unbounded
// value into the store lookup. One definition for the rule compiler that refuses a bad handle at
// publish and the service that creates the secret.
func ValidateHandle(h string) error {
	if h == "" {
		return errors.New("handle must not be empty")
	}
	if len(h) > MaxHandleLen {
		return fmt.Errorf("handle exceeds %d bytes", MaxHandleLen)
	}
	for _, r := range h {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '/' || r == '.') {
			return fmt.Errorf("handle contains an invalid character %q (allowed: alphanumeric - _ / .)", r)
		}
	}
	if h[0] == '/' {
		return errors.New("handle must not start with '/'")
	}
	for _, seg := range strings.Split(h, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("handle has an empty or dot path segment")
		}
	}
	return nil
}
