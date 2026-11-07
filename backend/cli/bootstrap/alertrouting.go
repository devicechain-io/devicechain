// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"sort"
	"strings"

	"github.com/fatih/color"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// The Alertmanager's configuration lives in a Secret dcctl writes, and the monitoring
// stack is told by name to read it (alertmanager_config_secret in the monitoring module).
// The name is a literal on both sides of a boundary nothing compiles across, so
// TestTheSecretNamesDcctlWritesAreTheOnesTheInfrastructureReads holds the two together.
//
// 🔴 A SECRET, NOT A TOFU VARIABLE. Receiver URLs and an SMTP password are credentials; a
// variable lands in the infrastructure state on this machine, and the stack is applied only
// by `install`, so `upgrade` could then not change a receiver at all. The Operator reloads a
// configuration Secret live, which lets both verbs write it.
const (
	alertConfigSecretName = "dc-alertmanager-config"
	alertConfigKey        = "alertmanager.yaml"

	nullReceiver     = "null"
	humanReceiver    = "devicechain-alerts"
	watchdogReceiver = "devicechain-watchdog"
)

// AlertRoutingRequest is what `--alert-webhook`, `--alert-email` (with its SMTP flags),
// `--alert-watchdog-webhook`, `--alert-config` and `--clear-alert-routing` ask for.
//
// 🔴 A REQUEST REPLACES THE ROUTING, IT DOES NOT MERGE INTO IT. Receivers are credentials
// and the config is a whole document; merging "the old email with the new webhook" would
// leave a receiver nobody just asked for. Naming any flag restates all of it, and
// --clear-alert-routing is the way back to no receiver.
type AlertRoutingRequest struct {
	Webhook         string
	WatchdogWebhook string
	EmailTo         []string
	SMTPHost        string // host:port
	SMTPFrom        string
	SMTPUser        string
	SMTPPassword    string
	// Config is the CONTENT of the operator's own Alertmanager configuration file.
	Config string
	Clear  bool
}

// Given reports whether any alert-routing flag was named.
func (r AlertRoutingRequest) Given() bool {
	return r.Webhook != "" || r.WatchdogWebhook != "" || len(r.EmailTo) > 0 || r.Config != "" || r.Clear ||
		r.SMTPHost != "" || r.SMTPFrom != "" || r.SMTPUser != "" || r.SMTPPassword != ""
}

func validHTTPURL(flag, v string) error {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%s must be an http(s) URL with a host", flag)
	}
	return nil
}

// Validate judges the request on its own, from argv alone, before any cluster is touched.
func (r AlertRoutingRequest) Validate() error {
	if !r.Given() {
		return nil
	}
	if r.Clear {
		if r.Webhook != "" || r.WatchdogWebhook != "" || len(r.EmailTo) > 0 || r.Config != "" ||
			r.SMTPHost != "" || r.SMTPFrom != "" || r.SMTPUser != "" || r.SMTPPassword != "" {
			return fmt.Errorf("--clear-alert-routing removes the routing and cannot be combined with another --alert-* flag")
		}
		return nil
	}
	if r.Config != "" {
		if r.Webhook != "" || r.WatchdogWebhook != "" || len(r.EmailTo) > 0 ||
			r.SMTPHost != "" || r.SMTPFrom != "" || r.SMTPUser != "" || r.SMTPPassword != "" {
			return fmt.Errorf("--alert-config is your whole Alertmanager configuration and cannot be combined with " +
				"--alert-webhook, --alert-email, --alert-watchdog-webhook or the --alert-smtp-* flags: route the Watchdog " +
				"alert inside your own file")
		}
		_, _, err := renderAlertmanagerConfig(r)
		return err
	}
	if r.Webhook != "" {
		if err := validHTTPURL("--alert-webhook", r.Webhook); err != nil {
			return err
		}
	}
	if r.WatchdogWebhook != "" {
		if err := validHTTPURL("--alert-watchdog-webhook", r.WatchdogWebhook); err != nil {
			return err
		}
	}
	smtp := r.SMTPHost != "" || r.SMTPFrom != "" || r.SMTPUser != "" || r.SMTPPassword != ""
	if len(r.EmailTo) == 0 {
		if smtp {
			return fmt.Errorf("the --alert-smtp-* flags configure --alert-email, which was not given")
		}
		return nil
	}
	for _, a := range r.EmailTo {
		if _, err := mail.ParseAddress(a); err != nil {
			return fmt.Errorf("--alert-email %q is not an email address", a)
		}
	}
	host, port, ok := strings.Cut(r.SMTPHost, ":")
	if !ok || host == "" || port == "" {
		return fmt.Errorf("--alert-email needs --alert-smtp-host as host:port (for example smtp.example.com:587)")
	}
	if _, err := mail.ParseAddress(r.SMTPFrom); err != nil {
		return fmt.Errorf("--alert-email needs --alert-smtp-from, the sender address (got %q)", r.SMTPFrom)
	}
	if (r.SMTPUser == "") != (r.SMTPPassword == "") {
		return fmt.Errorf("--alert-smtp-user and --alert-smtp-password-file go together: the server either authenticates or it does not")
	}
	return nil
}

