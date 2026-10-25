// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 THE ENDPOINT NEVER HOLDS MORE DATABASE CONNECTIONS THAN ITS BUDGET, however many requests
// are in flight. Registrations against one profile serialize on its row, so a burst of them
// would otherwise sit on pooled connections waiting for a lock and stall everything else that
// shares the pool. The model's calls are slowed so that every request is inside one for a
// while; the registrar records, from inside the calls, how many ran at once.
func TestRegistrationsNeverHoldMoreConnectionsThanTheBudget(t *testing.T) {
	const budget, requests = 2, 12
	f := newFixture(t, withConfig(func(c *Config) { c.MaxInFlight = requests; c.MaxConcurrentRegistrations = budget }), withFakeModel())
	f.profile("pp-1", model.ProvisionAllowNew)
	f.registrar.delay = 40 * time.Millisecond

	statuses := make([]int, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = f.post("acme", f.body("pp-1", fmt.Sprintf("SN-%d", i), i+1)).Status
		}()
	}
	wg.Wait()

	for i, s := range statuses {
		assert.Equal(t, 201, s, "request %d queued for a place and was lost", i)
	}
	assert.Equal(t, int64(budget), f.registrar.maxInside.Load(),
		"the model ran %d calls at once against a budget of %d (and the budget must be reached, or this proves nothing)",
		f.registrar.maxInside.Load(), budget)
}

// The control: with a budget as wide as the burst the same load DOES run that many at once, so
// the bound above is the semaphore's and not an accident of timing.
func TestAWideBudgetRunsTheBurstAtOnce(t *testing.T) {
	const requests = 8
	f := newFixture(t, withConfig(func(c *Config) { c.MaxInFlight = requests; c.MaxConcurrentRegistrations = requests }), withFakeModel())
	f.profile("pp-1", model.ProvisionAllowNew)
	f.registrar.delay = 150 * time.Millisecond
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.post("acme", f.body("pp-1", fmt.Sprintf("SN-%d", i), i+1))
		}()
	}
	wg.Wait()
	assert.Greater(t, f.registrar.maxInside.Load(), int64(2))
}

// A request waiting for a place holds no connection and is refused 503 with Retry-After after
// the queue wait, instead of waiting for ever.
func TestAWaiterHoldsNoConnectionAndGivesUpAfterTheQueueWait(t *testing.T) {
	f := newFixture(t, withConfig(func(c *Config) { c.MaxConcurrentRegistrations = 1; c.QueueWait = 250 * time.Millisecond }))
	f.profile("pp-1", model.ProvisionAllowNew)
	f.registrar.hold = make(chan struct{})
	f.registrar.entered = make(chan struct{}, 1)
	held := make(chan reply, 1)
	go func() { held <- f.post("acme", f.body("pp-1", "SN-1", 1)) }()
	<-f.registrar.entered

	start := time.Now()
	got := f.post("acme", f.body("pp-1", "SN-2", 2))
	waited := time.Since(start)
	assert.Equal(t, 503, got.Status)
	assert.Equal(t, "PROVISIONING_UNAVAILABLE", got.Code())
	assert.Equal(t, "1", got.Header.Get("Retry-After"))
	assert.GreaterOrEqual(t, waited, 200*time.Millisecond, "it did not wait for a place")
	assert.Less(t, waited, 3*time.Second)
	assert.Equal(t, int64(1), f.registrar.maxInside.Load(), "the waiter entered the model")
	assert.Equal(t, int64(1), f.registrar.auth.Load())
	assert.Equal(t, 1.0, f.count(ResultBusy))

	close(f.registrar.hold)
	assert.Equal(t, 201, (<-held).Status)
}

// 🔴 CLEARING THE READ DEADLINE IS WHAT LETS A REGISTRATION OUTLIVE IT. The registrar takes
// longer than the deadline after the body is in, and the request still registers: the deadline
// armed for the body must not cancel the request's context in the middle of the work.
func TestARegistrationMayOutlastTheReadDeadline(t *testing.T) {
	f := newFixture(t, withConfig(func(c *Config) { c.ReadDeadline = 300 * time.Millisecond }))
	f.profile("pp-1", model.ProvisionAllowNew)
	f.registrar.delay = 450 * time.Millisecond // both calls: 900 ms, three times the deadline
	got := f.post("acme", f.body("pp-1", "SN-1", 1))
	assert.Equal(t, 201, got.Status, "the deadline cancelled a request after its body was read: %s", got.Raw)
	d, c := f.rows()
	assert.Equal(t, [2]int64{1, 1}, [2]int64{d, c})
}

// The 429 for an exhausted tenant tells the caller how long ITS rate takes to earn another
// registration, not a constant.
func TestTheTenantRetryAfterFollowsTheTenantsRate(t *testing.T) {
	f := newFixture(t, withTenants(core.NewTenantRateLimiter(core.StaticCeiling(0.1, 1))))
	f.profile("pp-1", model.ProvisionAllowNew)
	require.Equal(t, 201, f.post("acme", f.body("pp-1", "SN-1", 1)).Status)
	got := f.post("acme", f.body("pp-1", "SN-2", 2))
	assert.Equal(t, 429, got.Status)
	assert.Equal(t, "10", got.Header.Get("Retry-After"))
}
