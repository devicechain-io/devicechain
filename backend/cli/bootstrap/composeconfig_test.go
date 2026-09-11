// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// 🔴 WHAT THIS FILE IS FOR. The instance configuration document used to be rendered
// into the Helm values, which put every credential the instance has into the release
// record — where rotating one does not retract it, because the previous revision
// still holds the previous value. dcctl now composes the document, writes it into a
// Secret it owns, and installs the chart pointing at that name.
//
// The obligations that switch off when the chart stops authoring the document are
// pinned in existing_secret_obligations_test.go, which was written against a path
// nothing took. This file is the other half: it pins that dcctl takes that path
// CORRECTLY.

// composeStateForTest is a State carrying values that are recognisable on sight, so
// a test can ask whether a specific credential reached a specific place.
func composeStateForTest() *State {
	return &State{
		Instance:      "dctest",
		Profile:       "default",
		ImageRegistry: DefaultImageRegistry,
		ImageVersion:  "v0.0.0-test",
		Values: map[string]string{
			"secretsRootKey":            base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
			"serviceAuthSecret":         "SERVICE-AUTH-SECRET-CANARY",
			"natsServicePassword":       "NATS-SERVICE-PASSWORD-CANARY",
			"natsSysPassword":           "NATS-SYS-PASSWORD-CANARY",
			"natsCalloutIssuerSeed":     "SUAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"natsCalloutIssuerPublic":   "UAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"natsServicePasswordBcrypt": "$2a$10$abcdefghijklmnopqrstuv",
			"ingressHost":               "localhost",
		},
	}
}

// composedForTest runs the real composition over the real embedded chart.
func composedForTest(t *testing.T, st *State) (doc []byte, install map[string]interface{}) {
	t.Helper()
	ch := chartForTest(t)
	authoring := helmValues(st)
	doc, err := composeInstanceConfig(t.Context(), ch, authoring)
	if err != nil {
		t.Fatalf("composing the instance configuration: %v", err)
	}
	install, err = installValuesFor(authoring, st.Instance, doc)
	if err != nil {
		t.Fatalf("deriving the install values: %v", err)
	}
	return doc, install
}

// 🔴 THE CREDENTIALS MUST LEAVE THE VALUES, AND THAT IS THE ENTIRE POINT.
//
// Helm keeps the values of every revision it retains, so a credential that stays in
// this map stays readable for as long as that revision does — by exactly whoever
// could read the new one after a rotation. The counterweight is the second half: the
// document the pods actually mount must still carry them, or this is a release that
// hides the credentials by not having any.
func TestTheCredentialsLeaveTheHelmValuesAndStayInTheDocument(t *testing.T) {
	st := composeStateForTest()
	doc, install := composedForTest(t, st)

	encoded, err := json.Marshal(install)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"secretsRootKey", "serviceAuthSecret", "natsServicePassword",
		"natsSysPassword", "natsCalloutIssuerSeed",
	} {
		secret := st.Values[key]
		if strings.Contains(string(encoded), secret) {
			t.Errorf("the install values still carry %s: every revision Helm keeps would hold "+
				"it, so rotating it would not retract it", key)
		}
		if !strings.Contains(string(doc), secret) {
			t.Errorf("the composed document does not carry %s, so the services would not "+
				"receive it at all", key)
		}
	}
}