// renderAlertmanagerConfig produces the document the Secret holds, and the routing summary
// that is published (kinds only, never a URL or an address).
func renderAlertmanagerConfig(r AlertRoutingRequest) (string, dcv1beta1.AlertRouting, error) {
	if r.Config != "" {
		routing, err := inspectAlertConfig(r.Config)
		if err != nil {
			return "", routing, fmt.Errorf("--alert-config: %w", err)
		}
		return r.Config, routing, nil
	}

	var routing dcv1beta1.AlertRouting
	receivers := []map[string]any{{"name": nullReceiver}}
	top := nullReceiver

	var human map[string]any
	if r.Webhook != "" || len(r.EmailTo) > 0 {
		human = map[string]any{"name": humanReceiver}
		top = humanReceiver
		receivers = append(receivers, human)
	}
	if r.Webhook != "" {
		human["webhook_configs"] = []map[string]any{{"url": r.Webhook, "send_resolved": true}}
		routing.Receivers = append(routing.Receivers, dcv1beta1.AlertReceiverWebhook)
	}
	if len(r.EmailTo) > 0 {
		var cfgs []map[string]any
		for _, to := range r.EmailTo {
			c := map[string]any{"to": to, "from": r.SMTPFrom, "smarthost": r.SMTPHost, "send_resolved": true}
			if r.SMTPUser != "" {
				c["auth_username"] = r.SMTPUser
				c["auth_password"] = r.SMTPPassword
			}
			cfgs = append(cfgs, c)
		}
		human["email_configs"] = cfgs
		routing.Receivers = append(routing.Receivers, dcv1beta1.AlertReceiverEmail)
	}

	// The Watchdog always fires, by design: it is the dead-man's signal. Routed to a human
	// receiver it would page them every group interval, so it goes to its own receiver or
	// to the null one, never to the alerts receiver. InfoInhibitor is the stack's own
	// inhibition helper and is never worth a message.
	routes := []map[string]any{}
	if r.WatchdogWebhook != "" {
		receivers = append(receivers, map[string]any{
			"name":            watchdogReceiver,
			"webhook_configs": []map[string]any{{"url": r.WatchdogWebhook, "send_resolved": false}},
		})
		routes = append(routes, map[string]any{
			"receiver": watchdogReceiver, "matchers": []string{`alertname = "Watchdog"`},
			"group_wait": "0s", "group_interval": "1m", "repeat_interval": "1m",
		})
		routing.Watchdog = true
	} else {
		routes = append(routes, map[string]any{"receiver": nullReceiver, "matchers": []string{`alertname = "Watchdog"`}})
	}
	routes = append(routes, map[string]any{"receiver": nullReceiver, "matchers": []string{`alertname = "InfoInhibitor"`}})

	doc := map[string]any{
		"global": map[string]any{"resolve_timeout": "5m"},
		"route": map[string]any{
			"receiver": top, "group_by": []string{"namespace", "alertname"},
			"group_wait": "30s", "group_interval": "5m", "repeat_interval": "12h",
			"routes": routes,
		},
		"receivers": receivers,
		"inhibit_rules": []map[string]any{{
			"source_matchers": []string{`severity = "critical"`},
			"target_matchers": []string{`severity =~ "warning|info"`},
			"equal":           []string{"namespace", "alertname"},
		}},
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return "", routing, err
	}
	sort.Strings(routing.Receivers)
	return string(b), routing, nil
}

