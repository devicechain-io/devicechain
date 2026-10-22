// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import "testing"

// The stream and bucket catalog is enforced only for a Deployed microservice
// (core/messaging, catalog.go), and NewMicroservice is the only production constructor.
// If it stopped setting the flag, every service would start unenforced and every test
// would stay green, because each one builds a struct literal (Deployed false). The
// catalog's start-time refusal would then be a check no production pod runs.
func TestNewMicroserviceIsDeployed(t *testing.T) {
	if ms := NewMicroservice(NewNoOpLifecycleCallbacks()); !ms.Deployed {
		t.Fatal("NewMicroservice built a microservice that is not Deployed: the stream and bucket " +
			"catalog would not be enforced in production")
	}
}
