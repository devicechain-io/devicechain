// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package processor

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// REGISTRATION ON FIRST CONNECT AGAINST A REAL POSTGRES, through the real callout and, for the
// race, real TLS clients on the embedded broker. The SQLite fixture of the other tests has one
// writer and drops FOR UPDATE, so neither a race nor a burst is visible there.
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./processor -run X509JitPostgres -v

var jitPgTenant atomic.Int64

// newPostgresJitApi runs the REAL migration chain against a live PostgreSQL and returns an Api.
func newPostgresJitApi(t *testing.T, _ *natsserver.Server) *model.Api {
	t.Helper()
	port, err := strconv.Atoi(itEnv("DC_IT_PGPORT", "5432"))
	require.NoError(t, err, "DC_IT_PGPORT must be numeric")
	require.NoError(t, rdbtest.EnsureDatabase(context.Background(), itEnv("DC_IT_PGHOST", "localhost"), port,
		itEnv("DC_IT_PGUSER", "postgres"), itEnv("DC_IT_PGPASSWORD", "postgres"), "dcjit", ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: "dcjit", FunctionalArea: "device-management"},
		Migrations:   schema.Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres95",
			Configuration: map[string]interface{}{
				"hostname": itEnv("DC_IT_PGHOST", "localhost"), "port": port,
				"username": itEnv("DC_IT_PGUSER", "postgres"), "password": itEnv("DC_IT_PGPASSWORD", "postgres"),
			},
		},
		MicroserviceConfig: config.MicroserviceDatastoreConfiguration{},
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()), "run the device-management migrations on the real server")
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	return model.NewApi(mgr)
}

func newPostgresJitRig(t *testing.T, o jitRigOpts) *jitRig {
	t.Helper()
	// A tenant of its own: the database outlives the test.
	o.tenant = fmt.Sprintf("itjit%d-%d", time.Now().UnixNano(), jitPgTenant.Add(1))
	o.newApi = newPostgresJitApi
	return newJitRig(t, o)
}

// admitting is a bucket that admits its first n charges and refuses every one after.
type admitting struct {
	mu   sync.Mutex
	left int
}

func (a *admitting) Allow(string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.left <= 0 {
		return false
	}
	a.left--
	return true
}

// 🔑 TWO CONNECTS WITH THE SAME NEW CERTIFICATE, ON THE WIRE, CREATE EXACTLY ONE DEVICE. The overlap
// is FORCED: both connects are held at the door of registration (each has found no pin, verified
// its chain and taken its slot) until both are there, and only then released. So neither can be an
// ordinary pinned connect that merely arrived late: one registers, and the other, having lost the
// race, is a replay that continues into the pinned grant. Both are granted, under client ids that
// differ only by the discriminator the pin allows.
func TestX509JitPostgresTwoConnectsWithOneNewCertificateCreateOneDevice(t *testing.T) {
	r := newPostgresJitRig(t, jitRigOpts{})
	leaf := r.ca.simDevice(t, "SN-RACE", time.Hour)
	before := r.counts()
	entered, release := r.gate.holdAtTheDoor()
	defer release()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = r.connect(fmt.Sprintf("%s:%c", r.clientID("SN-RACE"), 'a'+i), r.user(), "", *presenting(leaf))
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatalf("only %d of the 2 connects reached the door of registration", i)
		}
	}
	require.Zero(t, r.rows().devices, "nothing is written while both are held at the door")
	release()
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "connect %d", i)
	}
	assert.EqualValues(t, 1, r.rows().devices, "two connects created more than one device")
	assert.EqualValues(t, 1, r.rows().credentials)
	assert.EqualValues(t, 1, r.count(x509ResultJitCreated)-before[x509ResultJitCreated], "exactly one connect registered the device")
	assert.EqualValues(t, 1, r.count(x509ResultJitReplayed)-before[x509ResultJitReplayed], "the other lost the race and was a replay")
	assert.Zero(t, r.count(x509ResultOK)-before[x509ResultOK], "a connect that was not inside the window was counted")
}

