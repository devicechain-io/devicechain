// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
// than about a day to fill (every 03:00 base backup of each store's window is then
// kept, not one), one that never fills because a retention window bounds it (the
// store then holds each database's window of WAL at whatever rate the instances
// ingest: see TestSteadyStateRateIsTheOnePublished), and several instances
// ingesting into the one store, which belongs to the cluster.
//
// No recovery window appears in this rule, and that is not an oversight: it counts
// the archive of EVERY event the event store holds, with nothing pruned, so no
// window can keep more than it does. The relational store's 30-day window moved
// the steady-state case, not this one.
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
	// not measured. It was also measured with PostgreSQL's wal_compression off.
	// Turning that on shrinks the WAL written, so it should make this an
	// overestimate, but the archive gzips each segment as well (walCompression)
	// and the two savings do not simply multiply. Lower it only from a run that
	// reads the bucket with wal_compression on.
	archiveBytesPerEventHigh int64 = 1880

	// The fraction of the store still free when the event store is full. 35% is
	// BackupDestinationFillingFast's gate (prometheusrule-database-backup.yaml:
	// it fires only below 35% free), so neither backup-store alert can fire
	// before the event store is full -- the event store's own alerts name the
	// cause, rather than the backup store's pointing an operator at a bucket.
	backupStoreFreeAtFullFraction = 0.35

	// The relational store's share of what the backup store holds under ingest.
	// device-state writes last-known state there as events arrive, so its WAL grows
	// with ingest too. ONE sample, round 2 on GKE, a small fleet (1,000-1,600
	// devices reporting every 250 ms) at about 4,000 events/s: the relational
	// bucket held 700 MiB against the event store's 4.4 GiB, ≈ 13.5%, rounded to
	// 14%. Those are cumulative bucket contents, base backups and idle segments
	// included, not an archive rate.
	//
	// 🔴 NOT a conservative bound. At lower rates device-state batches fewer writes
	// per transaction, and a large fleet of slow devices touches a different row
	// and page per event, so the share is likely higher there; neither is
	// measured. That is why the published text gives the figure at a share of 1
	// beside this one, and why TestSteadyStateRateIsTheOnePublished pins both.
	relationalArchiveShare = 0.14
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

// steadyStateEventsPerSecond is the sustained ingest rate at which a store of size
// store fills with log alone when the event store never fills: each database keeps
// its own window of archive, weighted by its share of it.
//
//	rate = store / (86,400 × archiveBytesPerEventHigh × ((1-share)·eventDays + share·relationalDays))
func steadyStateEventsPerSecond(t *testing.T, store string, share float64, relationalDays, eventDays int64) float64 {
	t.Helper()

	weightedDays := (1-share)*float64(eventDays) + share*float64(relationalDays)
	if weightedDays <= 0 {
		t.Fatalf("a %d-day and %d-day window at share %.2f keep no log at all", relationalDays,
			eventDays, share)
	}
	return float64(volumeMiB(t, store)<<20) / (86400 * float64(archiveBytesPerEventHigh) * weightedDays)
}

// windowDays parses a default window of the form "<n>d", failing on any other unit
// rather than guessing how many days "1m" is.
func windowDays(t *testing.T, window string) int64 {
	t.Helper()

	m := regexp.MustCompile(`^([1-9][0-9]*)d$`).FindStringSubmatch(window)
	if m == nil {
		t.Fatalf("window %q is not whole days; this test does not guess what a week or a "+
			"month is in the arithmetic", window)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		t.Fatalf("window %q: %v", window, err)
	}
	return n
}

// publishedFigures returns every number the pattern's one group captures in text,
// failing when there is none: a sentence that was reworded away must not pass by
// being absent.
func publishedFigures(t *testing.T, what, text string, pattern *regexp.Regexp) []float64 {
	t.Helper()

	var out []float64
	for _, m := range pattern.FindAllStringSubmatch(text, -1) {
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatalf("%s: %q is not a number", what, m[1])
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		t.Fatalf("%s no longer matches %s; if the sentence was reworded, point this test at "+
			"the new wording rather than deleting the check", what, pattern)
	}
	return out
}

