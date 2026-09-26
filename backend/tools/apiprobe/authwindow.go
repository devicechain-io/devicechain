// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// signedInQuerier is what verify reads through; *userclient.TenantSession is one.
//
// AccessToken is called on its own, before the read, so that a SIGN-IN that failed
// is known to be a sign-in by WHERE it was observed rather than by what its message
// says: the two are different facts with different verdicts (errors.go).
type signedInQuerier interface {
	querier
	AccessToken(ctx context.Context) (string, error)
}

// authRetryDelay matches the platform validators' JWKS refetch interval
// (core/auth jwksRefreshInterval): a validator that refetched and missed a key will
// not fetch again sooner, so retrying sooner cannot find one that has.
const authRetryDelay = time.Second

// rotationTolerant is verify's session for an upgrade that REPLACES the signing key.
//
// 🔑 WHY THERE IS A WINDOW AT ALL. Such an upgrade leaves, for a few seconds, two
// user-management pods publishing DIFFERENT keys: the new pod, which minted one, and
// the old pod, still stopping, which serves the key the upgrade deleted. A service
// that refetches its key set in those seconds can be answered by the old pod, and
// then refuses a token the new pod has just signed with 401 until its next refetch
// after the old pod has gone. A token the OLD pod signed is worse: every service that
// has refetched from the new pod refuses it for good. And a sign-in is two requests,
// login then selectTenant, which can land on different pods and refuse each other's
// identity token.
//
// So inside the window a refused read is retried with a NEW session — a fresh sign-in
// from whichever pod answers now, never the cached token that may be the old pod's —
// and a sign-in the platform refused is retried the same way. Nothing else is: a 403,
// a GraphQL error on the read, a transport failure are returned at once, unchanged.
//
// 🔴 ONE BUDGET, MEASURED FROM verify's START, NOT ONE PER READ. The window belongs to
// the rollout that finished before verify began; a per-read window would multiply the
// bound by the number of rows on the receipt, which is no bound.
//
// 🔴 A ZERO WINDOW IS THE DEFAULT, AND MEANS NO TOLERANCE: the first 401 is exitDenied.
// Only an upgrade that replaces the key has anything to ride out, and a retry on any
// other is itself the defect — a validator that no longer refetches promptly would
// pass the drill green with nothing but retry lines to show for it. The rig asks for a
// window only for the baselines whose upgrade replaces the key.
//
// KNOWN LIMIT, deliberate: a sign-in that did not reach the platform — a transport
// error, or a 502/503 from the ingress while it still routes to the stopping pod — is
// exitSetup at once, never retried. A rotating key produces a REFUSAL, not an
// unreachable endpoint, and retrying those too would widen the tolerance past the
// one thing it exists for. So a red SETUP whose sign-in failed with a 5xx during the
// rollout is the drill being inconclusive, not a new authentication defect.
//
// The window bounds when a retry may START (elapsed + delay <= window); the attempt it
// starts can still take up to the HTTP client's own timeout. It is a retry budget, not
// a wall-clock deadline, and the messages say so.
type rotationTolerant struct {
	fresh  func() signedInQuerier // a NEW session, signed in on first use
	window time.Duration
	delay  time.Duration
	start  time.Time // verify's start: the one budget every read shares
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error
	out    io.Writer

	current signedInQuerier
	retries int
	last    time.Duration // elapsed at the most recent retry
}

func newRotationTolerant(fresh func() signedInQuerier, window time.Duration, out io.Writer) *rotationTolerant {
	r := &rotationTolerant{
		fresh:  fresh,
		window: window,
		delay:  authRetryDelay,
		now:    time.Now,
		sleep:  sleepCtx,
		out:    out,
	}
	r.start = r.now()
	r.current = fresh()
	return r
}

// sleepCtx waits d, or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// signInRefused reports whether a sign-in failure is the PLATFORM refusing it — a
// GraphQL error from login or selectTenant, or a 401 — as opposed to not being
// reached at all. Only the former is something a rotating key produces.
func signInRefused(err error) bool {
	var gqlErr *userclient.GraphQLError
	return errors.As(err, &gqlErr) || userclient.IsUnauthorized(err)
}

// Query reads url through the current session.
//
//   - A read refused with 401, or a sign-in the platform refused, with budget left:
//     one line is printed, the wait is taken, the session is replaced by a fresh one,
//     and the read is tried again.
//   - A read error that is not a 401 is returned untouched, at once — verify decides
//     what it means.
//   - A sign-in that failed for any other reason is exitSetup at once: nothing was read.
//   - Out of budget: a read still refused with 401 is exitDenied; a sign-in still
//     refused is exitSetup — the drill could not authenticate, so it measured nothing.
//   - A wait cut short by ctx is exitSetup.
func (r *rotationTolerant) Query(ctx context.Context, url, query string, vars map[string]any, out any) error {
	for {
		var err error
		signIn := false
		if _, err = r.current.AccessToken(ctx); err != nil {
			if !signInRefused(err) {
				return failWith(exitSetup, "sign in as the receipt's identity: %w", err)
			}
			signIn = true
		} else {
			err = r.current.Query(ctx, url, query, vars, out)
			if err == nil {
				return nil
			}
			if !userclient.IsUnauthorized(err) {
				return err
			}
		}

		elapsed := r.now().Sub(r.start)
		if elapsed+r.delay > r.window {
			if signIn {
				return failWith(exitSetup,
					"sign-in as the receipt's identity was still refused after %s and %d retries "+
						"(retry budget %s, measured from the start of verify): %w",
					roundElapsed(elapsed), r.retries, r.window, err)
			}
			if r.window == 0 {
				return failWith(exitDenied,
					"%s refused a freshly signed-in token with 401: %w. No retry window was allowed, "+
						"because only an upgrade that replaces the signing key has one to ride out", url, err)
			}
			return failWith(exitDenied,
				"%s still refused a freshly signed-in token with 401 after %s and %d retries "+
					"(retry budget %s, measured from the start of verify): %w. A signing-key "+
					"rotation settles once the old user-management pod has stopped; this did not",
				url, roundElapsed(elapsed), r.retries, r.window, err)
		}

		r.retries++
		r.last = elapsed
		if signIn {
			fmt.Fprintf(r.out, "  retry   sign-in refused at +%s (retry %d, window %s): %v; signing in again\n",
				roundElapsed(elapsed), r.retries, r.window, err)
		} else {
			fmt.Fprintf(r.out, "  retry   401 from %s at +%s (retry %d, window %s); signing in again\n",
				url, roundElapsed(elapsed), r.retries, r.window)
		}
		if err := r.sleep(ctx, r.delay); err != nil {
			return failWith(exitSetup, "waiting to retry %s was cut short: %w", url, err)
		}
		r.current = r.fresh()
	}
}

// settledNote is "" when nothing was retried, else one line for verify's summary,
// so a green run that rode out a rotation says so rather than reading like one
// that never met it.
func (r *rotationTolerant) settledNote() string {
	if r.retries == 0 {
		return ""
	}
	return fmt.Sprintf("%d request(s) were refused while the signing key rotated, the last at +%s; "+
		"each succeeded after signing in again.\n", r.retries, roundElapsed(r.last))
}

func roundElapsed(d time.Duration) time.Duration { return d.Round(100 * time.Millisecond) }
