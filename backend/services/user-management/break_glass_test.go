// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
)

// T18. The break-glass subcommand, run through runBreakGlass (the function main calls) against the
// REAL Manager and store: what a pod exec would do, minus the pod. The pod-only parts (the
// service environment, the mounted configuration) are the guards tested below.

type breakGlassRig struct {
	w       *mfaWiring
	m       *identity.Manager
	out     bytes.Buffer
	errOut  bytes.Buffer
	opened  int
	envVars map[string]string
}

func newBreakGlassRig(t *testing.T) *breakGlassRig {
	t.Helper()
	w := newMfaWiring(t)
	policy := credential.Policy{Free: 1000, Base: time.Minute, Cap: 4 * time.Minute}
	checker, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIdentity: policy, credential.KindOAuthClient: policy, credential.KindTotp: {Unthrottled: true},
	})
	require.NoError(t, err)
	m, err := newIdentityManager(nil, w.db, nil, w.secrets, 0, 0, "", identity.BootstrapConfig{}, checker,
		mscfg.InfrastructureConfiguration{})
	require.NoError(t, err)
	configureMfa(m, startMfaBroker(t), nil, nil)
	return &breakGlassRig{w: w, m: m, envVars: map[string]string{
		"KUBERNETES_SERVICE_HOST": "10.0.0.1", core.ENV_MS_FUNCTIONAL_AREA: "user-management",
	}}
}

func (r *breakGlassRig) env(stdin string) breakGlassEnv {
	return breakGlassEnv{
		getenv: func(k string) string { return r.envVars[k] },
		stdin:  strings.NewReader(stdin), stdout: &r.out, stderr: &r.errOut,
		open: func(context.Context) (breakGlassResetter, func(), error) {
			r.opened++
			return r.m, func() {}, nil
		},
	}
}

func (r *breakGlassRig) run(t *testing.T, stdin string, args ...string) int {
	t.Helper()
	r.out.Reset()
	r.errOut.Reset()
	return runBreakGlass(context.Background(), args, r.env(stdin))
}

// operator makes an enrolled identity holding the superuser system role.
func (r *breakGlassRig) operator(t *testing.T, email, password string) *iam.Identity {
	t.Helper()
	id := r.w.enrolledWithRecoveryCodes(t, email, password, "ABCD-EFGH-JKMN-PQRS")
	ctx := core.WithSystemContext(context.Background())
	role := iam.Role{Scope: iam.ScopeSystem, Token: iam.SuperuserRoleToken, Authorities: []string{"*"}}
	require.NoError(t, r.w.store.CreateRole(ctx, &role))
	require.NoError(t, r.w.store.AssignSystemRoles(ctx, id, []iam.Role{role}))
	return id
}

func (r *breakGlassRig) identity(t *testing.T, email string) *iam.Identity {
	t.Helper()
	id, err := r.w.store.IdentityByEmail(core.WithSystemContext(context.Background()), email)
	require.NoError(t, err)
	return id
}

func (r *breakGlassRig) hasFactor(t *testing.T, id *iam.Identity) bool {
	t.Helper()
	_, err := r.w.store.MfaFactorFor(core.WithSystemContext(context.Background()), id.ID)
	if errors.Is(err, iam.ErrMfaFactorNotFound) {
		return false
	}
	require.NoError(t, err)
	return true
}

func (r *breakGlassRig) resetAudits(t *testing.T) []string {
	t.Helper()
	var actors []string
	require.NoError(t, r.w.db.Database.Raw("SELECT actor FROM audit_events WHERE operation = ?", "mfa_reset").Scan(&actors).Error)
	return actors
}

