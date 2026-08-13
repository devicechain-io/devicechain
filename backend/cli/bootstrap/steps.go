// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	apply "github.com/devicechain-io/dc-k8s/apply"
	dck8s "github.com/devicechain-io/dc-k8s/config"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/fatih/color"
	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// defaultProfile is used when the user does not specify one: the standard system —
// the whole device/telemetry/automation platform, and the right default for a
// complete instance. It deliberately is NOT "full": full additionally ships the areas
// that reach outside the instance (AI inference, outbound connectors, MCP), each of
// which carries a decision an operator should make rather than inherit — a paid
// provider key, an egress surface, an agent-facing API. Pass --profile full to get
// everything. (The chart schema accepts: default, full, telemetry, ingest-only.)
const defaultProfile = "default"

// Local developer build-path conventions (mirrors deploy/local). The registry
// is reachable from the host as ImageRegistry (localhost:<port>) and from the
// cluster nodes via the same name on the kind docker network.
const (
	registryContainerName = "kind-registry"
	kindNetwork           = "kind"
	// operatorImageName must match the name the release pipeline publishes the
	// operator under — ghcr.io/devicechain-io/operator (see .github/workflows/
	// release.yml, which special-cases backend/k8s to ".../operator"). A
	// mismatch makes the published-image bootstrap pull a nonexistent image.
	operatorImageName = "operator"
)

// doing prints a white "doing…" progress line (house style).
func doing(msg string) {
	fmt.Print(color.WhiteString("%s... ", msg))
}

// done prints a green "done." after a doing() line.
func done() {
	fmt.Println(color.GreenString("done."))
}

// fail prints a red "failed." after a doing() line and wraps the error.
func fail(what string, err error) error {
	fmt.Println(color.RedString("failed."))
	return fmt.Errorf("%s: %w", what, err)
}

// wouldDo prints what a step WOULD do under --dry-run.
func wouldDo(msg string) {
	fmt.Println(color.YellowString("[dry-run] would %s", msg))
}

