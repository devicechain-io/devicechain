// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
)

// The disk a default install claims is published in three places: the
// prerequisites (both locales), and the Google Kubernetes Engine guide, which turns
// it into the SSD quota to request before installing. Every figure there is a SUM of
// shipped defaults, not an estimate, so it is held to the defaults by exact
// equality: a default that moves without the prose fails here.
//
// Units: the docs say GiB and the GKE guide says GB, and they are compared as
// equal on purpose. Google Cloud's disk "GB" is 2^30 bytes -- a 32Gi volume is
// charged as 32 GB of quota, which is what makes the 484 control below come out.
// Do not "correct" it by 1.0737.

// diskInputs are the volume sizes, in GiB, and the replica counts of a default
// install. Pure, so the formula's shape can be checked against a measurement with
// literal sizes, independently of today's defaults.
type diskInputs struct {
	dbReplicas                          int64 // N in "(var.ha ? N : 1)", both database roots
	natsReplicas                        int64 // NATS servers, which dcctl passes explicitly
	relational, backupStore, prometheus int64 // cluster root
	eventStore, jetStream               int64 // instance root, per instance
}

func (d diskInputs) clusterGiB() int64 {
	return d.dbReplicas*d.relational + d.backupStore + d.prometheus
}

func (d diskInputs) perInstanceGiB() int64 {
	return d.dbReplicas*d.eventStore + d.natsReplicas*d.jetStream
}

// haDatabaseReplicas reads N from "(var.ha ? N : 1)" in the cluster root's main.tf
// (the relational store) and the instance root's (the event store), failing unless
// both say the same N: a one-root reading would let the other move unnoticed.
func haDatabaseReplicas(t *testing.T) int64 {
	t.Helper()

	derive := regexp.MustCompile(`(?m)^\s*instances\s*=\s*var\.(postgres|timescale)_instances\s*!=\s*0\s*\?\s*` +
		`var\.(?:postgres|timescale)_instances\s*:\s*\(var\.ha\s*\?\s*([0-9]+)\s*:\s*1\)`)
	read := func(what string, root fs.FS) int64 {
		b, err := fs.ReadFile(root, "main.tf")
		if err != nil {
			t.Fatalf("reading the embedded %s main.tf: %v", what, err)
		}
		m := derive.FindAllSubmatch(b, -1)
		if len(m) != 1 {
			t.Fatalf("the %s main.tf has %d database replica derivations, not one; point this test "+
				"at the new shape", what, len(m))
		}
		n, err := strconv.ParseInt(string(m[0][2]), 10, 64)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return n
	}
	relational := read("cluster", assets.OpenTofuCluster())
	event := read("instance", assets.OpenTofuInstance())
	if relational != event {
		t.Fatalf("under --ha the relational store runs %d instances and the event store %d; the published "+
			"budget counts one replica count for both databases", relational, event)
	}
	return relational
}

// tofuNumberDefault is tofuDefault for an unquoted whole-number default. It reads
// only inside the named variable's own block, so a default this variable lacks is
// never taken from the next one's.
func tofuNumberDefault(t *testing.T, variablesTF []byte, name string) int64 {
	t.Helper()

	start := regexp.MustCompile(`variable\s+"` + regexp.QuoteMeta(name) + `"\s*\{`).FindIndex(variablesTF)
	if start == nil {
		t.Fatalf("no variable %q", name)
	}
	block := variablesTF[start[1]:]
	if next := regexp.MustCompile(`(?m)^variable\s+"`).FindIndex(block); next != nil {
		block = block[:next[0]]
	}
	m := regexp.MustCompile(`(?m)^\s*default\s*=\s*([0-9]+)\s*(?:#.*)?$`).FindSubmatch(block)
	if m == nil {
		t.Fatalf("variable %q has no whole-number default", name)
	}
	n, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		t.Fatalf("variable %q: %v", name, err)
	}
	return n
}

