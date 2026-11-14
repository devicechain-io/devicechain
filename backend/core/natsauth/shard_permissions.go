// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"strings"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// AddShardGroupPermissions adds only configured group variants of existing grants.
// It preserves the area's roles, exact durable names, and every deny. Identity and
// system accounts are untouched. A nil configuration leaves every byte unchanged.
func AddShardGroupPermissions(instance string, users map[string]map[string]Permissions, cfg *config.NatsShardGroupsConfiguration) error {
	if err := checkInstanceToken(instance); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}
	for user, p := range users[AppAccount] {
		if user == ServiceUser {
			continue
		} // transitional login has no allow-list
		for _, rules := range []*Rules{p.Publish, p.Subscribe} {
			if rules == nil {
				continue
			}
			grants := map[string]struct{}{}
			for _, subject := range rules.Allow {
				grants[subject] = struct{}{}
				for _, suffix := range cfg.Suffixes {
					for group := 1; group < cfg.Count; group++ {
						if subject := groupGrant(instance, subject, suffix, group); subject != "" {
							grants[subject] = struct{}{}
						}
					}
				}
			}
			rules.Allow = sortedKeys(grants)
		}
	}
	return nil
}

// groupGrant transforms only known subject, stream and durable tokens. In
// particular an instance ID that ends in a stream suffix is never rewritten.
func groupGrant(instance, subject, suffix string, group int) string {
	parts := strings.Split(subject, ".")
	grouped := messaging.GroupSuffix(suffix, group)
	if strings.HasPrefix(subject, instance+".") {
		if parts[len(parts)-1] != suffix {
			return ""
		}
		parts[len(parts)-1] = grouped
		return strings.Join(parts, ".")
	}
	if !strings.HasPrefix(subject, "$JS.") {
		return ""
	}
	names := map[string]string{messaging.StreamName(instance, suffix): messaging.StreamName(instance, grouped)}
	for _, reader := range streams.ReadersOf(suffix) {
		old := messaging.DeclaredDurables(instance, suffix, reader)
		// Grouped suffixes are intentionally not separate catalog declarations.
		for _, durable := range old {
			tail := strings.TrimPrefix(durable, messaging.DurableName(instance, reader.Area, suffix))
			names[durable] = messaging.DurableName(instance, reader.Area, grouped) + tail
		}
	}
	changed := false
	for i, part := range parts {
		if name, ok := names[part]; ok {
			parts[i] = name
			changed = true
		}
	}
	if !changed {
		return ""
	}
	return strings.Join(parts, ".")
}
