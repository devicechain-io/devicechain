// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import "context"

// PullResolver says which of a set of devices take their commands over the HTTP pull channel.
// With no error, absence means not pull. With an error, only entries present in the
// map were decided, including explicit false entries.
type PullResolver interface {
	PullDevices(ctx context.Context, deviceTokens []string) (map[string]bool, error)
}

// Empty is the Reader of an instance with no projection to read: it knows nothing about any
// device, which Decide answers Dispatch for. It exists so the pull channel can be layered over
// an instance that deploys no device-state.
type Empty struct{}

// StatesFor implements Reader.
func (Empty) StatesFor(context.Context, []string) (map[string]State, error) {
	return map[string]State{}, nil
}

type pullReader struct {
	inner    Reader
	resolver PullResolver
}

// WithPullChannels layers the pull channel over a presence Reader: every device the resolver
// names is returned with PullChannel set, which Decide answers Hold.
//
// 🔴 EVERY CALLER OF THE READER GETS IT, WHICH IS THE POINT OF LAYERING RATHER THAN CALLING THE
// RESOLVER IN EACH. The delivery sweep withholds on it, the hold reconciler declines to release
// on it, and the stranded-SENT pass sees it, all through the one Decide, so no path can publish
// a pull device's command or return it to the queue.
//
// An undecided channel defers without changing the command: the sweep leaves it queued,
// and the reconciler leaves an existing hold alone. Decided partial answers still apply.
// Recovery takes the next delivery sweep, without a durable hold to reconcile.
func WithPullChannels(inner Reader, resolver PullResolver) Reader {
	if resolver == nil {
		return inner
	}
	if inner == nil {
		inner = Empty{}
	}
	return &pullReader{inner: inner, resolver: resolver}
}

// StatesFor implements Reader.
func (r *pullReader) StatesFor(ctx context.Context, deviceTokens []string) (map[string]State, error) {
	states, err := r.inner.StatesFor(ctx, deviceTokens)
	if states == nil {
		states = map[string]State{}
	}
	pull, perr := r.resolver.PullDevices(ctx, deviceTokens)
	for _, token := range deviceTokens {
		isPull, decided := pull[token]
		s, present := states[token]
		s.PullChannel = isPull
		s.PullUnknown = perr != nil && !decided
		if present || isPull || s.PullUnknown || (perr != nil && decided && err == nil) {
			states[token] = s
		}
	}
	if err == nil {
		err = perr
	}
	return states, err
}