// inspectAlertConfig reads an operator's own configuration just far enough to answer what
// the AlertsRouted condition asks: does any receiver actually deliver, and is the Watchdog
// sent to one. It also refuses what Alertmanager would refuse, because a configuration the
// Alertmanager rejects is not applied (it keeps the previous one) and says so only in its log.
func inspectAlertConfig(text string) (dcv1beta1.AlertRouting, error) {
	var routing dcv1beta1.AlertRouting
	var doc struct {
		Route     map[string]any   `json:"route"`
		Receivers []map[string]any `json:"receivers"`
	}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return routing, fmt.Errorf("is not valid YAML: %w", err)
	}
	if len(doc.Route) == 0 || len(doc.Receivers) == 0 {
		return routing, fmt.Errorf("needs a top-level route and at least one receiver")
	}
	delivers := map[string]bool{}
	for _, r := range doc.Receivers {
		name, _ := r["name"].(string)
		if name == "" {
			return routing, fmt.Errorf("has a receiver with no name")
		}
		if _, dup := delivers[name]; dup {
			return routing, fmt.Errorf("names receiver %q twice", name)
		}
		d := false
		for k, v := range r {
			if k == "name" {
				continue
			}
			if !strings.HasSuffix(k, "_configs") {
				return routing, fmt.Errorf("receiver %q has a field %q, which is not a notification integration (they end in _configs)", name, k)
			}
			l, ok := v.([]any)
			if !ok {
				return routing, fmt.Errorf("receiver %q: %s must be a list", name, k)
			}
			for i, e := range l {
				if err := checkIntegration(name, k, i, e); err != nil {
					return routing, err
				}
				d = true
			}
		}
		delivers[name] = d
	}
	var walk func(route map[string]any) error
	walk = func(route map[string]any) error {
		recv, _ := route["receiver"].(string)
		if recv != "" {
			d, ok := delivers[recv]
			if !ok {
				return fmt.Errorf("routes to receiver %q, which it does not define", recv)
			}
			if d {
				// 🔴 A RECEIVER REACHED ONLY THROUGH THE WATCHDOG IS NOT A PERSON. The dead-man's
				// receiver delivers by design, and counting it as routing would silence the
				// warning over a configuration whose every real alert goes to the null receiver.
				if routesWatchdog(route) {
					routing.Watchdog = true
				} else {
					routing.Receivers = []string{dcv1beta1.AlertReceiverConfig}
				}
			}
		}
		if routesWatchdog(route) {
			// Everything beneath a route that selects the Watchdog sees only the Watchdog.
			return nil
		}
		kids, _ := route["routes"].([]any)
		for _, k := range kids {
			if m, ok := k.(map[string]any); ok {
				if err := walk(m); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(doc.Route); err != nil {
		return routing, err
	}
	return routing, nil
}

// checkIntegration refuses a notification integration Alertmanager would refuse for want of
// the field that makes it deliver. It is not Alertmanager's parser, and a configuration that
// passes here can still be rejected by it (see the docs: run amtool check-config first); it
// closes the common way a file looks complete and is not.
func checkIntegration(receiver, kind string, i int, e any) error {
	m, ok := e.(map[string]any)
	if !ok || len(m) == 0 {
		return fmt.Errorf("receiver %q: %s[%d] is empty, so it delivers nowhere", receiver, kind, i)
	}
	has := func(keys ...string) bool {
		for _, k := range keys {
			if v, ok := m[k]; ok && v != nil && v != "" {
				return true
			}
		}
		return false
	}
	switch kind {
	case "webhook_configs":
		if !has("url", "url_file") {
			return fmt.Errorf("receiver %q: webhook_configs[%d] needs a url (or url_file)", receiver, i)
		}
	case "email_configs":
		if !has("to") {
			return fmt.Errorf("receiver %q: email_configs[%d] needs a to address", receiver, i)
		}
	}
	return nil
}

// routesWatchdog reports whether a route selects the Watchdog alert by name.
func routesWatchdog(route map[string]any) bool {
	if m, ok := route["match"].(map[string]any); ok {
		if v, _ := m["alertname"].(string); v == "Watchdog" {
			return true
		}
	}
	if l, ok := route["matchers"].([]any); ok {
		for _, x := range l {
			s, _ := x.(string)
			if strings.Contains(s, "alertname") && strings.Contains(s, "Watchdog") && !strings.Contains(s, "!=") && !strings.Contains(s, "!~") {
				return true
			}
		}
	}
	return false
}

// alertConfigSecret is the Secret for a settled configuration.
func alertConfigSecret(cfg string, routing dcv1beta1.AlertRouting) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: alertConfigSecretName, Namespace: monitoringNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dcctl",
				"app.kubernetes.io/name":       "alertmanager",
				"app.kubernetes.io/component":  "monitoring",
			},
			Annotations: map[string]string{dcv1beta1.AnnotationAlertRouting: routing.Format()},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: nil,
		Data:       map[string][]byte{alertConfigKey: []byte(cfg)},
	}
}

