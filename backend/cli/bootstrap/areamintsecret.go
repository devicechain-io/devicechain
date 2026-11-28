// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The mint credential of an AREA-BOUND area (auth.AreaBoundCallers), in a Secret of its own.
//
// An area-bound area obtains service tokens only by presenting this credential to user-management's
// area-bound endpoint, which derives the token's subject from it and enforces the area's ceiling on
// every token minted that way. Its client never uses the shared service secret — but its pod still
// mounts the instance document, which carries it (helmValues writes infrastructure.serviceAuth into
// every area's document), until the area-bound areas are given a restricted one. The credential
// lives where the per-area broker logins live: a Secret named for the area,
// mounted by that area's Deployment and by user-management (which checks it), and by nothing else
// (templates/deployment.yaml). It is never a key in the instance document, which every pod mounts,
// and never a Helm value.
//
// 🔑 IT IS MINTED ONCE AND NEVER REPLACED HERE. bootstrap, enable-area and upgrade all pass through
// helmInstall, which calls ensureAreaMintSecrets before the release: a Secret that is absent is
// minted; one that is present is left exactly as it is. A present Secret with no usable credential is
// an error, not an invitation to mint over it — the operator who broke it decides.

const (
	// areaMintCredentialKey is the key the chart's volume projects (templates/deployment.yaml) and the
	// file both sides read (auth.AreaMintCredentialKey).
	areaMintCredentialKey = auth.AreaMintCredentialKey

	// areaMintCredentialBytes is the entropy of a minted credential.
	areaMintCredentialBytes = 32
)

// areaMintSecretName is the Secret the chart mounts for an area-bound area by default
// (devicechain.areaMintSecret in templates/_helpers.tpl).
func areaMintSecretName(instance, area string) string {
	return "dci-" + instance + "-mint-" + area
}

// areaMintAreas is the area-bound areas this run deploys, in the order the run lists its areas.
func areaMintAreas(st *State) ([]string, error) {
	areas, err := instanceAreas(st)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range areas {
		if isAreaBound(string(a)) {
			out = append(out, string(a))
		}
	}
	return out, nil
}

// isAreaBound is auth.IsAreaBoundCaller. A variable only so a test can stand an area the catalog
// already deploys in for one that is not in it yet; nothing else assigns it.
var isAreaBound = auth.IsAreaBoundCaller

// newAreaMintCredential returns a fresh credential: 32 random bytes, base64url without padding.
// A variable only so a test can make a mint fail.
var newAreaMintCredential = func() (string, error) {
	b := make([]byte, areaMintCredentialBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("minting an area credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ensureAreaMintSecrets makes sure every area-bound area this run deploys has its credential Secret.
// It runs before the release, beside the other per-area Secrets, so the pods find it at container
// start.
func ensureAreaMintSecrets(ctx context.Context, typed kubernetes.Interface, st *State) error {
	areas, err := areaMintAreas(st)
	if err != nil {
		return err
	}
	ns := InstanceNamespace(st.Instance)
	for _, area := range areas {
		name := areaMintSecretName(st.Instance, area)
		s, err := typed.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
			if len(s.Data[areaMintCredentialKey]) < auth.MinAreaCredentialLen {
				return fmt.Errorf("Secret %s/%s exists but holds no usable %q, so area %s can obtain no service token. "+
					"Refusing to mint over it: delete the Secret to have dcctl mint a new credential, or put one of at least %d characters under %q",
					ns, name, areaMintCredentialKey, area, auth.MinAreaCredentialLen, areaMintCredentialKey)
			}
			continue // present: never replaced here
		case !apierrors.IsNotFound(err):
			return fmt.Errorf("reading the mint credential of %s from Secret %s/%s: %w", area, ns, name, err)
		}
		cred, err := newAreaMintCredential()
		if err != nil {
			return err
		}
		if err := writeOwnedSecret(ctx, typed, instanceOwner(st.Instance, st.InstanceUID), ownedSecret{
			Name:      name,
			Namespace: ns,
			Type:      corev1.SecretTypeOpaque,
			Labels:    map[string]string{"app.kubernetes.io/component": area},
			Data:      map[string]string{areaMintCredentialKey: cred},
		}, time.Now); err != nil {
			return err
		}
	}
	return nil
}