// stepRenderConfig generates per-instance values into State. This step actually
// populates state (namespace, generated DB password, resolved profile) so later
// steps and the report have something concrete to consume.
func stepRenderConfig(ctx context.Context, st *State) error {
	if st.Values == nil {
		st.Values = map[string]string{}
	}

	// Default the profile when empty so downstream config is deterministic.
	if st.Profile == "" {
		st.Profile = defaultProfile
	}

	// The chart deploys every per-instance workload into a namespace named after
	// the instance id (templates/namespace.yaml uses .Values.instance.id), so the
	// readiness gate and report must target exactly that.
	namespace := st.Instance

	doing(fmt.Sprintf("rendering config for instance %q (profile %q)", st.Instance, st.Profile))

	// Collected and printed once the step's progress line is closed. See done() below.
	var notes []string

	// ASK THE CLUSTER WHAT THIS INSTANCE IS ALREADY RUNNING, ONCE, BEFORE MINTING
	// ANYTHING.
	//
	// Every credential below is generated, and a re-run that generates a fresh one
	// does not "update" a live instance — it rotates a secret out from under it. So
	// the one question that governs all of them is asked here, in one place: is there
	// an instance, and what is it holding? A credential added to this step later
	// should reuse `deployed` the same way, which is the point of hoisting it.
	//
	// Skipped under --dry-run, which deploys nothing and so cannot damage anything;
	// it mints throwaway values purely so the rendered plan is complete.
	//
	// Note this is a lookup, not a guard: a nil result means "fresh install, mint
	// everything", and an ERROR means we could not tell — which fails the run rather
	// than guessing, because guessing wrong is the destructive direction. See
	// DeployedInstanceConfig.
	var (
		deployed *config.InstanceConfiguration
		err      error
	)
	if !st.DryRun {
		deployed, err = lookupDeployedInstance(ctx, st.KubeContext, st.Instance)
		if err != nil {
			return fail("checking for an existing instance", err)
		}
	}

	// SETTLE THE ARCHIVE PATH EACH DATABASE OWNS, FROM WHAT IT IS ALREADY ARCHIVING
	// UNDER — not from this run's flags (ADR-020 A2.5 / ADR-028).
	//
	// Same shape, same reason as the credentials above: the path is permanent
	// configuration and --restore-* is a one-shot flag, so deriving it from the flag
	// would let an ordinary flagless re-run retarget a LIVE cluster's WAL archive at
	// a path holding no base backup — silently, on a green run. See
	// resolveArchivePaths.
	//
	// 🔴 DONE UNDER --dry-run TOO, unlike the credential lookup above. That one is
	// skipped because a dry run mints throwaway values it will never deploy; this
	// one is a READ, and a dry run that skips it predicts the OPPOSITE of what the
	// real run does. Both consumers were wrong without it: the "this restore will
	// not run" warning — the single most valuable thing a rehearsal could surface —
	// never fired, and a flagless dry run against an already-restored instance
	// showed its archive path being REMOVED, a plan alarming enough to invite an
	// operator to "fix" it.
	//
	// Best-effort here and fail-closed on a real run, which is the one asymmetry
	// that is right: a dry run is often aimed at a cluster that does not exist yet,
	// and failing it on an unreachable API server would break the rehearsal for the
	// case it serves best. Acting on a wrong answer costs nothing when nothing is
	// applied.
	var live liveArchiveState
	live, err = readLiveArchiveState(ctx, st.KubeContext)
	switch {
	case err != nil && !st.DryRun:
		return fail("reading the database archive state", err)
	case err != nil:
		live = liveArchiveState{}
		notes = append(notes, fmt.Sprintf(
			"could not read the database archive state (%v); the plan below assumes a fresh cluster", err))
	}
	paths := resolveArchivePaths(live, st.Restore, time.Now().UTC())
	st.Values["backupServerNameRdb"] = paths.Rdb
	st.Values["backupServerNameTsdb"] = paths.Tsdb
	if st.Restore.Active() {
		for _, p := range []struct{ store, from, path string }{
			{"relational store", st.Restore.RdbFrom, paths.Rdb},
			{"event store", st.Restore.TsdbFrom, paths.Tsdb},
		} {
			if p.from != "" {
				notes = append(notes, fmt.Sprintf(
					"%s recovering from archive %q, and will archive under %q",
					p.store, p.from, p.path))
			}
		}
	}
	// 🔴 A restore aimed at a store that already exists does NOTHING — CloudNativePG
	// reads `spec.bootstrap` when it CREATES a Cluster. The apply is green, the
	// operator is told a restore was requested, and no data moves. Say it here, out
	// loud, because from the outside "it restored" and "it declined to restore" look
	// identical.
	for _, name := range paths.AlreadyLive {
		fmt.Println(color.YellowString(
			"  Cluster %s already exists, so its restore will NOT run: CloudNativePG reads "+
				"spec.bootstrap only when it creates a cluster. Destroy the instance and rebuild "+
				"it with the same flags to actually recover.", name))
	}

	// The NATS broker-auth credentials (ADR-025): the callout issuer nkey and the
	// shared service password. These can't be generated declaratively (nkeys aren't a
	// TF primitive), so dcctl mints them here and threads the public issuer + bcrypt
	// password hash into the NATS config (applyInfra) and the seed + plaintext
	// password into the services' instance config (helmInstall) — one mint, both
	// sides, so the hash and the plaintext it verifies can't drift.
	//
	// A RE-RUN MUST NOT MINT. The two sides of that pair are updated by different
	// mechanisms with different timing — the broker picks up its ConfigMap when its
	// reloader sidecar sees the projected file change, the services get theirs when
	// Helm rolls the pods — so fresh credentials mean an interval in which the broker
	// enforces one password and the pods present another. Every pod that starts in
	// that interval dies on `nats: Authorization Violation`, which is enough to fail
	// the Helm step of the very run that caused it, and enough to leave device
	// connects rejected while the callout responder signs with a seed the broker no
	// longer trusts. It is silent, delayed, and detached from its cause.
	// Read once. The dry-run branch below reports on the same value the switch consulted,
	// rather than asking again — two reads of one file is two chances to disagree.
	localRecord := readDeployedBrokerRecord(st.Instance)

	var creds natsauth.Credentials
	switch {
	case deployed != nil:
		// The hashes the broker is ALREADY configured with, so reuse means reuse:
		// bcrypt salts, so re-hashing an unchanged password writes a different string
		// and — now that the broker adopts its config by rolling on a checksum change
		// — that would restart the broker on every idempotent re-run. Best-effort by
		// design: an empty result costs a fresh hash, and a supplied one is used only
		// if it verifies against the plaintext below.
		creds, err = natsauth.CredentialsFromDeployed(
			deployed.Infrastructure.Nats.Auth.CalloutIssuerSeed,
			deployed.Infrastructure.Nats.Auth.Password,
			deployed.Infrastructure.Nats.Auth.SysPassword,
			lookupDeployedBrokerHashes(ctx, st.KubeContext, natsStatefulSetName))
		if err != nil {
			return fail("reusing the running instance's NATS auth credentials", err)
		}
		notes = append(notes, "NATS broker credentials reused from the running instance")
	default:
		// NO DEPLOYED INSTANCE — WHICH IS NOT THE SAME AS NO DEPLOYED BROKER. The
		// lookup above reads the instance chart's config, written in step 5, while the
		// BROKER is configured in step 3. A run that died between them left a live
		// broker whose credentials this branch would happily rotate out from under it,
		// permanently, because nothing the cluster still holds can be turned back into
		// the seed and plaintexts. broker_record.go is the bridge, and this is the only
		// place it is consulted: the deployed instance config stays authoritative
		// whenever it exists, because it is what the running SERVICES are actually
		// holding, and a disagreement between the two means services are already failing
		// to connect — exactly when preferring the config is what lets a re-run repair.
		//
		// The record cannot make this branch fail. A missing, torn or foreign record
		// reads as absent and mints, which is what this branch did before it existed.
		if localRecord != nil && !st.DryRun {
			// 🔴 A RECORD THAT DOES NOT WORK IS A RECORD THAT IS NOT THERE. readBrokerRecord
			// screens shape, not cryptography: a hand-edited file can carry a non-empty seed
			// that is not a valid nkey, and CredentialsFromDeployed rejects it on the CRC.
			// Returning that error would be a bootstrap that fails at step 1 where it used to
			// succeed — the standing objection to adding this read at all — so it falls
			// through to the mint below, loudly. Post-adoption a wrong mint converges on the
			// broker's next roll; a refused run does not converge on anything.
			reused, rerr := natsauth.CredentialsFromDeployed(
				localRecord.IssuerSeed, localRecord.ServicePassword, localRecord.SysPassword,
				lookupDeployedBrokerHashes(ctx, st.KubeContext, natsStatefulSetName))
			if rerr == nil {
				creds = reused
				notes = append(notes, "NATS broker credentials reused from this machine's bootstrap record")
				break
			}
			notes = append(notes, fmt.Sprintf(
				"the bootstrap record for this instance is unusable (%v); minting fresh broker "+
					"credentials, which a running broker would be reconfigured with", rerr))
		}
		creds, err = natsauth.GenerateCredentials()
		if err != nil {
			return fail("minting NATS auth credentials", err)
		}
	}
	st.Values["natsCalloutIssuerPublic"] = creds.IssuerPublic
	st.Values["natsCalloutIssuerSeed"] = creds.IssuerSeed
	st.Values["natsServicePassword"] = creds.ServicePassword
	st.Values["natsServicePasswordBcrypt"] = creds.ServicePasswordBcrypt
	st.Values["natsSysPassword"] = creds.SysPassword
	st.Values["natsSysPasswordBcrypt"] = creds.SysPasswordBcrypt

	// RECORD THEM BEFORE ANYTHING CONSUMES THEM — the same placement, and the same
	// reasoning, as the root-key escrow write further down: the value of the artifact is
	// that it exists for every broker that exists, so a run that cannot write one must not
	// go on to configure a broker whose credentials have no second copy.
	//
	// Rewritten on every real run, including a reuse, so the record can never drift from
	// the instance config. That makes it a no-op write in the healthy case and a repair
	// when a stale record survived a rebuild.
	//
	// 🔴 NEVER UNDER --dry-run. A dry run skips the cluster lookup and mints throwaway
	// values; writing them here would overwrite the only bridge copy of a live
	// half-bootstrapped broker's credentials with garbage — worse than the defect this
	// record exists to fix. It reads the record (above) and reports, which is what a
	// rehearsal owes the operator.
	//
	// Failing the run on a write error is deliberate and costs nothing: the directory is
	// the one OpenTofu is about to write its state into, so a run that cannot write here
	// was going to die at step 3 anyway, later and with a worse message.
	if st.DryRun {
		// 🔑 REPORTED, NOT USED, AND WORDED FOR WHAT A REHEARSAL CAN ACTUALLY KNOW. A dry run
		// skips the deployed-instance lookup, so it cannot tell which of the two sources a
		// real run would take — and saying "reused from the record" over a healthy instance
		// would name the wrong one. The values it renders are throwaway either way, so the
		// record is not consulted for them at all; its existence is the useful fact.
		if localRecord != nil {
			notes = append(notes, "a bootstrap record for this instance exists on this machine; "+
				"a real run would reuse it if the instance itself is not yet deployed")
		}
	} else if err := storeBrokerRecord(st.Instance, brokerRecord{
		Instance:        st.Instance,
		Written:         time.Now().UTC(),
		IssuerSeed:      creds.IssuerSeed,
		ServicePassword: creds.ServicePassword,
		SysPassword:     creds.SysPassword,
	}); err != nil {
		return fail("recording the broker credentials before the broker is configured", err)
	}

	// The shared service secret (ADR-044 amendment) backing the synchronous
	// cross-service call primitive: a caller presents it to user-management's mint
	// endpoint for a short-lived service token. Threaded into every service's
	// instance config (helmInstall); user-management compares the presented value
	// against its copy. One secret, all services — the same trusted-boundary
	// tradeoff the NATS service credential above makes.
	//
	// Reused on a re-run for the same reason, with a narrower blast radius: every
	// service reads it from the one Secret and Helm rolls them together, so the
	// exposure is the rollout itself — a caller that has already restarted presenting
	// the new secret to a user-management that has not. That is a spell of failing
	// cross-service calls, not a broken instance, but it is still a rotation nobody
	// asked for.
	serviceSecret := ""
	if deployed != nil {
		serviceSecret = deployed.Infrastructure.ServiceAuth.Secret
	}
	if serviceSecret == "" {
		serviceSecret, err = randomSecret(32)
		if err != nil {
			return fail("minting service auth secret", err)
		}
	}
	st.Values["serviceAuthSecret"] = serviceSecret

	// Establish the instance secret-store root key (ADR-059): a base64-encoded
	// 256-bit KEK that wraps every per-secret DEK, threaded into every service's
	// instance config (helmInstall). One key for the whole instance.
	//
	// Normally minted fresh here and escrowed to a file the operator keeps, because
	// the K8s Secret this ends up in lives in etcd and etcd is in no backup the
	// platform takes — see escrow.go for what that costs at restore time. On the
	// recovery path the key instead comes FROM an escrow artifact, so that a fresh
	// cluster can read a restored database rather than rehydrating ciphertext
	// nothing alive can open.
	//
	// The three cases are decided here rather than anywhere downstream, because two
	// of them are irreversible and one of them used to be silent.
	secretsRootKey := st.Escrow.RestoredRootKey
	reused := false
	switch {
	case secretsRootKey != "":
		// Recovery: the key comes from the artifact — and the rule the default
		// branch below states applies here WORD FOR WORD. helmInstall upgrades an
		// existing release with this value, so aiming a restore at an instance that
		// is already alive under a different key rewrites its KEK and makes every
		// secret it holds permanently undecryptable, silently, on a run that reports
		// success. That is the same defect this switch was written to close; it was
		// closed on the re-run path and left open on this one.
		//
		// A genuine recovery runs where the instance does not exist at all, so this
		// costs nothing. Where it does exist, only an artifact carrying the key it is
		// already running is a no-op, and that case is worth allowing: re-running a
		// restore is exactly as ordinary as re-running a bootstrap.
		if err := refuseRestoreOverADifferentKey(st, deployed, secretsRootKey); err != nil {
			return fail("restoring the root key", err)
		}
		notes = append(notes, fmt.Sprintf("root key restored from %s", st.Escrow.RestoredFrom))

	case deployed != nil && deployed.Infrastructure.Secrets.RootKey != "":
		// A RE-RUN MUST NOT MINT. helmInstall upgrades an existing release with
		// whatever is in this value, so minting here would rewrite the KEK of a live
		// instance and make every secret it has stored permanently undecryptable —
		// silently, on a run that reports success, of a pipeline that documents itself
		// as idempotent. The lookup that answers this failed closed above, so reaching
		// here on a live instance without its key is not possible by accident.
		secretsRootKey = deployed.Infrastructure.Secrets.RootKey
		reused = true
		notes = append(notes, "root key reused from the running instance")

	default:
		secretsRootKey, err = randomKeyBase64(32)
		if err != nil {
			return fail("minting secrets root key", err)
		}
	}
	st.Values["secretsRootKey"] = secretsRootKey

	// Escrow it BEFORE it is used for anything. The whole value of the artifact is
	// that it exists for every instance that exists, so a run that cannot write one
	// must not go on to build an instance around a key with no second copy — the
	// resulting cluster would look perfectly healthy and be unrecoverable.
	if st.Escrow.Path != "" {
		switch {
		case st.DryRun:
			// A dry run must predict the same outcome the real run produces, so it
			// checks the destination rather than assuming it is free. Reporting a clean
			// plan for a run that dies at step 1 is the defect this whole line exists
			// to avoid.
			if _, statErr := os.Stat(st.Escrow.Path); statErr == nil {
				notes = append(notes, fmt.Sprintf(
					"an escrow artifact already exists at %s; a real run would stop here", st.Escrow.Path))
			} else {
				notes = append(notes, "would write the root-key escrow to "+st.Escrow.Path)
			}
		case reused:
			outcome, recErr := ReconcileEscrow(st.Escrow, secretsRootKey, st.Instance, time.Now().UTC())
			if recErr != nil {
				return fail("reconciling the root-key escrow", recErr)
			}
			notes = append(notes, "root-key escrow "+outcome)
		default:
			if err := WriteEscrow(st.Escrow, secretsRootKey, st.Instance, time.Now().UTC()); err != nil {
				return fail("escrowing the secret-store root key", err)
			}
		}
	}

	// Mint the Grafana OAuth client secret (ADR-047 SSO) when SSO is wired: one mint,
	// both sides — the cleartext goes to Grafana's generic_oauth config (the monitoring
	// tofu module) and the bcrypt hash is seeded into user-management (helmInstall), so
	// the two can't drift. Skipped (with a note) when SSO was requested but the issuer
	// would be invalid — http on a non-localhost host.
	//
	// THIS IS THE ONE GENERATED CREDENTIAL ABOVE THAT A RE-RUN STILL ROTATES, and it
	// is left that way on purpose rather than overlooked. The reuse the rest of this
	// step does works because the value is in DeviceChain's own instance config, which
	// dcctl can read back; this one is not. The cleartext goes only into the Grafana
	// subchart's envRenderSecret and user-management stores only its hash, so
	// recovering it would mean depending on an upstream chart's secret-naming
	// convention — a worse failure mode than what it fixes. The blast radius is also
	// different in kind: both halves are written by the same run, so a rotation costs
	// a window of failing logins during the rollout rather than a broker that rejects
	// every service. Closing it properly means giving the secret a home dcctl owns,
	// which is a design change, not a reuse. Tracked on the roadmap with this slice.
	if st.GrafanaSSO && st.NoMonitoring {
		fmt.Println(color.YellowString("  Grafana SSO skipped: it needs the monitoring stack, which --no-monitoring disables."))
	}
	if grafanaSSORequestedButInvalid(st) {
		fmt.Println(color.YellowString("  Grafana SSO skipped: an http issuer needs a localhost host. Re-run with --host localhost (and --no-tls) or enable TLS."))
	}
	if grafanaSSOEnabled(st) {
		secret, err := randomSecret(32)
		if err != nil {
			return fail("minting Grafana OAuth secret", err)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
		if err != nil {
			return fail("hashing Grafana OAuth secret", err)
		}
		st.Values["grafanaOAuthSecret"] = secret
		st.Values["grafanaOAuthSecretBcrypt"] = string(hash)
	}

	// Resolve the image source. Default to published images at a pinned version;
	// the developer path builds from source into a local registry instead.
	if st.ImageRegistry == "" {
		if st.BuildImages {
			st.ImageRegistry = LocalRegistry
		} else {
			st.ImageRegistry = DefaultImageRegistry
		}
	}
	if st.ImageVersion == "" {
		if st.BuildImages {
			st.ImageVersion = "dev"
		} else {
			st.ImageVersion = DefaultImageVersion
		}
	}
	// Deploying an image tag that was never published fails as an
	// ImagePullBackOff on every workload, several minutes into a run that looked
	// healthy — so reject it here, where we can say why.
	if !st.BuildImages && IsUnpublishedImageVersion(st.ImageVersion) {
		return fail("resolving image source", fmt.Errorf(
			"this dcctl build has no pinned image version (%q is not a published tag); deploy a tagged release with --version <tag>, or build from source with --build",
			st.ImageVersion))
	}

	imageSource := fmt.Sprintf("%s/<area>:%s (published)", st.ImageRegistry, st.ImageVersion)
	if st.BuildImages {
		imageSource = fmt.Sprintf("built from source → %s/<area>:%s", st.ImageRegistry, st.ImageVersion)
	}

	st.Values["instance"] = st.Instance
	st.Values["namespace"] = namespace
	// Profile label. When --enable-area added areas the deployment is an explicit set,
	// so name the base profile and the additive delta rather than a bare (possibly
	// empty) profile — otherwise a run that deployed lwm2m-ingest would report only
	// "default" and hide it.
	profileLabel := st.Profile
	if len(st.EnableAreas) > 0 {
		base := st.Profile
		if base == "" {
			base = "default"
		}
		profileLabel = fmt.Sprintf("%s (+ %s)", base, strings.Join(st.EnableAreas, ", "))
	}
	st.Values["profile"] = profileLabel

	// Ingress host + scheme. Default to DefaultIngressHost; a loopback host
	// (localhost/127.0.0.1) needs no /etc/hosts edit. TLS is on unless --no-tls,
	// which serves plain HTTP — a self-signed cert adds friction with no benefit
	// on localhost.
	host := st.IngressHost
	if host == "" {
		host = DefaultIngressHost
	}
	st.Values["ingressHost"] = host
	scheme := "https"
	if st.NoTLS {
		scheme = "http"
	}
	st.Values["scheme"] = scheme
	// NOTE: there is deliberately no generated database password here. One used to be
	// minted and stashed in Values on every run, and nothing ever read it — the
	// relational Postgres takes its credentials from the OpenTofu `postgres_password`
	// variable, which is stable across applies. It was removed rather than wired up:
	// it looked like a rotating credential in the audit of this step (which is how it
	// was found) while being dead weight, and a value nothing consumes is not the
	// place to introduce database credential management.
	st.Values["imageRegistry"] = st.ImageRegistry
	st.Values["imageVersion"] = st.ImageVersion
	st.Values["imageSource"] = imageSource
	done()
	// Printed AFTER done(), because doing() leaves its line unterminated and anything
	// written before the matching done() lands in the middle of it.
	for _, n := range notes {
		fmt.Printf("  %s\n", color.WhiteString(n))
	}
	return nil
}

// stepLocalRegistry ensures a local registry exists and builds+pushes images
// from source for the developer build path. End users pull published images, so
// this is a no-op unless BuildImages is set.
func stepLocalRegistry(ctx context.Context, st *State) error {
	if !st.BuildImages {
		doing("local image registry")
		fmt.Println(color.GreenString("not needed (using published images)."))
		return nil
	}

	if st.DryRun {
		doing("ensuring local registry + building images from source")
		fmt.Println()
		wouldDo(fmt.Sprintf("provision a registry at %s, wire it to the kind network, and ko-build+push images at tag %q",
			st.ImageRegistry, st.ImageVersion))
		return nil
	}

	root, err := repoRoot()
	if err != nil {
		return err
	}

	doing("ensuring local image registry")
	if err := ensureLocalRegistry(ctx, st); err != nil {
		return fail("provisioning local registry", err)
	}
	done()

	doing("building + pushing images from source (ko)")
	if err := buildImages(ctx, root, st); err != nil {
		return fail("building images", err)
	}
	done()
	return nil
}

// ensureLocalRegistry starts the registry:2 container (if not running), connects
// it to the kind network so cluster nodes can pull by reference, and advertises
// it to the cluster via the KEP-1755 ConfigMap.
func ensureLocalRegistry(ctx context.Context, st *State) error {
	port := registryPort(st.ImageRegistry)

	running, _ := outputOf(ctx, "docker", "inspect", "-f", "{{.State.Running}}", registryContainerName)
	if strings.TrimSpace(running) != "true" {
		if err := run(ctx, "docker", "run", "-d", "--restart=always",
			"-p", fmt.Sprintf("127.0.0.1:%s:5000", port), "--name", registryContainerName, "registry:2"); err != nil {
			return err
		}
	}
	// Idempotent: ignore "already connected" / "endpoint already exists".
	_ = run(ctx, "docker", "network", "connect", kindNetwork, registryContainerName)

	// KEP-1755 ConfigMap so tooling in the cluster can discover the registry.
	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "local-registry-hosting", Namespace: "kube-public"},
		Data: map[string]string{
			"localRegistryHosting.v1": fmt.Sprintf("host: \"localhost:%s\"\nhelp: \"https://kind.sigs.k8s.io/docs/user/local-registry/\"\n", port),
		},
	}
	_, err = typed.CoreV1().ConfigMaps("kube-public").Create(ctx, cm, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = typed.CoreV1().ConfigMaps("kube-public").Update(ctx, cm, metav1.UpdateOptions{})
	}
	return err
}

