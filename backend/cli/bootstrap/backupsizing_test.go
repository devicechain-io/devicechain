// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"io/fs"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
)

// The in-cluster backup store is sized from the event store, so that under
// sustained ingest the EVENT STORE's volume is what fills first. The other order
// is the dangerous one: a full backup store stops archiving, and the event
// store's primary then fills its own volume with write-ahead log it cannot ship,
// while every alert that fires points at the backup store.
//
// The rule: when the event store is 100% full (more events than the 90% its own
// test uses, so more archive -- the conservative side), the backup store holds
// what has landed in it by then, with at least backupStoreFreeAtFullFraction of
// it still free:
//
//	events   = (event store volume - pg_wal) / eventStoreDataBytesPerEventLow
//	archive  = events × archiveBytesPerEventHigh      (archived WAL, BOTH databases)
//	base     = (event store volume - pg_wal)          (one event-store base backup)
//	         + relational store volume                (one relational base backup)
//	content  = archive + base
//	need     = content / (1 - backupStoreFreeAtFullFraction)
//
// Each constant is taken from the end of its measured range that makes the need
// LARGER.
//
// 🔴 What this does NOT cover, and the docs say so: an event store that takes more
// than about a day to fill (every 03:00 base backup of the window is then kept,
// not one), one that never fills because a retention window bounds it (the store
// then holds seven days of WAL at whatever rate the instance ingests), and several
// instances ingesting into the one store, which belongs to the cluster.
const (
	// Stored bytes per event on the event store's primary. Round 2 on GKE (after
	// v0.18.0): 18,060 MiB of table at about 19.0M events, ≈ 997 B; round 1: about
	// 1.05 KB. The LOW end, rounded down: fewer bytes per event means more events
	// fit the volume, and each one adds archive. (The event store's own test uses
	// the HIGH end, 1100, for the opposite reason: fewer events fit.)
	eventStoreDataBytesPerEventLow int64 = 990

	// Archived WAL per ingested event, both databases' buckets together -- they
	// share the store's one volume, so that is what fills it. Round 2, two
	// 10-minute soaks at 4,000/s, 4.8M events: the store went from 9% to 22% of a
	// 60Gi volume. Those are whole percentages, so the growth was between 12% and
	// 14%: 1,610-1,880 B per event, central value ≈ 1,745. Round 1: ≈ 1,520 B;
	// round 2 cumulative over 19M events: ≈ 1,290 B. This is the TOP of the
	// highest interval. It was measured at 4,000 events/s; at much lower rates the
	// full-page images after each checkpoint may cost more per event, which is
	// not measured.
	archiveBytesPerEventHigh int64 = 1880

	// The fraction of the store still free when the event store is full. 35% is
	// BackupDestinationFillingFast's gate (prometheusrule-database-backup.yaml:
	// it fires only below 35% free), so neither backup-store alert can fire
	// before the event store is full -- the event store's own alerts name the
	// cause, rather than the backup store's pointing an operator at a bucket.
	backupStoreFreeAtFullFraction = 0.35
)

// backupStoreNeed returns the bytes a backup store must have to hold, with the
// free fraction above, what lands in it by the time an event store of size
// eventStore is full, with a relational store of size relational beside it. It
// also returns the events at full, for the failure message.
func backupStoreNeed(t *testing.T, eventStore, relational string) (need, events int64) {
	t.Helper()
	data := volumeMiB(t, eventStore)<<20 - measuredEventStoreWALBytes
	if data <= 0 {
		t.Fatalf("an event store of %s holds no data past its %d MiB of pg_wal", eventStore,
			measuredEventStoreWALBytes>>20)
	}
	events = data / eventStoreDataBytesPerEventLow
	archive := events * archiveBytesPerEventHigh
	base := data + volumeMiB(t, relational)<<20
	content := archive + base
	return int64(float64(content) / (1 - backupStoreFreeAtFullFraction)), events
}

