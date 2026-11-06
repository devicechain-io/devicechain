// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// entryOverhead mirrors messaging's localEntryOverhead (list element, entry and map slot),
// which TestLocalEntryOverheadCoversTheHeap there holds against the running Go.
const entryOverhead = 192

// deviceByTokenEntryCost is what the device-by-token cache charges against its byte bound for
// one device: the key, the encoded value, the per-entry overhead, and the decoded copy read
// back by GetCloned (deviceCharge: the encoded length times its factor, plus its base).
func deviceByTokenEntryCost(t *testing.T, d Device) int {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	key := deviceByTokenKey(d.TenantId, d.Token)
	return len(key) + len(raw) + entryOverhead + deviceCharge.factor*len(raw) + deviceCharge.base
}

// TestDeviceByTokenEntryCostSetsTheDefaultBound holds the arithmetic behind
// config.DefaultPerDeviceCacheMiB. Round 30 on GKE found the 24 MiB default smaller than a
// 12,000 to 20,000 device fleet: device-by-token evicted on 73% of lookups at 16,000 devices.
// A device in that cache costs 2.5 to 2.7 KB measured (3.1 KB planned) (not the 290 to 950 bytes the relationships cache
// charges, which the bound had been sized from), so the default must hold the 20,000-device
// fleet the sizing tables target with headroom. The counterweight is the upper bound: a
// default that holds ten times the target is memory promised and never used, and the chart's
// 768Mi memory floor is what the worst case is checked against.
func TestDeviceByTokenEntryCostSetsTheDefaultBound(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	meta := datatypes.JSON(`{"site":"north-yard","floor":3,"owner":"facilities"}`)
	minimal := Device{
		Model:          gorm.Model{ID: 123456, CreatedAt: now, UpdatedAt: now},
		TenantScoped:   rdb.TenantScoped{TenantId: "tenant-0003"},
		TokenReference: rdb.TokenReference{Token: "devicepulse-00558"},
		DeviceTypeId:   12,
		DeviceType: &DeviceType{
			Model:          gorm.Model{ID: 12},
			TokenReference: rdb.TokenReference{Token: "devicepulse-type"},
		},
	}
	named := minimal
	named.NamedEntity = rdb.NamedEntity{Name: sql.NullString{String: "Thermostat 42", Valid: true},
		Description: sql.NullString{String: "Second floor, north corridor", Valid: true}}
	named.MetadataEntity = rdb.MetadataEntity{Metadata: &meta}
	named.DeviceType = &DeviceType{
		Model:          gorm.Model{ID: 12, CreatedAt: now, UpdatedAt: now},
		TenantScoped:   rdb.TenantScoped{TenantId: "tenant-0003"},
		TokenReference: rdb.TokenReference{Token: "devicepulse-type"},
		NamedEntity:    rdb.NamedEntity{Name: sql.NullString{String: "Device pulse", Valid: true}},
	}

	lo, hi := deviceByTokenEntryCost(t, minimal), deviceByTokenEntryCost(t, named)
	t.Logf("device-by-token entry: %d B (minimal), %d B (named, with metadata)", lo, hi)
	if lo < 2200 || hi > 3600 {
		t.Fatalf("an entry costs %d to %d B, outside the 2.2 to 3.6 KB around the 2.5 to 3.1 KB the configuration comment and "+
			"the documentation state: restate them before moving this bound", lo, hi)
	}

	const targetFleet = 20000
	bound := config.DefaultPerDeviceCacheMiB << 20
	if held := bound / hi; held < targetFleet*5/4 {
		t.Errorf("the default %d MiB holds %d devices at %d B, under the %d the sizing tables target plus "+
			"a quarter: the cache would cycle (round 30, F-R30-2)", config.DefaultPerDeviceCacheMiB, held, hi, targetFleet*5/4)
	}
	if held := bound / lo; held > targetFleet*2 {
		t.Errorf("the default %d MiB holds %d devices at %d B, over twice the %d target: memory promised "+
			"and never used", config.DefaultPerDeviceCacheMiB, held, lo, targetFleet)
	}
}
