// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

func TestDisabledCertificateRejectionIdentifiesCertificate(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	r.device("sensor-001")
	leaf := r.ca.simDevice(t, "SN-0001", time.Hour)
	r.pin("c-1", "sensor-001", "maker", leaf)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&model.DeviceCredential{}).Where("token = ?", "c-1").Update("enabled", false).Error)
	var output bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&output).Level(zerolog.InfoLevel)
	defer func() { log.Logger = previous }()
	before := time.Now()
	_, why := r.authorize(r.clientID("SN-0001"), leaf)
	require.NotEmpty(t, why)
	var record struct{ Event, Tenant, Fingerprint, RejectedAt string }
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record), "platform rejection must produce one structured record at INFO")
	require.Equal(t, "x509_credential_rejected", record.Event)
	require.Equal(t, r.tenant, record.Tenant)
	require.Equal(t, x509trust.Fingerprint(leaf.cert), record.Fingerprint)
	at, err := time.Parse(time.RFC3339Nano, record.RejectedAt)
	require.NoError(t, err)
	require.False(t, at.Before(before))
	require.False(t, at.After(time.Now()))
}
