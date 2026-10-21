// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"regexp"

	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/fatih/color"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// brokerXKeyValue is the State value that holds the curve key the BROKER APPLY is given
// (nats_callout_xkey_public). It is not the same as natsCalloutXKeyPublic, which is the key the
// instance document records for the responder: the two agree except while an upgrade is
// deliberately holding the broker back.
const brokerXKeyValue = "natsBrokerXKeyPublic"

// 🔴 THE ORDER THAT KEEPS DEVICES CONNECTING THROUGH THE UPGRADE.
//
// auth_callout cannot be hot-reloaded, so telling the broker to encrypt its callout is a rolling
// restart of the broker. The responder that reads those requests is device-management. A broker
// that starts encrypting while the running device-management cannot read it refuses every device
// connect until the services have rolled too, and `dcctl upgrade` rolls the broker BEFORE the
// services. That window is the whole of the broker roll plus the whole of the service rollout.
//
// So for an instance whose broker is not yet encrypting, the first apply leaves the broker's
// callout as it is, the services roll onto a responder that opens encrypted requests AND still
// answers plaintext ones, and only then is the broker applied again with the key. Each broker
// server restarts once, the responder understands both forms throughout, and the longest a
// device can be refused is the reconnect of the server it was on.

// brokerXKey matches the curve key in the broker's rendered configuration. The chart renders
// the account users and auth_callout as JSON objects, which is what makes a targeted match
// viable; being wrong about that is survivable (see DeployedBrokerXKey).
var brokerXKey = regexp.MustCompile(`"xkey":\s*"(X[A-Z2-7]{55})"`)

// DeployedBrokerXKey reads the curve key the RUNNING broker's configuration encrypts callout
// requests to, or "" when it has none or cannot be read.
//
// 🔑 EVERY FAILURE ANSWERS "" AND NO ERROR, and "" is the SAFE answer: it makes the caller defer
// the encryption to after the services have rolled, which converges on the same end state. The
// opposite error (believing the broker encrypts when it does not) would skip the deferral
// only if it returned a key, and it returns one only when it found that exact key in the
// configuration the broker is mounted with. It is a ConfigMap, not the process; the roll that
// adopts it is waited on by the apply.
func DeployedBrokerXKey(ctx context.Context, kubeContext, namespace, releaseName string) string {
	restCfg, err := RestConfig(kubeContext)
	if err != nil {
		return ""
	}
	typed, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return ""
	}
	return deployedBrokerXKeyIn(ctx, typed, namespace, releaseName)
}

// deployedBrokerXKeyIn is DeployedBrokerXKey against a given client.
func deployedBrokerXKeyIn(ctx context.Context, typed kubernetes.Interface, namespace, releaseName string) string {
	cm, err := typed.CoreV1().ConfigMaps(namespace).Get(ctx, releaseName+"-config", metav1.GetOptions{})
	if err != nil {
		return ""
	}
	for _, conf := range cm.Data {
		if m := brokerXKey.FindStringSubmatch(conf); m != nil {
			return m[1]
		}
	}
	return ""
}

// withholdXKeyFromBroker keeps the broker apply of THIS run from turning callout encryption on,
// when the broker is not encrypting yet and the services have not rolled onto a responder that
// can read it. It returns whether it withheld. After the services have rolled
// (st.XKeyServicesReady) it never withholds, which is how the second apply turns it on.
func withholdXKeyFromBroker(ctx context.Context, st *State) bool {
	pub := st.Values["natsCalloutXKeyPublic"]
	if pub == "" || st.XKeyServicesReady {
		return false
	}
	if lookupDeployedBrokerXKey(ctx, st.KubeContext, InstanceNamespace(st.Instance), natsStatefulSetName) == pub {
		return false
	}
	st.Values[brokerXKeyValue] = ""
	return true
}

// xkeyPending reports whether this run withheld the key from the broker and so owes it a second
// apply once the services have rolled.
func xkeyPending(st *State) bool {
	return st.Values["natsCalloutXKeyPublic"] != "" && st.Values[brokerXKeyValue] == "" && !st.XKeyServicesReady
}

// enableCalloutEncryption is the second broker apply: the services are running a responder that
// opens encrypted callout requests, so the broker is now told to send them. It is its own function
// so that Upgrade keeps one plan and one apply of its own, which the order tests read off its
// source; this is the same pair of calls, run after the services instead of before them.
func enableCalloutEncryption(ctx context.Context, st *State, typed kubernetes.Interface, dyn dynamic.Interface) error {
	fmt.Println(color.WhiteString("Turning on encryption of the broker's device auth callout:"))
	fmt.Println("  The services are rolled out and can read it; the broker's servers restart one at a time.")
	// Set before the plan: the settle step restores the broker's value from the credentials it reads
	// back, and this is what tells it not to hold the key back again.
	st.XKeyServicesReady = true
	var again *upgradeInfraPlan
	if err := runStreamed("Planning the broker with callout encryption on", "infrastructure plan", func() error {
		var perr error
		again, perr = planUpgradeInfra(ctx, st)
		return perr
	}); err != nil {
		return err
	}
	defer func() { _ = again.Close() }()
	again.say()
	if err := runStreamed("Applying the instance's message broker", "infrastructure", func() error {
		return applyUpgradeInfra(ctx, st, again, clusterInfraWaits(st, typed, dyn))
	}); err != nil {
		return fmt.Errorf("%w. The services already run a responder that reads both forms, so devices keep "+
			"connecting; re-run `dcctl upgrade` to finish turning the encryption on", err)
	}
	return nil
}

// introduceCalloutXKey settles the callout's curve key for an upgrade, from the seed the running
// instance holds (read from its callout Secret) or, when it has none, a freshly minted one.
//
// 🔑 THE ONE PLACE AN UPGRADE MINTS, and it is allowed to because the key is INTRODUCED: absent
// from the starting state, so nothing yet authenticates with it and minting replaces nothing.
// Every other credential in this verb is read back or the run is refused (readInstanceCredentials).
// A seed that is present but unusable is an error and not a reason to mint, so a corrupted Secret
// is named rather than overwritten.
//
// It runs whether or not the infrastructure is applied: the Secret and the document written by
// the services' release carry the key either way, and a `--skip-infrastructure` run must not
// leave a responder with a document that names a key its Secret lacks.
func introduceCalloutXKey(st *State) error {
	issuer := st.Values["natsCalloutIssuerSeed"]
	if issuer == "" {
		return nil // no device callout on this instance: nothing to encrypt
	}
	// The document of a release before the split records no issuer public key; it is derived
	// from the seed, which is what the broker was configured with in the first place.
	if st.Values["natsCalloutIssuerPublic"] == "" {
		pub, err := natsauth.IssuerPublicFromSeed(issuer)
		if err != nil {
			return err
		}
		st.Values["natsCalloutIssuerPublic"] = pub
	}
	creds, err := natsauth.Credentials{}.WithXKey(st.Values["natsCalloutXKeySeed"])
	if err != nil {
		return err
	}
	st.Values["natsCalloutXKeySeed"] = creds.XKeySeed
	st.Values["natsCalloutXKeyPublic"] = creds.XKeyPublic
	return nil
}
