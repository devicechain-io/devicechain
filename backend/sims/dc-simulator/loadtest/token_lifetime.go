// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/graphqlws"
	"github.com/devicechain-io/dc-microservice/userclient"
)

// tokenMargin is added to a run's own length when asking for the access token its live
// monitor dials with. The server closes a GraphQL WebSocket when the token it was opened
// with expires, and these monitors do not re-dial, so the token has to outlive the run.
// The margin absorbs what the run's arithmetic does not see: clock skew between this
// machine and the server, a slow bootstrap between asking for the token and dialling, and
// the few requests a run makes after its last timed phase.
const tokenMargin = 2 * time.Minute

// monitorLifetimeNeeded is how long the access token RunMonitored dials with must stay
// valid: the drive, the drain after it, and the margin.
func monitorLifetimeNeeded(p Profile) time.Duration {
	return p.Hold + monitorDrainWait + tokenMargin
}

// detectionLifetimeNeeded is how long the access token the detection run's live monitor
// dials with must stay valid. The monitor is subscribed before the probes are driven and
// stopped only after the alarm wait and the settle, so every one of those phases counts,
// at the longest each is allowed to run.
func detectionLifetimeNeeded(cfg DetectionConfig) time.Duration {
	probes := time.Duration(max(2*cfg.Cycles-1, 0)) * cfg.ProbeInterval
	return subscribeSettle + probes + cfg.AlarmTimeout + cfg.AlarmSettle + tokenMargin
}

// pinnedToken fetches ONE access token that stays valid for need and returns a provider
// that always hands back that token, for a WebSocket that is dialled once and held for the
// whole run. It returns an error wrapping userclient.ErrTokenLifetimeTooShort when the
// server's access tokens do not live that long.
func pinnedToken(ctx context.Context, s *userclient.TenantSession, need time.Duration) (graphqlws.TokenProvider, error) {
	tok, err := s.AccessTokenValidFor(ctx, need)
	if err != nil {
		return nil, err
	}
	return func(context.Context) (string, error) { return tok, nil }, nil
}

// tokenLifetimeRefusal is the error a monitored run returns, before it provisions or
// drives anything, when its live monitor's token would expire mid-run.
func tokenLifetimeRefusal(need time.Duration, err error) error {
	return fmt.Errorf("cannot measure: this run holds a live monitor socket for %s, longer than an "+
		"access token lives; shorten the hold or raise the access-token lifetime: %w", need, err)
}

// liveTokenAbsentReason says, for the report, why the detection run's live monitor has no
// token. The two reasons are different and the report must name whichever it was: a token
// that cannot outlive the run is a property of the server's configuration, while a failed
// fetch (a network error, a refused sign-in) says nothing about lifetimes.
func liveTokenAbsentReason(need time.Duration, err error) string {
	if errors.Is(err, userclient.ErrTokenLifetimeTooShort) {
		return fmt.Sprintf("no access token lives the %s the run holds the socket for", need)
	}
	return "the access token for the detectionStream could not be fetched"
}