// 🔑 A BURST OF FIRST CONNECTS HITS THE BUDGETS EXACTLY. The tenant's allowance is 10: of 40
// distinct certificates presented at once, 10 register and 30 are rate limited, and nothing else
// was written.
func TestX509JitPostgresBurstHitsTheTenantCeiling(t *testing.T) {
	r := newPostgresJitRig(t, jitRigOpts{maxInFlight: 64})
	ceiling := &admitting{left: 10}
	_, err := r.resp.WithCertificateRegistration(JitConfig{Registrar: r.gate, Tenants: ceiling, MaxInFlight: 64})
	require.NoError(t, err)

	const n = 40
	leaves := make([]*testCert, n)
	for i := range leaves {
		leaves[i] = r.ca.simDevice(t, fmt.Sprintf("BURST-%02d", i), time.Hour)
	}
	before := r.counts()
	var wg sync.WaitGroup
	granted, refused := atomic.Int64{}, atomic.Int64{}
	for i := range leaves {
		wg.Add(1)
		go func() {
			defer wg.Done()
			userJWT, why := r.authorize(r.clientID(fmt.Sprintf("BURST-%02d", i)), leaves[i])
			if why == "" && userJWT != "" {
				granted.Add(1)
			} else {
				refused.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 10, granted.Load())
	assert.EqualValues(t, 30, refused.Load())
	assert.EqualValues(t, 10, r.count(x509ResultJitCreated)-before[x509ResultJitCreated])
	assert.EqualValues(t, 30, r.count(x509ResultJitRateLimited)-before[x509ResultJitRateLimited])
	assert.EqualValues(t, 10, r.rows().devices, "a rate-limited connect wrote a device")
}

// 🔑 THE IN-FLIGHT BOUND AGAINST THE DATABASE: with two slots and two registrations held at the
// door, further first connects are busy (writing nothing, charging nothing), a pinned device
// connects the whole time, and the held ones complete once released.
func TestX509JitPostgresBurstHitsTheInFlightBound(t *testing.T) {
	r := newPostgresJitRig(t, jitRigOpts{maxInFlight: 2})
	pinned := r.ca.simDevice(t, "SN-PINNED", time.Hour)
	r.grants(x509ResultJitCreated, r.clientID("SN-PINNED"), pinned)

	entered, release := r.gate.holdAtTheDoor()
	defer release()
	var wg sync.WaitGroup
	held := make([]error, 2)
	for i := range held {
		wg.Add(1)
		cn := fmt.Sprintf("HELD-%d", i)
		leaf := r.ca.simDevice(t, cn, time.Hour)
		go func() {
			defer wg.Done()
			if _, why := r.authorize(r.clientID(cn), leaf); why != "" {
				held[i] = fmt.Errorf("refused: %s", why)
			}
		}()
	}
	for range held {
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatal("a held registration never reached the door")
		}
	}
	charged := len(r.tenants.charged())
	rows := r.rows()
	before := r.counts()
	const extra = 8
	var busy atomic.Int64
	var wg2 sync.WaitGroup
	for i := 0; i < extra; i++ {
		wg2.Add(1)
		cn := fmt.Sprintf("EXTRA-%d", i)
		leaf := r.ca.simDevice(t, cn, time.Hour)
		go func() {
			defer wg2.Done()
			if _, why := r.authorize(r.clientID(cn), leaf); why == genericAuthFailure {
				busy.Add(1)
			}
		}()
	}
	wg2.Wait()
	assert.EqualValues(t, extra, busy.Load())
	assert.EqualValues(t, extra, r.count(x509ResultJitBusy)-before[x509ResultJitBusy])
	assert.Len(t, r.tenants.charged(), charged, "a busy refusal consumed the tenant's allowance")
	assert.Equal(t, rows, r.rows(), "a busy refusal wrote rows")
	r.grants(x509ResultOK, r.clientID("SN-PINNED"), pinned)

	release()
	wg.Wait()
	for i, err := range held {
		assert.NoError(t, err, "held registration %d", i)
	}
	assert.EqualValues(t, rows.devices+2, r.rows().devices)
}
