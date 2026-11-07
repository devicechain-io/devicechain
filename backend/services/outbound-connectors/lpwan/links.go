// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
	"github.com/devicechain-io/dc-outbound-connectors/model"
)

// ConnectorStore is the slice of the connector API the link lookup reads.
type ConnectorStore interface {
	Connectors(ctx context.Context, criteria model.ConnectorSearchCriteria) (*model.ConnectorSearchResults, error)
	LatestPublishedConnector(ctx context.Context, token string) (*model.ConnectorVersion, error)
}

// SecretSource resolves a connector's sealed credential (processor.SecretResolver).
type SecretSource interface {
	Resolve(ctx context.Context, tenant, handle string) (string, error)
}

const (
	// linkCacheTTL bounds how long a resolved link is reused. A link changes when someone publishes
	// or rotates a connector, which is rare and tolerates seconds of staleness; the cache spares the
	// connector store a scan on every command of a burst.
	linkCacheTTL = 15 * time.Second
	// linkCacheMax bounds the cache. At the bound it is dropped whole (it is an optimization only).
	linkCacheMax = 1024
	// maxLinkPages bounds the connector scan. A tenant with more lorawan_downlink connectors than
	// this many pages is not a case to guess in: the lookup fails closed.
	maxLinkPages = 10
)

type linkKey struct{ tenant, integration string }

type linkEntry struct {
	target  connectorspec.LoRaWANDownlinkTarget
	err     error
	expires time.Time
}

// StoreLinks finds an integration's network-server link among the tenant's PUBLISHED
// lorawan_downlink connectors. The draft is never used: it is work in progress, exactly as for
// every other connector.
type StoreLinks struct {
	store   ConnectorStore
	secrets SecretSource
	now     func() time.Time

	mu    sync.Mutex
	cache map[linkKey]linkEntry
}

// NewStoreLinks builds the lookup.
func NewStoreLinks(store ConnectorStore, secrets SecretSource) *StoreLinks {
	return &StoreLinks{store: store, secrets: secrets, now: time.Now, cache: map[linkKey]linkEntry{}}
}

// Resolve implements Links. Only definite answers are cached (a link, no link, ambiguous, not
// usable); a store or secret-store failure is returned uncached so the next command retries it.
func (l *StoreLinks) Resolve(ctx context.Context, tenant, integration string) (connectorspec.LoRaWANDownlinkTarget, error) {
	key := linkKey{tenant, integration}
	l.mu.Lock()
	if e, ok := l.cache[key]; ok && l.now().Before(e.expires) {
		l.mu.Unlock()
		return e.target, e.err
	}
	l.mu.Unlock()

	target, err := l.resolve(ctx, tenant, integration)
	if err == nil || errors.Is(err, ErrNoLink) || errors.Is(err, ErrAmbiguous) || errors.Is(err, ErrBadLink) {
		l.mu.Lock()
		if len(l.cache) >= linkCacheMax {
			l.cache = map[linkKey]linkEntry{}
		}
		l.cache[key] = linkEntry{target: target, err: err, expires: l.now().Add(linkCacheTTL)}
		l.mu.Unlock()
	}
	return target, err
}

type candidate struct {
	version *model.ConnectorVersion
}

func (l *StoreLinks) resolve(ctx context.Context, tenant, integration string) (connectorspec.LoRaWANDownlinkTarget, error) {
	tctx := core.WithTenant(ctx, tenant)
	typ := string(model.ConnectorTypeLoRaWANDownlink)
	var matches []candidate
	for page := int32(1); ; page++ {
		res, err := l.store.Connectors(tctx, model.ConnectorSearchCriteria{
			Type: &typ, Pagination: rdb.Pagination{PageNumber: page, PageSize: rdb.MaxPageSize},
		})
		if err != nil {
			return connectorspec.LoRaWANDownlinkTarget{}, fmt.Errorf("list network-server links: %w", err)
		}
		for _, c := range res.Results {
			v, err := l.store.LatestPublishedConnector(tctx, c.Token)
			if errors.Is(err, model.ErrNotPublished) || isNotFound(err) {
				continue // a draft, or deleted since the list: not a link
			}
			if err != nil {
				return connectorspec.LoRaWANDownlinkTarget{}, fmt.Errorf("read network-server link %q: %w", c.Token, err)
			}
			if v.Type != typ {
				continue
			}
			var cfg struct {
				Integration string `json:"integration"`
			}
			if json.Unmarshal(v.Config, &cfg) != nil {
				continue // unreadable: it can name nothing, and dispatch would refuse it anyway
			}
			if cfg.Integration == integration {
				matches = append(matches, candidate{version: v})
			}
		}
		if int32(len(res.Results)) < rdb.MaxPageSize {
			break
		}
		if page >= maxLinkPages {
			return connectorspec.LoRaWANDownlinkTarget{}, fmt.Errorf("%w: more network-server links than can be searched", ErrBadLink)
		}
	}
	switch len(matches) {
	case 0:
		return connectorspec.LoRaWANDownlinkTarget{}, ErrNoLink
	case 1:
	default:
		return connectorspec.LoRaWANDownlinkTarget{}, ErrAmbiguous
	}
	v := matches[0].version

	var secret string
	got, err := l.secrets.Resolve(ctx, tenant, model.ConnectorSecretName(v.ConnectorID))
	switch {
	case errors.Is(err, secrets.ErrSecretNotFound):
		secret = ""
	case err != nil:
		return connectorspec.LoRaWANDownlinkTarget{}, fmt.Errorf("resolve network-server credential: %w", err)
	default:
		secret = got
	}
	t, err := connectorspec.Build(v.Type, v.Config, secret)
	if err != nil {
		return connectorspec.LoRaWANDownlinkTarget{}, fmt.Errorf("%w: %v", ErrBadLink, err)
	}
	target, ok := t.(connectorspec.LoRaWANDownlinkTarget)
	if !ok {
		return connectorspec.LoRaWANDownlinkTarget{}, fmt.Errorf("%w: not a network-server link", ErrBadLink)
	}
	return target, nil
}

func isNotFound(err error) bool { return errors.Is(err, errRecordNotFound) }
