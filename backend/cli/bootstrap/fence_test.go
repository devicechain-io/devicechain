// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 🔴 AN INSTANCE BUILT BEFORE THE CREDENTIALS MOVED MUST BE REFUSED, NOT UPGRADED.
//
// Its state still manages the resources that used to hold those credentials, and this
// configuration no longer declares them — so the apply would not fail, it would
// SUCCEED, having deleted the Secret the database authenticates its own services with
// and the certificate authority the broker's clients trust.
//
// The address here is a literal for the same reason the list is: it names a resource
// that no longer exists in this tree, so nothing would fail to compile if it moved.
func TestAnInstanceBuiltBeforeTheCutoverIsRefused(t *testing.T) {
	f := &fakeState{addresses: []string{"module.cnpg_rdb.kubernetes_secret_v1.app"}}

	err := checkNoRetiredInfrastructure(context.Background(), f, "prod")
	if err == nil {
		t.Fatal("an instance whose state still holds the retired credentials was accepted: " +
			"the apply would delete them")
	}
	for _, want := range []string{
		"module.cnpg_rdb.kubernetes_secret_v1.app", // which resource
		"dcctl destroy prod",                       // what to do about it
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// ...and the counterweight, which is the half that makes the fence a guard rather
// than a ban: an instance with none of them must pass.
func TestAnInstanceBuiltAfterTheCutoverPasses(t *testing.T) {
	f := &fakeState{addresses: []string{
		"module.namespace.kubernetes_namespace_v1.this[0]",
		"module.cnpg_rdb.helm_release.cluster",
	}}
	if err := checkNoRetiredInfrastructure(context.Background(), f, "prod"); err != nil {
		t.Errorf("a current instance was refused: %v", err)
	}
}

// 🔴 EVERY RETIRED ADDRESS MUST BE FENCED, not just the first one anybody thought of.
// A missing entry is a silent hole: that one resource is destroyed on an apply that
// reports success. Each is exercised on its own, so a list that happens to catch an
// instance through a DIFFERENT entry does not hide a missing one.
func TestEveryRetiredResourceIsFenced(t *testing.T) {
	for _, address := range []string{
		"module.cnpg_rdb.kubernetes_secret_v1.app",
		"module.cnpg_tsdb.kubernetes_secret_v1.app",
		"module.object_store[0].kubernetes_secret_v1.credentials",
		"module.nats.tls_private_key.ca[0]",
		"module.nats.tls_self_signed_cert.ca[0]",
		"module.nats.tls_private_key.server[0]",
		"module.nats.tls_cert_request.server[0]",
		"module.nats.tls_locally_signed_cert.server[0]",
		"module.nats.kubernetes_secret_v1.nats_tls[0]",
	} {
		t.Run(address, func(t *testing.T) {
			f := &fakeState{addresses: []string{address}}
			if err := checkNoRetiredInfrastructure(context.Background(), f, "prod"); err == nil {
				t.Errorf("state holding %s was accepted, so an apply would destroy it silently",
					address)
			}
		})
	}
}

// 🔴 "CANNOT TELL" IS NOT "NOTHING RETIRED HERE". A state that will not read is
// exactly when guessing is worst: the guess that lets the run continue is the one
// that deletes the credentials.
func TestAnUnreadableStateStopsTheRunRatherThanPassingTheFence(t *testing.T) {
	boom := errors.New("state file is corrupt")
	err := checkNoRetiredInfrastructure(context.Background(), &fakeState{showErr: boom}, "prod")
	if err == nil {
		t.Fatal("an unreadable state passed the fence")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the failure lost its cause: %v", err)
	}
}
