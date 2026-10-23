// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/stretchr/testify/require"
)

// The shared broker login is retired by the upgrade that STARTS from an instance where every area
// already runs its own login, and by no other. These tests hold both directions: the run that must
// retire, and each starting state that must not.

// issuerOnly marks the run as one whose broker authenticates, and touches nothing else.
func issuerOnly(st *State) *State {
	st.Values["natsCalloutIssuerPublic"] = "AISSUERPUBLIC"
	return st
}

// retireFixture is an upgrade's starting point: the credentials of a freshly minted instance, with a
// running document that already says per-area logins, every login Secret present, every login in the
// broker's configuration, and the broker administrator's Job in the release.
type retireFixture struct {
	creds natsauth.Credentials
	// mutate adjusts the starting state before the run hydrates.
	deployedFlag  bool
	logins        map[string]string
	brokerHolds   []string // principals the broker's configuration already has a login for
	adminJob      bool
	adminJobError error
	skipInfra     bool
	sharedPw      string
	// startPhase is the phase the instance's declaration carried when the run started.
	startPhase string
}

func newRetireFixture(t *testing.T) retireFixture {
	t.Helper()
	creds, err := mintBrokerCredentials()
	require.NoError(t, err)
	f := retireFixture{creds: creds, deployedFlag: true, logins: loginPlaintexts(creds), adminJob: true, sharedPw: "shared-pw", startPhase: dcv1beta1.PhaseReady}
	f.brokerHolds = config.NatsLoginPrincipals()
	return f
}

// run hydrates an upgrade from the fixture and decides the shared login's phase.
func (f retireFixture) run(t *testing.T) *State {
	t.Helper()
	prevHashes, prevJob := lookupDeployedBrokerHashes, brokerAdminJobInRelease
	t.Cleanup(func() { lookupDeployedBrokerHashes, brokerAdminJobInRelease = prevHashes, prevJob })
	lookupDeployedBrokerHashes = func(context.Context, string, string, string) natsauth.DeployedHashes {
		h := natsauth.DeployedHashes{Logins: map[string]string{}}
		for _, p := range f.brokerHolds {
			h.Logins[natsauth.LoginUser(p)] = "$2a$hash"
		}
		return h
	}
	brokerAdminJobInRelease = func(context.Context, *State) (bool, error) { return f.adminJob, f.adminJobError }

	st := &State{Instance: testInstance, SkipInfrastructure: f.skipInfra, Values: map[string]string{}}
	d := &config.InstanceConfiguration{}
	d.Infrastructure.Nats.Auth.CalloutIssuerSeed = f.creds.IssuerSeed
	d.Infrastructure.Nats.Auth.Password = f.sharedPw
	d.Infrastructure.Nats.Auth.SysPassword = f.creds.SysPassword
	d.Infrastructure.Nats.Auth.AreaPasswords = f.logins
	d.Infrastructure.Nats.Auth.AreaLogins = f.deployedFlag
	require.NoError(t, applyDeployedInfrastructure(st, d))
	if f.sharedPw != "" {
		// What the broker's configuration holds for the shared login, so a retirement that forgot to
		// drop it would show.
		st.Values["natsServicePasswordBcrypt"] = "$2a$10$shared"
	}
	decl := &dcv1beta1.Instance{}
	if f.startPhase != "" {
		decl.Annotations = map[string]string{dcv1beta1.AnnotationPhase: f.startPhase}
	}
	noteStartPhase(st, decl)
	decideSharedLogin(t.Context(), st)
	return st
}

func TestAnUpgradeFromAFullyPerAreaInstanceRetiresTheSharedLogin(t *testing.T) {
	st := newRetireFixture(t).run(t)
	require.Equal(t, sharedLoginRetiring, st.Values[sharedLoginPhaseValue])
	require.Empty(t, st.Values[sharedLoginBlockedValue])
	require.Empty(t, st.Values["natsServicePassword"], "the run still carries the shared password")
	require.False(t, sharedLoginRetainedFor(st))
	require.NoError(t, checkSharedLoginHasSuccessors(st))

	// Everything downstream follows from the one value.
	require.NotContains(t, strings.Join(infraVars(issuerOnly(st)), "\n"), "nats_service_password_bcrypt")
	doc, err := BrokerPermissionsJSON(st.Instance, sharedLoginRetainedFor(st))
	require.NoError(t, err)
	require.NotContains(t, doc, natsauth.ServiceUser)
}

