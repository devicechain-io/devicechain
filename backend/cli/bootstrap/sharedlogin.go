// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/fatih/color"
	"helm.sh/helm/v3/pkg/action"
)

// The retirement of the shared broker login (`dc_service`).
//
// An instance built before the per-area logins ran every service as one login. The upgrade that
// introduces the per-area logins KEEPS that login, so pods that have not rolled yet stay connected
// (natsloginsecret.go). This file is the other half: the upgrade after it, which finds every area
// already running its own login, drops the shared one from the broker AND from the instance
// document, in one broker roll.
//
// 🔴 KEYED OFF THE STARTING STATE, NOT A RELEASE NUMBER. Whether to retire is a question about the
// instance this run starts from, so it works for any skip-release path, is finished by running
// `dcctl upgrade` twice, and is never asked of a fresh install (which never had the login).
//
// 🔴 THE SHARED PASSWORD IN THE RUN'S VALUES IS THE WHOLE STATE. While it is non-empty the broker
// keeps the login and the document names it; once it is empty neither does. Everything downstream
// (the document, the broker's users, the permissions document, the record) asks that one value, so
// the four cannot disagree about whether the login exists.

const (
	// sharedLoginPhaseValue says which phase this run is in; see the phases below.
	sharedLoginPhaseValue = "natsSharedLoginPhase"
	// sharedLoginBlockedValue holds, when the running instance was ready to retire the login and
	// something stopped it, what stopped it.
	sharedLoginBlockedValue = "natsSharedLoginBlocked"
	// loginsPresentAtStartValue records whether every principal's login Secret existed BEFORE this
	// run minted anything. A login this run mints is introduced by it; nothing runs on it yet.
	loginsPresentAtStartValue = "natsLoginsPresentAtStart"
	// startPhaseValue records the phase the instance's declaration carried when this run started:
	// what the LAST run over it ended in.
	startPhaseValue = "natsStartPhase"
)

// The phases of the shared login, as a run sees them.
const (
	// sharedLoginRetained: the login stays, because this run introduces the per-area logins (the
	// first upgrade) or because retirement is blocked (see sharedLoginBlockedValue).
	sharedLoginRetained = "retained"
	// sharedLoginRetiring: this run drops the login.
	sharedLoginRetiring = "retiring"
	// sharedLoginRetired: the instance already has none (a fresh install, or an earlier retirement).
	sharedLoginRetired = "retired"
)

