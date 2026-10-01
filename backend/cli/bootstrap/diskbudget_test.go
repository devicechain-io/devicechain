// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
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
// it into whether one instance fits a new project's SSD quota and the quota to
// request before a second. Every figure there is a SUM of
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
	// The README prices every further node (a load generator, an upgrade's surge
	// node) at one boot-disk size, so the pools must agree on it. It also says every
	// node boots from a standard persistent disk, which counts against DISKS_TOTAL_GB
	// and leaves SSD_TOTAL_GB to the volumes. pd-balanced and pd-ssd boot disks draw
	// on SSD_TOTAL_GB, and any other type is one this test does not know the quota of,
	// so both fail rather than leave the guide's quota step describing the wrong disks.
	boot := tofuNumberDefault(t, gkeTF, "loadgen_disk_size_gb")
	var nodes, bootTotal int64
	for _, pool := range []string{"database", "services", "loadgen"} {
		if diskType := tofuDefault(t, gkeTF, pool+"_disk_type"); diskType != "pd-standard" {
			t.Fatalf("the GKE %s pool's boot disks default to %s; the guide says every node boots from a "+
				"pd-standard disk, which does not count against SSD_TOTAL_GB. Rewrite its quota step, both "+
				"bootstrap.md prerequisites and this test", pool, diskType)
		}
		size := tofuNumberDefault(t, gkeTF, pool+"_disk_size_gb")
		if size != boot {
			t.Fatalf("the GKE %s pool's boot disks default to %d GB and the load generator's to %d; the README "+
				"prices every node at one size", pool, size, boot)
		}
		if pool == "loadgen" {
			continue // optional, priced below as an extra node
		}
		n := tofuNumberDefault(t, gkeTF, pool+"_node_count")
		nodes += n
		bootTotal += n * size
	}

	// The boot disks are off the SSD quota (above), so the volumes are all of it.
	oneInstance := ha.clusterGiB() + ha.perInstanceGiB()
	// The quota the README asks for before a second instance, one that ingests
	// continuously and so also wants the backup store grown.
	floor := oneInstance + ha.perInstanceGiB() + ha.backupStore
	t.Logf("GKE: %d nodes, %d GB of standard boot disk; one instance %d GB of SSD, floor to request for two %d",
		nodes, bootTotal, oneInstance, floor)

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
	// default install with one instance fits on the guide's cluster. The volumes
	// are all that draw on it (the boot disks are held to pd-standard above), and
	// the day they outgrow it this fails, so the guide cannot keep saying it fits.
	quotas := figures("the GKE README's new-project quota", readme, `\*\*(\d+) GB of SSD per region\*\*`)
	quota := quotas[0]
	if oneInstance > quota {
		t.Fatalf("a default --ha install with one instance claims %d GB of SSD, over the %d GB of a new project; "+
			"the GKE README and both bootstrap.md prerequisites say it fits. Rewrite them and this test",
			oneInstance, quota)
	}
	const standardBoot = "standard persistent disks, which count against `DISKS_TOTAL_GB`"
	if !regexp.MustCompile(strings.ReplaceAll(regexp.QuoteMeta(standardBoot), " ", `\s+`)).MatchString(readme) {
		t.Errorf("the GKE README no longer says the boot disks are %q; the fit claim rests on it", standardBoot)
	}

	exact := []struct {
		what, text, pattern string
		want                int64
	}{
		{"the GKE README", readme, `fits a new project's (\d+) GB`, quota},
		{"the GKE README", readme, `With one instance, DeviceChain claims (\d+) GB`, oneInstance},
		{"the GKE README", readme, `(\d+) GB of it the backup store`, ha.backupStore},
		{"the GKE README", readme, `boot disks use (\d+) GB`, bootTotal},
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
					"cluster, %d/%d GiB per instance; %d nodes, %d GB of boot disk) give %d. Change the prose "+
					"with the defaults, in both locales and the GKE README.", tc.what, published, tc.pattern,
					ha.dbReplicas, ha.natsReplicas, ha.relational, ha.backupStore, ha.prometheus,
					ha.eventStore, ha.jetStream, nodes, bootTotal, tc.want)
			}
		}
	}

	// The quota to request is a round figure, so it is held to the floor it must
	// cover rather than to the floor itself.
	for _, published := range figures("the GKE README", readme, `quota of at least (\d+) GB`) {
		if published < floor {
			t.Errorf("the GKE README asks for an SSD quota of %d GB before a second instance; two instances, "+
				"the second ingesting continuously, need %d", published, floor)
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

	// The fit claim the prerequisites make is about the GKE guide's cluster, whose
	// nodes boot from standard disks, not about any cluster: a cluster whose boot
	// disks draw on the SSD quota can be over it. The scope is held here; that the
	// claim is true is held above (oneInstance against quota). The Spanish pattern
	// includes "una instancia" so that "una instancia no cabe" cannot match it.
	for _, tc := range []struct{ what, text, pattern string }{
		{"bootstrap.md#prerequisites", enBootstrap,
			"a default `--ha` install with one instance fits on the cluster the \\[Google Kubernetes Engine guide\\]"},
		{"the es bootstrap.md#prerequisites", esBootstrap,
			"una instalación `--ha` predeterminada con una instancia cabe en el clúster que crea la \\[guía de Google Kubernetes Engine\\]"},
	} {
		if !regexp.MustCompile(strings.ReplaceAll(tc.pattern, " ", `\s+`)).MatchString(tc.text) {
			t.Errorf("%s no longer says a default install with one instance fits on the cluster the GKE guide "+
				"creates (%d GiB of volumes against a new project's %d GB of SSD)", tc.what, oneInstance, quota)
		}
	}
}

// numberWord reads a small number the prose spells out.
func numberWord(t *testing.T, w string) int64 {
	t.Helper()

	words := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
		"eleven", "twelve"}
	for i, x := range words {
		if strings.EqualFold(w, x) {
			return int64(i)
		}
	}
	t.Fatalf("%q is not a number word this test reads; extend numberWord", w)
	return 0
}

