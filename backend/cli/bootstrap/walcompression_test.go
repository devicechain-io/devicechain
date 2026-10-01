// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"

	assets "github.com/devicechain-io/dc-deploy"
)

// tfStringMapAttr returns the entries of the map attribute `name` inside a block
// body (as tfBlockBody returns it).
//
// 🔴 FAIL-CLOSED. Every line between the braces must be blank, a comment, or a
// `"key" = "value"` / `key = "value"` string literal; anything else is a Fatal
// naming the line. A parser that skipped what it could not read would report a
// value written as an expression as ABSENT, and an absence is exactly the answer
// the test below fails on for a different reason.
func tfStringMapAttr(t *testing.T, body, name string) map[string]string {
	t.Helper()
	open := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s*=\s*\{\s*$`)
	loc := open.FindAllStringIndex(body, -1)
	if len(loc) != 1 {
		t.Fatalf("found %d `%s = {` attributes, want exactly 1", len(loc), name)
	}
	rest := body[loc[0][1]:]
	// The closing brace is found by its two-space indent, which tofu fmt keeps
	// stable. A wrong match fails closed: the line loop below Fatals on any line
	// it cannot read, and the caller requires the value it is looking for.
	end := strings.Index(rest, "\n  }")
	if end < 0 {
		t.Fatalf("the %s map is never closed", name)
	}
	entry := regexp.MustCompile(`^\s*"?([A-Za-z0-9_.]+)"?\s*=\s*"([^"$]*)"\s*$`)
	out := map[string]string{}
	for _, line := range strings.Split(rest[:end], "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		m := entry.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("the %s map holds a line that is not a string literal: %q (this test reads only literal values)", name, trimmed)
		}
		if _, dup := out[m[1]]; dup {
			t.Fatalf("the %s map sets %q twice", name, m[1])
		}
		out[m[1]] = m[2]
	}
	return out
}

// renderedClusterParameters renders the cnpg-cluster module's chart, as embedded
// in dcctl, with the given Postgres parameters and, when backups is true, the
// backup block the module hands it on a default install. It returns the Cluster's
// spec.postgresql.parameters.
//
// The release name and namespace are renderChartClientSide's placeholders. This
// chart DOES read .Release.Namespace (unlike the instance chart that function's
// comment is about), but only as the metadata.namespace of the objects it renders,
// never for the parameters read here, so the placeholder cannot change them.
func renderedClusterParameters(t *testing.T, params map[string]string, backups bool) map[string]string {
	t.Helper()
	src, err := fs.Sub(assets.OpenTofu(), "modules/cnpg-cluster/chart")
	if err != nil {
		t.Fatalf("locating the cnpg-cluster chart: %v", err)
	}
	ch, err := loadChartFS(src)
	if err != nil {
		t.Fatalf("loading the cnpg-cluster chart: %v", err)
	}
	p := map[string]interface{}{}
	for k, v := range params {
		p[k] = v
	}
	vals := map[string]interface{}{
		"name":                   "dc-tsdb",
		"imageName":              "example/operand:test",
		"instances":              1,
		"aliasServiceName":       "dc-timescaledb-single",
		"storage":                map[string]interface{}{"size": "32Gi"},
		"bootstrap":              map[string]interface{}{"database": "dc", "owner": "devicechain", "secretName": "dc-tsdb-app"},
		"sharedPreloadLibraries": []interface{}{"timescaledb"},
		"parameters":             p,
	}
	if backups {
		vals["backup"] = map[string]interface{}{
			"enabled":            true,
			"bucket":             "devicechain-tsdb",
			"endpointURL":        "http://dc-object-store.dc-system:9000",
			"credentialsSecret":  "dc-object-store-credentials",
			"accessKeyIdKey":     "MINIO_ROOT_USER",
			"secretAccessKeyKey": "MINIO_ROOT_PASSWORD",
		}
	}
	manifest, err := renderChartClientSide(context.Background(), ch, vals)
	if err != nil {
		t.Fatalf("rendering the cnpg-cluster chart: %v", err)
	}
	var found []map[string]string
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var obj struct {
			Kind string `json:"kind"`
			Spec struct {
				Postgresql struct {
					Parameters map[string]string `json:"parameters"`
				} `json:"postgresql"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("decoding a rendered document: %v\n%s", err, doc)
		}
		if obj.Kind == "Cluster" {
			found = append(found, obj.Spec.Postgresql.Parameters)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the chart rendered %d Clusters, want exactly 1", len(found))
	}
	return found[0]
}

// The event store compresses the full-page images in its write-ahead log. They
// were about half its WAL at soak scale, and lz4 cut the log written per event by
// 45% in a same-configuration comparison. Read by VALUE off the Cluster the chart
// renders from the instance root's own literal, with backups on and off: the
// chart merges archive_timeout into the map only when backups are on, so each
// branch is a separate way to lose the setting.
func TestTheEventStoreCompressesItsWriteAheadLog(t *testing.T) {
	body := tfBlockBody(t, readTF(t, assets.OpenTofuInstance(), "main.tf"), `module "cnpg_tsdb"`)
	params := tfStringMapAttr(t, body, "parameters")

	// A case known to be positive: if this is missing, the parser no longer reads
	// the map it is pointed at, and every answer below would be about nothing.
	if got := params["timescaledb.telemetry_level"]; got != "off" {
		t.Fatalf("read timescaledb.telemetry_level = %q off the event store's parameters, want \"off\": the parser no longer reads the map it is pointed at (read %v)", got, params)
	}

	for _, tc := range []struct {
		name    string
		backups bool
	}{
		{"backups off", false},
		{"backups on", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := renderedClusterParameters(t, params, tc.backups)
			if got := rendered["wal_compression"]; got != "lz4" {
				t.Errorf("the event store's Cluster renders wal_compression %q, want \"lz4\" (rendered parameters: %v)", got, rendered)
			}
			if got := rendered["timescaledb.telemetry_level"]; got != "off" {
				t.Errorf("the event store's Cluster renders timescaledb.telemetry_level %q, want \"off\"", got)
			}
			if tc.backups {
				// Proves the chart's backup branch ran, so this subtest is the path a
				// default install takes and not a second copy of the one above.
				if got := rendered["archive_timeout"]; got != "5min" {
					t.Errorf("with backups on the Cluster renders archive_timeout %q, want \"5min\": the backup branch did not run", got)
				}
			}
		})
	}
}