// buildImages ko-builds every service (backend/services/*/main.go) and the
// operator, pushing to ImageRegistry at ImageVersion. --bare names each image
// exactly REGISTRY/<area>:TAG, matching what the chart and operator deploy pull.
func buildImages(ctx context.Context, root string, st *State) error {
	koConfig := filepath.Join(root, ".ko.yaml")
	build := func(moduleDir, imageName string) error {
		cmd := exec.CommandContext(ctx, "ko", "build", "--bare",
			"--tags", st.ImageVersion, "--platform", "linux/amd64", "./")
		cmd.Dir = moduleDir
		cmd.Env = append(os.Environ(),
			"KO_DOCKER_REPO="+imageName,
			"KO_CONFIG_PATH="+koConfig)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ko build %s: %w\n%s", imageName, err, out)
		}
		return nil
	}

	servicesDir := filepath.Join(root, "backend", "services")
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		moduleDir := filepath.Join(servicesDir, e.Name())
		if _, err := os.Stat(filepath.Join(moduleDir, "main.go")); err != nil {
			continue
		}
		if err := build(moduleDir, fmt.Sprintf("%s/%s", st.ImageRegistry, e.Name())); err != nil {
			return err
		}
	}
	if err := build(filepath.Join(root, "backend", "k8s"),
		fmt.Sprintf("%s/%s", st.ImageRegistry, operatorImageName)); err != nil {
		return err
	}

	// The web console is a static nginx SPA, not a Go service, so ko can't build
	// it — docker build from frontend/Dockerfile and push to the same registry
	// the chart resolves ({registry}/frontend:{tag}).
	return buildFrontend(ctx, root, st)
}

