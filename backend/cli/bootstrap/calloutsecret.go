// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The device auth callout's SEEDS, in a Secret of their own.
//
// 🔴 WHY A SECRET OF ITS OWN. The instance configuration is one document mounted into every
// pod, and until now it carried the callout's issuer seed. Any pod that can read it can sign a
// grant for any device on any tenant, and (with the callout encrypted) the curve seed in the same
// place would let that pod open every device's CONNECT credentials. The seeds are read by
// exactly one service, device-management, so exactly one Deployment mounts this Secret
// (templates/deployment.yaml). The PUBLIC halves are not secret and stay in the document.
//
// 🔑 THE XKEY IS AN INTRODUCED CREDENTIAL. Every other broker credential is already load-bearing
// when an instance is evolved, so replacing one breaks it while reporting success; `dcctl upgrade`
// therefore mints nothing. The xkey is absent from an existing instance's starting state, nothing
// authenticates with it until the broker is told, and so minting it replaces nothing. It is
// minted once, by bootstrap or by the first upgrade that finds none, recorded next to the other
// broker credentials before the broker is configured, and reused from then on.

const (
	// calloutSecretKeyIssuer and calloutSecretKeyXKey are the keys the chart's secretKeyRefs
	// read. Restated in deploy/helm/devicechain/templates/deployment.yaml and held against it by
	// TestTheChartReadsTheCalloutSecretDcctlWrites.
	calloutSecretKeyIssuer = "issuerSeed"
	calloutSecretKeyXKey   = "xkeySeed"
)

// calloutSecretName is the Secret the chart projects into device-management by default
// (devicechain.calloutSecret in templates/_helpers.tpl).
func calloutSecretName(instance string) string {
	return "dci-" + instance + "-callout"
}

// calloutSecret is the instance's callout-seed Secret, or false when there is nothing to put in
// it (a broker with no callout).
func calloutSecret(st *State) (ownedSecret, bool) {
	issuer := st.Values["natsCalloutIssuerSeed"]
	if issuer == "" {
		return ownedSecret{}, false
	}
	data := map[string]string{calloutSecretKeyIssuer: issuer}
	if xkey := st.Values["natsCalloutXKeySeed"]; xkey != "" {
		data[calloutSecretKeyXKey] = xkey
	}
	return ownedSecret{
		Name:      calloutSecretName(st.Instance),
		Namespace: InstanceNamespace(st.Instance),
		Type:      corev1.SecretTypeOpaque,
		Labels: map[string]string{
			"app.kubernetes.io/component": "device-management",
		},
		Data: data,
	}, true
}

// writeCalloutSecret writes the seeds the services of this run will find. It runs before the
// release so the pods can project the Secret at container start, like the instance document.
func writeCalloutSecret(ctx context.Context, typed kubernetes.Interface, st *State) error {
	spec, ok := calloutSecret(st)
	if !ok {
		return nil
	}
	return writeOwnedSecret(ctx, typed, instanceOwner(st.Instance, st.InstanceUID), spec, time.Now)
}

// readCalloutSeeds reads the seeds a running instance holds, from the Secret in namespace.
// An absent Secret yields empty strings and no error: an instance built before the seeds had
// a Secret has none, and its issuer seed is still in the document.
//
// 🔴 IT READS BY NAME AND NOT BY OWNER, the way DeployedInstanceConfig reads the document it
// sits beside, because the question is "what is this instance running on". A Secret that is
// present but unreadable, or present with an empty issuer seed, is an ERROR: a caller that took
// it for "no seed" would mint one over a broker that is already trusting the real one.
func readCalloutSeeds(ctx context.Context, typed kubernetes.Interface, namespace, instance string) (issuer, xkey string, err error) {
	name := calloutSecretName(instance)
	s, err := typed.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return "", "", nil
	case err != nil:
		return "", "", fmt.Errorf("reading the callout seeds from Secret %s/%s: %w. Refusing to continue: "+
			"treating an unreadable Secret as absent would mint new seeds over a broker that trusts the old ones",
			namespace, name, err)
	}
	issuer = string(s.Data[calloutSecretKeyIssuer])
	if issuer == "" {
		return "", "", fmt.Errorf("Secret %s/%s exists but holds no %q, so the key the broker trusts for device "+
			"grants cannot be recovered from it. Minting a replacement would leave the broker refusing every "+
			"device", namespace, name, calloutSecretKeyIssuer)
	}
	return issuer, string(s.Data[calloutSecretKeyXKey]), nil
}