// 🔴 THE POD ANNOTATION MUST NOT MOVE WHEN THE SOURCE OF THE DOCUMENT DOES.
//
// With inline config the chart hashes the document it is about to write and stamps
// the digest on every pod template; under an external Secret it stamps the digest its
// supplier declared. If the two disagree for identical bytes, the first install after
// this change rolls every workload in the instance for no reason — and worse, it
// makes the digest something other than "the hash of what the pods mount", which is
// the one thing it is relied on for.
func TestTheDeclaredChecksumIsTheOneTheInlinePathWouldHaveStamped(t *testing.T) {
	st := composeStateForTest()
	ch := chartForTest(t)
	authoring := helmValues(st)

	inline, err := renderChartClientSide(t.Context(), ch, authoring)
	if err != nil {
		t.Fatalf("rendering the inline path: %v", err)
	}
	const marker = "checksum/instance-secret: "
	idx := strings.Index(inline, marker)
	if idx < 0 {
		t.Fatal("the inline render carries no instance-config checksum annotation, so there is " +
			"nothing to compare against")
	}
	rest := inline[idx+len(marker):]
	stamped := strings.TrimSpace(rest[:strings.Index(rest, "\n")])

	doc, install := composedForTest(t, st)
	declared := strconv.Quote(instanceConfigChecksum(doc))
	if declared != stamped {
		t.Errorf("the declared checksum is %s and the chart would have stamped %s: moving an "+
			"instance onto a dcctl-owned Secret would roll every pod for no change", declared, stamped)
	}

	// ...and the declared value must actually be the one that reaches the annotation
	// on the external path, or it is computed correctly and then ignored.
	external, err := renderChartClientSide(t.Context(), ch, install)
	if err != nil {
		t.Fatalf("rendering the install values: %v", err)
	}
	if !strings.Contains(external, marker+declared) {
		t.Error("the composed document's digest did not reach the pod annotation")
	}
}

