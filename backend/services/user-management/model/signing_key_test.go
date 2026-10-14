// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
	"testing"

	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/stretchr/testify/require"
)

// An explicit Active=false on create must be stored: with `default:true` on the column gorm
// omitted the zero value from the INSERT and the DEFAULT turned the retired key active.
func TestSigningKeyCreateStoresExplicitActiveFalse(t *testing.T) {
	db := putest.NewSQLiteDB(t, &SigningKey{})
	require.NoError(t, db.WithContext(core.WithSystemContext(context.Background())).Create(&SigningKey{Active: false, PublicKeyPem: "pem"}).Error)
	var n int64
	require.NoError(t, db.WithContext(core.WithSystemContext(context.Background())).Model(&SigningKey{}).Where("active = ?", false).Count(&n).Error)
	require.EqualValues(t, 1, n)
}