// The negative direction, one cause at a time: a starting state that is not "every area already on
// its own login, every principal minted, admin Job present, broker applied" keeps the login, and says
// why.
func TestTheSharedLoginIsNotRetiredFromAnyOtherStartingState(t *testing.T) {
	missingOne := func(t *testing.T) retireFixture {
		f := newRetireFixture(t)
		logins := map[string]string{}
		for p, pw := range f.logins {
			if p != "device-state" {
				logins[p] = pw
			}
		}
		f.logins = logins
		return f
	}
	for name, c := range map[string]struct {
		mutate func(*testing.T) retireFixture
		reason string
	}{
		"a login Secret is missing": {missingOne, "no login Secret"},
		"the broker lacks one login": {func(t *testing.T) retireFixture {
			f := newRetireFixture(t)
			f.brokerHolds = f.brokerHolds[1:]
			return f
		}, "does not hold a login for"},
		"the broker holds none": {func(t *testing.T) retireFixture {
			f := newRetireFixture(t)
			f.brokerHolds = nil
			return f
		}, "does not hold a login for"},
		"there is no broker administrator Job": {func(t *testing.T) retireFixture {
			f := newRetireFixture(t)
			f.adminJob = false
			return f
		}, "no broker administrator Job"},
		"the release cannot be read": {func(t *testing.T) retireFixture {
			f := newRetireFixture(t)
			f.adminJobError = errors.New("release not found")
			return f
		}, "release not found"},
		// The first upgrade wrote the per-area document and then its rollout failed: old pods may still
		// be serving on the shared login.
		"the last run did not end Ready": {func(t *testing.T) retireFixture {
			f := newRetireFixture(t)
			f.startPhase = dcv1beta1.PhaseFailed
			return f
		}, "did not end Ready (phase: Failed)"},
		"the last run is still marked as upgrading": {func(t *testing.T) retireFixture {
			f := newRetireFixture(t)
			f.startPhase = dcv1beta1.PhaseUpgrading
			return f
		}, "did not end Ready (phase: Upgrading)"},
		"no phase is recorded": {func(t *testing.T) retireFixture {
			f := newRetireFixture(t)
			f.startPhase = ""
			return f
		}, "did not end Ready (phase: none recorded)"},
		"the run applies nothing to the broker": {func(t *testing.T) retireFixture {
			f := newRetireFixture(t)
			f.skipInfra = true
			return f
		}, "--skip-infrastructure"},
	} {
		t.Run(name, func(t *testing.T) {
			st := c.mutate(t).run(t)
			require.Equal(t, sharedLoginRetained, st.Values[sharedLoginPhaseValue])
			require.Contains(t, st.Values[sharedLoginBlockedValue], c.reason)
			require.Equal(t, "shared-pw", st.Values["natsServicePassword"], "a blocked retirement must keep the shared login")
			require.True(t, sharedLoginRetainedFor(st))
			doc, err := BrokerPermissionsJSON(st.Instance, sharedLoginRetainedFor(st))
			require.NoError(t, err)
			require.Contains(t, doc, natsauth.ServiceUser)
		})
	}
}

// The upgrade that INTRODUCES the per-area logins never retires: the services have not rolled yet.
func TestTheUpgradeThatIntroducesTheLoginsKeepsTheSharedLogin(t *testing.T) {
	f := newRetireFixture(t)
	f.deployedFlag = false
	f.logins = nil
	f.brokerHolds = nil
	f.adminJob = false
	st := f.run(t)
	require.Equal(t, sharedLoginRetained, st.Values[sharedLoginPhaseValue])
	require.Empty(t, st.Values[sharedLoginBlockedValue], "phase 1 is not a blocked retirement")
	require.Equal(t, "shared-pw", st.Values["natsServicePassword"])
	require.True(t, areaLoginsIntroduced(st))
	// ...even when everything else is in place: the running document is what counts, not what is minted.
	g := newRetireFixture(t)
	g.deployedFlag = false
	require.Equal(t, sharedLoginRetained, g.run(t).Values[sharedLoginPhaseValue])
}