// wholeGiB converts a volume default to GiB, failing on anything that is not a
// whole number of GiB rather than rounding it into a plausible figure.
func wholeGiB(t *testing.T, size string) int64 {
	t.Helper()

	m := volumeMiB(t, size)
	if m%1024 != 0 {
		t.Fatalf("volume size %q is not a whole number of GiB; the published budget is in whole GiB", size)
	}
	return m / 1024
}

// defaultDiskInputs reads every size from the embedded roots' variables.tf.
func defaultDiskInputs(t *testing.T, dbReplicas, natsReplicas int64) diskInputs {
	t.Helper()

	clusterTF, err := fs.ReadFile(assets.OpenTofuCluster(), "variables.tf")
	if err != nil {
		t.Fatalf("reading the embedded cluster variables.tf: %v", err)
	}
	instanceTF, err := fs.ReadFile(assets.OpenTofuInstance(), "variables.tf")
	if err != nil {
		t.Fatalf("reading the embedded instance variables.tf: %v", err)
	}
	return diskInputs{
		dbReplicas:   dbReplicas,
		natsReplicas: natsReplicas,
		relational:   wholeGiB(t, tofuDefault(t, clusterTF, relationalVolumeVar)),
		backupStore:  wholeGiB(t, tofuDefault(t, clusterTF, backupStoreVolumeVar)),
		prometheus:   wholeGiB(t, tofuDefault(t, clusterTF, prometheusVolumeVar)),
		eventStore:   wholeGiB(t, tofuDefault(t, instanceTF, eventStoreVolumeVar)),
		jetStream:    wholeGiB(t, tofuDefault(t, instanceTF, jetStreamVolumeVar)),
	}
}

// The root variables the budget counts, one per volume. TestDiskBudgetCountsEveryVolume
// holds this list to what the roots actually size, so the list cannot quietly be
// shorter than the install.
const (
	relationalVolumeVar  = "postgres_storage"
	backupStoreVolumeVar = "backup_object_store_storage"
	prometheusVolumeVar  = "monitoring_prometheus_storage"
	eventStoreVolumeVar  = "timescale_storage"
	jetStreamVolumeVar   = "nats_jetstream_storage"
)

// The budget is a fixed list of volumes, so a volume the list does not know about
// would leave every published figure passing TestPublishedDiskBudgetIsTheDefaults
// while the install claims more: a dedicated WAL volume on either database (the
// cnpg-cluster module already offers one) is the obvious case. A volume is sized
// in exactly two places -- a root passing a *storage attribute to a module, or a
// module's own *storage default that no root overrides -- and this checks both.
func TestDiskBudgetCountsEveryVolume(t *testing.T) {
	tree := assets.OpenTofu()
	sizeAttr := regexp.MustCompile(`(?m)^\s*([a-z_]*storage)\s*=\s*(\S.*?)\s*(?:#.*)?$`)
	moduleBlock := regexp.MustCompile(`(?ms)^module\s+"[^"]+"\s*\{\s*$(.*?)^\}`)
	moduleSource := regexp.MustCompile(`(?m)^\s*source\s*=\s*"\.\./modules/([^"]+)"`)

	counted := map[string]bool{"var." + relationalVolumeVar: true, "var." + backupStoreVolumeVar: true,
		"var." + prometheusVolumeVar: true, "var." + eventStoreVolumeVar: true, "var." + jetStreamVolumeVar: true}
	seen := map[string]int{}
	// passed[module] = the *storage attributes some root sets on it.
	passed := map[string]map[string]bool{}
	for _, root := range []string{"cluster", "instance"} {
		b, err := fs.ReadFile(tree, root+"/main.tf")
		if err != nil {
			t.Fatalf("reading the embedded %s main.tf: %v", root, err)
		}
		for _, blk := range moduleBlock.FindAllSubmatch(b, -1) {
			src := moduleSource.FindSubmatch(blk[1])
			if src == nil {
				continue
			}
			mod := string(src[1])
			if passed[mod] == nil {
				passed[mod] = map[string]bool{}
			}
			for _, a := range sizeAttr.FindAllSubmatch(blk[1], -1) {
				attr, value := string(a[1]), string(a[2])
				passed[mod][attr] = true
				if !counted[value] {
					t.Errorf("the %s root sizes a volume on module %s with %s = %s, which the published disk "+
						"budget does not count. Add it to diskInputs and to the prose in both locales and "+
						"the GKE README, or do not size it here", root, mod, attr, value)
					continue
				}
				seen[value]++
			}
		}
	}
	for v := range counted {
		if seen[v] != 1 {
			t.Errorf("%s sizes %d module volumes across the roots; the budget counts it once", v, seen[v])
		}
	}
	if len(passed) == 0 {
		t.Fatal("found no module calls in the roots; point this test at the new shape")
	}

	variableBlock := regexp.MustCompile(`(?ms)^variable\s+"([a-z_]*storage)"\s*\{\s*$(.*?)^\}`)
	defaultLine := regexp.MustCompile(`(?m)^\s*default\s*=\s*(\S.*?)\s*$`)
	for mod := range passed {
		files, err := fs.Glob(tree, "modules/"+mod+"/*.tf")
		if err != nil || len(files) == 0 {
			t.Fatalf("module %s has no embedded .tf files (%v)", mod, err)
		}
		for _, f := range files {
			b, err := fs.ReadFile(tree, f)
			if err != nil {
				t.Fatalf("reading %s: %v", f, err)
			}
			for _, v := range variableBlock.FindAllSubmatch(b, -1) {
				name := string(v[1])
				if passed[mod][name] {
					continue
				}
				def := "none"
				if d := defaultLine.FindSubmatch(v[2]); d != nil {
					def = string(d[1])
				}
				if def != `""` {
					t.Errorf("module %s sizes a volume with %s (default %s) that no root sets, so every "+
						"install claims it and the published disk budget does not count it", mod, name, def)
				}
			}
		}
	}
}