func isOurAlertSecret(s *corev1.Secret) bool {
	return s.Labels["app.kubernetes.io/managed-by"] == "dcctl"
}

// readAlertRouting reads the routing the configuration Secret was last written with.
// found is false when the Secret does not exist.
func readAlertRouting(ctx context.Context, typed kubernetes.Interface) (routing dcv1beta1.AlertRouting, found bool, err error) {
	s, err := typed.CoreV1().Secrets(monitoringNamespace).Get(ctx, alertConfigSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return routing, false, nil
	}
	if err != nil {
		return routing, false, fmt.Errorf("reading the Alertmanager configuration Secret: %w", err)
	}
	if !isOurAlertSecret(s) {
		return routing, true, fmt.Errorf("Secret %s/%s exists but dcctl did not write it, so it is not read as this cluster's alert routing",
			monitoringNamespace, alertConfigSecretName)
	}
	routing, err = dcv1beta1.ParseAlertRouting(s.Annotations[dcv1beta1.AnnotationAlertRouting])
	if err != nil {
		return routing, true, fmt.Errorf("Secret %s/%s: %w", monitoringNamespace, alertConfigSecretName, err)
	}
	return routing, true, nil
}

// writeAlertConfig creates or replaces the configuration Secret. It refuses to overwrite a
// Secret of that name it did not write.
func writeAlertConfig(ctx context.Context, typed kubernetes.Interface, cfg string, routing dcv1beta1.AlertRouting) error {
	api := typed.CoreV1().Secrets(monitoringNamespace)
	want := alertConfigSecret(cfg, routing)
	existing, err := api.Get(ctx, alertConfigSecretName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := api.Create(ctx, want, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("writing the Alertmanager configuration Secret: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("reading the Alertmanager configuration Secret: %w", err)
	case !isOurAlertSecret(existing):
		return fmt.Errorf("refusing to overwrite Secret %s/%s: it carries no app.kubernetes.io/managed-by=dcctl label, so dcctl did not write it",
			monitoringNamespace, alertConfigSecretName)
	}
	updated := existing.DeepCopy()
	updated.Data = want.Data
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[dcv1beta1.AnnotationAlertRouting] = routing.Format()
	if _, err := api.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating the Alertmanager configuration Secret: %w", err)
	}
	return nil
}

// settleAlertRouting is the install's step: it leaves the Alertmanager configuration
// Secret in the state the monitoring stack, applied right after, needs to start with.
//
// 🔴 THE SECRET MUST EXIST BEFORE THE APPLY. The stack is told to read it by name, and an
// Alertmanager whose configuration Secret is missing does not start. So an install with no
// alert flag writes the default (no receiver) rather than leaving the Secret out, and an
// install that finds one already there LEAVES IT: re-running install to change something
// else must not undo the routing an earlier run set up.
func settleAlertRouting(ctx context.Context, typed kubernetes.Interface, req AlertRoutingRequest) (dcv1beta1.AlertRouting, error) {
	if !req.Given() {
		current, found, err := readAlertRouting(ctx, typed)
		if err != nil {
			return current, err
		}
		if found {
			return current, nil
		}
	}
	return applyAlertRoutingRequest(ctx, typed, req)
}

func applyAlertRoutingRequest(ctx context.Context, typed kubernetes.Interface, req AlertRoutingRequest) (dcv1beta1.AlertRouting, error) {
	if req.Clear {
		req = AlertRoutingRequest{}
	}
	cfg, routing, err := renderAlertmanagerConfig(req)
	if err != nil {
		return routing, err
	}
	return routing, writeAlertConfig(ctx, typed, cfg, routing)
}

// applyAlertRoutingUpgrade is the upgrade's step. The Secret has to exist already: it is
// written by `dcctl install` at this release, together with the stack's wiring to it, so a
// Secret nobody reads must not be created here and reported as routing.
func applyAlertRoutingUpgrade(ctx context.Context, typed kubernetes.Interface, dyn dynamic.Interface, req AlertRoutingRequest) (dcv1beta1.AlertRouting, error) {
	if err := requireAlertWiring(ctx, typed, dyn); err != nil {
		return dcv1beta1.AlertRouting{}, err
	}
	return applyAlertRoutingRequest(ctx, typed, req)
}

// alertmanagerGVR is the Prometheus Operator's Alertmanager, which the monitoring stack
// creates and whose spec.configSecret names the Secret it reads its configuration from.
var alertmanagerGVR = schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "alertmanagers"}