// requireEveryPrincipalHasALogin is the fail-closed test for "no service would lose its only
// credential": it passes only when a password is recorded for every area that connects and for the
// broker administrator.
func requireEveryPrincipalHasALogin(logins map[string]string) error {
	var missing []string
	for _, p := range config.NatsLoginPrincipals() {
		if logins[p] == "" {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		them := "them"
		if len(missing) == 1 {
			them = "it"
		}
		return fmt.Errorf("no login of its own is recorded for %s, so removing the shared login would leave %s "+
			"with nothing to connect with", strings.Join(missing, ", "), them)
	}
	return nil
}

// brokerAdminJobInRelease reports whether the instance's RUNNING Helm release carries the broker
// administrator's hook Job: the thing that, from the next upgrade on, is the only writer of the
// broker's streams. A seam, because reading a release needs a cluster.
var brokerAdminJobInRelease = func(ctx context.Context, st *State) (bool, error) {
	cfg, err := helmActionConfigFor(st.KubeContext)
	if err != nil {
		return false, fmt.Errorf("reading the instance's Helm release: %w", err)
	}
	rel, err := action.NewGet(cfg).Run(helmReleaseNameFor(st.Instance))
	if err != nil {
		return false, fmt.Errorf("reading the instance's Helm release %q: %w", helmReleaseNameFor(st.Instance), err)
	}
	job, _, err := brokerAdminHookObjects(rel, st.Instance)
	return err == nil && job != nil, nil
}

// noteLoginsPresentAtStart records, before introduceLogins mints anything, whether the running
// instance already held a login Secret for every principal.
func noteLoginsPresentAtStart(st *State) {
	st.Values[loginsPresentAtStartValue] = strconv.FormatBool(
		requireEveryPrincipalHasALogin(loginsInValues(st)) == nil)
}

// noteStartPhase records, before this run stamps anything, the phase the last run over the
// instance ended in.
func noteStartPhase(st *State, inst *dcv1beta1.Instance) {
	phase := ""
	if inst != nil {
		phase = inst.Annotations[dcv1beta1.AnnotationPhase]
	}
	st.Values[startPhaseValue] = phase
}

// decideSharedLogin settles the shared login's phase for an UPGRADE, from the instance it starts
// from. It runs after the running instance's credentials have been read into the run (and before
// anything is written), and a run that retires the login blanks the shared password it carries.
//
// It retires the login only when ALL of these hold, and says which did not when it does not:
//
//   - the running document already tells the areas to use their own logins (areaLogins). An upgrade
//     that INTRODUCES them keeps the shared login for the services that have not rolled;
//   - every principal's login Secret existed before this run, so none is being minted now;
//   - the running broker already holds a login for every principal, which is what proves each area
//     can authenticate now rather than after this run writes it;
//   - the running release carries the broker administrator's Job, so the streams stay reconcilable
//     without the shared login;
//   - the last run over the instance ended Ready. 🔴 THE DOCUMENT SAYING areaLogins IS A FACT ABOUT
//     THE DOCUMENT, NOT ABOUT THE PODS: a first upgrade whose service rollout failed or timed out
//     wrote that document and left Deployments with their OLD pods still serving, on the shared
//     login they started with. Retiring under them cuts those areas off at the broker apply, before
//     this run's own rollout replaces them, and if the new pods still cannot start they never come
//     back. A Ready stamp is written only after every Deployment has fully rolled (waitForAreas), so
//     it is what proves no pod is still on the old document;
//   - this run applies the broker. A --skip-infrastructure run changes nothing broker-side, and a
//     document that dropped the login while the broker kept it would be a half retirement.
func decideSharedLogin(ctx context.Context, st *State) {
	delete(st.Values, sharedLoginBlockedValue)
	if st.Values["natsCalloutIssuerSeed"] == "" {
		delete(st.Values, sharedLoginPhaseValue)
		return // a broker with no authentication has no logins at all
	}
	if st.Values["natsServicePassword"] == "" {
		st.Values[sharedLoginPhaseValue] = sharedLoginRetired
		return
	}
	if st.Values[areaLoginsDeployedValue] != "true" {
		st.Values[sharedLoginPhaseValue] = sharedLoginRetained
		return
	}

	var blocked []string
	if st.SkipInfrastructure {
		blocked = append(blocked, "--skip-infrastructure applies nothing to the broker")
	}
	if st.Values[loginsPresentAtStartValue] != "true" {
		blocked = append(blocked, "an area or the broker administrator had no login Secret, so this run minted it")
	}
	if p := st.Values[startPhaseValue]; p != dcv1beta1.PhaseReady {
		if p == "" {
			p = "none recorded"
		}
		blocked = append(blocked, fmt.Sprintf("the last run over this instance did not end Ready (phase: %s), so a "+
			"service may still be running pods that started on the shared login", p))
	}
	hashes := lookupDeployedBrokerHashes(ctx, st.KubeContext, InstanceNamespace(st.Instance), natsStatefulSetName)
	for _, p := range config.NatsLoginPrincipals() {
		held := false
		for user := range hashes.Logins {
			if config.IsNatsLoginOf(user, p) {
				held = true
				break
			}
		}
		if !held {
			blocked = append(blocked, "the broker does not hold a login for "+p)
		}
	}
	// Asked last, and only when nothing else has already decided: it reads the Helm release.
	if len(blocked) == 0 {
		present, err := brokerAdminJobInRelease(ctx, st)
		switch {
		case err != nil:
			blocked = append(blocked, err.Error())
		case !present:
			blocked = append(blocked, "the running release has no broker administrator Job")
		}
	}

	if len(blocked) > 0 {
		st.Values[sharedLoginPhaseValue] = sharedLoginRetained
		st.Values[sharedLoginBlockedValue] = strings.Join(blocked, "; ")
		return
	}
	st.Values[sharedLoginPhaseValue] = sharedLoginRetiring
	st.Values["natsServicePassword"] = ""
	st.Values["natsServicePasswordBcrypt"] = ""
}

// sharedLoginRetainedFor says whether the broker and the document of this run still carry the
// shared login.
func sharedLoginRetainedFor(st *State) bool {
	return st.Values["natsServicePassword"] != ""
}

// checkSharedLoginHasSuccessors is the last fail-closed gate, asked before a run writes anything the
// areas read: a run without the shared login must tell every area to use its own.
func checkSharedLoginHasSuccessors(st *State) error {
	if st.Values["natsCalloutIssuerSeed"] == "" || sharedLoginRetainedFor(st) {
		return nil
	}
	if err := requireEveryPrincipalHasALogin(loginsInValues(st)); err != nil {
		return fmt.Errorf("the shared broker login is gone from this run and %w", err)
	}
	if st.Values[areaLoginsEnabledValue] != "true" {
		return fmt.Errorf("the shared broker login is gone from this run and the instance document would not tell " +
			"the areas to use their own logins, so every area would start with no credential")
	}
	return nil
}

// sayAboutSharedLogin is what the closing summary says about the shared login: which phase ran,
// and, when retirement was ready and blocked, what blocked it.
func sayAboutSharedLogin(st *State) {
	switch st.Values[sharedLoginPhaseValue] {
	case sharedLoginRetiring:
		fmt.Println(color.WhiteString(
			"\nThe shared message broker login is retired. Every area was already connecting with a login of\n" +
				"its own, so the broker no longer has the shared login and the instance document no longer names it.\n" +
				"Rolling back across this release is not supported from here: re-run the newer `dcctl upgrade`."))
	case sharedLoginRetained:
		if r := st.Values[sharedLoginBlockedValue]; r != "" {
			fmt.Println(color.YellowString(
				"\nThe shared message broker login was NOT retired: %s.\n"+
					"Run `dcctl upgrade` again once that is resolved to finish the retirement.", r))
			return
		}
		// Phase 1 of 2 is said by sayAreaLoginsIntroduced, beside what it introduced.
	}
}

// sayDryRunSharedLogin is what a rehearsed upgrade says it would do about the shared login.
func sayDryRunSharedLogin(st *State) {
	switch st.Values[sharedLoginPhaseValue] {
	case sharedLoginRetiring:
		wouldDo("retire the shared message broker login: every area already connects with a login of its own, " +
			"so the broker and the instance configuration document stop carrying the shared one (one broker roll)")
	case sharedLoginRetained:
		if r := st.Values[sharedLoginBlockedValue]; r != "" {
			wouldDo("keep the shared message broker login, because " + r)
		} else if areaLoginsIntroduced(st) {
			wouldDo("give every area a message broker login of its own and keep the shared login for now " +
				"(phase 1 of 2; the next upgrade retires it)")
		}
	}
}