const gib = float64(1 << 30)

// The shipped default outlasts the shipped event store. Before this rule it was
// 20Gi, which archive at the measured 1.3-1.9 KB per event fills after 12-16
// million events -- about half of what the 32Gi event store holds.
func TestDefaultBackupStoreOutlastsTheDefaultEventStore(t *testing.T) {
	instanceTF, err := fs.ReadFile(assets.OpenTofuInstance(), "variables.tf")
	if err != nil {
		t.Fatalf("reading the embedded instance variables.tf: %v", err)
	}
	clusterTF, err := fs.ReadFile(assets.OpenTofuCluster(), "variables.tf")
	if err != nil {
		t.Fatalf("reading the embedded cluster variables.tf: %v", err)
	}
	eventStore := tofuDefault(t, instanceTF, "timescale_storage")
	relational := tofuDefault(t, clusterTF, "postgres_storage")
	store := tofuDefault(t, clusterTF, "backup_object_store_storage")

	need, events := backupStoreNeed(t, eventStore, relational)

	// The rule has to be able to fail: the previous default does not meet it.
	if prev := volumeMiB(t, "20Gi") << 20; prev >= need {
		t.Fatalf("20Gi meets the rule's %.1f GiB: the rule no longer tells the previous "+
			"default from a sufficient one", float64(need)/gib)
	}

	if got := volumeMiB(t, store) << 20; got < need {
		t.Errorf("backup_object_store_storage default %s is %.1f GiB, below the %.1f GiB it needs.\n"+
			"  A %s event store (timescale_storage) holds about %d events when full; their archive "+
			"plus one base backup of it and of the %s relational store must fit with %.0f%% free,\n"+
			"  or the backup store fills first and the event store's primary fills with log it "+
			"cannot ship.", store, float64(got)/gib, float64(need)/gib, eventStore, events,
			relational, backupStoreFreeAtFullFraction*100)
	}
}

// The same rule for --compact, on the TLS-keeping path that has a store at all.
// On main before this rule it was 8Gi.
func TestCompactBackupStoreOutlastsTheCompactEventStore(t *testing.T) {
	need, events := backupStoreNeed(t, compact.TimescaleStorage, compact.PostgresStorage)

	if prev := volumeMiB(t, "8Gi") << 20; prev >= need {
		t.Fatalf("8Gi meets the rule's %.1f GiB: the rule no longer tells the previous "+
			"compact size from a sufficient one", float64(need)/gib)
	}

	if got := volumeMiB(t, compact.ObjectStoreStorage) << 20; got < need {
		t.Errorf("--compact's ObjectStoreStorage %s is %.1f GiB, below the %.1f GiB it needs: a "+
			"%s event store holds about %d events when full, and their archive plus one base "+
			"backup of each database must fit with %.0f%% free.", compact.ObjectStoreStorage,
			float64(got)/gib, float64(need)/gib, compact.TimescaleStorage, events,
			backupStoreFreeAtFullFraction*100)
	}
}

// --compact exists for small nodes, so its store must stay below the shipped
// default. TestCompactSizesEveryGrowingVolume cannot say this: it reads only the
// instance root's variables.tf, and this volume is the cluster root's.
func TestCompactBackupStoreIsSmallerThanTheDefault(t *testing.T) {
	clusterTF, err := fs.ReadFile(assets.OpenTofuCluster(), "variables.tf")
	if err != nil {
		t.Fatalf("reading the embedded cluster variables.tf: %v", err)
	}
	def := tofuDefault(t, clusterTF, "backup_object_store_storage")
	if volumeMiB(t, compact.ObjectStoreStorage) >= volumeMiB(t, def) {
		t.Errorf("--compact's ObjectStoreStorage %s is not below the default %s: the preset "+
			"would claim at least a full-size install's backup disk", compact.ObjectStoreStorage, def)
	}
}