// The install values must render, and they must render with the chart authoring
// nothing: a release that carries BOTH a rendered Secret and a document dcctl wrote
// is two documents for one mount point.
func TestTheInstallValuesPointAtTheSecretAndTheChartWritesNone(t *testing.T) {
	st := composeStateForTest()
	ch := chartForTest(t)
	doc, install := composedForTest(t, st)

	manifest, err := renderChartClientSide(t.Context(), ch, install)
	if err != nil {
		t.Fatalf("the install values were refused by the chart: %v", err)
	}
	rendered, err := instanceConfigFromManifest(ch, install, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if rendered != nil {
		t.Error("the chart still rendered an instance config Secret, so the pods would mount " +
			"the chart's document and not the one dcctl wrote")
	}

	// The name the release points at is the one DeployedInstanceConfig reads back.
	// Written as a literal: deriving it from the same helper the code uses would make
	// this test follow a rename that breaks the read-back.
	inst := install["instance"].(map[string]interface{})
	if got := inst["existingSecret"]; got != "dci-dctest-config" {
		t.Errorf("the release points at %q, and the next bootstrap reads %q — a mismatch is "+
			"read as a fresh install and rotates every credential", got, "dci-dctest-config")
	}

	// And the whole thing must pass the pre-flight on the authored path.
	if err := validateRenderedInstanceConfig(t.Context(), ch, install, doc); err != nil {
		t.Errorf("the composed document was refused by the pre-flight: %v", err)
	}
}

// 🔴 THE RESTATED COORDINATES MUST COME FROM THE DOCUMENT, NOT FROM A GUESS THAT
// MATCHES THE DEFAULT.
//
// Under an external Secret every template reading instance.config reads the chart's
// DEFAULTS, so a restatement that happens to equal the default is indistinguishable
// from a correct one until it is not. The document here is built with a broker and a
// database that are nothing like the chart's, so a restatement taken from anywhere
// but the document gives the wrong answer.
func TestTheRestatedCoordinatesFollowTheDocumentAndNotTheChartDefaults(t *testing.T) {
	authoring := map[string]interface{}{
		// The policy only renders where outbound-connectors is deployed — it exists to
		// bound that one service's egress — so the fixture deploys it.
		"profile": "full",
		"ingress": map[string]interface{}{"enabled": true, "host": "dc.example.com"},
		"instance": map[string]interface{}{
			"id": "dctest",
			"config": map[string]interface{}{
				"infrastructure": map[string]interface{}{
					"secrets": map[string]interface{}{
						"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
					},
					"nats": map[string]interface{}{
						"hostname": "broker.elsewhere",
						"port":     4333,
					},
				},
				"persistence": map[string]interface{}{
					"rdb": map[string]interface{}{
						"configuration": map[string]interface{}{"port": 6543},
					},
				},
			},
		},
	}

	ch := chartForTest(t)
	doc, err := composeInstanceConfig(t.Context(), ch, authoring)
	if err != nil {
		t.Fatal(err)
	}
	install, err := installValuesFor(authoring, "dctest", doc)
	if err != nil {
		t.Fatal(err)
	}

	// Read the document back through its own literal key path rather than through the
	// struct the code reads it with: a fixture built from the thing under test cannot
	// see that thing move.
	var asWritten struct {
		Infrastructure struct {
			Nats struct {
				Hostname string `json:"hostname"`
				Port     int    `json:"port"`
			} `json:"nats"`
		} `json:"infrastructure"`
		Persistence struct {
			Rdb struct {
				Configuration struct {
					Port int `json:"port"`
				} `json:"configuration"`
			} `json:"rdb"`
		} `json:"persistence"`
	}
	if err := json.Unmarshal(doc, &asWritten); err != nil {
		t.Fatal(err)
	}
	if asWritten.Infrastructure.Nats.Hostname != "broker.elsewhere" ||
		asWritten.Persistence.Rdb.Configuration.Port != 6543 {
		t.Fatalf("the fixture did not reach the document (%+v), so the rest of this test would "+
			"pass against the defaults", asWritten)
	}

	metrics := install["metrics"].(map[string]interface{})
	if got := metrics["natsBrokerHost"]; got != "broker.elsewhere" {
		t.Errorf("metrics.natsBrokerHost is %q: the PodMonitor would watch the namespace of a "+
			"broker this instance does not use, and collect nothing", got)
	}
	ports := install["networkPolicy"].(map[string]interface{})["externalConfigPorts"].(map[string]interface{})
	if ports["nats"] != 4333 || ports["rdb"] != 6543 {
		t.Errorf("networkPolicy.externalConfigPorts is %v, not the document's ports: the egress "+
			"rule would block the services' own broker and database traffic", ports)
	}

	// 🔑 AND THE RESTATEMENT MUST REACH THE RULE. dcctl leaves the policy off, so
	// turning it on here is what makes the values above something other than a map
	// nothing reads.
	install["networkPolicy"].(map[string]interface{})["enabled"] = true
	install["metrics"].(map[string]interface{})["enabled"] = false
	manifest, err := renderChartClientSide(t.Context(), ch, install)
	if err != nil {
		t.Fatalf("the policy was refused with the ports restated: %v", err)
	}
	for _, want := range []string{"port: 4333", "port: 6543"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("the egress rule does not carry %q, so the restated port was ignored", want)
		}
	}
	for _, unwanted := range []string{"port: 4222", "port: 5432"} {
		if strings.Contains(manifest, unwanted) {
			t.Errorf("the egress rule still carries the chart default %q", unwanted)
		}
	}
}

// The chart's authoring-time guards have to keep firing, and composition is now the
// only place they can. The root-key refusal is the one that matters most: it turns a
// whole instance of crash-looping pods into a sentence, and under an external Secret
// the chart never runs it.
func TestComposingStillRunsTheChartsAuthoringGuards(t *testing.T) {
	ch := chartForTest(t)
	_, err := composeInstanceConfig(t.Context(), ch, map[string]interface{}{
		"instance": map[string]interface{}{"id": "dctest"},
	})
	if err == nil {
		t.Fatal("a deploy with a secret-store area and no root key composed cleanly: the " +
			"operator would learn about it as a crash-loop")
	}
	if !strings.Contains(err.Error(), "rootKey is required") {
		t.Errorf("composition failed for some other reason, so this proves nothing: %v", err)
	}
}

// Composition takes the AUTHORING values. Handing it values that already point at a
// Secret makes the chart render no document, and a nil read as success would write
// an empty document over a live instance's configuration.
func TestComposingRefusesValuesThatAlreadyNameASecret(t *testing.T) {
	ch := chartForTest(t)
	_, err := composeInstanceConfig(t.Context(), ch, externalValues(digest("a")))
	if err == nil {
		t.Fatal("composition accepted values that render no document")
	}
	if !strings.Contains(err.Error(), "rendered no instance configuration document") {
		t.Errorf("the refusal says something else: %v", err)
	}
}

