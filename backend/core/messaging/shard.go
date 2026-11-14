// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
)

// ErrUnplacedTenant refuses a publish whose tenant placement cannot be established.
var ErrUnplacedTenant = errors.New("messaging: tenant has no valid shard-group placement")

// ShardGroupRouter is satisfied by partition.Router. An error never means group zero.
type ShardGroupRouter interface {
	ShardGroup(tenant string) (int, error)
}

// A non-nil snapshot also represents disabled routing, so frozen legacy
// publishers never need streamMu to distinguish disabled from not yet frozen.
type streamRouting struct{ shards *streamShards }

// streamShards is immutable after publication under streamMu.
type streamShards struct {
	router ShardGroupRouter
	cfg    config.NatsShardGroupsConfiguration
}

// EnableStreamSharding installs the opt-in routing seam once. Services do not call it yet.
// Group streams multiply each selected stream's disk reservation by Count; budget
// accounting is deferred to a later slice.
func (nmgr *NatsManager) EnableStreamSharding(router ShardGroupRouter, cfg *config.NatsShardGroupsConfiguration) error {
	if router == nil {
		return fmt.Errorf("messaging: nil shard-group router")
	}
	v := reflect.ValueOf(router)
	if (v.Kind() == reflect.Pointer || v.Kind() == reflect.Func || v.Kind() == reflect.Map || v.Kind() == reflect.Slice || v.Kind() == reflect.Interface || v.Kind() == reflect.Chan) && v.IsNil() {
		return fmt.Errorf("messaging: nil shard-group router")
	}
	if cfg == nil {
		return fmt.Errorf("messaging: nil shard-group configuration")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	copyCfg := *cfg
	copyCfg.Suffixes = slices.Clone(cfg.Suffixes)
	copyCfg.PlacementTags = make(map[int][]string, len(cfg.PlacementTags))
	for g, tags := range cfg.PlacementTags {
		copyCfg.PlacementTags[g] = slices.Clone(tags)
	}
	nmgr.streamMu.Lock()
	defer nmgr.streamMu.Unlock()
	if nmgr.routingFrozen {
		return fmt.Errorf("messaging: cannot enable stream sharding after constructing a handle or ensuring a stream")
	}
	if nmgr.shards != nil {
		return fmt.Errorf("messaging: stream sharding already enabled")
	}
	nmgr.shards = &streamShards{router: router, cfg: copyCfg}
	return nil
}

func (nmgr *NatsManager) streamSharding() *streamShards {
	if snapshot := nmgr.routingSnapshot.Load(); snapshot != nil {
		return snapshot.shards
	}
	nmgr.streamMu.Lock()
	defer nmgr.streamMu.Unlock()
	return nmgr.shards
}

func (s *streamShards) selects(suffix string) bool {
	return s != nil && slices.Contains(s.cfg.Suffixes, suffix)
}

func (nmgr *NatsManager) tenantSuffix(tenant, suffix string) (string, error) {
	s := nmgr.streamSharding()
	if !s.selects(suffix) {
		return suffix, nil
	}
	g, err := s.router.ShardGroup(tenant)
	if err != nil {
		return "", fmt.Errorf("%w: tenant %q: %w", ErrUnplacedTenant, tenant, err)
	}
	if g < 0 || g >= s.cfg.Count {
		return "", fmt.Errorf("%w: tenant %q has group %d outside [0,%d)", ErrUnplacedTenant, tenant, g, s.cfg.Count)
	}
	return GroupSuffix(suffix, g), nil
}

// declaredSuffix recognizes only group names enabled by this manager's configuration.
func (nmgr *NatsManager) declaredSuffix(suffix string) string {
	s := nmgr.streamSharding()
	if s != nil {
		for _, base := range s.cfg.Suffixes {
			for g := 1; g < s.cfg.Count; g++ {
				if suffix == GroupSuffix(base, g) {
					return base
				}
			}
		}
	}
	return suffix
}

func (nmgr *NatsManager) groupPlacement(suffix string) *nats.Placement {
	s := nmgr.streamSharding()
	if s != nil {
		for _, base := range s.cfg.Suffixes {
			for g, tags := range s.cfg.PlacementTags {
				if suffix == GroupSuffix(base, g) && len(tags) > 0 {
					return &nats.Placement{Tags: slices.Clone(tags)}
				}
			}
		}
	}
	return nil
}

func applyGroupPlacement(cfg *nats.StreamConfig, want *nats.Placement) bool {
	if want == nil {
		return false
	}
	if cfg.Placement != nil && slices.Equal(cfg.Placement.Tags, want.Tags) {
		return false
	}
	cfg.Placement = want
	return true
}

// ReaderWithShardGroup explicitly selects one group. Every selected suffix needs
// one reader per group, including group zero.
func ReaderWithShardGroup(g int) ReaderOption {
	return func(r *natsReader) { r.shardGroup = &g }
}

func (nmgr *NatsManager) readerSuffix(suffix string, group *int) (string, error) {
	s := nmgr.streamSharding()
	if group == nil {
		if s.selects(suffix) {
			return "", fmt.Errorf("messaging: sharded suffix %q needs one reader per group; use ReaderWithShardGroup", suffix)
		}
		return suffix, nil
	}
	if s == nil {
		return "", fmt.Errorf("messaging: ReaderWithShardGroup used while stream sharding is disabled")
	}
	if *group < 0 || *group >= s.cfg.Count {
		return "", fmt.Errorf("messaging: shard group %d out of range [0,%d)", *group, s.cfg.Count)
	}
	if !s.selects(suffix) && *group != 0 {
		return "", fmt.Errorf("messaging: suffix %q is not sharded", suffix)
	}
	return GroupSuffix(suffix, *group), nil
}

// newStreamConfig is the configuration sent to JetStream when creating a stream.
func (nmgr *NatsManager) newStreamConfig(suffix string) *nats.StreamConfig {
	base := nmgr.declaredSuffix(suffix)
	cfg := &nats.StreamConfig{
		Name:        StreamName(nmgr.Microservice.InstanceId, suffix),
		Subjects:    nmgr.streamSubjects(suffix),
		Storage:     nats.FileStorage,
		Retention:   retentionPolicy(streams.RetentionFor(base)),
		Discard:     nats.DiscardOld,
		MaxAge:      streamMaxAge,
		Replicas:    nmgr.effectiveStreamReplicas(),
		AllowDirect: streams.AllowDirectFor(base),
		Placement:   nmgr.groupPlacement(suffix),
	}
	applyStreamBounds(cfg, nmgr.streamBounds(base))
	applyStreamDuplicateWindow(cfg, time.Duration(streams.DuplicateWindowSecondsFor(base))*time.Second)
	return cfg
}

// freezeStreamRouting serializes the first handle with EnableStreamSharding.
func (nmgr *NatsManager) freezeStreamRouting() {
	nmgr.streamMu.Lock()
	nmgr.routingFrozen = true
	if nmgr.routingSnapshot.Load() == nil {
		nmgr.routingSnapshot.Store(&streamRouting{shards: nmgr.shards})
	}
	nmgr.streamMu.Unlock()
}

func (nmgr *NatsManager) refuseShardedRead(suffix, slice string) error {
	nmgr.freezeStreamRouting()
	s := nmgr.streamSharding()
	if s.selects(suffix) && s.cfg.Count >= 2 {
		return fmt.Errorf("messaging: %s not supported for a sharded suffix yet: %q", slice, suffix)
	}
	return nil
}

// publishBackpressure checks the tenant's stream, never the base stream by default.
func (nmgr *NatsManager) publishBackpressure(ctx context.Context, suffix string) error {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return core.ErrNoTenant
	}
	routed, err := nmgr.tenantSuffix(tenant, suffix)
	if err != nil {
		return err
	}
	return nmgr.Backpressure(routed)
}