// The steady-state figures the docs and the backup_object_store_storage description
// publish -- the rate that fills the default store when the event store never
// fills, at the measured relational share and at a share of 1 -- are the ones the
// shipped defaults give. The windows are read from the variables each store's
// root declares, so changing a window, the store or the constants without the
// prose fails here.
func TestSteadyStateRateIsTheOnePublished(t *testing.T) {
	instanceTF, err := fs.ReadFile(assets.OpenTofuInstance(), "variables.tf")
	if err != nil {
		t.Fatalf("reading the embedded instance variables.tf: %v", err)
	}
	clusterTF, err := fs.ReadFile(assets.OpenTofuCluster(), "variables.tf")
	if err != nil {
		t.Fatalf("reading the embedded cluster variables.tf: %v", err)
	}
	relationalDays := windowDays(t, tofuDefault(t, clusterTF, "backup_retention_rdb"))
	eventDays := windowDays(t, tofuDefault(t, instanceTF, "backup_retention_tsdb"))
	store := tofuDefault(t, clusterTF, "backup_object_store_storage")

	// Control: with one 7-day window for both stores -- the shape before each store
	// had its own -- the formula reproduces the "about 150" published for it then.
	// If it does not, it no longer computes what it claims to.
	if got := steadyStateEventsPerSecond(t, store, relationalArchiveShare, 7, 7); got < 145 || got > 155 {
		t.Fatalf("a shared 7-day window gives %.1f events/s against %s; the figure published "+
			"for that shape was about 150, so the formula has drifted", got, store)
	}

	measured := steadyStateEventsPerSecond(t, store, relationalArchiveShare, relationalDays, eventDays)
	compactMeasured := steadyStateEventsPerSecond(t, compact.ObjectStoreStorage, relationalArchiveShare,
		relationalDays, eventDays)
	allRelational := steadyStateEventsPerSecond(t, store, 1, relationalDays, eventDays)
	t.Logf("%s store, %dd relational / %dd event windows: %.1f events/s at share %.2f, %.1f at share 1",
		store, relationalDays, eventDays, measured, relationalArchiveShare, allRelational)
	t.Logf("--compact's %s store at the same windows: %.1f events/s at share %.2f",
		compact.ObjectStoreStorage, compactMeasured, relationalArchiveShare)

	// "About N" is honest when N is within 10% of the computed figure.
	near := func(published, computed float64) bool {
		return math.Abs(published-computed) <= 0.1*computed
	}

	// The docs are outside this module, so `go test` does not track them for its
	// cache: run with -count=1, as CI does.
	docs := filepath.Join("..", "..", "..", "docs")
	readDoc := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(docs, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		return string(b)
	}
	enBootstrap := readDoc(filepath.Join("docs", "deployment", "bootstrap.md"))
	esBootstrap := readDoc(filepath.Join("i18n", "es", "docusaurus-plugin-content-docs", "current",
		"deployment", "bootstrap.md"))
	// compact.go is this package's own source, so the cache does track it.
	compactSrc, err := os.ReadFile("compact.go")
	if err != nil {
		t.Fatalf("reading compact.go: %v", err)
	}

	// Each space in a pattern matches any run of whitespace, so reflowing the prose
	// -- a line break moving inside the sentence -- does not break the match.
	for _, tc := range []struct {
		what     string
		text     string
		pattern  string
		computed float64
		store    string
	}{
		{"backup_object_store_storage's description", string(clusterTF),
			`about (\d+) events/s sustained fills this default`, measured, store},
		{"backup_object_store_storage's description", string(clusterTF),
			`the figure would be about (\d+) events/s`, allRelational, store},
		{"bootstrap.md#backup-store-size", enBootstrap,
			`about (\d+) events per second sustained fills the default store`, measured, store},
		{"bootstrap.md#backup-store-size", enBootstrap,
			`the figure would be about (\d+) events per second`, allRelational, store},
		{"the es bootstrap.md#backup-store-size", esBootstrap,
			`unos (\d+) eventos por segundo sostenidos llenan el almacén predeterminado`, measured, store},
		{"the es bootstrap.md#backup-store-size", esBootstrap,
			`la cifra sería de unos (\d+) eventos`, allRelational, store},
		// compact.go's comment on ObjectStoreStorage: the steady-state rate for the
		// --compact store, at the same shipped windows.
		{"compact.go's ObjectStoreStorage comment", string(compactSrc),
			`(?:\d+)Gi fills at about (\d+) events/s sustained`, compactMeasured, compact.ObjectStoreStorage},
	} {
		pattern := regexp.MustCompile(strings.ReplaceAll(tc.pattern, " ", `\s+`))
		for _, published := range publishedFigures(t, tc.what, tc.text, pattern) {
			if !near(published, tc.computed) {
				t.Errorf("%s says about %.0f events/s; the shipped defaults (%s store, %dd relational "+
					"and %dd event-store windows) give %.1f. Change the prose with the defaults, in "+
					"both locales and the variable description.", tc.what, published, tc.store,
					relationalDays, eventDays, tc.computed)
			}
		}
	}
}

// objectStoreIntervalDays is how many days the default object-store schedule used
// with volume-snapshot base backups leaves between base backups: 7 for a weekly one.
// Anything but a plain weekly or daily six-field schedule fails, rather than guessing.
func objectStoreIntervalDays(t *testing.T, schedule string) int64 {
	t.Helper()
	f := strings.Fields(schedule)
	switch {
	case len(f) == 6 && f[3] == "*" && f[4] == "*" && regexp.MustCompile(`^[0-6]$`).MatchString(f[5]):
		return 7
	case len(f) == 6 && f[3] == "*" && f[4] == "*" && f[5] == "*":
		return 1
	}
	t.Fatalf("object-store schedule %q is neither weekly nor daily; this test does not guess its interval",
		schedule)
	return 0
}

