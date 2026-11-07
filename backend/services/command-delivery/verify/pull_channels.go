// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// PullChannels asks device-management, the owner of the device types, which devices take their
// commands over the HTTP pull channel. It satisfies model.PullChannelResolver and
// presence.PullResolver.
//
// Answers are remembered for pullTTL, positive and negative alike, keyed by tenant and device:
// the delivery sweep asks about every device in every batch it dispatches, and a device's type
// changes channel only when an operator edits it. A stale answer is bounded by the TTL and is
// harmless in both directions: a type just moved to HTTP_PULL has at most a TTL of its commands
// published to a subject nobody reads (they time out, as they did before the move), and a type
// just moved off it has at most a TTL of its commands held, which the hold reconciler releases.
type PullChannels struct {
	client queryClient
	url    string
	now    func() time.Time

	mu    sync.Mutex
	known map[pullKey]pullAnswer
}

// queryClient is the slice of svcclient.Client the resolver needs, so a test needs no HTTP.
type queryClient interface {
	Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error
}

type pullKey struct{ tenant, device string }

type pullAnswer struct {
	pull    bool
	expires time.Time
}

const (
	pullTTL = 15 * time.Second
	// pullCacheMax bounds the cache. When it is reached it is dropped whole, which costs one
	// burst of questions and needs no eviction order.
	pullCacheMax = 20000
	// pullChunk bounds how many devices one question names; device-management refuses more
	// than 2000.
	pullChunk = 500
)

// NewPullChannels binds a resolver to a service client and device-management's GraphQL URL.
func NewPullChannels(client queryClient, graphqlURL string) *PullChannels {
	return &PullChannels{client: client, url: graphqlURL, now: time.Now, known: make(map[pullKey]pullAnswer)}
}

const commandPullDevices = `query($deviceTokens: [String!]!) {
  commandPullDevices(deviceTokens: $deviceTokens)
}`

var _ model.PullChannelResolver = (*PullChannels)(nil)

// PullDevices implements model.PullChannelResolver.
//
// 🔴 AN ABSENT LIST IS AN ERROR, NOT AN EMPTY ANSWER. "No device here is on the pull channel" is
// the healthy answer for almost every tenant, so a response that lost the field would decode to
// the same thing and read as unanimous permission to publish.
func (p *PullChannels) PullDevices(ctx context.Context, deviceTokens []string) (map[string]bool, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("verify: no tenant in context")
	}
	out := make(map[string]bool, len(deviceTokens))
	var ask []string
	now := p.now()
	p.mu.Lock()
	for _, d := range deviceTokens {
		if a, hit := p.known[pullKey{tenant, d}]; hit && now.Before(a.expires) {
			if a.pull {
				out[d] = true
			}
			continue
		}
		ask = append(ask, d)
	}
	p.mu.Unlock()

	for start := 0; start < len(ask); start += pullChunk {
		chunk := ask[start:min(start+pullChunk, len(ask))]
		var resp struct {
			CommandPullDevices *[]string `json:"commandPullDevices"`
		}
		if err := p.client.Query(ctx, p.url, tenant, commandPullDevices,
			map[string]any{"deviceTokens": chunk}, &resp); err != nil {
			return nil, err
		}
		if resp.CommandPullDevices == nil {
			return nil, fmt.Errorf("verify: device-management returned no command channels")
		}
		asked := make(map[string]bool, len(chunk))
		for _, d := range *resp.CommandPullDevices {
			asked[d] = true
		}
		p.mu.Lock()
		if len(p.known)+len(chunk) > pullCacheMax {
			p.known = make(map[pullKey]pullAnswer)
		}
		for _, d := range chunk {
			p.known[pullKey{tenant, d}] = pullAnswer{pull: asked[d], expires: now.Add(pullTTL)}
			if asked[d] {
				out[d] = true
			}
		}
		p.mu.Unlock()
	}
	return out, nil
}