// 🔴 A PORT THAT CANNOT BE READ MUST REFUSE, NOT RESTATE ZERO. The relational
// store's port lives in an untyped configuration map that nothing upstream judges,
// and `port: 0` in the egress rule blocks every connection to the database while
// rendering, applying and reporting success.
func TestAnUnreadableDatabasePortIsRefusedRatherThanRestatedAsZero(t *testing.T) {
	st := composeStateForTest()
	doc, _ := composedForTest(t, st)

	for name, mutate := range map[string]func(cfg map[string]interface{}){
		"absent": func(cfg map[string]interface{}) { delete(cfg, "port") },
		"zero":   func(cfg map[string]interface{}) { cfg["port"] = 0 },
		"a string": func(cfg map[string]interface{}) {
			cfg["port"] = "5432"
		},
	} {
		t.Run(name, func(t *testing.T) {
			var parsed map[string]interface{}
			if err := json.Unmarshal(doc, &parsed); err != nil {
				t.Fatal(err)
			}
			cfg := parsed["persistence"].(map[string]interface{})["rdb"].(map[string]interface{})["configuration"].(map[string]interface{})
			mutate(cfg)
			mutated, err := json.Marshal(parsed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readConfigCoordinates(mutated); err == nil {
				t.Fatal("the coordinates were read from a document with no usable database port, " +
					"so the egress rule would be written from a zero")
			}
		})
	}

	// The counterweight: the unmutated document must still be readable, or the checks
	// above are passing because nothing works.
	coords, err := readConfigCoordinates(doc)
	if err != nil {
		t.Fatalf("the composed document's own coordinates were refused: %v", err)
	}
	if coords.RdbPort <= 0 || coords.NatsPort <= 0 || coords.NatsHost == "" {
		t.Errorf("the coordinates came back empty: %+v", coords)
	}
}

// 🔴 THE NAMESPACE MUST BE CREATED IN A SHAPE THE CHART CAN ADOPT, or the first
// install refuses outright — Helm protecting a namespace it did not make.
//
// The three keys are written as LITERALS. Reading them from the constants the code
// uses would make this test follow a rename straight past the only thing it checks,
// and these are not our names to choose: they are what Helm's own checkOwnership
// requires.
func TestTheNamespaceIsCreatedInAShapeHelmCanAdopt(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := ensureNamespaceForRelease(context.Background(), c, "dctest", "dc", "default"); err != nil {
		t.Fatalf("creating the namespace: %v", err)
	}

	ns, err := c.CoreV1().Namespaces().Get(context.Background(), "dctest", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the namespace back: %v", err)
	}
	if got := ns.Labels["app.kubernetes.io/managed-by"]; got != "Helm" {
		t.Errorf("app.kubernetes.io/managed-by is %q, so the chart cannot adopt this namespace "+
			"and the install refuses", got)
	}
	if got := ns.Annotations["meta.helm.sh/release-name"]; got != "dc" {
		t.Errorf("meta.helm.sh/release-name is %q, not the release the chart installs under", got)
	}
	if got := ns.Annotations["meta.helm.sh/release-namespace"]; got != "default" {
		t.Errorf("meta.helm.sh/release-namespace is %q, not where the release record lives", got)
	}
	if got := ns.Labels["devicechain.io/instance"]; got != "dctest" {
		t.Errorf("devicechain.io/instance is %q, so a namespace dcctl created does not look "+
			"like one the chart created", got)
	}
}

// An existing namespace is left exactly as it is. Stamping our metadata onto one
// somebody else is using would be claiming it rather than checking it — and Helm
// already refuses it, with a better message than anything here could give.
func TestAnExistingNamespaceIsNotRestamped(t *testing.T) {
	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        "dctest",
		Labels:      map[string]string{"app.kubernetes.io/managed-by": "someone-else"},
		Annotations: map[string]string{"meta.helm.sh/release-name": "their-release"},
	}})
	if err := ensureNamespaceForRelease(context.Background(), c, "dctest", "dc", "default"); err != nil {
		t.Fatalf("an existing namespace was treated as a failure: %v", err)
	}

	ns, err := c.CoreV1().Namespaces().Get(context.Background(), "dctest", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ns.Labels["app.kubernetes.io/managed-by"] != "someone-else" ||
		ns.Annotations["meta.helm.sh/release-name"] != "their-release" {
		t.Error("an existing namespace was restamped: dcctl would be claiming a namespace " +
			"another release owns rather than letting Helm refuse it")
	}
}