var n2Machine = regexp.MustCompile(`^n2-(?:(standard|highcpu|highmem)-([0-9]+)|custom-([0-9]+)-([0-9]+))$`)

// n2Shape sizes an N2 machine type the way the GKE guide prints it: vCPUs and
// whole GB, where Google's GB is 2^30 bytes. A predefined type is named
// family-class-vCPUs, and its class fixes the memory per vCPU; a custom type is
// n2-custom-<vCPUs>-<MiB>. Anything else, extended memory included, and a custom
// memory that is not a whole number of GB, is an error rather than a guess.
func n2Shape(mt string) (vcpu, gb int64, err error) {
	m := n2Machine.FindStringSubmatch(mt)
	if m == nil {
		return 0, 0, fmt.Errorf("%s is not an N2 type this test can size; extend n2Shape", mt)
	}
	if m[1] != "" {
		vcpu, err = strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		gbPerVCPU := map[string]int64{"highcpu": 1, "standard": 4, "highmem": 8}
		return vcpu, vcpu * gbPerVCPU[m[1]], nil
	}
	vcpu, err = strconv.ParseInt(m[3], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	mib, err := strconv.ParseInt(m[4], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	if mib%1024 != 0 {
		return 0, 0, fmt.Errorf("%s has %d MiB, which is not a whole number of GB", mt, mib)
	}
	return vcpu, mib / 1024, nil
}

// The literals tell the conversions apart: 65536 MiB is 64 GB, where dividing by
// 1000 would give 65, and 6400 MiB is not a whole number of GB.
func TestN2Shape(t *testing.T) {
	for _, tc := range []struct {
		mt        string
		vcpu, gb  int64
		wantError bool
	}{
		{mt: "n2-highcpu-4", vcpu: 4, gb: 4},
		{mt: "n2-standard-4", vcpu: 4, gb: 16},
		{mt: "n2-highmem-8", vcpu: 8, gb: 64},
		{mt: "n2-custom-4-8192", vcpu: 4, gb: 8},
		{mt: "n2-custom-8-65536", vcpu: 8, gb: 64},
		{mt: "n2-custom-4-6400", wantError: true},
		{mt: "n2-custom-4-8192-ext", wantError: true},
		{mt: "n2d-standard-4", wantError: true},
		{mt: "e2-standard-4", wantError: true},
		{mt: "n2-ultracpu-4", wantError: true},
	} {
		vcpu, gb, err := n2Shape(tc.mt)
		switch {
		case tc.wantError && err == nil:
			t.Errorf("n2Shape(%s) = %d vCPU, %d GB; want an error", tc.mt, vcpu, gb)
		case !tc.wantError && err != nil:
			t.Errorf("n2Shape(%s): %v", tc.mt, err)
		case !tc.wantError && (vcpu != tc.vcpu || gb != tc.gb):
			t.Errorf("n2Shape(%s) = %d vCPU, %d GB; want %d, %d", tc.mt, vcpu, gb, tc.vcpu, tc.gb)
		}
	}
}

// The GKE guide describes the cluster its configuration creates: each pool's
// machine type, size and node count, the vCPUs that adds up to against the quota,
// and what a cluster from the earlier one-pool configuration needs while it is
// replaced. Every one of those is a function of the defaults in deploy/gke, so a
// default that moves without the prose fails here. The release notes are not
// checked: they record what a release changed, and stay true when a later one
// changes the defaults again.
func TestGKEGuideShapeIsTheDefaults(t *testing.T) {
	repo := filepath.Join("..", "..", "..")
	read := func(rel ...string) string {
		b, err := os.ReadFile(filepath.Join(append([]string{repo}, rel...)...))
		if err != nil {
			t.Fatalf("reading %s: %v", filepath.Join(rel...), err)
		}
		return string(b)
	}
	gkeTF := []byte(read("deploy", "gke", "variables.tf"))
	readme := read("deploy", "gke", "README.md")

	shape := func(pool string) (mt string, vcpu, gb int64) {
		mt = tofuDefault(t, gkeTF, pool+"_machine_type")
		vcpu, gb, err := n2Shape(mt)
		if err != nil {
			t.Fatalf("the GKE %s pool defaults to %s: %v", pool, mt, err)
		}
		return mt, vcpu, gb
	}

	figures := func(what, pattern string) [][]string {
		ms := regexp.MustCompile(strings.ReplaceAll(pattern, " ", `\s+`)).FindAllStringSubmatch(readme, -1)
		if len(ms) == 0 {
			t.Fatalf("the GKE README no longer matches %s (%s); if the sentence was reworded, point this "+
				"test at the new wording rather than deleting the check", pattern, what)
		}
		return ms
	}
	num := func(s string) int64 {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return numberWord(t, s)
		}
		return n
	}
	expect := func(what string, got, want int64) {
		if got != want {
			t.Errorf("the GKE README says %d for %s; the defaults in deploy/gke/variables.tf give %d", got, what, want)
		}
	}

	var nodes, vcpus, maxNodeVCPU int64
	gbOf := map[string]int64{}
	for _, pool := range []string{"database", "services"} {
		mt, vcpu, gb := shape(pool)
		n := tofuNumberDefault(t, gkeTF, pool+"_node_count")
		nodes += n
		vcpus += n * vcpu
		maxNodeVCPU = max(maxNodeVCPU, vcpu)
		gbOf[pool] = gb
		for _, m := range figures(pool+"'s row", "\\| A `"+pool+"` node pool[^|\\n]*\\| ([0-9]+) × `([a-z0-9-]+)` \\(([0-9]+) vCPU, ([0-9]+) GB\\)") {
			expect(pool+" nodes in the table", num(m[1]), n)
			if m[2] != mt {
				t.Errorf("the GKE README's table gives the %s pool %s; it defaults to %s", pool, m[2], mt)
			}
			expect(pool+"'s vCPUs per node in the table", num(m[3]), vcpu)
			expect(pool+"'s GB per node in the table", num(m[4]), gb)
		}
	}
	_, loadgenVCPU, _ := shape("loadgen")

	for _, m := range figures("node sizes", "the `database` pool has ([0-9]+) GB nodes and the `services` pool ([0-9]+) GB ones") {
		expect("the database pool's GB per node", num(m[1]), gbOf["database"])
		expect("the services pool's GB per node", num(m[2]), gbOf["services"])
	}
	for _, m := range figures("the default vCPUs", "(?:The defaults use|The default cluster uses) ([0-9]+) vCPUs, and ([0-9]+) with a load-generator node") {
		expect("the default cluster's vCPUs", num(m[1]), vcpus)
		expect("the vCPUs with a load-generator node", num(m[2]), vcpus+loadgenVCPU)
	}
	for _, m := range figures("an upgrade's surge node", "takes ([0-9]+) more, which reaches ([0-9]+)") {
		expect("an upgrade's surge node's vCPUs", num(m[1]), maxNodeVCPU)
		expect("the vCPUs during an upgrade with a load generator", num(m[2]), vcpus+loadgenVCPU+maxNodeVCPU)
	}
	for _, m := range figures("the default node count", "the default ([a-z]+) nodes' boot disks") {
		expect("the default cluster's nodes", num(m[1]), nodes)
	}

	// A cluster from the earlier configuration had one pool of three 8-vCPU nodes;
	// while it is replaced the project may hold both shapes.
	const oldNodes, oldVCPUs = 3, 24
	for _, m := range figures("both shapes at once", "up to ([0-9]+) vCPUs and the boot disks of ([a-z]+) nodes") {
		expect("the vCPUs of both shapes", num(m[1]), oldVCPUs+vcpus)
		expect("the nodes of both shapes", num(m[2]), oldNodes+nodes)
	}
}
