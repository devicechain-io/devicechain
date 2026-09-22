// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// 🔑 WHAT IS TESTED WHERE, because this file deliberately does not test the main thing.
//
// The ORDER is what this package exists for, and it is pinned by the adopting services'
// graphql_shutdown_order_test.go: those fixtures wrap their three real managers with
// FromManagers and drive beforeMicroserviceStopped, so they exercise this package's
// sequence rather than a copy. Reversing the walk in reverse() fails them.
//
// It is not re-tested here because Managers holds the three CONCRETE manager types, so
// there is no seam to inject ordered fakes through — and widening those fields to
// interfaces to create one would cost every caller the concrete methods it needs
// (NatsManager.NewWriter and friends). The seam not existing is a deliberate trade, so
// the test for the order lives where real managers already do.
//
// What this file covers is everything the service-level fixtures cannot see: a Spec that
// asks for fewer than three managers, and what an error on the way down actually says.

func testMicroservice(t *testing.T) *core.Microservice {
	t.Helper()
	return &core.Microservice{
		InstanceId:     "svc-test",
		FunctionalArea: "svc-test",
		Readiness:      core.NewReadinessGate(),
	}
}

// TestAServiceWithNoManagersIsDrivable is the degenerate case, and it is worth pinning
// because the walk is written over a slice that may legitimately be empty.
//
// Two services in the tree hold no relational database and two hold no broker, so "fewer
// than three" is an ordinary shape rather than a misconfiguration. A walk that treated an
// absent manager as an error — or that indexed into the sequence assuming three — would
// take those services down at the first phase.
func TestAServiceWithNoManagersIsDrivable(t *testing.T) {
	svc := New(testMicroservice(t), Spec{})
	ctx := context.Background()

	require.NoError(t, svc.Initialize(ctx), "initialize with an empty Spec")
	require.NoError(t, svc.Start(ctx), "start with no managers")
	require.NoError(t, svc.Stop(ctx), "stop with no managers")
	require.NoError(t, svc.Terminate(ctx), "terminate with no managers")

	require.Nil(t, svc.Rdb)
	require.Nil(t, svc.Nats)
	require.Nil(t, svc.GraphQL)
}

// TestAFailedStepSaysWhichManagerFailed is about the message, not the failure.
//
// 🔴 THE POINT IS THAT THE ERROR NAMES THE MANAGER. Now that four ordered sequences live
// in one place, a bare error surfacing from a walk says only that "a manager" would not
// stop — and the whole reason a service reads that line is to find out WHICH. The
// lifecycle refusal underneath does carry a name, but it is the component's own
// (area-rdb), which is a string this package composed elsewhere rather than one a reader
// of this error would recognize.
//
// The refusal is induced honestly: an rdb manager that was never initialized cannot be
// stopped, which is the same state a real service reaches when its startup died early.
func TestAFailedStepSaysWhichManagerFailed(t *testing.T) {
	ms := testMicroservice(t)
	uninitialized := rdb.NewRdbManager(ms, core.NewNoOpLifecycleCallbacks(), nil,
		ms.InstanceConfiguration.Persistence.Rdb, mscfg.MicroserviceDatastoreConfiguration{})

	svc := FromManagers(ms, Managers{Rdb: uninitialized})

	err := svc.Stop(context.Background())
	require.Error(t, err, "stopping a manager that was never initialized must fail")
	require.ErrorContains(t, err, "the relational database manager",
		"the error does not say which manager would not stop, which is the one thing a "+
			"reader of it needs now that the sequence lives in one place")
	require.ErrorContains(t, err, "Uninitialized",
		"the wrapping swallowed the lifecycle refusal underneath, so the reason is gone")
}

// TestFromManagersNeedsNoSpec pins the contract FromManagers advertises: it wraps managers
// somebody else built, so Initialize has nothing to do and must not claim otherwise.
//
// Without this, a future Initialize that started assuming a Spec would fail every fixture
// that uses FromManagers — and it would fail them at setup, where the cause reads as a
// broken test rather than a broken contract.
func TestFromManagersNeedsNoSpec(t *testing.T) {
	ms := testMicroservice(t)
	built := rdb.NewRdbManager(ms, core.NewNoOpLifecycleCallbacks(), nil,
		ms.InstanceConfiguration.Persistence.Rdb, mscfg.MicroserviceDatastoreConfiguration{})

	svc := FromManagers(ms, Managers{Rdb: built})

	require.NoError(t, svc.Initialize(context.Background()),
		"Initialize on a FromManagers service must be a no-op, not an attempt to build")
	require.Same(t, built, svc.Rdb, "Initialize replaced a manager it was not given a Spec for")
}
