// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"sigs.k8s.io/yaml"
)

// renderedArchiveCompressions renders the cnpg-cluster chart (renderCNPGChart)
// with the given backup block, or none when it is nil, and returns
// spec.configuration.wal.compression of every ObjectStore it renders.
func renderedArchiveCompressions(t *testing.T, backup map[string]interface{}) []string {
	t.Helper()
	var out []string
	for _, doc := range renderCNPGChart(t, nil, backup) {
		var obj struct {
			Kind string `json:"kind"`
			Spec struct {
				Configuration struct {
					Wal struct {
						Compression string `json:"compression"`
					} `json:"wal"`
				} `json:"configuration"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("decoding a rendered document: %v\n%s", err, doc)
		}
		if obj.Kind == "ObjectStore" {
			out = append(out, obj.Spec.Configuration.Wal.Compression)
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
		if got[0] != "zstd" {
			t.Errorf("the ObjectStore archives the write-ahead log with %q, want \"zstd\": zstd took 29-39%% less "+
				"archiver CPU per segment than gzip, and the backup-store sizing (archiveBytesPerEventHigh) bounds only an "+
				"archive no larger than gzip's -- re-derive it before choosing anything else", got[0])
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
		if got[0] != "lz4" {
			t.Errorf("handed walCompression \"lz4\", the ObjectStore renders %q: the template does not use the value it is given", got[0])
		}
	})
}