// Running the upgrade again after a retirement finishes nothing more and changes nothing: the
// instance it starts from has no shared login, which is the "retired" phase, not a second retirement
// and not an error.
func TestRetirementIsIdempotent(t *testing.T) {
	f := newRetireFixture(t)
	f.sharedPw = "" // the document the retiring run wrote carries no shared password
	st := f.run(t)
	require.Equal(t, sharedLoginRetired, st.Values[sharedLoginPhaseValue])
	require.Empty(t, st.Values["natsServicePassword"])
	require.NoError(t, checkSharedLoginHasSuccessors(st))

	// The credentials rebuild from that state keeps the broker as it is: no shared hash, the same
	// system login, and an error rather than a mint when a login is missing.
	creds, err := brokerCredentialsFromRunning(t.Context(), st, f.creds.IssuerSeed, "", "", loginsInValues(st), "", f.creds.SysPassword)
	require.NoError(t, err)
	require.Empty(t, creds.ServicePassword)
	require.Empty(t, creds.ServicePasswordBcrypt)
	setBrokerCredentialValues(st, creds)
	require.Empty(t, st.Values["natsServicePassword"])
	require.NotContains(t, strings.Join(infraVars(st), "\n"), "nats_service_password_bcrypt")
}

// A service must never lose its only credential: no shared password and a principal without a login
// is refused at the credentials, at the gate, and in the record.
func TestNoServiceLosesItsOnlyCredential(t *testing.T) {
	f := newRetireFixture(t)
	logins := loginPlaintexts(f.creds)
	delete(logins, "event-sources")

	_, err := brokerCredentialsFromRunning(t.Context(), &State{Instance: testInstance, Values: map[string]string{}},
		f.creds.IssuerSeed, "", "", logins, "", f.creds.SysPassword)
	require.ErrorContains(t, err, "event-sources")

	st := &State{Instance: testInstance, Values: map[string]string{"natsCalloutIssuerSeed": f.creds.IssuerSeed}}
	for p, pw := range logins {
		st.Values[natsLoginPasswordPrefix+p] = pw
	}
	err = checkSharedLoginHasSuccessors(st)
	require.ErrorContains(t, err, "event-sources")

	// ...and a complete set whose document would not tell the areas to use them is refused too.
	full := &State{Instance: testInstance, Values: map[string]string{"natsCalloutIssuerSeed": f.creds.IssuerSeed}}
	for p, pw := range loginPlaintexts(f.creds) {
		full.Values[natsLoginPasswordPrefix+p] = pw
	}
	require.ErrorContains(t, checkSharedLoginHasSuccessors(full), "would not tell")
	full.Values[areaLoginsEnabledValue] = "true"
	require.NoError(t, checkSharedLoginHasSuccessors(full))

	// A broker with no authentication has no logins to lose.
	require.NoError(t, checkSharedLoginHasSuccessors(&State{Values: map[string]string{}}))

	// The record is valid without the shared password only when it carries every login.
	dir := t.TempDir()
	prev := brokerRecordPathFor
	t.Cleanup(func() { brokerRecordPathFor = prev })
	brokerRecordPathFor = func(string) (string, error) { return dir + "/" + brokerRecordFile, nil }
	write := func(l map[string]string) {
		raw, err := json.Marshal(brokerRecord{Instance: "prod", IssuerSeed: f.creds.IssuerSeed, AreaLogins: l, SysPassword: "s"})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(dir+"/"+brokerRecordFile, raw, 0o600))
	}
	write(loginPlaintexts(f.creds))
	require.NotNil(t, readBrokerRecord("prod"), "a fresh install's record has no shared password")
	write(logins)
	require.Nil(t, readBrokerRecord("prod"), "a record with neither a shared password nor every login rebuilds nothing")
	write(nil)
	require.Nil(t, readBrokerRecord("prod"))
}