// 🔴 THE SECRET DCCTL WRITES MUST BE THE ONE THE NEXT BOOTSTRAP READS BACK.
//
// DeployedInstanceConfig looks for Secret `dci-<id>-config` in namespace `<id>` and
// takes the document from the `instance` key; a NotFound there is read as "fresh
// install, mint everything". All three coordinates are literals here for that
// reason — a test that derived them from the writer would follow a rename into the
// exact failure this pins, which is a re-bootstrap over a live instance.
func TestTheWrittenSecretIsTheOneTheReadBackLooksFor(t *testing.T) {
	c := fake.NewSimpleClientset()
	doc := []byte(`{"infrastructure":{}}`)
	err := writeOwnedSecret(context.Background(), c, "dctest", testUID,
		instanceConfigSecret("dctest", doc), fixedClock("2026-09-11T10:00:00Z"))
	if err != nil {
		t.Fatalf("writing the instance configuration: %v", err)
	}

	s, err := c.CoreV1().Secrets("dctest").Get(context.Background(), "dci-dctest-config", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the next bootstrap would not find this Secret and would call the instance "+
			"fresh: %v", err)
	}
	if got := s.StringData["instance"]; got != string(doc) {
		t.Errorf("the document is under a different key (%v), so every service would start with "+
			"no configuration", s.StringData)
	}
}

// The writer refuses without an owner, and the composition path is where that first
// bites: a State assembled without a declaration read-back has no UID, and minting
// against no owner is how a rebuild inherits a destroyed instance's credentials.
func TestWritingTheDocumentRefusesWithoutADeclarationUID(t *testing.T) {
	c := fake.NewSimpleClientset()
	err := writeOwnedSecret(context.Background(), c, "dctest", "",
		instanceConfigSecret("dctest", []byte(`{}`)), time.Now)
	if err == nil {
		t.Fatal("the instance configuration was written with no owning declaration")
	}
	if !strings.Contains(err.Error(), "no UID") {
		t.Errorf("the refusal says something else: %v", err)
	}
}

// A sanity check on the two halves of the name, kept because the chart fails the
// install on any disagreement and the message is worth matching exactly.
func TestTheSecretNameMatchesWhatTheChartDemands(t *testing.T) {
	if got := instanceConfigSecretName("dctest"); got != "dci-dctest-config" {
		t.Errorf("instanceConfigSecretName gave %q; the chart refuses anything but %q",
			got, "dci-dctest-config")
	}
	if got := fmt.Sprintf("%d", len(instanceConfigChecksum([]byte("x")))); got != "64" {
		t.Errorf("the checksum is %s characters and the chart requires 64 lowercase hex", got)
	}
}

// helmWrittenConfigSecret is the config Secret as an instance built before this
// change has it: authored by the chart, stamped by Helm, with no dcctl ownership.
func helmWrittenConfigSecret(release, releaseNamespace string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:              "dci-dctest-config",
		Namespace:         "dctest",
		CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		Labels: map[string]string{
			"devicechain.io/instance":      "dctest",
			"app.kubernetes.io/managed-by": "Helm",
		},
		Annotations: map[string]string{
			"meta.helm.sh/release-name":      release,
			"meta.helm.sh/release-namespace": releaseNamespace,
		},
	}}
}

