// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"regexp"
	"testing"

	"sigs.k8s.io/yaml"

	assets "github.com/devicechain-io/dc-deploy"
)

// archiveCompression is what one rendered ObjectStore compresses with: the
// write-ahead log it archives and the base backups it takes.
type archiveCompression struct {
	wal, data string
}

// renderedArchiveCompressions renders the cnpg-cluster chart (renderCNPGChart)
// with the given backup block, or none when it is nil, and returns
// spec.configuration.wal.compression and spec.configuration.data.compression of
// every ObjectStore it renders.
func renderedArchiveCompressions(t *testing.T, backup map[string]interface{}) []archiveCompression {
	t.Helper()
	var out []archiveCompression
	for _, doc := range renderCNPGChart(t, nil, backup) {
		var obj struct {
			Kind string `json:"kind"`
			Spec struct {
				Configuration struct {
					Wal struct {
						Compression string `json:"compression"`
					} `json:"wal"`
					Data struct {
						Compression string `json:"compression"`
					} `json:"data"`
				} `json:"configuration"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("decoding a rendered document: %v\n%s", err, doc)
		}
		if obj.Kind == "ObjectStore" {
			out = append(out, archiveCompression{
				wal:  obj.Spec.Configuration.Wal.Compression,
				data: obj.Spec.Configuration.Data.Compression,
			})
		}
	}
	return out
}

// Both databases archive their write-ahead log with zstd, read by VALUE off the
// ObjectStore the chart renders. The module's own value reaching the chart is the
// module's OpenTofu test (tests/backup_archive.tftest.hcl); this covers what the
// chart does with it: its default, and the template passing a value through.
//
// zstd is not only the cheaper choice. The backup-store sizing
// (archiveBytesPerEventHigh) was measured with a gzip archive, and it bounds
// another compression only while that one's archive is no larger. zstd's was
// smaller on every log measured; none, snappy and barman's default lz4 level were
// larger. So a change here re-derives that constant first.
func TestTheArchiveCompressesTheWriteAheadLogWithZstd(t *testing.T) {
	// The control: with backups off there is no ObjectStore, so the finder reads
	// ObjectStores and nothing else, and a pass below is about that object.
	if got := renderedArchiveCompressions(t, nil); len(got) != 0 {
		t.Fatalf("with backups off the chart rendered %d ObjectStores, want 0", len(got))
	}

	t.Run("the chart's default", func(t *testing.T) {
		got := renderedArchiveCompressions(t, defaultBackupValues())
		if len(got) != 1 {
			t.Fatalf("the chart rendered %d ObjectStores with backups on, want exactly 1", len(got))
		}
		if got[0].wal != "zstd" {
			t.Errorf("the ObjectStore archives the write-ahead log with %q, want \"zstd\": zstd took 29-39%% less "+
				"archiver CPU per segment than gzip, and the backup-store sizing (archiveBytesPerEventHigh) bounds only an "+
				"archive no larger than gzip's -- re-derive it before choosing anything else", got[0].wal)
		}
		// The base backups stay gzip. The two settings sit side by side in the
		// ObjectStore and no longer share a value, but they do not share a
		// vocabulary either: the pinned plugin's CRD accepts zstd for the
		// write-ahead log and only bzip2, gzip, lz4 or snappy for base backups,
		// so the API server refuses an ObjectStore that hands zstd to the second.
		if got[0].data != "gzip" {
			t.Errorf("the ObjectStore compresses base backups with %q, want \"gzip\": the plugin's CRD does not accept "+
				"zstd for base backups, and gzip is what they have always used", got[0].data)
		}
	})

	// The module always sets walCompression, so the template must pass it through
	// rather than print a value of its own. lz4 is a value no default has.
	t.Run("the module's value is what renders", func(t *testing.T) {
		b := defaultBackupValues()
		b["walCompression"] = "lz4"
		got := renderedArchiveCompressions(t, b)
		if len(got) != 1 {
			t.Fatalf("the chart rendered %d ObjectStores with backups on, want exactly 1", len(got))
		}
		if got[0].wal != "lz4" {
			t.Errorf("handed walCompression \"lz4\", the ObjectStore renders %q: the template does not use the value it is given", got[0].wal)
		}
		if got[0].data != "gzip" {
			t.Errorf("handed walCompression \"lz4\", the ObjectStore compresses base backups with %q, want \"gzip\": "+
				"the write-ahead log's setting reached the base backups", got[0].data)
		}
	})
}

// The event store archives with four parallel uploads, read by VALUE off the
// instance root's own literal. The module's test feeds 4 in and checks it reaches
// the chart; this is the value an install actually hands it. The relational
// store, in the cluster root, sets none and takes the module's 2.
//
// Four is what kept the archiver up: on GKE at up to 6,800 events/s offered the
// event store reported no failed archives at 4. Parallelism does not change the
// archiver's CPU per segment, so the cheaper compression is no reason to lower it.
func TestTheEventStoreArchivesFourSegmentsAtOnce(t *testing.T) {
	body := backupLocalBody(t, assets.InstanceRootDir, "tsdb_backup")

	// A case known to be positive: the block this reads also wires the event
	// store's retention, so if that line is missing the reader is pointed at
	// something else and an answer below would be about nothing.
	if !regexp.MustCompile(`\n retention_policy = var\.backup_retention_tsdb\n`).MatchString(body) {
		t.Fatalf("local.tsdb_backup in the instance root does not read as the event store's backup block (no "+
			"retention_policy = var.backup_retention_tsdb):%s", body)
	}

	m := regexp.MustCompile(`\n wal_max_parallel = ([^\n]*)\n`).FindAllStringSubmatch(body, -1)
	if len(m) != 1 {
		t.Fatalf("local.tsdb_backup sets wal_max_parallel %d times, want exactly once", len(m))
	}
	if got := m[0][1]; got != "4" {
		t.Errorf("the event store archives wal_max_parallel = %s, want 4: an archiver that falls behind leaves "+
			"segments on the database's own volume until it fills", got)
	}

	// The relational store takes the module's default. A value set here would be
	// a second decision about that store that this test does not see.
	if rdb := backupLocalBody(t, assets.ClusterRootDir, "rdb_backup"); regexp.MustCompile(`\n wal_max_parallel =`).MatchString(rdb) {
		t.Errorf("local.rdb_backup in the cluster root sets wal_max_parallel; the relational store takes the module's default of 2")
	}
}
