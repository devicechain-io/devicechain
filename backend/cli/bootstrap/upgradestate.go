// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
	"k8s.io/client-go/kubernetes"
)

// hydrateUpgradeState rebuilds the State an upgrade acts on, entirely from what the
// cluster already holds.
//
// 🔴 FOUR SOURCES, AND WHICH ONE ANSWERS WHICH QUESTION IS NOT INTERCHANGEABLE:
//
//   - THE DECLARATION says what this instance IS — profile, topology, exposure, the
//     areas it runs. It is the only source that records intent rather than
//     consequence, which is why an upgrade reads shape from it and not from the
//     objects that shape produced.
//   - THE CONFIG DOCUMENT holds the credentials the SERVICES are running on: the
//     broker's authority and logins, the cross-service secret, the secret-store root
//     key. Recomposing it is the job; reading it is how the recomposition keeps
//     meaning the same instance.
//   - THE dcctl-OWNED SECRETS hold what the INFRASTRUCTURE was built from — the
//     database owner passwords above all, which the document also carries but which
//     the Secret is authoritative for. See reuseMintedCredential.
//   - THE PREVIOUS RELEASE holds the handful of values that came out of an
//     infrastructure apply this verb does not run. See carryForwardFromRelease.
//
// 🔴 AND THE ORDER MATTERS FOR ONE REASON: the declaration is read first because a
// missing one is the cheapest, clearest refusal available. Reading credentials first
// would report a missing Secret on an instance that does not exist.
func hydrateUpgradeState(
	ctx context.Context,
	typed kubernetes.Interface,
	provider Provider,
	binding ClusterBinding,
	opts UpgradeOptions,
) (*State, error) {
	st := &State{
		Instance:    opts.Instance,
		KubeContext: binding.KubeContext,
		Binding:     binding,
		Provider:    provider.Name(),
		DryRun:      opts.DryRun,
		Values:      map[string]string{},
	}

	// 1. THE DECLARATION.
	inst, err := ReadInstanceCR(ctx, binding.KubeContext, opts.Instance)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		// 🔴 NOT "SO BOOTSTRAP IT". An upgrade is asked for by an operator who
		// believes they have an instance, and the useful answer says which of the two
		// things went wrong — wrong name, or wrong cluster — rather than offering to
		// build something.
		return nil, fmt.Errorf(
			"there is no instance %q declared in this cluster, so there is nothing to upgrade. "+
				"Check the name, and check that %q is the cluster you mean — `dcctl instances "+
				"list` shows what is declared where",
			opts.Instance, binding.KubeContext)
	}
	applyDeclaration(st, inst.Spec)
	st.GrafanaSSO = inst.Spec.GrafanaSSO
	st.InstanceUID = string(inst.GetUID())
	if st.InstanceUID == "" {
		// The same refusal the bootstrap path makes, for the same reason: every
		// Secret this run touches is matched on the declaration's UID, and an empty
		// one matches a Secret written with no UID at all.
		return nil, fmt.Errorf(
			"the declaration for instance %q carries no UID, so the Secrets this upgrade has "+
				"to read cannot be matched to it", opts.Instance)
	}

	// 🔴 RE-DERIVED, NOT COPIED. The declaration records the area DELTA — what
	// --enable-area asked for on top of the profile — and applyDeclaration writes
	// only that. A State carrying the extras with EnabledAreas empty makes helmValues
	// emit `profile` and drop every extra area, so an upgrade would quietly undeploy
	// exactly the areas an operator added on purpose.
	if st.EnabledAreas, err = ResolveEnabledAreas(st.Profile, st.EnableAreas); err != nil {
		return nil, fmt.Errorf("the declaration for instance %q names an area set this dcctl "+
			"cannot deploy: %w", opts.Instance, err)
	}

	// The image source. The declaration records what the instance is running; the
	// flags say what it is moving to, and an absent flag means "stay".
	st.ImageRegistry, st.ImageVersion = inst.Spec.ImageRegistry, inst.Spec.ImageVersion
	if opts.ImageRegistry != "" {
		st.ImageRegistry = opts.ImageRegistry
	}
	if opts.ImageVersion != "" {
		st.ImageVersion = opts.ImageVersion
	}

	// 2. THE CONFIG DOCUMENT.
	deployed, err := lookupDeployedInstance(ctx, binding.KubeContext, opts.Instance)
	if err != nil {
		return nil, fmt.Errorf("reading the configuration instance %q is running on: %w", opts.Instance, err)
	}
	if deployed == nil {
		// 🔴 A DECLARATION WITH NO DOCUMENT IS A BOOTSTRAP THAT DIED BEFORE ITS
		// SEVENTH STEP. The declaration lands at step 4 and the document at step 7,
		// so this state is reachable and it is not an upgrade's to repair: the
		// credentials the half-built instance is holding were never written down
		// anywhere this verb reads.
		return nil, fmt.Errorf(
			"instance %q is declared but has no configuration document, so it was never "+
				"finished — there is no running instance here to upgrade. Complete the bootstrap, "+
				"or destroy the instance and build it again", opts.Instance)
	}
	applyDeployedInfrastructure(st, deployed)

	// 3. THE SECRETS.
	if st.Credentials, err = readInstanceCredentials(ctx, typed, st); err != nil {
		return nil, err
	}

	return st, nil
}

// applyDeployedInfrastructure threads the credentials the running services hold back
// into the values the document is recomposed from.
//
// 🔴 EVERY ONE OF THESE IS A VALUE SOMETHING ELSE IS ALREADY AUTHENTICATING WITH, and
// the failure mode of dropping one is uniform and quiet: helmValues simply omits the
// block, the recomposed document loses a credential it used to carry, and the pods
// roll onto a configuration that cannot reach the broker, cannot call another service,
// or cannot decrypt a single stored secret. Nothing errors — the document is valid,
// it is just missing something.
//
// The system-account password is the sharp one: its ABSENCE is the off switch for the
// broker-presence tap, so a value that fails to come across here does not fail, it
// silently turns a feature off.
func applyDeployedInfrastructure(st *State, deployed *config.InstanceConfiguration) {
	nats := deployed.Infrastructure.Nats
	if nats.Tls.Enabled {
		st.Values["natsTlsEnabled"] = "true"
		st.Values["natsCA"] = nats.Tls.Ca
	}
	st.Values["natsCalloutIssuerSeed"] = nats.Auth.CalloutIssuerSeed
	st.Values["natsServicePassword"] = nats.Auth.Password
	st.Values["natsSysPassword"] = nats.Auth.SysPassword
	st.Values["serviceAuthSecret"] = deployed.Infrastructure.ServiceAuth.Secret
	st.Values["secretsRootKey"] = deployed.Infrastructure.Secrets.RootKey
}