// buildFrontend builds the web console image from frontend/Dockerfile and pushes
// it to the local registry at the same registry/tag the services use, so the
// chart's default frontend image (registry/frontend:tag) resolves.
func buildFrontend(ctx context.Context, root string, st *State) error {
	image := fmt.Sprintf("%s/frontend:%s", st.ImageRegistry, st.ImageVersion)
	frontendDir := filepath.Join(root, "frontend")
	if err := run(ctx, "docker", "build", "-t", image, frontendDir); err != nil {
		return err
	}
	return run(ctx, "docker", "push", image)
}

// removeLocalRegistry force-removes the shared local registry container. Used by
// destroy --purge-registry; best-effort (a missing container is fine).
func removeLocalRegistry(ctx context.Context) error {
	return run(ctx, "docker", "rm", "-f", registryContainerName)
}

// stepInfraApply deploys the shared data/infra stack via OpenTofu, driving the
// tofu/terraform binary through terraform-exec.
func stepInfraApply(ctx context.Context, st *State) error {
	if err := checkHaNodeCapacity(ctx, st); err != nil {
		return err
	}
	// An immutable field on an EXISTING StatefulSet, so this must be answered
	// before the apply that would trip it, not after.
	if err := checkJetStreamVolumeIsUpgradable(ctx, st); err != nil {
		return err
	}
	if st.DryRun {
		doing("applying infrastructure stack (OpenTofu)")
		fmt.Println()
		monitoring := "monitoring (Prometheus/Grafana)"
		if st.NoMonitoring {
			monitoring = "monitoring SKIPPED (--no-monitoring)"
		}
		wouldDo("tofu init+apply deploy/opentofu (NATS, Postgres, Timescale, ingress, cert-manager, " + monitoring + ")")
		return nil
	}
	return runStreamed("applying infrastructure stack (OpenTofu)", "infrastructure stack",
		func() error { return applyInfra(ctx, st) })
}