// An identity with no system role is refused, and NOTHING changes: the factor, the password and
// the epoch are as they were, and no audit row is written.
func TestBreakGlassRefusesAnIdentityWithNoSystemRole(t *testing.T) {
	r := newBreakGlassRig(t)
	before := r.w.enrolledWithRecoveryCodes(t, "pat@example.com", "pw-1", "ABCD-EFGH-JKMN-PQRS")

	code := r.run(t, "", "mfa-reset", "--email", "pat@example.com")
	require.Equal(t, 1, code)
	require.Contains(t, r.errOut.String(), "system role")
	require.NotContains(t, r.out.String(), "New password", "a refused reset must not print a password")

	after := r.identity(t, "pat@example.com")
	require.True(t, r.hasFactor(t, before), "the factor was removed from a non-operator")
	require.Equal(t, before.PasswordHash, after.PasswordHash)
	require.Equal(t, before.SessionEpoch, after.SessionEpoch)
	require.Empty(t, r.resetAudits(t))

	// The control: the same call against an operator succeeds, so the refusal above was the role.
	r.operator(t, "root@example.com", "pw-root")
	require.Equal(t, 0, r.run(t, "", "mfa-reset", "--email", "root@example.com"), r.errOut.String())
}

// An enrolled superuser: the factor is gone, the password rotated, the epoch bumped, the audit
// actor names the route, the old password and the old challenge both fail, and the generated
// password is printed once and is the one the store now holds.
func TestBreakGlassResetsAnEnrolledSuperuser(t *testing.T) {
	r := newBreakGlassRig(t)
	root := r.operator(t, "root@example.com", "pw-root")
	ctx := context.Background()

	// A challenge earned with the old password, before the reset.
	_, err := r.m.Login(ctx, "root@example.com", "pw-root")
	var ch *identity.MfaChallengeError
	require.True(t, errors.As(err, &ch), "an enrolled operator is challenged, got %v", err)

	require.Equal(t, 0, r.run(t, "", "mfa-reset", "--email", "root@example.com"), r.errOut.String())

	after := r.identity(t, "root@example.com")
	require.False(t, r.hasFactor(t, root), "the factor must be gone")
	require.NotEqual(t, root.SessionEpoch, after.SessionEpoch, "the epoch must rotate")
	require.Error(t, bcrypt.CompareHashAndPassword([]byte(after.PasswordHash), []byte("pw-root")), "the old password must fail")

	// The password on screen is the password in the store, and it is shown once.
	var shown string
	for _, line := range strings.Split(r.out.String(), "\n") {
		if strings.HasPrefix(line, "New password") {
			shown = strings.TrimSpace(line[strings.LastIndex(line, ":")+1:])
		}
	}
	require.Len(t, shown, breakGlassPasswordLength)
	require.Equal(t, 1, strings.Count(r.out.String(), shown))
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(after.PasswordHash), []byte(shown)))

	require.Equal(t, []string{identity.BreakGlassActor}, r.resetAudits(t))
	require.Equal(t, "break-glass:pod-exec", identity.BreakGlassActor)

	// The old challenge is void: a recovery code the factor held no longer completes it.
	_, err = r.m.VerifyMfa(ctx, ch.Challenge, "ABCD-EFGH-JKMN-PQRS", "192.0.2.1")
	require.True(t, isCode(err, "MFA_CHALLENGE_EXPIRED"), "got %v", err)
}

func isCode(err error, code string) bool {
	var coded interface{ Extensions() map[string]any }
	return errors.As(err, &coded) && coded.Extensions()["code"] == code
}

// A password read from stdin is the one set; an empty stdin is refused, with nothing changed.
func TestBreakGlassPasswordFromStdinAndNeverSkipped(t *testing.T) {
	r := newBreakGlassRig(t)
	root := r.operator(t, "root@example.com", "pw-root")

	require.Equal(t, 1, r.run(t, "\n", "mfa-reset", "--email", "root@example.com", "--password-stdin"))
	require.Contains(t, r.errOut.String(), "always replaced")
	require.True(t, r.hasFactor(t, root), "an empty password must not reset the factor")
	require.Equal(t, root.PasswordHash, r.identity(t, "root@example.com").PasswordHash)

	require.Equal(t, 0, r.run(t, "chosen-by-the-operator\n", "mfa-reset", "--email", "root@example.com", "--password-stdin"),
		r.errOut.String())
	require.NotContains(t, r.out.String(), "chosen-by-the-operator", "a password read from stdin is not echoed back")
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(r.identity(t, "root@example.com").PasswordHash),
		[]byte("chosen-by-the-operator")))
}