// With volume-snapshot base backups the object-store base backup is weekly, and barman
// keeps the newest base before each window and every segment since: up to one interval
// MORE log for each database. The steady-state figures published for that mode are the
// ones the shipped defaults give with each window lengthened by that interval.
func TestSnapshotModeSteadyStateRateIsTheOnePublished(t *testing.T) {
	instanceTF, err := fs.ReadFile(assets.OpenTofuInstance(), "variables.tf")
	if err != nil {
		t.Fatalf("reading the embedded instance variables.tf: %v", err)
	}
	clusterTF, err := fs.ReadFile(assets.OpenTofuCluster(), "variables.tf")
	if err != nil {
		t.Fatalf("reading the embedded cluster variables.tf: %v", err)
	}
	extra := objectStoreIntervalDays(t, tofuDefault(t, clusterTF, "backup_object_store_schedule"))
	if got := objectStoreIntervalDays(t, tofuDefault(t, instanceTF, "backup_object_store_schedule")); got != extra {
		t.Fatalf("the two roots' object-store schedules leave %d and %d days between base backups; "+
			"the published figure assumes one", extra, got)
	}
	relationalDays := windowDays(t, tofuDefault(t, clusterTF, "backup_retention_rdb")) + extra
	eventDays := windowDays(t, tofuDefault(t, instanceTF, "backup_retention_tsdb")) + extra
	store := tofuDefault(t, clusterTF, "backup_object_store_storage")

	measured := steadyStateEventsPerSecond(t, store, relationalArchiveShare, relationalDays, eventDays)
	allRelational := steadyStateEventsPerSecond(t, store, 1, relationalDays, eventDays)
	daily := steadyStateEventsPerSecond(t, store, relationalArchiveShare, relationalDays-extra, eventDays-extra)
	t.Logf("%s store with volume-snapshot base backups (%dd relational / %dd event log): %.1f events/s at "+
		"share %.2f, %.1f at share 1; %.1f with daily object-store base backups", store, relationalDays,
		eventDays, measured, relationalArchiveShare, allRelational, daily)
	// The control: the trade this publishes is real -- snapshots cost store room.
	if measured >= daily {
		t.Fatalf("with snapshots the store fills at %.1f events/s, not below the %.1f of daily base backups; "+
			"the arithmetic no longer says what the docs claim", measured, daily)
	}

	near := func(published, computed float64) bool { return math.Abs(published-computed) <= 0.1*computed }
	docs := filepath.Join("..", "..", "..", "docs")
	readDoc := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(docs, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		return string(b)
	}
	enBootstrap := readDoc(filepath.Join("docs", "deployment", "bootstrap.md"))
	esBootstrap := readDoc(filepath.Join("i18n", "es", "docusaurus-plugin-content-docs", "current",
		"deployment", "bootstrap.md"))
	enRelease := readDoc(filepath.Join("docs", "deployment", "releases-and-upgrades.md"))
	esRelease := readDoc(filepath.Join("i18n", "es", "docusaurus-plugin-content-docs", "current",
		"deployment", "releases-and-upgrades.md"))

	for _, tc := range []struct {
		what, text, pattern string
		computed            float64
	}{
		{"backup_object_store_storage's description", string(clusterTF),
			`fills at about (\d+) events/s sustained with snapshots`, measured},
		{"backup_object_store_storage's description", string(clusterTF),
			`and at about (\d+) if the relational store's log were all of it`, allRelational},
		{"bootstrap.md#snapshot-base-backups", enBootstrap,
			`volume-snapshot base backups fill the store at about (\d+) events per second sustained`, measured},
		{"bootstrap.md#snapshot-base-backups", enBootstrap,
			`and at about (\d+) if the relational database's log were all of it`, allRelational},
		{"the es bootstrap.md#snapshot-base-backups", esBootstrap,
			`las copias base como instantáneas de volumen llenan el almacén a unos (\d+) eventos por segundo sostenidos`, measured},
		{"the es bootstrap.md#snapshot-base-backups", esBootstrap,
			`y a unos (\d+) si todo fuera registro de la base de datos relacional`, allRelational},
		{"the release note", enRelease,
			`at about (\d+) events per second of sustained ingest rather than about`, measured},
		{"the es release note", esRelease,
			`a unos (\d+) eventos por segundo de ingesta sostenida en lugar de unos`, measured},
	} {
		pattern := regexp.MustCompile(strings.ReplaceAll(tc.pattern, " ", `\s+`))
		for _, published := range publishedFigures(t, tc.what, tc.text, pattern) {
			if !near(published, tc.computed) {
				t.Errorf("%s says about %.0f events/s with volume-snapshot base backups; the shipped defaults "+
					"(%s store, %dd relational and %dd event-store log) give %.1f. Change the prose with the "+
					"defaults, in both locales and the variable description.", tc.what, published, store,
					relationalDays, eventDays, tc.computed)
			}
		}
	}
}