// stepInstallCore renders the operator overlay (CRDs + RBAC + controller) the
// same way `make deploy` does and applies it via client-go server-side apply.
// The manifests are rendered in-process from manifests embedded in the binary —
// no source checkout or kubectl/kustomize binary required.
func stepInstallCore(ctx context.Context, st *State) error {
	operatorImage := fmt.Sprintf("%s/%s:%s", st.ImageRegistry, operatorImageName, st.ImageVersion)
	doing("installing core components (CRDs + operator)")
	if st.DryRun {
		fmt.Println()
		wouldDo("render the operator overlay and apply CRDs/RBAC/operator (" + operatorImage + ") to " + st.KubeContext)
		return nil
	}

	manifests, err := dck8s.RenderOperator(operatorImage)
	if err != nil {
		return fail("rendering operator manifests", err)
	}
	dyn, disco, _, err := kubeClients(st.KubeContext)
	if err != nil {
		return fail("building kube clients", err)
	}
	if err := apply.NewApplyOptions(dyn, disco).WithServerSide(true).Apply(ctx, manifests); err != nil {
		return fail("applying operator manifests", err)
	}
	done()
	return nil
}

// stepHelmInstall installs (or upgrades) the per-instance chart via the Helm Go
// SDK, blocking until the rendered workloads are ready.
func stepHelmInstall(ctx context.Context, st *State) error {
	// The broker that was actually provisioned must be able to host the replica
	// factor this install is about to configure. Checked here — after the infra
	// apply, before the chart — because that is the only point where BOTH halves of
	// the HA toggle are known: the applied server count is an output of the step
	// before, and the replica factor is a value of the step after.
	if err := checkBrokerHostsReplication(st); err != nil {
		return err
	}
	if st.DryRun {
		doing("installing instance chart (Helm)")
		fmt.Println()
		wouldDo("helm upgrade --install the devicechain chart into namespace " + st.Values["namespace"])
		return nil
	}
	return runStreamed("installing instance chart (Helm)", "instance chart",
		func() error { return helmInstall(ctx, st) })
}