// The store-level refusal that backs the CLI one: a reset with no password does nothing, even when
// called directly.
func TestBreakGlassResetMfaRefusesWithoutAPassword(t *testing.T) {
	r := newBreakGlassRig(t)
	root := r.operator(t, "root@example.com", "pw-root")
	_, err := r.m.BreakGlassResetMfa(context.Background(), "root@example.com", "")
	require.ErrorIs(t, err, identity.ErrBreakGlassNeedsPassword)
	require.True(t, r.hasFactor(t, root))
	require.Equal(t, root.PasswordHash, r.identity(t, "root@example.com").PasswordHash)
	require.Empty(t, r.resetAudits(t))
}

// An address that names nobody is named as such.
func TestBreakGlassNamesNothingToReset(t *testing.T) {
	r := newBreakGlassRig(t)
	require.Equal(t, 1, r.run(t, "", "mfa-reset", "--email", "nobody@example.com"))
	require.Contains(t, r.errOut.String(), "not found")
}

// It runs only in a user-management pod: without the Kubernetes service environment, or in
// another service container, it does not even boot the store.
func TestBreakGlassRunsOnlyInsideAUserManagementPod(t *testing.T) {
	r := newBreakGlassRig(t)
	r.operator(t, "root@example.com", "pw-root")

	delete(r.envVars, "KUBERNETES_SERVICE_HOST")
	require.Equal(t, 1, r.run(t, "", "mfa-reset", "--email", "root@example.com"))
	require.Contains(t, r.errOut.String(), "inside a user-management pod")

	r.envVars["KUBERNETES_SERVICE_HOST"] = "10.0.0.1"
	r.envVars[core.ENV_MS_FUNCTIONAL_AREA] = "device-management"
	require.Equal(t, 1, r.run(t, "", "mfa-reset", "--email", "root@example.com"))
	require.Contains(t, r.errOut.String(), "functional area")
	require.Zero(t, r.opened, "a refused run must not open the store")
}

// The production boot refuses when the pod mounted configuration or the root key is absent: on
// this machine there is no /etc/dci-config, so it must fail with a message and open nothing.
func TestBreakGlassProductionBootNeedsTheMountedConfiguration(t *testing.T) {
	var out, errOut bytes.Buffer
	env := productionBreakGlassEnv()
	env.stdout, env.stderr = &out, &errOut
	env.getenv = func(k string) string {
		return map[string]string{"KUBERNETES_SERVICE_HOST": "10.0.0.1", core.ENV_MS_FUNCTIONAL_AREA: "user-management"}[k]
	}
	env.stdin = strings.NewReader("")
	require.Equal(t, 1, runBreakGlass(context.Background(), []string{"mfa-reset", "--email", "root@example.com"}, env))
	require.Contains(t, errOut.String(), "instance configuration")
	require.NotContains(t, out.String(), "New password")
}

func TestBreakGlassUsage(t *testing.T) {
	r := newBreakGlassRig(t)
	require.Equal(t, 2, r.run(t, ""))
	require.Equal(t, 2, r.run(t, "", "mfa-reset"))
	require.Equal(t, 2, r.run(t, "", "mfa-reset", "--email", "a@b.c", "extra"))
	require.Equal(t, 2, r.run(t, "", "something-else"))
}

// main hands `break-glass` to the subcommand and nothing else: a plain start is not handled, so
// the service starts as it always has, and the subcommand never starts a server.
func TestBreakGlassMainDispatches(t *testing.T) {
	r := newBreakGlassRig(t)
	r.operator(t, "root@example.com", "pw-root")

	handled, code := runSubcommand([]string{"user-management", "break-glass", "mfa-reset", "--email", "root@example.com"}, r.env(""))
	require.True(t, handled)
	require.Equal(t, 0, code, r.errOut.String())
	require.Equal(t, 1, r.opened, "the dispatch ran the store code")

	for _, args := range [][]string{{"user-management"}, {"user-management", "serve"}, {"user-management", "mfa-reset"}} {
		handled, _ := runSubcommand(args, r.env(""))
		require.False(t, handled, "%v must be a plain start", args)
	}
}
