// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/maintwindow"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type SuppressionMatch struct {
	WindowToken string
	WindowName  string
	Until       time.Time
}

type compiledWindow struct {
	window   AlarmSuppressionWindow
	schedule *maintwindow.Schedule
}

type deviceFacts struct {
	deviceToken     string
	deviceTypeToken string
	groupTokens     map[string]bool
	currentAnchors  map[string]bool
}

var invalidSuppressionWindows = promauto.NewCounter(prometheus.CounterOpts{
	Name: "devicechain_devicemanagement_suppression_invalid_windows_total",
	Help: "Stored suppression windows skipped because validation failed.",
})

func compileSuppressionWindows(windows []AlarmSuppressionWindow) []compiledWindow {
	out := make([]compiledWindow, 0, len(windows))
	for _, w := range windows {
		if err := ValidateAlarmSuppression(&w); err != nil {
			log.Error().Err(err).Str("window", w.Token).Msg("Skipping invalid alarm suppression window")
			invalidSuppressionWindows.Inc()
			continue
		}
		c := compiledWindow{window: w}
		if w.Kind == SuppressionRecurring {
			c.schedule, _ = suppressionSchedule(&w) // validated above
		}
		out = append(out, c)
	}
	return out
}

func matchWindows(windows []compiledWindow, facts deviceFacts, alarmKey, severity string, at time.Time) *SuppressionMatch {
	if !AlarmSeverity(severity).Valid() {
		return nil
	}
	var best *SuppressionMatch
	for _, c := range windows {
		w := c.window
		if w.DeletedAt.Valid || (len(w.AlarmKeys) > 0 && !slices.Contains(w.AlarmKeys, alarmKey)) {
			continue
		}
		if w.BypassSeverity.Valid && AlarmSeverity(severity).Rank() <= AlarmSeverity(w.BypassSeverity.String).Rank() {
			continue
		}
		var until time.Time
		if w.Kind == SuppressionOneShot {
			if at.Before(*w.StartTime) {
				continue
			}
			until = *w.EndTime
		} else {
			if w.ValidFrom != nil && at.Before(*w.ValidFrom) {
				continue
			}
			iv := c.schedule.Next(at)
			if iv.Start.After(at) {
				continue
			}
			until = iv.End
			if w.ValidUntil != nil && w.ValidUntil.Before(until) {
				until = *w.ValidUntil
			}
		}
		if w.EndedAt != nil && w.EndedAt.Before(until) {
			until = *w.EndedAt
		}
		if !at.Before(until) {
			continue
		}
		if w.CreatorScoped && !suppressionScopeIntersects(w.CreatorScope, facts.currentAnchors) {
			continue
		}
		matched := false
		for _, target := range w.Targets {
			switch target.Type {
			case "DEVICE":
				matched = matched || target.Token == facts.deviceToken
			case "DEVICE_TYPE":
				matched = matched || target.Token == facts.deviceTypeToken
			case "ENTITY_GROUP":
				matched = matched || facts.groupTokens[target.Token]
			case "AREA", "ASSET", "CUSTOMER":
				// AREA is direct anchors only. SUBTREE/ancestor expansion is unsupported.
				matched = matched || facts.currentAnchors[strings.ToLower(target.Type)+":"+target.Token]
			case "TENANT":
				matched = true
			}
		}
		if matched && (best == nil || until.After(best.Until) || (until.Equal(best.Until) && w.Token < best.WindowToken)) {
			best = &SuppressionMatch{WindowToken: w.Token, WindowName: w.Name.String, Until: until}
		}
	}
	return best
}

func suppressionScopeIntersects(anchors []string, current map[string]bool) bool {
	for _, a := range anchors {
		if current[a] {
			return true
		}
	}
	return false
}

type suppressionCacheEntry struct {
	loaded  time.Time
	count   int64
	version int64
	windows []compiledWindow
}

type suppressionCache struct {
	sync.Mutex
	tenants map[string]suppressionCacheEntry
}