// runStreamed frames a step whose underlying tool (tofu, helm) streams its own
// progress to stdout: it prints a heading, runs the work, then prints a final
// indented status line so the noisy sub-tool output is bracketed cleanly.
func runStreamed(heading, label string, work func() error) error {
	fmt.Println(color.WhiteString("%s:", heading))
	if err := work(); err != nil {
		return fail(label, err)
	}
	fmt.Print("  ")
	doing(label)
	done()
	return nil
}

// stepSeedAdmin surfaces the bootstrap superuser credential. user-management
// seeds a global superuser identity automatically on first start (ADR-033); this
// step records the coordinates so the final report can show them.
func stepSeedAdmin(ctx context.Context, st *State) error {
	doing("recording bootstrap superuser credential")
	// These mirror user-management's config defaults (config.ApplyDefaults): a
	// global superuser identity with a well-known initial password that MUST be
	// changed. The bootstrap is tenant-less (ADR-033) — the superuser signs in to
	// the admin console and creates the first tenant there. A future enhancement
	// can read a generated password from the chart values / a secret.
	st.Values["superuserEmail"] = "superuser@devicechain.local"
	st.Values["superuserPassword"] = "devicechain"
	done()
	return nil
}

// stepWaitReady waits for every per-instance workload to report ready. The Helm
// step already blocks on readiness; this is an explicit confirmation gate.
func stepWaitReady(ctx context.Context, st *State) error {
	doing("waiting for areas to become ready")
	if st.DryRun {
		fmt.Println()
		wouldDo("poll each area's deployment until available")
		return nil
	}
	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return fail("building kube clients", err)
	}
	ns := st.Values["namespace"]
	deadline := time.Now().Add(5 * time.Minute)
	for {
		deps, err := typed.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return fail("listing deployments", err)
		}
		ready, total := 0, len(deps.Items)
		for _, d := range deps.Items {
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			if d.Status.AvailableReplicas >= want {
				ready++
			}
		}
		if total > 0 && ready == total {
			fmt.Println(color.GreenString("done (%d/%d ready).", ready, total))
			return nil
		}
		if time.Now().After(deadline) {
			fmt.Println(color.RedString("timed out (%d/%d ready).", ready, total))
			return fmt.Errorf("not all areas became ready in namespace %q (%d/%d)", ns, ready, total)
		}
		time.Sleep(3 * time.Second)
	}
}

