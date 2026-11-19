// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/backupset"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/nats-io/nats.go"
)

// ShardGroups records the instance's shard-group layout in a manifest: every
// listed suffix is deployed as Count streams, the base stream (group zero) and
// <stream>-g1 .. <stream>-g<Count-1>. Absent means the instance shards nothing.
type ShardGroups struct {
	Count    int      `json:"count"`
	Suffixes []string `json:"suffixes"`
}

// recordShards is the manifest form of the deployed shardGroups block: nil when
// nothing is sharded, otherwise validated with the suffixes sorted.
func recordShards(cfg *config.NatsShardGroupsConfiguration) (*ShardGroups, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg == nil || len(cfg.Suffixes) == 0 {
		return nil, nil
	}
	s := &ShardGroups{Count: cfg.Count, Suffixes: slices.Clone(cfg.Suffixes)}
	slices.Sort(s.Suffixes)
	return s, nil
}

func (s *ShardGroups) validate() error {
	if s == nil {
		return nil
	}
	if len(s.Suffixes) == 0 || !slices.IsSorted(s.Suffixes) {
		return fmt.Errorf("shard_groups suffixes must be nonempty and sorted")
	}
	return (&config.NatsShardGroupsConfiguration{Count: s.Count, Suffixes: s.Suffixes}).Validate()
}

func (s *ShardGroups) equal(o *ShardGroups) bool {
	if s == nil || o == nil {
		return s == o
	}
	return s.Count == o.Count && slices.Equal(s.Suffixes, o.Suffixes)
}

func (s *ShardGroups) String() string {
	if s == nil {
		return "none"
	}
	return fmt.Sprintf("count %d of %s", s.Count, strings.Join(s.Suffixes, ", "))
}

// target is one resource the inventory expects. A group stream carries its base
// stream's declaration (class, owning areas) and its group number.
type target struct {
	item  backupset.Item
	group int
}

func (t target) resource(instance string) (string, string) {
	if t.group == 0 {
		return resource(t.item, instance)
	}
	return "stream", messaging.StreamName(instance, messaging.GroupSuffix(t.item.Name, t.group))
}

// inventory is backupset.Items with every preserved stream the layout shards
// followed by its group streams. Every consumer of the backup inventory goes
// through here: a group stream left out of it is left out of the backup.
func inventory(shards *ShardGroups) []target {
	var out []target
	for _, item := range backupset.Items() {
		out = append(out, target{item: item})
		if shards == nil || item.Kind != backupset.KindStream || !preserved(item) || !slices.Contains(shards.Suffixes, item.Name) {
			continue
		}
		for g := 1; g < shards.Count; g++ {
			out = append(out, target{item: item, group: g})
		}
	}
	return out
}

var groupTail = regexp.MustCompile(`^-g[0-9]+$`)

// refuseUndeclaredGroups cross-checks the broker against the recorded layout: a
// stream named like a group of a preserved stream that the layout does not
// declare means the configuration does not describe the broker, and a backup
// taken from that configuration would leave the stream out.
func refuseUndeclaredGroups(ctx context.Context, nc *nats.Conn, instance string, expected map[string]bool) error {
	names, err := streamNames(ctx, nc)
	if err != nil {
		return fmt.Errorf("list streams to cross-check shard groups: %w", err)
	}
	var bases []string
	for _, item := range backupset.Items() {
		if item.Kind == backupset.KindStream && preserved(item) {
			bases = append(bases, messaging.StreamName(instance, item.Name))
		}
	}
	for _, name := range names {
		if expected[name] {
			continue
		}
		for _, base := range bases {
			if tail, ok := strings.CutPrefix(name, base); ok && groupTail.MatchString(tail) {
				return fmt.Errorf("stream %q is a shard-group stream that the deployed shardGroups configuration does not declare; refusing an incomplete backup", name)
			}
		}
	}
	return nil
}

// streamNames pages $JS.API.STREAM.NAMES itself: nats.go's StreamNames closes
// its channel on an error, which would read as a short list. The server rebuilds
// its sorted inventory for every page, so a stream created or deleted between
// pages (another instance shares the account) can shift a name past the pages
// read; the listing is therefore taken twice and must agree, and each page must
// agree on the total and answer the offset asked for.
func streamNames(ctx context.Context, nc *nats.Conn) ([]string, error) {
	first, err := streamNamesOnce(ctx, nc)
	if err != nil {
		return nil, err
	}
	second, err := streamNamesOnce(ctx, nc)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(first, second) {
		return nil, fmt.Errorf("the stream list changed while it was read; freeze stream creation and deletion and retry")
	}
	return first, nil
}

func streamNamesOnce(ctx context.Context, nc *nats.Conn) ([]string, error) {
	var out []string
	total := -1
	for {
		req, _ := json.Marshal(map[string]int{"offset": len(out)})
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		msg, err := nc.RequestWithContext(rctx, "$JS.API.STREAM.NAMES", req)
		cancel()
		if err != nil {
			return nil, err
		}
		var resp struct {
			Total   int      `json:"total"`
			Offset  int      `json:"offset"`
			Streams []string `json:"streams"`
			Error   *struct {
				Description string `json:"description"`
			} `json:"error"`
		}
		if err := json.Unmarshal(msg.Data, &resp); err != nil {
			return nil, err
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("%s", resp.Error.Description)
		}
		if total >= 0 && resp.Total != total || resp.Offset != len(out) {
			return nil, fmt.Errorf("the stream list changed while it was read (total %d then %d, offset %d for %d); freeze stream creation and deletion and retry", total, resp.Total, resp.Offset, len(out))
		}
		total = resp.Total
		out = append(out, resp.Streams...)
		if len(out) > total {
			return nil, fmt.Errorf("stream listing returned %d names for a total of %d", len(out), total)
		}
		if len(out) == total {
			return out, nil
		}
		if len(resp.Streams) == 0 {
			return nil, fmt.Errorf("stream listing stopped at %d of %d", len(out), total)
		}
	}
}