func (api *Api) suppressionWindows(ctx context.Context) ([]compiledWindow, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	api.suppressionCache.Lock()
	negative, found := api.suppressionCache.tenants[tenant]
	api.suppressionCache.Unlock()
	// An empty tenant is checked at most every five seconds. Replica-created
	// windows may be missed for this bounded interval, failing toward paging.
	if found && negative.count == 0 && now.Sub(negative.loaded) < 5*time.Second {
		return negative.windows, nil
	}
	db := api.RDB.DB(suppressionFactContext(ctx)).Scopes(suppressionLiveAt(now.Add(-10 * time.Minute)))
	var signature struct {
		Count   int64
		Version int64
	}
	if err := db.Session(&gorm.Session{}).Model(&AlarmSuppressionWindow{}).
		Select("count(*) AS count, COALESCE(sum(version), 0) AS version").Scan(&signature).Error; err != nil {
		return nil, err
	}
	api.suppressionCache.Lock()
	entry, ok := api.suppressionCache.tenants[tenant]
	api.suppressionCache.Unlock()
	if ok && now.Sub(entry.loaded) < 5*time.Second && entry.count == signature.Count && entry.version == signature.Version {
		return entry.windows, nil
	}
	compiled, err := loadSuppressionWindows(db)
	if err != nil {
		return nil, err
	}
	api.suppressionCache.Lock()
	defer api.suppressionCache.Unlock()
	if api.suppressionCache.tenants == nil {
		api.suppressionCache.tenants = make(map[string]suppressionCacheEntry)
	}
	// Bound stale tenant retention without a background goroutine.
	for key, old := range api.suppressionCache.tenants {
		if now.Sub(old.loaded) >= 5*time.Second {
			delete(api.suppressionCache.tenants, key)
		}
	}
	api.suppressionCache.tenants[tenant] = suppressionCacheEntry{loaded: now, count: signature.Count, version: signature.Version, windows: compiled}
	return compiled, nil
}

// suppressionLiveAt includes historical ends and excludes finished one-shots/recurrences.
func suppressionLiveAt(at time.Time) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("(ended_at IS NULL OR ended_at > ?) AND (end_time IS NULL OR end_time > ?) AND (valid_until IS NULL OR valid_until > ?)", at, at, at)
	}
}

func loadSuppressionWindows(db *gorm.DB) ([]compiledWindow, error) {
	// Decode JSON per row: valid jsonb can still have the wrong document shape.
	// One such row must not prevent other, valid windows from being evaluated.
	var stored []suppressionStoredWindow
	if err := db.Session(&gorm.Session{}).Model(&AlarmSuppressionWindow{}).Scan(&stored).Error; err != nil {
		return nil, err
	}
	rows := make([]AlarmSuppressionWindow, 0, len(stored))
	for _, row := range stored {
		w := row.AlarmSuppressionWindow
		if err := json.Unmarshal(row.TargetsJSON, &w.Targets); err != nil {
			log.Error().Err(err).Str("window", w.Token).Msg("Skipping invalid alarm suppression targets")
			invalidSuppressionWindows.Inc()
			continue
		}
		if len(row.KeysJSON) != 0 {
			if err := json.Unmarshal(row.KeysJSON, &w.AlarmKeys); err != nil {
				log.Error().Err(err).Str("window", w.Token).Msg("Skipping invalid alarm suppression keys")
				invalidSuppressionWindows.Inc()
				continue
			}
		}
		rows = append(rows, w)
	}
	compiled := compileSuppressionWindows(rows)
	return compiled, nil
}

// SuppressionFor evaluates the current device, never the alarm's historical scope stamp.
func (api *Api) SuppressionFor(ctx context.Context, deviceId uint, alarmKey, severity string, at time.Time) (*SuppressionMatch, error) {
	var windows []compiledWindow
	var err error
	if at.Before(time.Now().Add(-10 * time.Minute)) {
		db := api.RDB.DB(suppressionFactContext(ctx)).Scopes(suppressionLiveAt(at)).
			Where("start_time <= ? OR (kind = ? AND (valid_from IS NULL OR valid_from <= ?))", at, SuppressionRecurring, at)
		windows, err = loadSuppressionWindows(db)
	} else {
		windows, err = api.suppressionWindows(ctx)
	}
	if err != nil {
		return nil, err
	}
	return api.suppressionForWindows(ctx, deviceId, alarmKey, severity, at, windows)
}

