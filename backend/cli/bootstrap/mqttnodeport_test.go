// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

var allLocalMQTTNodePorts = []int32{31883, 31884, 31885}

// A NEW local instance takes the first node port the cluster publishes that no other instance
// holds, and none when every one is held.
func TestANewLocalInstanceTakesTheFirstFreeMQTTNodePort(t *testing.T) {
	held := func(m map[int32]string) clusterSingletons { return clusterSingletons{MQTTNodePortHolders: m} }
	for _, c := range []struct {
		name string
		held clusterSingletons
		pub  []int32
		want int32
	}{
		{"empty cluster", held(nil), allLocalMQTTNodePorts, 31883},
		{"first taken", held(map[int32]string{31883: "dci-alpha"}), allLocalMQTTNodePorts, 31884},
		{"first two taken", held(map[int32]string{31883: "dci-alpha", 31884: "dci-beta"}), allLocalMQTTNodePorts, 31885},
		{"a gap is filled", held(map[int32]string{31883: "dci-alpha", 31885: "dci-gamma"}), allLocalMQTTNodePorts, 31884},
		{"all taken", held(map[int32]string{31883: "a", 31884: "b", 31885: "c"}), allLocalMQTTNodePorts, 0},
		// A cluster created before the extra maps publishes the first alone: the old behaviour.
		{"old cluster, first free", held(nil), []int32{31883}, 31883},
		{"old cluster, first taken", held(map[int32]string{31883: "dci-alpha"}), []int32{31883}, 0},
		// The holder field the step reads for its message counts as taken on its own too.
		{"holder field alone", clusterSingletons{MQTTNodePortHolder: "dci-alpha"}, allLocalMQTTNodePorts, 31884},
		// A re-run keeps the port its own Service has, even when an earlier one is now free.
		{"re-run keeps its own", clusterSingletons{OwnMQTTNodePort: 31885}, allLocalMQTTNodePorts, 31885},
	} {
		if got := chooseNewMQTTNodePort(c.held, c.pub); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// 🔴 AN UPGRADE KEEPS THE PORT AN INSTANCE HAS AND NEVER HANDS IT A NEW ONE. An instance that had
// none before this release (a second instance whose broker was in-cluster only) still has none
// after it, unless the first port is free, which is what every upgrade did before.
func TestAnUpgradeKeepsItsMQTTNodePortAndGetsNoNewOne(t *testing.T) {
	for _, c := range []struct {
		name     string
		held     clusterSingletons
		recorded int32
		want     int32
	}{
		{"own Service wins", clusterSingletons{OwnMQTTNodePort: 31884}, 0, 31884},
		{"own Service wins over the record", clusterSingletons{OwnMQTTNodePort: 31884}, 31885, 31884},
		{"recorded port restored", clusterSingletons{MQTTNodePortHolders: map[int32]string{31883: "dci-alpha"}}, 31885, 31885},
		{"recorded port held by another falls back", clusterSingletons{MQTTNodePortHolders: map[int32]string{31883: "a", 31885: "c"}}, 31885, 0},
		{"pre-existing instance with the first port", clusterSingletons{}, 0, 31883},
		{"pre-existing second instance stays without", clusterSingletons{MQTTNodePortHolders: map[int32]string{31883: "dci-alpha"}}, 0, 0},
		{"a recorded port outside the set is ignored", clusterSingletons{}, 30001, 31883},
	} {
		if got := keptMQTTNodePort(c.held, c.recorded); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// The cluster read attributes every local MQTT node port: this instance's own, and the others'
// by namespace. A node port outside the set is nobody's concern.
func TestSingletonsAttributeEveryLocalMQTTNodePort(t *testing.T) {
	got := singletonsFrom([]corev1.Service{
		svc(InstanceNamespace("alpha"), 31883),
		svc(InstanceNamespace("beta"), 31885),
		svc(InstanceNamespace("gamma"), 30001),
		svc(InstanceNamespace("delta"), 31884),
	}, nil, nil, "delta", "delta.localhost")
	if got.OwnMQTTNodePort != 31884 {
		t.Errorf("own port %d, want 31884", got.OwnMQTTNodePort)
	}
	want := map[int32]string{31883: InstanceNamespace("alpha"), 31885: InstanceNamespace("beta")}
	if len(got.MQTTNodePortHolders) != len(want) {
		t.Fatalf("holders %v, want %v", got.MQTTNodePortHolders, want)
	}
	for p, ns := range want {
		if got.MQTTNodePortHolders[p] != ns {
			t.Errorf("port %d held by %q, want %q", p, got.MQTTNodePortHolders[p], ns)
		}
	}
	if got.MQTTNodePortHolder != InstanceNamespace("alpha") {
		t.Errorf("the first port's holder %q, want alpha's namespace", got.MQTTNodePortHolder)
	}
}

// The ports a kind cluster publishes are read from it; a cluster that cannot be read, or that
// predates the extra maps, offers the first alone.
func TestPublishedMQTTNodePortsAreReadFromTheCluster(t *testing.T) {
	orig := dockerPortOutput
	t.Cleanup(func() { dockerPortOutput = orig })
	dockerPortOutput = func(string) (string, error) {
		return "31883/tcp -> 0.0.0.0:1883\n31885/tcp -> 0.0.0.0:1885\n80/tcp -> 0.0.0.0:80\n", nil
	}
	if got := publishedMQTTNodePorts("kind-dev"); !slices.Equal(got, []int32{31883, 31885}) {
		t.Errorf("published %v, want [31883 31885]", got)
	}
	dockerPortOutput = func(string) (string, error) { return "31883/tcp -> 0.0.0.0:1883\n", nil }
	if got := publishedMQTTNodePorts("kind-dev"); !slices.Equal(got, []int32{31883}) {
		t.Errorf("an old cluster published %v, want [31883]", got)
	}
	if got := publishedMQTTNodePorts("docker-desktop"); !slices.Equal(got, []int32{31883}) {
		t.Errorf("a context that is not kind's offered %v, want [31883]", got)
	}
}

// The step, end to end: a second local instance on a cluster where another holds the first port
// is built with the next one, asks the apply for it, and records it.
func TestASecondLocalInstanceIsBuiltWithTheNextMQTTNodePort(t *testing.T) {
	stubNamespacePrecheck(t)
	stubSingletons(t, clusterSingletons{MQTTNodePortHolder: InstanceNamespace("alpha"),
		MQTTNodePortHolders: map[int32]string{31883: InstanceNamespace("alpha")}}, nil)
	origPub, origRec := publishedMQTTNodePorts, recordMQTTNodePort
	t.Cleanup(func() { publishedMQTTNodePorts, recordMQTTNodePort = origPub, origRec })
	publishedMQTTNodePorts = func(string) []int32 { return allLocalMQTTNodePorts }
	var recorded []int32
	recordMQTTNodePort = func(instance string, port int32) error {
		if instance != "beta" {
			t.Errorf("recorded for %q, want beta", instance)
		}
		recorded = append(recorded, port)
		return nil
	}
	st := &State{Instance: "beta", IngressHost: "beta.localhost", KubeContext: "kind-dev", Values: map[string]string{}}
	if err := stepCheckClusterSingletons(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(infraVars(st), "nats_mqtt_node_port=31884") {
		t.Errorf("beta does not ask for node port 31884: %v", infraVars(st))
	}
	if len(recorded) != 0 {
		t.Errorf("the step recorded %v before the apply; the record follows a successful apply", recorded)
	}
	recordMQTTNodePortAfterApply(stdConsole, st)
	if !slices.Equal(recorded, []int32{31884}) {
		t.Errorf("recorded after the apply %v, want [31884]", recorded)
	}
	if line := mqttReportLine(st); !strings.Contains(line, "31884") {
		t.Errorf("the report does not say where beta's broker is: %q", line)
	}
	// None free: no node port asked for, and the report says in-cluster only.
	stubSingletons(t, clusterSingletons{MQTTNodePortHolders: map[int32]string{31883: "a", 31884: "b", 31885: "c"}}, nil)
	recorded = nil
	st = &State{Instance: "beta", IngressHost: "beta.localhost", KubeContext: "kind-dev", Values: map[string]string{}}
	if err := stepCheckClusterSingletons(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	for _, v := range infraVars(st) {
		if strings.HasPrefix(v, "nats_mqtt_node_port=") {
			t.Errorf("an instance with no free port asks for %s", v)
		}
	}
	if line := mqttReportLine(st); !strings.Contains(line, "in-cluster only") {
		t.Errorf("the report hides that the broker is in-cluster only: %q", line)
	}
	recordMQTTNodePortAfterApply(stdConsole, st)
	if !slices.Equal(recorded, []int32{0}) {
		t.Errorf("recorded %v, want [0]", recorded)
	}
}

// 🔴 THE RECORD IS BOOKKEEPING: a failure to write it is a warning, never a failed run, and a
// rehearsal, or a run that decided no port, writes nothing.
func TestRecordingTheMQTTNodePortNeverFailsTheRun(t *testing.T) {
	origRec := recordMQTTNodePort
	t.Cleanup(func() { recordMQTTNodePort = origRec })
	calls := 0
	recordMQTTNodePort = func(string, int32) error { calls++; return errors.New("disk full") }

	st := &State{Instance: "beta", KubeContext: "kind-dev", Values: map[string]string{mqttNodePortKey: "31884"}}
	out := captureStdout(t, func() { recordMQTTNodePortAfterApply(stdConsole, st) })
	if calls != 1 || !strings.Contains(out, "warning") || !strings.Contains(out, "disk full") {
		t.Errorf("calls %d, output %q: want one attempt and a warning naming the error", calls, out)
	}

	calls = 0
	recordMQTTNodePortAfterApply(stdConsole, &State{Instance: "beta", KubeContext: "kind-dev", DryRun: true,
		Values: map[string]string{mqttNodePortKey: "31884"}})
	recordMQTTNodePortAfterApply(stdConsole, &State{Instance: "beta", KubeContext: "kind-dev", Values: map[string]string{}})
	recordMQTTNodePortAfterApply(stdConsole, &State{Instance: "beta", KubeContext: "prod-eks", Values: map[string]string{mqttNodePortKey: "31884"}})
	if calls != 0 {
		t.Errorf("a rehearsal, an undecided run or a cloud run wrote the record %d time(s)", calls)
	}
}

// A hand-edited record naming something other than a port of the set reads as none: it is never
// narrowed into a different number.
func TestAHandEditedRecordedPortOutsideTheSetReadsAsNone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, v := range []int{31884, 4294998652 /* 31884 + 2^32 */, -1, 30001} {
		if err := WriteInstanceRecord(InstanceRecord{Instance: "beta", Provider: "local", MQTTNodePort: v}); err != nil {
			t.Fatal(err)
		}
		want := int32(0)
		if v == 31884 {
			want = 31884
		}
		if got := recordedMQTTNodePort("beta"); got != want {
			t.Errorf("recorded %d reads as %d, want %d", v, got, want)
		}
	}
}

// One `docker port` call answers for every port: a read per port could drop the first and keep a
// later one.
func TestThePublishedPortsComeFromOneDockerRead(t *testing.T) {
	orig := dockerPortOutput
	t.Cleanup(func() { dockerPortOutput = orig })
	calls := 0
	dockerPortOutput = func(string) (string, error) {
		calls++
		return "31883/tcp -> 0.0.0.0:1883\n31884/tcp -> 0.0.0.0:1884\n31885/tcp -> 0.0.0.0:1885\n", nil
	}
	if got := publishedMQTTNodePorts("kind-dev"); !slices.Equal(got, allLocalMQTTNodePorts) || calls != 1 {
		t.Errorf("published %v in %d docker call(s), want all three in one", got, calls)
	}
}

// The instance record keeps the port, and a tool on this machine reads it back.
func TestTheMQTTNodePortIsRecordedOnTheInstance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := WriteInstanceRecord(InstanceRecord{Instance: "beta", Provider: "local", KubeContext: "kind-dev"}); err != nil {
		t.Fatal(err)
	}
	stubOwnMQTTNodePort(t, 0, errors.New("no cluster in this test"))
	if got := ResolveInstanceMQTTNodePort(context.Background(), "kind-dev", "beta"); got != KindMQTTNodePort {
		t.Errorf("an instance with no recorded port, on a cluster that cannot be read, reads %d, want the first (%d)", got, KindMQTTNodePort)
	}
	if err := recordMQTTNodePort("beta", 31885); err != nil {
		t.Fatal(err)
	}
	rec, err := ReadInstanceRecord("beta")
	if err != nil {
		t.Fatal(err)
	}
	if rec.MQTTNodePort != 31885 || rec.KubeContext != "kind-dev" {
		t.Errorf("record after the write: %+v", rec)
	}
	if got := ResolveInstanceMQTTNodePort(context.Background(), "kind-dev", "beta"); got != 31885 {
		t.Errorf("read back %d, want 31885", got)
	}
	// No record (an instance built on another machine) is not an error.
	if err := recordMQTTNodePort("nobody", 31884); err != nil {
		t.Errorf("a missing record failed the step: %v", err)
	}
}

// 🔴 EVERY PORT dcctl MAY GIVE IS ONE THE EMBEDDED KIND CONFIGURATION MAPS. A node port the
// cluster does not publish is a dead route from the host, which is the failure this list exists
// to avoid. Read from the embedded config, not a second copy of the numbers.
func TestEveryLocalMQTTNodePortIsMappedByTheEmbeddedKindConfig(t *testing.T) {
	var cfg struct {
		Nodes []struct {
			ExtraPortMappings []struct {
				ContainerPort int32 `json:"containerPort"`
				HostPort      int32 `json:"hostPort"`
			} `json:"extraPortMappings"`
		} `json:"nodes"`
	}
	if err := yaml.Unmarshal(assets.KindClusterConfig(), &cfg); err != nil {
		t.Fatal(err)
	}
	mapped := map[int32]int32{}
	for _, n := range cfg.Nodes {
		for _, m := range n.ExtraPortMappings {
			mapped[m.ContainerPort] = m.HostPort
		}
	}
	for _, p := range localMQTTNodePorts {
		if _, ok := mapped[p]; !ok {
			t.Errorf("node port %d is one dcctl may give, but the embedded kind config does not map it", p)
		}
	}
	if !slices.Equal(localMQTTNodePorts, allLocalMQTTNodePorts) {
		t.Errorf("localMQTTNodePorts %v changed; update this test's list deliberately", localMQTTNodePorts)
	}
}

func stubOwnMQTTNodePort(t *testing.T, port int32, err error) *int {
	t.Helper()
	orig := instanceOwnMQTTNodePort
	t.Cleanup(func() { instanceOwnMQTTNodePort = orig })
	calls := 0
	instanceOwnMQTTNodePort = func(context.Context, string, string) (int32, error) {
		calls++
		return port, err
	}
	return &calls
}

// 🔴 "NONE" IS NOT "NOT RECORDED". An instance that was given no MQTT node port is recorded as
// such, and resolves to none without asking the cluster; a record that predates the field is
// settled by the instance's own Services, so an older second instance with no port is not
// addressed at the first instance's 31883, while an older first instance still resolves to it.
func TestResolvingTheMQTTNodePortTellsNoneFromNotRecorded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ctx := context.Background()
	if err := WriteInstanceRecord(InstanceRecord{Instance: "beta", Provider: "local", KubeContext: "kind-dev"}); err != nil {
		t.Fatal(err)
	}

	// Recorded none: 0, and the cluster is not asked.
	if err := recordMQTTNodePort("beta", 0); err != nil {
		t.Fatal(err)
	}
	rec, _ := ReadInstanceRecord("beta")
	if rec.MQTTNodePort != MQTTNodePortNone {
		t.Fatalf("no port was recorded as %d, want %d", rec.MQTTNodePort, MQTTNodePortNone)
	}
	calls := stubOwnMQTTNodePort(t, 31883, nil)
	if got := ResolveInstanceMQTTNodePort(ctx, "kind-dev", "beta"); got != 0 || *calls != 0 {
		t.Errorf("a recorded none resolved to %d after %d cluster read(s), want 0 and none", got, *calls)
	}

	// Not recorded (an older record): the instance's own Service decides.
	if err := WriteInstanceRecord(InstanceRecord{Instance: "beta", Provider: "local", KubeContext: "kind-dev"}); err != nil {
		t.Fatal(err)
	}
	stubOwnMQTTNodePort(t, 31883, nil)
	if got := ResolveInstanceMQTTNodePort(ctx, "kind-dev", "beta"); got != 31883 {
		t.Errorf("an older first instance with a 31883 Service resolved to %d, want 31883", got)
	}
	stubOwnMQTTNodePort(t, 0, nil)
	if got := ResolveInstanceMQTTNodePort(ctx, "kind-dev", "beta"); got != 0 {
		t.Errorf("an older second instance with no node-port Service resolved to %d, want 0", got)
	}
	// No record at all (built on another machine) is settled the same way.
	if got := ResolveInstanceMQTTNodePort(ctx, "kind-dev", "nobody"); got != 0 {
		t.Errorf("an unrecorded instance with no node-port Service resolved to %d, want 0", got)
	}
}