// alertmanagerReadsOurSecret reports whether the Alertmanager the monitoring stack runs is
// told to read the configuration Secret dcctl writes.
//
// 🔴 THE SECRET'S EXISTENCE PROVES NOTHING ABOUT WHO READS IT. Install writes the Secret
// BEFORE the apply that points the Alertmanager at it, so an apply that failed leaves a
// Secret nothing reads while the Alertmanager still runs its previous (null) configuration;
// a later upgrade would then write receivers into it and report them routed. Asking the
// Alertmanager itself is the only answer that is not a guess.
func alertmanagerReadsOurSecret(ctx context.Context, dyn dynamic.Interface) (bool, error) {
	list, err := dyn.Resource(alertmanagerGVR).Namespace(monitoringNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("reading the Alertmanager the monitoring stack runs: %w", err)
	}
	for _, a := range list.Items {
		if v, _, _ := unstructured.NestedString(a.Object, "spec", "configSecret"); v == alertConfigSecretName {
			return true, nil
		}
	}
	return false, nil
}

func requireAlertWiring(ctx context.Context, typed kubernetes.Interface, dyn dynamic.Interface) error {
	_, found, err := readAlertRouting(ctx, typed)
	if err != nil {
		return err
	}
	reads := false
	if found {
		if reads, err = alertmanagerReadsOurSecret(ctx, dyn); err != nil {
			return err
		}
	}
	if !found || !reads {
		return fmt.Errorf("this cluster's Alertmanager is not wired to a configuration dcctl writes yet " +
			"(it was installed by an earlier release, or an install that would have wired it did not finish), " +
			"so the alert flags would write a Secret nothing reads. " +
			"Run `dcctl install` with the same flags first; it re-applies the cluster prerequisites in place")
	}
	return nil
}

// PrecheckAlertRoutingUpgrade is the read an upgrade makes before it writes anything.
func PrecheckAlertRoutingUpgrade(ctx context.Context, typed kubernetes.Interface, dyn dynamic.Interface, st *State) error {
	if !st.AlertRouting.Given() {
		return nil
	}
	if !monitoringEnabled(st) {
		return fmt.Errorf("this instance's cluster runs no monitoring stack (installed with --no-monitoring), so there is no Alertmanager to route alerts from")
	}
	return requireAlertWiring(ctx, typed, dyn)
}

// patchAlertRouting publishes the routing on one Instance. A merge patch, for the reason
// setInstancePhase gives: the operator writes this object's status concurrently.
func patchAlertRouting(ctx context.Context, dyn dynamic.Interface, id string, routing dcv1beta1.AlertRouting) error {
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{
		dcv1beta1.AnnotationAlertRouting: routing.Format(),
	}}})
	if err != nil {
		return err
	}
	if _, err := dyn.Resource(instanceGVR).Patch(ctx, id, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		if isInstanceNotFound(err, id) {
			return nil
		}
		return fmt.Errorf("publishing the alert routing on instance %q: %w", id, err)
	}
	return nil
}