// stepReport prints an access-info summary from State. This step is real.
func stepReport(ctx context.Context, st *State) error {
	fmt.Println(color.HiGreenString("\nDeviceChain bootstrap summary"))
	fmt.Printf("  %s %s\n", color.WhiteString("Instance:"), color.GreenString(st.Values["instance"]))
	fmt.Printf("  %s %s\n", color.WhiteString("Namespace:"), color.GreenString(st.Values["namespace"]))
	fmt.Printf("  %s %s\n", color.WhiteString("Profile:"), color.GreenString(st.Values["profile"]))
	// Print the resolved footprint next to the profile, so the sizing a run applied
	// is visible without reading the chart. Only when compact — the default sizing
	// is the one the docs describe.
	if st.Compact {
		fmt.Printf("  %s %s\n", color.WhiteString("Footprint:"), color.GreenString(compact.summary()))
	}
	// The messaging topology, and only when it is the replicated one. Printed from
	// the SAME haTopology both tools rendered from, so the summary cannot describe a
	// topology the instance does not have — and printed at all because "is this
	// instance actually HA?" is otherwise a question answered by reading a tofu
	// variable and a Helm value in two different places.
	if ha := haFor(st.HA); ha.Replicated() {
		fmt.Printf("  %s %s\n", color.WhiteString("Messaging HA:"), color.GreenString(ha.summary()))
	}
	fmt.Printf("  %s %s\n", color.WhiteString("Images:"), color.GreenString(st.Values["imageSource"]))
	fmt.Printf("  %s %s\n", color.WhiteString("Kube context:"), color.GreenString(st.KubeContext))
	if host := st.Values["ingressHost"]; host != "" {
		scheme := st.Values["scheme"]
		fmt.Printf("  %s %s\n", color.WhiteString("Console:"), color.GreenString("%s://%s/", scheme, host))
		fmt.Printf("  %s %s\n", color.WhiteString("GraphQL:"), color.GreenString("%s://%s/api/<area>/graphql", scheme, host))
	}
	if st.Values["superuserEmail"] != "" {
		fmt.Printf("  %s %s / %s  %s\n",
			color.WhiteString("Superuser:"),
			color.GreenString(st.Values["superuserEmail"]),
			color.GreenString(st.Values["superuserPassword"]),
			color.YellowString("(sign in to the admin console to create your first tenant — change this password immediately)"))
	}
	if svc := st.Values["grafanaService"]; svc != "" {
		if grafanaSSOEnabled(st) {
			u := grafanaSSOURLsFor(st)
			fmt.Printf("  %s %s\n",
				color.WhiteString("Grafana:"),
				color.GreenString("%s  (sign in with DeviceChain SSO — operators/superusers only)", u.RootURL))
			fmt.Printf("           %s\n", color.YellowString("cross-tenant metrics are operator-tier only; the native admin login stays available as break-glass"))
		} else {
			ns := st.Values["grafanaNamespace"]
			fmt.Printf("  %s %s\n",
				color.WhiteString("Grafana:"),
				color.GreenString("kubectl -n %s port-forward svc/%s 3000:80  → http://localhost:3000/  (admin / devicechain)", ns, svc))
			fmt.Printf("           %s\n", color.YellowString("dev-grade default password — override monitoring_grafana_admin_password, or enable SSO with --grafana-sso (ADR-047)"))
		}
	}
	// Database backups, printed here for exactly the reason the escrow line below
	// is: this is the screen an operator actually reads, and every branch of it
	// says something they need.
	//
	// 🔴 UNCONDITIONAL, and the branch that matters most is the DEFAULT one. The
	// in-cluster destination is real backups — WAL archived continuously, a base
	// backup taken on schedule — and it is not disaster recovery, because it dies
	// with the cluster it lives in. Those two facts are easy to hold at once and
	// almost impossible to infer, so the only honest thing is to say both.
	//
	// Before this existed, the only surfaces stating it were the OpenTofu README
	// and terraform.tfvars.example. A `dcctl bootstrap` user reads neither.
	switch {
	case st.Values[databaseBackupsKey] != "true":
		fmt.Printf("  %s %s\n",
			color.WhiteString("Backups:"),
			color.YellowString("NONE — this instance archives no WAL and takes no base backups, so it cannot be restored to any point in time"))
	case st.Values[databaseBackupOffsiteKey] == "true":
		fmt.Printf("  %s %s\n",
			color.WhiteString("Backups:"),
			color.GreenString("WAL archiving + scheduled base backups, to storage outside this cluster"))
	default:
		fmt.Printf("  %s %s\n",
			color.WhiteString("Backups:"),
			color.GreenString("WAL archiving + scheduled base backups, to an object store IN THIS CLUSTER"))
		fmt.Printf("           %s\n",
			color.YellowString("this is point-in-time recovery, NOT disaster recovery — the backups share the cluster's"))
		fmt.Printf("           %s\n",
			color.YellowString("failure domain and are lost with it. Set backup_destination=\"external\" for off-site."))
	}
	// The archive path each store OWNS — which is the INPUT to the next restore.
	//
	// Printed only when it is not the default, because that is exactly when an
	// operator cannot work it out: after a recovery the paths are stamped names
	// nobody chose, and `--restore-rdb-from dc-rdb` — the obvious guess — points at
	// the archive of the instance that already died. Alongside it, the escrow line
	// below completes the pair a restore actually needs.
	if st.Values[databaseBackupsKey] == "true" {
		for _, p := range []struct{ label, path string }{
			{"Relational archive:", st.Values["backupServerNameRdb"]},
			{"Event archive:", st.Values["backupServerNameTsdb"]},
		} {
			if p.path != "" {
				fmt.Printf("  %s %s\n", color.WhiteString(p.label), color.GreenString(p.path))
			}
		}
	}
	// The root-key escrow, printed here because this is the screen an operator
	// actually reads. Every branch says something: the file to back up, the artifact
	// a restore came from, or — for --no-escrow — that this instance's secrets die
	// with its cluster. The last one is the whole reason the line is unconditional.
	//
	// Keyed on the PLAN, not on a value the write step leaves behind. Keying it on
	// the side effect is what the first version did, and under --dry-run — where
	// there is no side effect — the summary announced "none (--no-escrow)" for a run
	// that had a passphrase, a path and every intention of escrowing. A line whose
	// whole job is to tell an operator whether their key has a second copy must not
	// be able to say no when the answer is yes. A failed write aborts the pipeline
	// before this ever prints, so the plan is also the truth.
	switch {
	case st.Escrow.Path != "":
		what := st.Escrow.Path
		if st.DryRun {
			what += "  (dry run — not written)"
		}
		fmt.Printf("  %s %s\n", color.WhiteString("Root-key escrow:"), color.GreenString(what))
		fmt.Printf("                   %s\n", color.YellowString(
			"copy this off the machine and store the passphrase separately — it is the ONLY copy of the key outside the cluster, and no database backup contains it"))
	case st.Escrow.RestoredFrom != "":
		fmt.Printf("  %s %s\n", color.WhiteString("Root-key escrow:"),
			color.GreenString("restored from %s (that artifact remains the second copy)", st.Escrow.RestoredFrom))
	default:
		fmt.Printf("  %s %s\n", color.WhiteString("Root-key escrow:"), color.RedString("none (--no-escrow)"))
		fmt.Printf("                   %s\n", color.YellowString(
			"this instance's root key exists only inside the cluster; if the cluster is lost, every stored secret is permanently unreadable even with a database backup"))
	}
	if !st.DryRun {
		host := st.Values["ingressHost"]
		scheme := st.Values["scheme"]
		fmt.Println(color.HiGreenString("\nDeviceChain is up. Open the web console at %s://%s/ and sign in.", scheme, host))
		// A non-loopback host on a local cluster won't resolve, so a non-dev needs
		// the one-time hosts mapping spelled out. localhost/127.0.0.1 need nothing.
		if looksLocal(st.KubeContext) && !isLoopbackHost(host) {
			fmt.Println(color.YellowString("  Local cluster: map the host first — add \"127.0.0.1 %s\" to /etc/hosts (or bootstrap with --host localhost).", host))
		}
		// A self-signed cert (TLS on) makes the browser warn once.
		if scheme == "https" {
			fmt.Println(color.YellowString("  The TLS cert is self-signed (dev-grade), so your browser will warn once (or use --no-tls)."))
		}
	}
	return nil
}

