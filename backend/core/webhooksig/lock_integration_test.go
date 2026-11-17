// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package webhooksig

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/test/secretstest"
)

func TestIntegrationRotationAcrossStoreHandles(t *testing.T) {
	for _, end := range []bool{false, true} {
		t.Run(map[bool]string{false: "rotate", true: "end"}[end], func(t *testing.T) {
			_, stores := secretstest.Stores(t)
			rotationSchedule(t, stores, end, false)
		})
	}
}