// 🔴 THE SECRET MUST SURVIVE LEAVING THE CHART'S MANIFEST.
//
// Every instance that already exists has this Secret in its release manifest,
// because the chart rendered it. Pointing the release at instance.existingSecret
// takes it out of the manifest, and Helm deletes what leaves the manifest — so
// without this annotation the upgrade immediately after the write deletes the
// document it just wrote, and every pod in the instance has nothing to mount.
//
// The key and value are literals: they are Helm's, not ours, and a test that read
// them from the same constants the code passes would follow a rename straight past
// the deletion it exists to prevent.
func TestTheWrittenDocumentIsKeptWhenItLeavesTheChartsManifest(t *testing.T) {
	c := fake.NewSimpleClientset()
	err := writeOwnedSecret(context.Background(), c, "dctest", testUID,
		instanceConfigSecret("dctest", []byte(`{}`)), fixedClock("2026-09-11T10:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}

	s := getSecret(t, c, "dctest", "dci-dctest-config")
	if got := s.Annotations["helm.sh/resource-policy"]; got != "keep" {
		t.Errorf("helm.sh/resource-policy is %q: the upgrade that follows this write would "+
			"delete the document, because it is in the previous release's manifest and not "+
			"in the new one", got)
	}
}

// 🔴 THE FIRST RUN AGAINST AN INSTANCE THAT ALREADY EXISTS MUST NOT STOP DEAD.
//
// The Secret is there and the chart wrote it, so the writer's "dcctl did not write
// this" refusal would fire on every instance in existence. The takeover is keyed on
// Kubernetes' own record of which release owns the object, and it must leave the
// minted-at stamp saying when the credential came into existence rather than when it
// changed hands.
func TestAChartWrittenConfigIsTakenOverRatherThanRefused(t *testing.T) {
	c := fake.NewSimpleClientset(helmWrittenConfigSecret("dc", "default"))

	if err := adoptChartWrittenInstanceConfig(context.Background(), c, "dctest", testUID, "dc", "default"); err != nil {
		t.Fatalf("taking over the chart's Secret: %v", err)
	}
	if err := writeOwnedSecret(context.Background(), c, "dctest", testUID,
		instanceConfigSecret("dctest", []byte(`{"infrastructure":{}}`)), fixedClock("2026-09-11T10:00:00Z")); err != nil {
		t.Fatalf("writing after the takeover: %v", err)
	}

	s := getSecret(t, c, "dctest", "dci-dctest-config")
	if s.Annotations[annotationManagedBy] != managedByDcctl || s.Annotations[annotationOwnerUID] != testUID {
		t.Errorf("the Secret is not owned after the takeover: %v", s.Annotations)
	}
	if got := s.Annotations[annotationMintedAt]; got != "2026-01-02T03:04:05Z" {
		t.Errorf("minted-at is %q, not the Secret's own creation time: a takeover would be "+
			"reported as a rotation, which is the one question the stamp answers", got)
	}
	if s.Annotations["helm.sh/resource-policy"] != "keep" {
		t.Error("the taken-over Secret is not marked keep, so the upgrade that follows " +
			"deletes it")
	}
	if got := s.StringData["instance"]; got != `{"infrastructure":{}}` {
		t.Errorf("the document did not reach the taken-over Secret (%q)", got)
	}
}

// ...and the takeover must be an IDENTIFICATION, not a habit. A Secret the cluster
// does not record as belonging to this release is left exactly as found, so the
// writer refuses it and says so.
func TestOnlyTheReleasesOwnConfigSecretIsTakenOver(t *testing.T) {
	for name, existing := range map[string]*corev1.Secret{
		"claimed by nobody": func() *corev1.Secret {
			s := helmWrittenConfigSecret("dc", "default")
			s.Labels = map[string]string{"devicechain.io/instance": "dctest"}
			s.Annotations = nil
			return s
		}(),
		"claimed by another release":   helmWrittenConfigSecret("someone-else", "default"),
		"claimed in another namespace": helmWrittenConfigSecret("dc", "somewhere-else"),
	} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewSimpleClientset(existing)
			if err := adoptChartWrittenInstanceConfig(context.Background(), c, "dctest", testUID, "dc", "default"); err != nil {
				t.Fatalf("the takeover reported an error rather than declining: %v", err)
			}
			err := writeOwnedSecret(context.Background(), c, "dctest", testUID,
				instanceConfigSecret("dctest", []byte(`{}`)), fixedClock("2026-09-11T10:00:00Z"))
			var foreign *ErrForeignSecret
			if !errors.As(err, &foreign) {
				t.Fatalf("a Secret this release does not own was written anyway (err %v)", err)
			}
		})
	}
}