func TestPublishedDiskBudgetIsTheDefaults(t *testing.T) {
	// Control: the formula against the one measurement there is. When the event
	// store's last two replicas could not get their volumes, Google Cloud reported
	// 484 GB of SSD in use: four 50 GB boot disks (three platform nodes and a
	// load-generator node) and every volume of an --ha cluster with one instance
	// but those two. The decomposition is the run's own, so this shows the formula
	// is consistent with the measured total, not that it is proven by it; it does
	// catch a formula that drops or double-counts a volume. Literals, not the
	// defaults, so a legitimate default change does not trip it.
	measured := diskInputs{dbReplicas: 3, natsReplicas: 3, relational: 8, backupStore: 160, prometheus: 20,
		eventStore: 32, jetStream: 16}
	if got := 4*50 + measured.clusterGiB() + measured.perInstanceGiB() - 2*measured.eventStore; got != 484 {
		t.Fatalf("the formula gives %d GB for the measured install; Google Cloud reported 484", got)
	}

	ha := defaultDiskInputs(t, haDatabaseReplicas(t), int64(haFor(true).ServerReplicas))
	single := defaultDiskInputs(t, 1, int64(haFor(false).ServerReplicas))
	t.Logf("--ha: %+v, cluster %d GiB, per instance %d GiB", ha, ha.clusterGiB(), ha.perInstanceGiB())
	t.Logf("without --ha: %+v, cluster %d GiB, per instance %d GiB", single, single.clusterGiB(),
		single.perInstanceGiB())

	// deploy/gke is not embedded in dcctl, so it is read from disk like the docs.
	// Neither is tracked by `go test`'s cache: run with -count=1, as CI does.
	repo := filepath.Join("..", "..", "..")
	read := func(rel ...string) string {
		b, err := os.ReadFile(filepath.Join(append([]string{repo}, rel...)...))
		if err != nil {
			t.Fatalf("reading %s: %v", filepath.Join(rel...), err)
		}
		return string(b)
	}
	gkeTF := []byte(read("deploy", "gke", "variables.tf"))
	nodes := tofuNumberDefault(t, gkeTF, "node_count")
	boot := tofuNumberDefault(t, gkeTF, "node_disk_size_gb")
	if diskType := tofuDefault(t, gkeTF, "node_disk_type"); diskType != "pd-balanced" && diskType != "pd-ssd" {
		t.Fatalf("the GKE boot disks default to %s; the guide counts them against SSD_TOTAL_GB, which a "+
			"pd-standard disk does not draw on. Rewrite its quota step", diskType)
	}

	oneInstance := ha.clusterGiB() + ha.perInstanceGiB()
	withBoot := oneInstance + nodes*boot
	// One load-generator node and the one surge node GKE adds while it upgrades a pool.
	floor := oneInstance + (nodes+2)*boot
	t.Logf("GKE: %d nodes × %d GB boot; one instance %d, with boot disks %d, floor to request %d",
		nodes, boot, oneInstance, withBoot, floor)

	readme := read("deploy", "gke", "README.md")
	enBootstrap := read("docs", "docs", "deployment", "bootstrap.md")
	esBootstrap := read("docs", "i18n", "es", "docusaurus-plugin-content-docs", "current", "deployment",
		"bootstrap.md")

	// Each space in a pattern matches any run of whitespace, so reflowing the prose
	// does not break the match.
	figures := func(what, text, pattern string) []int64 {
		var out []int64
		for _, f := range publishedFigures(t, what, text, regexp.MustCompile(strings.ReplaceAll(pattern, " ", `\s+`))) {
			out = append(out, int64(f))
		}
		return out
	}

	// The new-project quota the guide names, and the claim it makes about it: a
	// default install fits with almost nothing to spare, and the next 50 GB disk
	// does not fit. Both halves are checked, so the guide's "leaves almost none"
	// fails here the day the defaults make it false either way.
	quotas := figures("the GKE README's new-project quota", readme, `\*\*(\d+) GB of SSD per region\*\*`)
	quota := quotas[0]
	if withBoot > quota {
		t.Fatalf("a default --ha install with one instance needs %d GB, over the %d GB of a new project; "+
			"the README says it fits with almost none to spare", withBoot, quota)
	}
	if withBoot+boot <= quota {
		t.Fatalf("a default --ha install with one instance and one more node needs %d GB, within the %d GB "+
			"of a new project; the README says the next node takes it over", withBoot+boot, quota)
	}

	exact := []struct {
		what, text, pattern string
		want                int64
	}{
		{"the GKE README", readme, `leaves almost none of a new project's (\d+) GB`, quota},
		{"the GKE README", readme, `With one instance, DeviceChain claims (\d+) GB`, oneInstance},
		{"the GKE README", readme, `(\d+) GB of it the backup store`, ha.backupStore},
		{"the GKE README", readme, `boot disks add (\d+) GB`, nodes * boot},
		{"the GKE README", readme, `(\d+) GB in all`, withBoot},
		{"the GKE README", readme, `add (\d+) GB each`, boot},
		{"the GKE README", readme, `each further instance claims (\d+) GB more`, ha.perInstanceGiB()},
		{"the GKE README", readme, `about (\d+) GB more backup store`, ha.backupStore},

		{"bootstrap.md#prerequisites", enBootstrap, `claims (\d+) GiB for the cluster`, ha.clusterGiB()},
		{"bootstrap.md#prerequisites", enBootstrap, `\(#backup-store-size\) \((\d+) GiB\)`, ha.backupStore},
		{"bootstrap.md#prerequisites", enBootstrap, `Each instance claims (\d+) GiB more`, ha.perInstanceGiB()},
		{"bootstrap.md#prerequisites", enBootstrap, `(\d+) GiB for a cluster with one instance`, oneInstance},
		{"bootstrap.md#prerequisites", enBootstrap, `the cluster claims (\d+) GiB and each instance`, single.clusterGiB()},
		{"bootstrap.md#prerequisites", enBootstrap, `and each instance (\d+) GiB\.`, single.perInstanceGiB()},
		{"bootstrap.md#prerequisites", enBootstrap, `the backup store grown by about (\d+) GiB`, ha.backupStore},
		{"bootstrap.md#prerequisites", enBootstrap, `project allows (\d+) GB of SSD per region`, quota},
		{"bootstrap.md#snapshot-base-backups", enBootstrap, `gives the same (\d+) GiB with or without snapshots`,
			ha.backupStore},

		{"the es bootstrap.md#prerequisites", esBootstrap, `reclama (\d+) GiB para el clúster`, ha.clusterGiB()},
		{"the es bootstrap.md#prerequisites", esBootstrap, `\(#backup-store-size\) \((\d+) GiB\)`, ha.backupStore},
		{"the es bootstrap.md#prerequisites", esBootstrap, `Cada instancia reclama (\d+) GiB más`, ha.perInstanceGiB()},
		{"the es bootstrap.md#prerequisites", esBootstrap, `(\d+) GiB para un clúster con una instancia`, oneInstance},
		{"the es bootstrap.md#prerequisites", esBootstrap, `el clúster reclama (\d+) GiB y cada instancia`, single.clusterGiB()},
		{"the es bootstrap.md#prerequisites", esBootstrap, `y cada instancia (\d+) GiB\.`, single.perInstanceGiB()},
		{"the es bootstrap.md#prerequisites", esBootstrap, `ampliar el almacén de respaldos en unos (\d+) GiB`, ha.backupStore},
		{"the es bootstrap.md#prerequisites", esBootstrap, `permite (\d+) GB de SSD por región`, quota},
		{"the es bootstrap.md#snapshot-base-backups", esBootstrap, `da los mismos (\d+) GiB con o sin instantáneas`,
			ha.backupStore},
	}
	for _, tc := range exact {
		for _, published := range figures(tc.what, tc.text, tc.pattern) {
			if published != tc.want {
				t.Errorf("%s says %d (%q); the shipped defaults (%d database and %d NATS replicas; %d/%d/%d GiB "+
					"cluster, %d/%d GiB per instance; %d × %d GB boot) give %d. Change the prose with the "+
					"defaults, in both locales and the GKE README.", tc.what, published, tc.pattern,
					ha.dbReplicas, ha.natsReplicas, ha.relational, ha.backupStore, ha.prometheus,
					ha.eventStore, ha.jetStream, nodes, boot, tc.want)
			}
		}
	}

	// The quota to request is a round figure, so it is held to the floor it must
	// cover rather than to the floor itself.
	for _, published := range figures("the GKE README", readme, `quota of at least (\d+) GB`) {
		if published < floor {
			t.Errorf("the GKE README asks for an SSD quota of %d GB; one instance, a load-generator node and "+
				"the surge node of an upgrade need %d", published, floor)
		}
	}

	// The figures hold only on the path they are published for. On a local
	// cluster the monitoring stack keeps no volume, and --compact, --no-cnpg,
	// --no-monitoring and --backup-credentials-file each change a term (--no-cnpg
	// turns database backups off, so no in-cluster backup store is claimed; --no-tls
	// does only together with --compact, which is already named); a reword that drops the
	// scope would leave every number above true of a narrower case than it claims.
	for _, tc := range []struct{ what, text, pattern string }{
		{"bootstrap.md#prerequisites", enBootstrap,
			"On a cluster that is not local \\(not kind, minikube, k3d, docker-desktop or rancher-desktop\\), with the default " +
				"install settings \\(no `--compact`, `--no-cnpg`, `--no-monitoring` or `--backup-credentials-file`\\)"},
		{"the es bootstrap.md#prerequisites", esBootstrap,
			"En un clúster que no es local \\(ni kind, ni minikube, ni k3d, ni docker-desktop, ni rancher-desktop\\), con los ajustes " +
				"de install predeterminados \\(sin `--compact`, `--no-cnpg`, `--no-monitoring` ni `--backup-credentials-file`\\)"},
	} {
		if !regexp.MustCompile(strings.ReplaceAll(tc.pattern, " ", `\s+`)).MatchString(tc.text) {
			t.Errorf("%s no longer scopes the disk budget to a non-local cluster with the default install "+
				"settings; the figures are not true without that scope", tc.what)
		}
	}
}
