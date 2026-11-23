// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Kind is what a run is for. The values equal the writer slot's run kinds (a test in slotfence
// proves it), but this package does not import that one: the runner has no database.
type Kind string

// Run kinds.
const (
	KindCreate     Kind = "CREATE"
	KindChange     Kind = "CHANGE"
	KindUpgrade    Kind = "UPGRADE"
	KindDriftCheck Kind = "DRIFT_CHECK"
	KindDestroy    Kind = "DESTROY"
	KindRecover    Kind = "RECOVER"
	// KindUnprotect and KindReprotect are the only kinds that may change deletion_protection (the
	// teardown protocol's UNPROTECT and RE-PROTECT). The writer slot does not admit them in this
	// build, so a run of either is refused at the fence.
	KindUnprotect Kind = "UNPROTECT"
	KindReprotect Kind = "REPROTECT"
)

// IdentityClass is the cloud identity a run may hold. Apply cannot delete a cluster (that is the
// cloud's IAM deny, not this package); destroy exists only for a destroy run.
type IdentityClass string

// Identity classes.
const (
	ClassApply   IdentityClass = "apply"
	ClassDestroy IdentityClass = "destroy"
)

// ErrUnknownKind is returned for a kind this build does not know. An unknown value is a refusal,
// never a default: a newer manager may have written it.
var ErrUnknownKind = errors.New("tofurun: unknown run kind")

// ClassFor is the identity class a kind runs under: DESTROY runs as destroy, everything else as apply.
func ClassFor(k Kind) (IdentityClass, error) {
	switch k {
	case KindCreate, KindChange, KindUpgrade, KindDriftCheck, KindRecover, KindUnprotect, KindReprotect:
		return ClassApply, nil
	case KindDestroy:
		return ClassDestroy, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownKind, string(k))
}

// refreshOnly reports whether a kind plans with -refresh-only: it observes and never changes.
func (k Kind) refreshOnly() bool { return k == KindDriftCheck || k == KindRecover }

// applies reports whether a kind may apply at all. A drift check only looks.
func (k Kind) applies() bool { return k != KindDriftCheck }

// ErrIdentityClass is returned when the credential offered is not of the class the run's kind
// requires, before any process exists.
var ErrIdentityClass = errors.New("tofurun: credential class does not match the run's identity class")

var (
	envNameRe   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	reservedEnv = map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "TEMP": true, "TMP": true, "SYSTEMROOT": true, "PG_CONN_STR": true}
)

// Credential is the environment one identity class's run is handed: a token or a credential file's
// path, injected into the child's environment and nowhere else. Its String, GoString and
// MarshalText never include a value.
type Credential struct {
	class     IdentityClass
	principal string
	env       map[string]string
}

// credentialPrefixes are the variable names a credential may set by default: the Google client
// libraries' own (GOOGLE_*, CLOUDSDK_*). Anything else must be allowed explicitly.
var credentialPrefixes = []string{"GOOGLE_", "CLOUDSDK_"}

// NewCredential validates and builds a Credential. Names are upper-case identifiers beginning
// GOOGLE_ or CLOUDSDK_; PATH, HOME, the temp variables, PG_CONN_STR and every TF_ name are the
// runner's own and are refused, so a credential cannot redirect tofu's data dir, its CLI config or
// its backend, or smuggle in a variable no provider reads for authentication.
func NewCredential(class IdentityClass, principal string, env map[string]string) (Credential, error) {
	return NewCredentialAllowing(class, principal, env)
}

// NewCredentialAllowing is NewCredential with further allowed name prefixes (a provider that reads
// its token from another variable, or a test's fake cloud). Every prefix is a decision to review.
func NewCredentialAllowing(class IdentityClass, principal string, env map[string]string, extraPrefixes ...string) (Credential, error) {
	if class != ClassApply && class != ClassDestroy {
		return Credential{}, fmt.Errorf("%w: %q", ErrIdentityClass, string(class))
	}
	if strings.TrimSpace(principal) == "" {
		return Credential{}, errors.New("tofurun: a credential names the principal it impersonates")
	}
	cp := make(map[string]string, len(env))
	for k, v := range env {
		if !envNameRe.MatchString(k) || reservedEnv[k] || strings.HasPrefix(k, "TF_") || !hasPrefix(k, credentialPrefixes, extraPrefixes) {
			return Credential{}, fmt.Errorf("tofurun: credential variable %q is not allowed", k)
		}
		if strings.ContainsRune(v, 0) {
			return Credential{}, fmt.Errorf("tofurun: credential variable %q holds a NUL", k)
		}
		cp[k] = v
	}
	return Credential{class: class, principal: principal, env: cp}, nil
}

// Class is the identity class.
func (c Credential) Class() IdentityClass { return c.class }

// Principal is the identity the credential impersonates (not a secret).
func (c Credential) Principal() string { return c.principal }

// String never includes a value.
func (c Credential) String() string {
	return fmt.Sprintf("Credential{class=%s principal=%s vars=%d}", c.class, c.principal, len(c.env))
}

// GoString never includes a value.
func (c Credential) GoString() string { return c.String() }

// MarshalText never includes a value.
func (c Credential) MarshalText() ([]byte, error) { return []byte(c.String()), nil }

// Credentials supplies the credential of a class. A runner holds one source per class it may use;
// a source must hand out only its own class, and the runner checks it did.
type Credentials interface {
	For(class IdentityClass) (Credential, error)
}

func hasPrefix(name string, sets ...[]string) bool {
	for _, set := range sets {
		for _, p := range set {
			if p != "" && strings.HasPrefix(name, p) {
				return true
			}
		}
	}
	return false
}