// isLoopbackHost reports whether a host resolves to the local machine with no
// /etc/hosts entry.
func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1"
}

// randomSecret returns a hex-encoded random string with the given byte length
// of entropy. Uses crypto/rand so generated credentials are not predictable.
func randomSecret(nbytes int) (string, error) {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// randomKeyBase64 returns a base64-encoded random key of the given byte length.
// Base64 (not hex) because the secret-store root key is consumed as base64 by the
// instance config (SecretsConfiguration.DecodedRootKey), and 32 bytes → a 256-bit
// AES key. Uses crypto/rand so the KEK is not predictable.
func randomKeyBase64(nbytes int) (string, error) {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// refuseRestoreOverADifferentKey stops a recovery from overwriting the root key of
// an instance that is already alive under a different one.
//
// The destructive case is not exotic: point --restore-root-key at the wrong
// artifact, or at the right artifact but the wrong instance name, and the helm
// upgrade rewrites the KEK of a running instance. Every secret it holds becomes
// permanently unreadable, with no error at the time. There is no undo, so this
// fails closed on anything it cannot establish — including not being able to tell
// whether the instance exists, since "could not tell" plus "overwrite" is the one
// combination with no recovery.
//
// Keys are compared DECODED. Both sides are base64 the platform produced, but a
// string compare would treat a difference in padding or encoding as a different
// key and refuse a legitimate re-run; comparing bytes asks the question actually
// being asked.
// deployed is what the caller's single lookup already returned: nil when the
// instance is not there, and nil under --dry-run, which deploys nothing and so has
// nothing to put at risk (and must not require a reachable cluster).
func refuseRestoreOverADifferentKey(st *State, deployed *config.InstanceConfiguration, restoredKeyBase64 string) error {
	if deployed == nil {
		return nil // the ordinary recovery: the instance is not there yet
	}
	existing := deployed.Infrastructure.Secrets.RootKey
	if existing == "" {
		return nil
	}

	existingKey, err := base64.StdEncoding.DecodeString(existing)
	if err != nil {
		return fmt.Errorf("instance %q is running a root key that is not valid base64, so it cannot be "+
			"compared with the artifact's: %w", st.Instance, err)
	}
	restoredKey, err := base64.StdEncoding.DecodeString(restoredKeyBase64)
	if err != nil {
		return fmt.Errorf("the artifact's root key is not valid base64: %w", err)
	}
	if subtle.ConstantTimeCompare(existingKey, restoredKey) == 1 {
		return nil // re-running a restore, which is as ordinary as re-running a bootstrap
	}

	return fmt.Errorf("instance %q is already running a DIFFERENT secret-store root key.\n"+
		"Restoring %s over it would rewrite the KEK and make every secret that instance has "+
		"stored permanently unreadable — connector credentials, SMTP passwords, AI provider keys — "+
		"with no error at the time and no way back.\n"+
		"If this IS the instance you meant to recover, it is already up: destroy it first "+
		"(dcctl destroy %s) and bootstrap again from the artifact. If it is not, recover under a "+
		"different instance name",
		st.Instance, st.Escrow.RestoredFrom, st.Instance)
}

// registryPort extracts the port from an ImageRegistry like "localhost:5000".
// Defaults to 5000 when no port is present.
func registryPort(registry string) string {
	host := registry
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[i+1:]
	}
	return "5000"
}

// run executes a command, returning a combined-output error on failure.
func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

// outputOf runs a command and returns its stdout (trimmed of nothing), ignoring
// the combined error for callers that only care about the value.
func outputOf(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}