// effectiveAlertRouting is what the cluster's alerts do RIGHT NOW: the routing the
// configuration Secret holds, but only when the Alertmanager is told to read it. A cluster
// whose Alertmanager has never been wired to a Secret dcctl writes (installed by an earlier
// release, or an install that did not finish) runs the stack's default configuration, whose
// only receiver drops everything, and that is "none".
//
// An error means this could not be established, which is not "none": the caller publishes
// nothing, and the operator reports Unknown rather than a claim in either direction.
func effectiveAlertRouting(ctx context.Context, typed kubernetes.Interface, dyn dynamic.Interface) (dcv1beta1.AlertRouting, error) {
	routing, found, err := readAlertRouting(ctx, typed)
	if err != nil {
		return dcv1beta1.AlertRouting{}, err
	}
	if !found {
		return dcv1beta1.AlertRouting{}, nil
	}
	reads, err := alertmanagerReadsOurSecret(ctx, dyn)
	if err != nil {
		return dcv1beta1.AlertRouting{}, err
	}
	if !reads {
		return dcv1beta1.AlertRouting{}, nil
	}
	return routing, nil
}

// publishAlertRouting records, on the Instance, where the cluster's alerts go, so the
// operator can report the AlertsRouted condition without access to Secrets (see
// dcv1beta1.AnnotationAlertRouting). Callers are bootstrap and upgrade.
//
// With no monitoring stack it publishes nothing: the operator reports that from the
// declaration. When the routing cannot be established it publishes nothing and says so,
// rather than failing a bootstrap or an upgrade over an observation.
func publishAlertRouting(ctx context.Context, typed kubernetes.Interface, dyn dynamic.Interface, st *State) error {
	if !monitoringEnabled(st) {
		return nil
	}
	routing, err := effectiveAlertRouting(ctx, typed, dyn)
	if err != nil {
		fmt.Printf("  %s not publishing the alert routing: %v\n", color.YellowString("warning:"), err)
		return nil
	}
	return patchAlertRouting(ctx, dyn, st.Instance, routing)
}

// publishAlertRoutingToCluster publishes the routing on every instance the cluster holds.
//
// 🔴 IT READS THE ROUTING AT PUBLICATION, NOT AT THE START OF THE RUN. An install holds the
// routing it settled across a long apply, and a second dcctl run can change it meanwhile;
// publishing the earlier reading would tell every instance a webhook exists that the Secret
// no longer has. The cluster is the source of truth, read as late as possible.
//
// The errors are returned for the caller to decide on: an install that has already built the
// cluster prints them, an upgrade fails on them, because an instance left holding an older
// "routed" claim would read as routed after the routing was cleared.
func publishAlertRoutingToCluster(ctx context.Context, kubeContext string) (dcv1beta1.AlertRouting, []error) {
	dyn, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return dcv1beta1.AlertRouting{}, []error{err}
	}
	routing, err := effectiveAlertRouting(ctx, typed, dyn)
	if err != nil {
		return routing, []error{err}
	}
	held, err := readClusterInstances(ctx, kubeContext)
	if err != nil {
		return routing, []error{err}
	}
	var errs []error
	for _, id := range held.IDs {
		if err := patchAlertRouting(ctx, dyn, id, routing); err != nil {
			errs = append(errs, err)
		}
	}
	return routing, errs
}

// SayAlertRouting says what the cluster's alerts do, in the install's and upgrade's output.
func SayAlertRouting(routing dcv1beta1.AlertRouting) string {
	switch {
	case !routing.Routed():
		return "ALERTS NOT ROUTED: no receiver is configured, so every alert is dropped. " +
			"Re-run with --alert-webhook, --alert-email or --alert-config"
	case !routing.Watchdog:
		return "routed to " + strings.Join(routing.Receivers, ", ") + "; the Watchdog dead-man alert is not routed (--alert-watchdog-webhook)"
	}
	return "routed to " + strings.Join(routing.Receivers, ", ") + ", with the Watchdog dead-man alert routed"
}