// registerWriterBackpressure registers every configured group before publishing.
func (nmgr *NatsManager) registerWriterBackpressure(suffix string) *gateState {
	s := nmgr.streamSharding()
	if s.selects(suffix) {
		for g := 1; g < s.cfg.Count; g++ {
			nmgr.registerBackpressure(GroupSuffix(suffix, g))
			nmgr.metrics.initRefused(StreamName(nmgr.Microservice.InstanceId, GroupSuffix(suffix, g)))
		}
	}
	return nmgr.registerBackpressure(suffix)
}

// noteRoutedPublish charges sampling volume to the stream receiving the message.
func (nmgr *NatsManager) noteRoutedPublish(ctx context.Context, suffix string, size int) {
	tenant, _ := core.TenantFromContext(ctx)
	routed, err := nmgr.tenantSuffix(tenant, suffix)
	if err != nil {
		// Placement was validated before the publish; no volume was routed here.
		return
	}
	gates := nmgr.backpressure()
	gates.mu.Lock()
	gate := gates.gates[routed]
	gates.mu.Unlock()
	gate.notePublished(size)
}

// initRoutedPublish exposes each configured stream before its first publish.
func (nmgr *NatsManager) initRoutedPublish(suffix, mode string) {
	nmgr.metrics.initPublish(suffix, mode)
	if s := nmgr.streamSharding(); s.selects(suffix) {
		for g := 1; g < s.cfg.Count; g++ {
			nmgr.metrics.initPublish(GroupSuffix(suffix, g), mode)
		}
	}
}

func (nmgr *NatsManager) countRoutedRefused(ctx context.Context, suffix string, n int) {
	tenant, _ := core.TenantFromContext(ctx)
	routed, err := nmgr.tenantSuffix(tenant, suffix)
	if err == nil {
		nmgr.countRefused(StreamName(nmgr.Microservice.InstanceId, routed), n)
	}
}

// Selected streams are per-event subjects, whose final token is the routed suffix.
func (nmgr *NatsManager) publishMetricSuffix(subject, suffix string) string {
	if !nmgr.streamSharding().selects(suffix) {
		return suffix
	}
	return subject[strings.LastIndexByte(subject, '.')+1:]
}
