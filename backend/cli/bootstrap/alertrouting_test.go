// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

const (
	testWebhook  = "https://hooks.example.test/T000/secret-token"
	testWatchdog = "https://hc.example.test/ping/abc-123"
	testSMTPPass = "hunter2-smtp"
)

// stackClient is a dynamic client holding the declarations a test puts in it and, when
// configSecret is not empty, the monitoring stack's Alertmanager told to read that Secret.
func stackClient(configSecret string, objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	if configSecret != "" || configSecret == "-" {
		am := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "monitoring.coreos.com/v1", "kind": "Alertmanager",
			"metadata": map[string]any{"name": "kps", "namespace": monitoringNamespace},
			"spec":     map[string]any{},
		}}
		if configSecret != "-" {
			am.Object["spec"] = map[string]any{"configSecret": configSecret}
		}
		objs = append(objs, am)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{instanceGVR: "InstanceList", alertmanagerGVR: "AlertmanagerList"},
		objs...)
}

// brokenStackClient cannot list Alertmanagers, as a client without the right to would not.
func brokenStackClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	c := stackClient("", objs...)
	c.PrependReactor("list", "alertmanagers", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	return c
}

func emailRequest() AlertRoutingRequest {
	return AlertRoutingRequest{
		EmailTo: []string{"oncall@example.test"}, SMTPHost: "smtp.example.test:587",
		SMTPFrom: "alerts@example.test", SMTPUser: "mailer", SMTPPassword: testSMTPPass,
	}
}