// 🔴 THE TAKEOVER MUST NOT PAPER OVER THE DEAD-GENERATION CHECK. A Secret left
// behind by a destroy that died halfway carries dcctl's ownership and a UID that is
// gone; adopting it would be exactly the "rebuild inherits the old instance's
// credentials" the UID exists to stop.
func TestTheTakeoverDoesNotRescueAPreviousGenerationsSecret(t *testing.T) {
	stale := helmWrittenConfigSecret("dc", "default")
	stale.Annotations[annotationManagedBy] = managedByDcctl
	stale.Annotations[annotationOwnerName] = "dctest"
	stale.Annotations[annotationOwnerUID] = "99999999-9999-9999-9999-999999999999"

	c := fake.NewSimpleClientset(stale)
	if err := adoptChartWrittenInstanceConfig(context.Background(), c, "dctest", testUID, "dc", "default"); err != nil {
		t.Fatalf("the takeover errored on an already-owned Secret: %v", err)
	}
	err := writeOwnedSecret(context.Background(), c, "dctest", testUID,
		instanceConfigSecret("dctest", []byte(`{}`)), fixedClock("2026-09-11T10:00:00Z"))
	if err == nil {
		t.Fatal("a Secret minted for a previous instance of this name was written over: the " +
			"rebuild would inherit the dead instance's credentials")
	}
	if !strings.Contains(err.Error(), "previous") {
		t.Errorf("the refusal says something else: %v", err)
	}
}

// A fresh install has nothing to take over, and saying so must not be an error.
func TestTheTakeoverIsANoOpOnAFreshInstall(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := adoptChartWrittenInstanceConfig(context.Background(), c, "dctest", testUID, "dc", "default"); err != nil {
		t.Fatalf("a fresh install was treated as a failure: %v", err)
	}
	if _, err := c.CoreV1().Secrets("dctest").Get(context.Background(), "dci-dctest-config", metav1.GetOptions{}); err == nil {
		t.Error("the takeover created a Secret; it is meant only to re-stamp one that exists")
	}
}

// The install values must keep everything the authoring values put beside the
// document, not just the coordinates restated over them.
//
// metrics and networkPolicy are the two blocks this rewrites, and rewriting is where
// a whole block goes missing: helmValues decides from the infrastructure apply
// whether the backup alerts can fire and which namespaces they select, and losing
// those leaves rules that load, evaluate nothing and never fire — an instance that
// looks monitored and is not.
func TestRestatingACoordinateDoesNotDropTheBlockItLandsIn(t *testing.T) {
	st := composeStateForTest()
	st.Values[databaseBackupsKey] = "true"
	st.Values[cnpgNamespaceKey] = "cnpg-system"

	authoring := helmValues(st)
	before := authoring["metrics"].(map[string]interface{})
	_, install := composedForTest(t, st)
	after := install["metrics"].(map[string]interface{})

	for _, key := range []string{"enabled", "databaseBackups", "databaseNamespace", "cnpgNamespace"} {
		if after[key] != before[key] {
			t.Errorf("metrics.%s was %v before the restatement and %v after: rewriting the "+
				"block dropped what the infrastructure apply reported", key, before[key], after[key])
		}
	}
	if after["natsBrokerHost"] == nil || after["natsBrokerHost"] == "" {
		t.Error("the restated broker host is not in the block it was merged into")
	}
}
