// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The instance document says AreaLogins=true but carries no passwords; they live in per-login
// Secrets. Callers that present an operator login must overlay them, or operatorLogin refuses
// with the administrator's Secret "could not be read" although it exists.
func loginFixture(t *testing.T, inst, adminPassword string) *fake.Clientset {
	t.Helper()
	ns := InstanceNamespace(inst)
	doc := &config.InstanceConfiguration{}
	doc.Infrastructure.Nats.Auth.AreaLogins = true
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	cs := fake.NewSimpleClientset()
	create := func(name string, data map[string][]byte) {
		_, err := cs.CoreV1().Secrets(ns).Create(context.Background(),
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: data}, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	create(instanceConfigSecretName(inst), map[string][]byte{"instance": raw})
	for _, p := range config.NatsLoginPrincipals() {
		pw := "pw-" + p
		if p == config.NatsAdminPrincipal {
			pw = adminPassword
		}
		create(natsLoginSecretName(inst, p), map[string][]byte{natsLoginUserKey: []byte("u-" + p), natsLoginPasswordKey: []byte(pw)})
	}
	return cs
}

func TestOperatorViewCarriesTheBrokerAdministratorLogin(t *testing.T) {
	cs := loginFixture(t, "rc", "admin-secret")
	cfg, err := deployedInstanceConfigWithLogins(context.Background(), cs, "rc")
	require.NoError(t, err)
	_, pw, err := operatorLogin(cfg.Infrastructure.Nats)
	require.NoError(t, err, "the administrator's Secret exists; the operator login must be found")
	require.Equal(t, "admin-secret", pw)
}

// The document-only read (used by DeployedRootKey) must not touch the login Secrets: one that is
// empty would otherwise fail a restore drill that needs only the root key.
func TestDocumentOnlyViewDoesNotReadLoginSecrets(t *testing.T) {
	cs := loginFixture(t, "rc", "")
	cfg, err := deployedInstanceConfig(context.Background(), cs, "rc")
	require.NoError(t, err, "an empty login Secret must not fail the document-only read")
	require.Empty(t, cfg.Infrastructure.Nats.Auth.AreaPasswords)

	_, err = deployedInstanceConfigWithLogins(context.Background(), cs, "rc")
	require.Error(t, err, "the operator view must still refuse an empty login")
}

func TestOperatorLoginNamesTheSecretWhenTheAdministratorIsAbsent(t *testing.T) {
	_, _, err := operatorLogin(config.NatsConfiguration{Auth: config.NatsAuthConfiguration{
		AreaLogins: true, AreaPasswords: map[string]string{},
	}})
	require.ErrorContains(t, err, "nats-broker-admin")
}