func TestAlertRoutingRequestValidate(t *testing.T) {
	for _, c := range []struct {
		name string
		req  AlertRoutingRequest
		want string // "" = valid
	}{
		{"nothing is valid", AlertRoutingRequest{}, ""},
		{"a webhook", AlertRoutingRequest{Webhook: testWebhook}, ""},
		{"a webhook that is not a URL", AlertRoutingRequest{Webhook: "hooks.example.test"}, "--alert-webhook"},
		{"a non-http scheme", AlertRoutingRequest{Webhook: "ftp://x.example.test/a"}, "--alert-webhook"},
		{"a bad watchdog URL", AlertRoutingRequest{WatchdogWebhook: "nope"}, "--alert-watchdog-webhook"},
		{"a watchdog alone", AlertRoutingRequest{WatchdogWebhook: testWatchdog}, ""},
		{"email", emailRequest(), ""},
		{"email without a server", AlertRoutingRequest{EmailTo: []string{"a@example.test"}, SMTPFrom: "b@example.test"}, "--alert-smtp-host"},
		{"email with a hostless server", AlertRoutingRequest{EmailTo: []string{"a@example.test"}, SMTPHost: "smtp.example.test", SMTPFrom: "b@example.test"}, "--alert-smtp-host"},
		{"email without a sender", AlertRoutingRequest{EmailTo: []string{"a@example.test"}, SMTPHost: "s:25"}, "--alert-smtp-from"},
		{"email to a non-address", AlertRoutingRequest{EmailTo: []string{"nobody"}, SMTPHost: "s:25", SMTPFrom: "b@example.test"}, "not an email address"},
		{"a user without a password", AlertRoutingRequest{EmailTo: []string{"a@example.test"}, SMTPHost: "s:25", SMTPFrom: "b@example.test", SMTPUser: "u"}, "go together"},
		{"smtp flags with no email", AlertRoutingRequest{SMTPHost: "s:25"}, "--alert-email"},
		{"clear alone", AlertRoutingRequest{Clear: true}, ""},
		{"clear with a webhook", AlertRoutingRequest{Clear: true, Webhook: testWebhook}, "cannot be combined"},
		{"a config with a webhook", AlertRoutingRequest{Config: "route: {}", Webhook: testWebhook}, "cannot be combined"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.req.Validate()
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("error = %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestRenderedConfigRoutesWhatWasAskedFor(t *testing.T) {
	req := emailRequest()
	req.Webhook = testWebhook
	req.WatchdogWebhook = testWatchdog
	text, routing, err := renderAlertmanagerConfig(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := routing.Format(); got != "email,webhook,watchdog" {
		t.Fatalf("routing = %q", got)
	}
	// The config is a valid document of the shape the inspector (and Alertmanager) reads,
	// and what the inspector concludes from it is what was asked for.
	back, err := inspectAlertConfig(text)
	if err != nil {
		t.Fatalf("the config this renders does not pass the inspection an operator's own must: %v\n%s", err, text)
	}
	if !back.Routed() || !back.Watchdog {
		t.Errorf("inspecting the rendered config gives %+v, want routed with the watchdog", back)
	}
	for _, want := range []string{testWebhook, testWatchdog, "oncall@example.test", testSMTPPass, "smtp.example.test:587"} {
		if !strings.Contains(text, want) {
			t.Errorf("the config does not carry %q", want)
		}
	}
	// 🔴 The Watchdog fires forever by design. It must never reach the receiver a person reads.
	var doc struct {
		Route struct {
			Receiver string `json:"receiver"`
			Routes   []struct {
				Receiver string   `json:"receiver"`
				Matchers []string `json:"matchers"`
			} `json:"routes"`
		} `json:"route"`
	}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Route.Receiver != humanReceiver {
		t.Errorf("default receiver = %q, want the alerts receiver", doc.Route.Receiver)
	}
	if len(doc.Route.Routes) == 0 || doc.Route.Routes[0].Receiver != watchdogReceiver ||
		!strings.Contains(doc.Route.Routes[0].Matchers[0], "Watchdog") {
		t.Errorf("the first route must send the Watchdog to its own receiver, got %+v", doc.Route.Routes)
	}
}

func TestWithoutAReceiverNothingIsRoutedAndTheWatchdogIsDropped(t *testing.T) {
	text, routing, err := renderAlertmanagerConfig(AlertRoutingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if routing.Routed() || routing.Watchdog || routing.Format() != "none" {
		t.Fatalf("routing = %+v", routing)
	}
	back, err := inspectAlertConfig(text)
	if err != nil || back.Routed() {
		t.Fatalf("inspecting the default: %+v, %v", back, err)
	}
	if strings.Contains(text, humanReceiver) || strings.Contains(text, watchdogReceiver) {
		t.Error("the default config names a receiver that delivers")
	}
}

func TestAWatchdogAloneIsNotRoutingToAPerson(t *testing.T) {
	_, routing, err := renderAlertmanagerConfig(AlertRoutingRequest{WatchdogWebhook: testWatchdog})
	if err != nil {
		t.Fatal(err)
	}
	if routing.Routed() || !routing.Watchdog || routing.Format() != "none,watchdog" {
		t.Fatalf("routing = %+v (%s)", routing, routing.Format())
	}
}

func TestInspectingAnOperatorsOwnConfig(t *testing.T) {
	for _, c := range []struct {
		name, text, want string
		wantErr          string
	}{
		{"a slack receiver", `
route:
  receiver: team
  routes:
    - receiver: team
      matchers: ['severity="critical"']
receivers:
  - name: team
    slack_configs:
      - api_url: https://hooks.slack.test/x
`, "config", ""},
		{"with the watchdog routed", `
route:
  receiver: "null"
  routes:
    - receiver: snitch
      match: {alertname: Watchdog}
receivers:
  - name: "null"
  - name: snitch
    webhook_configs: [{url: "https://snitch.test/x"}]
`, "none,watchdog", ""},
		{"a person and the watchdog", `
route:
  receiver: team
  routes:
    - receiver: snitch
      matchers: ['alertname = "Watchdog"']
receivers:
  - name: team
    email_configs: [{to: ops@example.test}]
  - name: snitch
    webhook_configs: [{url: "https://snitch.test/x"}]
`, "config,watchdog", ""},
		{"a receiver reached only through the watchdog is not a person", `
route:
  receiver: "null"
  routes:
    - receiver: snitch
      matchers: ['alertname = "Watchdog"']
      routes:
        - receiver: other
receivers:
  - name: "null"
  - name: snitch
    webhook_configs: [{url: "https://snitch.test/x"}]
  - name: other
    webhook_configs: [{url: "https://other.test/x"}]
`, "none,watchdog", ""},
		{"only the null receiver", "route: {receiver: \"null\"}\nreceivers:\n  - name: \"null\"\n", "none", ""},
		{"a receiver that delivers nowhere", "route: {receiver: r}\nreceivers:\n  - name: r\n    webhook_configs: []\n", "none", ""},
		{"a webhook with no url", "route: {receiver: r}\nreceivers:\n  - name: r\n    webhook_configs: [{}]\n", "", "is empty"},
		{"a webhook whose url is blank", "route: {receiver: r}\nreceivers:\n  - name: r\n    webhook_configs: [{send_resolved: true}]\n", "", "needs a url"},
		{"an email with no recipient", "route: {receiver: r}\nreceivers:\n  - name: r\n    email_configs: [{from: a@example.test}]\n", "", "needs a to"},
		{"a receiver field that is not an integration", "route: {receiver: r}\nreceivers:\n  - name: r\n    webhook_url: https://x.test\n", "", "not a notification integration"},
		{"an integration that is not a list", "route: {receiver: r}\nreceivers:\n  - name: r\n    webhook_configs: {url: https://x.test}\n", "", "must be a list"},
		{"a route to a receiver it does not define", "route: {receiver: ghost}\nreceivers:\n  - name: r\n", "", `receiver "ghost"`},
		{"a duplicate receiver", "route: {receiver: r}\nreceivers:\n  - name: r\n  - name: r\n", "", "twice"},
		{"no receivers", "route: {receiver: r}\n", "", "at least one receiver"},
		{"not YAML", "route: [unterminated", "", "YAML"},
	} {
		t.Run(c.name, func(t *testing.T) {
			routing, err := inspectAlertConfig(c.text)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if routing.Format() != c.want {
				t.Fatalf("routing = %q, want %q", routing.Format(), c.want)
			}
		})
	}
}

func getAlertSecret(t *testing.T, c *fake.Clientset) *corev1.Secret {
	t.Helper()
	s, err := c.CoreV1().Secrets(monitoringNamespace).Get(context.Background(), alertConfigSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the configuration Secret is not there: %v", err)
	}
	return s
}

func TestInstallWritesTheDefaultWhenNoFlagIsGivenAndKeepsAnExistingRouting(t *testing.T) {
	ctx := context.Background()
	c := fake.NewSimpleClientset()

	routing, err := settleAlertRouting(ctx, c, AlertRoutingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if routing.Routed() {
		t.Fatalf("a first install with no flag routes nothing, got %+v", routing)
	}
	// The Alertmanager does not start without this Secret, so the install always leaves one.
	if got := getAlertSecret(t, c).Annotations[dcv1beta1.AnnotationAlertRouting]; got != "none" {
		t.Fatalf("annotation = %q, want none", got)
	}

	if _, err := settleAlertRouting(ctx, c, AlertRoutingRequest{Webhook: testWebhook, WatchdogWebhook: testWatchdog}); err != nil {
		t.Fatal(err)
	}
	// 🔴 A re-run of install for another reason must not undo the routing.
	routing, err = settleAlertRouting(ctx, c, AlertRoutingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if routing.Format() != "webhook,watchdog" {
		t.Fatalf("a flagless re-run changed the routing to %q", routing.Format())
	}
	if !strings.Contains(string(getAlertSecret(t, c).Data[alertConfigKey]), testWebhook) {
		t.Fatal("a flagless re-run rewrote the configuration")
	}
}

func TestARequestReplacesTheRoutingAndClearRemovesIt(t *testing.T) {
	ctx := context.Background()
	c := fake.NewSimpleClientset()
	if _, err := settleAlertRouting(ctx, c, AlertRoutingRequest{Webhook: testWebhook}); err != nil {
		t.Fatal(err)
	}
	routing, err := settleAlertRouting(ctx, c, emailRequest())
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(getAlertSecret(t, c).Data[alertConfigKey])
	if routing.Format() != "email" || strings.Contains(cfg, testWebhook) {
		t.Fatalf("naming email left the old webhook behind: %q\n%s", routing.Format(), cfg)
	}
	routing, err = settleAlertRouting(ctx, c, AlertRoutingRequest{Clear: true})
	if err != nil {
		t.Fatal(err)
	}
	if routing.Routed() || getAlertSecret(t, c).Annotations[dcv1beta1.AnnotationAlertRouting] != "none" {
		t.Fatalf("clear left %+v", routing)
	}
}

// 🔴 NO CREDENTIAL IS PUBLISHED. The annotation is read by the operator and by anyone who can
// read the Instance; only the KINDS of receiver go there.
func TestThePublishedSummaryCarriesNoCredential(t *testing.T) {
	req := emailRequest()
	req.Webhook = testWebhook
	req.WatchdogWebhook = testWatchdog
	c := fake.NewSimpleClientset()
	if _, err := settleAlertRouting(context.Background(), c, req); err != nil {
		t.Fatal(err)
	}
	s := getAlertSecret(t, c)
	for k, v := range s.Annotations {
		for _, secret := range []string{testWebhook, testWatchdog, testSMTPPass, "oncall@example.test", "token"} {
			if strings.Contains(v, secret) {
				t.Errorf("annotation %s carries %q", k, secret)
			}
		}
	}
	for k, v := range s.Labels {
		if strings.Contains(v, "token") || strings.Contains(v, "hooks") {
			t.Errorf("label %s = %q", k, v)
		}
	}
}

func TestAForeignSecretIsNeverOverwritten(t *testing.T) {
	c := fake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: alertConfigSecretName, Namespace: monitoringNamespace}, Data: map[string][]byte{alertConfigKey: []byte("mine")}})
	if _, err := settleAlertRouting(context.Background(), c, AlertRoutingRequest{Webhook: testWebhook}); err == nil {
		t.Fatal("overwrote a Secret dcctl did not write")
	}
	if got := string(getAlertSecret(t, c).Data[alertConfigKey]); got != "mine" {
		t.Fatalf("the foreign Secret now holds %q", got)
	}
}

func TestAnUpgradeNeedsTheWiringAnInstallLeaves(t *testing.T) {
	ctx := context.Background()
	c := fake.NewSimpleClientset()
	st := &State{AlertRouting: AlertRoutingRequest{Webhook: testWebhook}}

	// An Alertmanager that reads the stack's own configuration.
	if err := PrecheckAlertRoutingUpgrade(ctx, c, stackClient("-"), st); err == nil || !strings.Contains(err.Error(), "dcctl install") {
		t.Fatalf("an upgrade on a cluster whose Alertmanager reads nothing of ours must say to install first, got %v", err)
	}
	if _, err := c.CoreV1().Secrets(monitoringNamespace).Get(ctx, alertConfigSecretName, metav1.GetOptions{}); err == nil {
		t.Fatal("the refusal created the Secret anyway: a Secret nothing reads would be reported as routing")
	}

	// 🔴 The Secret is there but the apply that points the Alertmanager at it never finished
	// (install writes the Secret first). Existence is not wiring.
	if _, err := settleAlertRouting(ctx, c, AlertRoutingRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := PrecheckAlertRoutingUpgrade(ctx, c, stackClient("-"), st); err == nil || !strings.Contains(err.Error(), "dcctl install") {
		t.Fatalf("a Secret the Alertmanager does not read was accepted as wiring: %v", err)
	}
	if err := PrecheckAlertRoutingUpgrade(ctx, c, stackClient("some-other-secret"), st); err == nil {
		t.Fatal("an Alertmanager reading a different Secret was accepted as wiring")
	}

	wired := stackClient(alertConfigSecretName)
	if err := PrecheckAlertRoutingUpgrade(ctx, c, wired, st); err != nil {
		t.Fatal(err)
	}
	routing, err := applyAlertRoutingUpgrade(ctx, c, wired, st.AlertRouting)
	if err != nil || routing.Format() != "webhook" {
		t.Fatalf("got %+v, %v", routing, err)
	}
	// No flag, no precheck, no write.
	if err := PrecheckAlertRoutingUpgrade(ctx, fake.NewSimpleClientset(), stackClient(""), &State{}); err != nil {
		t.Fatalf("an upgrade without alert flags must not look at the wiring: %v", err)
	}
	// And an Alertmanager that cannot be listed is a refusal, not a pass.
	if err := PrecheckAlertRoutingUpgrade(ctx, c, brokenStackClient(), st); err == nil {
		t.Fatal("an unreadable Alertmanager was treated as wired")
	}
}

func TestAlertFlagsAreRefusedWhereThereIsNoMonitoring(t *testing.T) {
	st := &State{NoMonitoring: true, AlertRouting: AlertRoutingRequest{Webhook: testWebhook}}
	err := PrecheckAlertRoutingUpgrade(context.Background(), fake.NewSimpleClientset(), stackClient(""), st)
	if err == nil || !strings.Contains(err.Error(), "no monitoring") {
		t.Fatalf("got %v", err)
	}
}

func TestPublishingTheRoutingOnTheInstance(t *testing.T) {
	ctx := context.Background()
	c := fake.NewSimpleClientset()
	dyn := stackClient("-", declaredInstance(t, "prod", nil))
	st := &State{Instance: "prod"}

	// A cluster installed before alert routing existed: its Alertmanager drops everything,
	// and that is what is published.
	if err := publishAlertRouting(ctx, c, dyn, st); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, dyn, "prod").Annotations[dcv1beta1.AnnotationAlertRouting]; got != "none" {
		t.Fatalf("published %q, want none", got)
	}

	if _, err := settleAlertRouting(ctx, c, AlertRoutingRequest{Webhook: testWebhook, WatchdogWebhook: testWatchdog}); err != nil {
		t.Fatal(err)
	}
	// 🔴 The Secret says webhook, but the Alertmanager does not read it: that is not routed.
	if err := publishAlertRouting(ctx, c, dyn, st); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, dyn, "prod").Annotations[dcv1beta1.AnnotationAlertRouting]; got != "none" {
		t.Fatalf("published %q over an Alertmanager that does not read the Secret", got)
	}

	wired := stackClient(alertConfigSecretName, declaredInstance(t, "prod", nil))
	if err := publishAlertRouting(ctx, c, wired, st); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, wired, "prod").Annotations[dcv1beta1.AnnotationAlertRouting]; got != "webhook,watchdog" {
		t.Fatalf("published %q", got)
	}

	// Without a monitoring stack there is nothing to publish.
	off := stackClient("", declaredInstance(t, "bare", nil))
	if err := publishAlertRouting(ctx, c, off, &State{Instance: "bare", NoMonitoring: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readBack(t, off, "bare").Annotations[dcv1beta1.AnnotationAlertRouting]; ok {
		t.Fatal("published a routing for an instance with no monitoring")
	}

	// A routing that cannot be established publishes nothing rather than a guess.
	unknown := brokenStackClient(declaredInstance(t, "prod", nil))
	if err := publishAlertRouting(ctx, c, unknown, st); err != nil {
		t.Fatal(err)
	}
	if _, ok := readBack(t, unknown, "prod").Annotations[dcv1beta1.AnnotationAlertRouting]; ok {
		t.Fatal("published a routing that could not be established")
	}

	// A missing declaration is quiet: the next bootstrap writes one.
	if err := publishAlertRouting(ctx, c, stackClient("-"), &State{Instance: "ghost"}); err != nil {
		t.Fatalf("a missing declaration must not fail the run: %v", err)
	}
}

func TestAnUnreadableSecretAnnotationIsAnErrorNotARouting(t *testing.T) {
	c := fake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: alertConfigSecretName, Namespace: monitoringNamespace,
		Labels:      map[string]string{"app.kubernetes.io/managed-by": "dcctl"},
		Annotations: map[string]string{dcv1beta1.AnnotationAlertRouting: "pagerduty"}}})
	if r, _, err := readAlertRouting(context.Background(), c); err == nil {
		t.Fatalf("read %+v from a value dcctl does not write", r)
	}
}

func TestEveryAlertFlagIsInTheRequestThatReachesTheCluster(t *testing.T) {
	// Given() is what decides whether a run touches the routing; a field it forgot would let a
	// flag be silently ignored.
	for name, r := range map[string]AlertRoutingRequest{
		"webhook": {Webhook: "x"}, "watchdog": {WatchdogWebhook: "x"}, "email": {EmailTo: []string{"x"}},
		"config": {Config: "x"}, "clear": {Clear: true}, "smtp host": {SMTPHost: "x"}, "smtp from": {SMTPFrom: "x"},
		"smtp user": {SMTPUser: "x"}, "smtp password": {SMTPPassword: "x"},
	} {
		if !r.Given() {
			t.Errorf("%s alone is not Given", name)
		}
	}
	if (AlertRoutingRequest{}).Given() {
		t.Error("the zero request is Given")
	}
}

// A settled option dropped from the State literal is invisible to every other test, because
// Install needs a provider and a live cluster to reach it (see installState).
func TestTheInstallStateCarriesTheAlertRequest(t *testing.T) {
	st := installState(ClusterBinding{}, "local", InstallOptions{AlertRouting: AlertRoutingRequest{Webhook: testWebhook}})
	if st.AlertRouting.Webhook != testWebhook {
		t.Fatalf("the install state dropped the alert request: %+v", st.AlertRouting)
	}
}