// suppressionForWindows reuses a single window snapshot for a batched status read.
func (api *Api) suppressionForWindows(ctx context.Context, deviceId uint, alarmKey, severity string, at time.Time, windows []compiledWindow) (*SuppressionMatch, error) {
	if len(windows) == 0 {
		return nil, nil
	}
	var device Device
	if err := api.RDB.DB(suppressionFactContext(ctx)).First(&device, deviceId).Error; err != nil {
		return nil, err
	}
	facts, err := api.suppressionDeviceFacts(suppressionFactContext(ctx), &device, windows)
	if err != nil {
		return nil, err
	}
	return matchWindows(windows, facts, alarmKey, severity, at), nil
}

func (api *Api) suppressionDeviceFacts(ctx context.Context, device *Device, windows []compiledWindow) (deviceFacts, error) {
	facts := deviceFacts{deviceToken: device.Token, groupTokens: map[string]bool{}, currentAnchors: map[string]bool{}}
	if device.DeviceTypeId != 0 {
		var typ DeviceType
		if err := api.RDB.DB(ctx).First(&typ, device.DeviceTypeId).Error; err != nil {
			return facts, err
		}
		facts.deviceTypeToken = typ.Token
	}
	anchors, err := api.deviceScopeAnchors(ctx, device.ID)
	if err != nil {
		return facts, err
	}
	for _, a := range anchors {
		facts.currentAnchors[a] = true
	}
	named := map[string]bool{}
	for _, c := range windows {
		for _, t := range c.window.Targets {
			if t.Type == "ENTITY_GROUP" {
				named[t.Token] = true
			}
		}
	}
	tokens := make([]string, 0, len(named))
	for token := range named {
		tokens = append(tokens, token)
	}
	const batchSize = 200
	for start := 0; start < len(tokens); start += batchSize {
		var groups []EntityGroup
		if err := api.RDB.DB(ctx).Where("token IN ?", tokens[start:min(start+batchSize, len(tokens))]).Find(&groups).Error; err != nil {
			return facts, err
		}
		staticIDs := []uint{}
		staticTokens := map[uint]string{}
		pairs := [][]any{}
		dynamic := map[uint]EntityGroup{}
		for _, g := range groups {
			if g.MemberType != string(entity.TypeDevice) {
				continue
			}
			if MembershipMode(g.MembershipMode) == MembershipDynamic {
				if g.ActiveVersion.Valid {
					pairs = append(pairs, []any{g.ID, g.ActiveVersion.Int32})
					dynamic[g.ID] = g
				}
			} else {
				staticIDs = append(staticIDs, g.ID)
				staticTokens[g.ID] = g.Token
			}
		}
		if len(staticIDs) > 0 {
			// The tenant equality makes the joined table tenant-safe by the query
			// itself (hack/check-raw-joins.sh).
			var members []EntityRelationship
			if err := api.RDB.DB(ctx).
				Joins("JOIN entity_relationship_types rt ON rt.id = entity_relationships.relationship_type_id AND rt.tenant_id = entity_relationships.tenant_id").
				Where("rt.token = ?", MembershipRelationshipType).
				Where("entity_relationships.source_type = ? AND entity_relationships.source_id IN ?", string(entity.TypeGroup), staticIDs).
				Where("entity_relationships.target_type = ? AND entity_relationships.target_id = ?", string(entity.TypeDevice), device.ID).
				Find(&members).Error; err != nil {
				return facts, err
			}
			for _, member := range members {
				facts.groupTokens[staticTokens[member.SourceId]] = true
			}
		}
		if len(pairs) > 0 {
			var versions []EntityGroupVersion
			if err := api.RDB.DB(ctx).Where("(entity_group_id, version) IN ?", pairs).Find(&versions).Error; err != nil {
				return facts, err
			}
			if len(versions) != len(pairs) {
				return facts, gorm.ErrRecordNotFound
			}
			for _, version := range versions {
				if version.MemberType != string(entity.TypeDevice) {
					continue
				}
				member, err := api.isMemberBySelector(ctx, api.RDB.DB(ctx), version.MemberType, version.Selector, device.ID)
				if err != nil {
					return facts, err
				}
				facts.groupTokens[dynamic[version.EntityGroupId].Token] = member
			}
		}
	}
	return facts, nil
}

// Direct fields shadow the embedded serializer fields while scanning stored documents.
// Model(&AlarmSuppressionWindow{}) still supplies the table and tenant/soft-delete rules.
type suppressionStoredWindow struct {
	AlarmSuppressionWindow
	TargetsJSON datatypes.JSON `gorm:"column:targets"`
	KeysJSON    datatypes.JSON `gorm:"column:alarm_keys"`
}