// A running instance that carries neither a shared password nor per-area logins is a state nothing
// can rebuild: the old guard still holds.
func TestAnInstanceWithNeitherCredentialFailsClosed(t *testing.T) {
	f := newRetireFixture(t)
	f.sharedPw = ""
	f.deployedFlag = false
	f.logins = nil
	st := f.run(t)
	// introduceLogins mints for the absent principals, but the gate still refuses a document that
	// would not tell the areas: the run never reaches a write.
	creds, err := brokerCredentialsFromRunning(t.Context(), &State{Instance: testInstance, Values: map[string]string{}},
		f.creds.IssuerSeed, "", "", nil, "", f.creds.SysPassword)
	require.Error(t, err)
	require.Empty(t, creds.IssuerSeed)
	_ = st
}

// A fresh install is "retired" by construction, and its instance document names no shared user.
func TestAFreshInstallNeverCarriesTheSharedLogin(t *testing.T) {
	st, creds := loginState(t)
	require.Empty(t, creds.ServicePassword)
	require.Empty(t, st.Values["natsServicePassword"])
	vals := helmValues(issuerOnly(st))
	auth := vals["instance"].(map[string]interface{})["config"].(map[string]interface{})["infrastructure"].(map[string]interface{})["nats"].(map[string]interface{})["auth"].(map[string]interface{})
	require.NotContains(t, auth, "user")
	require.NotContains(t, auth, "password")
	require.Equal(t, true, auth["areaLogins"])
	for _, v := range infraVars(issuerOnly(st)) {
		require.False(t, strings.HasPrefix(v, "nats_service_password_bcrypt"), v)
	}
	doc, err := BrokerPermissionsJSON(st.Instance, false)
	require.NoError(t, err)
	require.NotContains(t, doc, natsauth.ServiceUser)
}

// An instance that keeps the shared login still writes it everywhere it was written.
func TestARetainedSharedLoginIsStillRenderedEverywhere(t *testing.T) {
	st, _ := loginState(t)
	st.Values["natsServicePassword"] = "shared-pw"
	st.Values["natsServicePasswordBcrypt"] = "$2a$10$shared"
	vals := helmValues(issuerOnly(st))
	auth := vals["instance"].(map[string]interface{})["config"].(map[string]interface{})["infrastructure"].(map[string]interface{})["nats"].(map[string]interface{})["auth"].(map[string]interface{})
	require.Equal(t, natsauth.ServiceUser, auth["user"])
	require.Equal(t, "shared-pw", auth["password"])
	require.Contains(t, strings.Join(infraVars(issuerOnly(st)), "\n"), "nats_service_password_bcrypt=$2a$10$shared")
	doc, err := BrokerPermissionsJSON(st.Instance, sharedLoginRetainedFor(st))
	require.NoError(t, err)
	require.Contains(t, doc, natsauth.ServiceUser)
}

// The closing summary and the rehearsal say which phase ran.
func TestTheSummaryAndTheRehearsalSayWhichPhaseRan(t *testing.T) {
	retiring := newRetireFixture(t).run(t)
	out := captureStdout(t, func() { sayAboutSharedLogin(retiring); sayDryRunSharedLogin(retiring) })
	require.Contains(t, out, "retired")
	require.Contains(t, out, "not supported")

	blocked := newRetireFixture(t)
	blocked.adminJob = false
	out = captureStdout(t, func() { sayAboutSharedLogin(blocked.run(t)) })
	require.Contains(t, out, "NOT retired")
	require.Contains(t, out, "no broker administrator Job")

	intro := newRetireFixture(t)
	intro.deployedFlag = false
	out = captureStdout(t, func() { sayDryRunSharedLogin(intro.run(t)) })
	require.Contains(t, out, "phase 1 of 2")

	// An instance that is already retired has nothing to say.
	done := newRetireFixture(t)
	done.sharedPw = ""
	out = captureStdout(t, func() { sayAboutSharedLogin(done.run(t)); sayDryRunSharedLogin(done.run(t)) })
	require.Empty(t, strings.TrimSpace(out))
}
