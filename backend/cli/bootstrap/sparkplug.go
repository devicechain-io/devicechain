// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

// The Sparkplug source path: `dcctl bootstrap|upgrade --sparkplug-source <file>`.
//
// A Sparkplug source is a connection OUT to a customer's MQTT broker, bound to one tenant:
// the broker's address and login, the groups to read, the device type a new node is
// registered under, and whether the downlink commands the plant. It is wiring and a
// credential, so it is not tunable: a --values overlay refuses it (see ownedWithinTunable),
// and before this flag the only way to give an instance one was to edit the Helm release,
// which the next `dcctl upgrade` rebuilt without it.
//
// 🔴 TWO CONFIG PLANES, AND THE SOURCE STRADDLES THEM ON PURPOSE. The source itself (address,
// username, groups, device type, command limits) is FIXED configuration: it lands in the
// sparkplug-ingest area's mounted config document, which a pod reads once at start and which
// rolls the pods when it changes. The broker PASSWORD is not configuration at all, so it never
// enters that document, and it never enters the Helm release either.
//
// 🔴 THE PASSWORD IS NEVER A HELM VALUE. A release records the values of every revision it keeps
// (secret driver, readable with `helm get values`), and rotating a password does not retract the
// old one: the previous revision still holds it. So dcctl writes the Secret itself,
// dci-<instance>-sparkplug-broker, before the release and outside its manifest, the way it writes
// the instance configuration; the release carries only the Secret's NAME (in the pod's secretKeyRef)
// and a stamp, salted per instance and derived from the Secret's version rather than its content,
// that rolls the pods when the password changes. An upgrade
// therefore reads nothing secret out of the release, and the Secret survives it because Helm
// never owned it. A source may instead name a Secret the operator manages themselves
// (broker.passwordSecretRef), which dcctl neither writes nor reads. The file dcctl reads may
// carry the password, or name a file that does; nothing here prints it, and an error names a
// source by its position and tenant only.
//
// What the operator hands dcctl is not recorded anywhere dcctl can read it back, so the rendered
// source configuration is CARRIED from the previous release by every later upgrade
// (carriedValuePaths) and replaced only by a run that is given the file again. Withdrawing the
// sources is explicit: --withdraw-sparkplug-sources, and a bootstrap re-run that would silently
// drop them is refused without it.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// sparkplugBrokerSecretName is the one Secret dcctl writes to hold every source's broker
// password, one key per source. It is written outside the Helm release.
func sparkplugBrokerSecretName(instance string) string {
	return fmt.Sprintf("dci-%s-sparkplug-broker", instance)
}

// SparkplugBroker is a source's broker connection as the --sparkplug-source file writes it.
type SparkplugBroker struct {
	// URL is the broker address: tcp://host:1883, or ssl:// (or tls://) for TLS.
	URL string `json:"url"`
	// Username is the MQTT login; empty connects anonymously.
	Username string `json:"username,omitempty"`
	// Password is the MQTT password. It is rendered into a Secret, never into the source's
	// configuration. PasswordFile names a file holding it instead (one trailing newline is
	// dropped), so the secret need not sit in the file you keep.
	Password     string `json:"password,omitempty"`
	PasswordFile string `json:"passwordFile,omitempty"`
	// PasswordSecretRef names a Secret in the instance's namespace that holds the password, for an
	// operator who manages it themselves. dcctl projects it into the pod and never reads or
	// writes it, so it appears in no value and no digest of one.
	PasswordSecretRef *SparkplugSecretRef `json:"passwordSecretRef,omitempty"`
}

// SparkplugSecretRef names one key of a Secret.
type SparkplugSecretRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// String and GoString keep the password out of any %v / %+v / %#v of a source that ends up in
// an error or a log line.
func (b SparkplugBroker) String() string {
	return fmt.Sprintf("{url:%s username:%s password:%s}", b.URL, b.Username, redactedIfSet(b.Password))
}

// hasDcctlPassword says the password is one dcctl holds and writes (not an external reference).
func (b SparkplugBroker) hasDcctlPassword() bool { return b.Password != "" }
func (b SparkplugBroker) GoString() string       { return b.String() }

func redactedIfSet(s string) string {
	if s == "" {
		return "<unset>"
	}
	return "<redacted>"
}

// SparkplugSource is one tenant-bound Sparkplug Host Application connection, as the
// --sparkplug-source file writes it. The keys are the sparkplug-ingest area's own
// (sources[] in its configuration), plus broker.password / broker.passwordFile standing in
// for broker.passwordEnv, which dcctl writes itself.
type SparkplugSource struct {
	Tenant          string          `json:"tenant"`
	HostId          string          `json:"hostId,omitempty"`
	Broker          SparkplugBroker `json:"broker"`
	Groups          []string        `json:"groups,omitempty"`
	DeviceTypeToken string          `json:"deviceTypeToken,omitempty"`
	AutoRegister    bool            `json:"autoRegister,omitempty"`
	// Commands is the source's command downlink, passed to the area as written. Its keys are
	// checked against sparkplugCommandKeys, so a misspelling fails here and not as a pod that
	// refuses its configuration.
	Commands map[string]interface{} `json:"commands,omitempty"`
}

// sparkplugCommandKeys are the keys of a source's commands block (SourceCommands in
// sparkplug-ingest/config). TestSparkplugSourceKeysAreTheServicesOwn holds it, and the
// struct above, to that declaration.
var sparkplugCommandKeys = []string{
	"enabled", "hostRole", "writableMetrics", "allowReboot", "confirmWindowSeconds",
	"rebootWindowSeconds", "clockSkewSeconds", "publishPerSecond", "burst",
}

// ParseSparkplugSources reads and validates the --sparkplug-source file: a JSON or YAML list of
// sources. It validates up front, before any cluster exists, so a missing tenant, a bad broker
// URL or an autoRegister without a device type fails as a one-line CLI error and not as a
// source the pod refuses at start. An empty path returns (nil, nil): the flag is off.
func ParseSparkplugSources(path string) ([]SparkplugSource, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the sources file: %w", err)
	}
	var sources []SparkplugSource
	if err := yaml.UnmarshalStrict(raw, &sources); err != nil {
		return nil, fmt.Errorf("parsing the sources file (expected a list of {tenant, hostId, broker{url, username, password}, groups, deviceTypeToken, autoRegister, commands}): %w", err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("sources file %q holds no sources", path)
	}
	if err := resolveSparkplugPasswords(sources); err != nil {
		return nil, err
	}
	if err := validateSparkplugSources(sources); err != nil {
		return nil, err
	}
	return sources, nil
}

// resolveSparkplugPasswords reads each passwordFile into its source's Password.
func resolveSparkplugPasswords(sources []SparkplugSource) error {
	for i := range sources {
		b := &sources[i].Broker
		if b.PasswordFile == "" {
			continue
		}
		if b.Password != "" {
			return fmt.Errorf("sources[%d] (tenant %q): give broker.password or broker.passwordFile, not both", i, sources[i].Tenant)
		}
		if b.PasswordSecretRef != nil {
			return fmt.Errorf("sources[%d] (tenant %q): give a password (password, passwordFile) or passwordSecretRef, not both", i, sources[i].Tenant)
		}
		raw, err := os.ReadFile(b.PasswordFile)
		if err != nil {
			return fmt.Errorf("sources[%d] (tenant %q): reading broker.passwordFile: %w", i, sources[i].Tenant, err)
		}
		b.Password = strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
		b.PasswordFile = ""
		if b.Password == "" {
			return fmt.Errorf("sources[%d] (tenant %q): broker.passwordFile is empty", i, sources[i].Tenant)
		}
	}
	return nil
}

// validateSparkplugSources mirrors the checks sparkplug-ingest makes of its configuration at
// start (SparkplugConfiguration.Validate), for the failures an operator can cause with this
// file. The service stays the fail-closed authority; this turns its startup refusal, which
// surfaces after a ten-minute rollout wait, into a sentence.
func validateSparkplugSources(sources []SparkplugSource) error {
	endpointTenant := map[string]string{}
	stateIdentity := map[string]int{}
	for i := range sources {
		s := &sources[i]
		where := fmt.Sprintf("sources[%d]", i)
		if strings.TrimSpace(s.Tenant) == "" {
			return fmt.Errorf("%s: tenant is required", where)
		}
		where = fmt.Sprintf("sources[%d] (tenant %q)", i, s.Tenant)
		u, err := url.Parse(s.Broker.URL)
		switch {
		case strings.TrimSpace(s.Broker.URL) == "":
			return fmt.Errorf("%s: broker.url is required", where)
		case err != nil:
			return fmt.Errorf("%s: broker.url is not a valid URL: %w", where, err)
		case u.Scheme != "tcp" && u.Scheme != "ssl" && u.Scheme != "tls":
			return fmt.Errorf("%s: broker.url has unsupported scheme %q (want tcp:// or ssl://)", where, u.Scheme)
		case u.Host == "":
			return fmt.Errorf("%s: broker.url must include a host", where)
		}
		endpoint := strings.ToLower(u.Host)
		if s.HostId != "" && strings.ContainsAny(s.HostId, "/+#\x00") {
			return fmt.Errorf("%s: hostId must be a single MQTT topic level with no '/', '+' or '#'", where)
		}
		for _, g := range s.Groups {
			if g == "" || strings.ContainsAny(g, "/+#\x00") {
				return fmt.Errorf("%s: each group must be a single MQTT topic level with no '/', '+' or '#' (got %q)", where, g)
			}
		}
		if s.AutoRegister && strings.TrimSpace(s.DeviceTypeToken) == "" {
			return fmt.Errorf("%s: deviceTypeToken is required when autoRegister is true (a device cannot be created without a device type)", where)
		}
		if (s.Broker.Password != "" || s.Broker.PasswordSecretRef != nil) && s.Broker.Username == "" {
			return fmt.Errorf("%s: a broker password is set without broker.username", where)
		}
		if ref := s.Broker.PasswordSecretRef; ref != nil {
			if s.Broker.Password != "" {
				return fmt.Errorf("%s: give a password (password, passwordFile) or passwordSecretRef, not both", where)
			}
			if !secretRefNamePattern.MatchString(ref.Name) || ref.Key == "" || strings.ContainsAny(ref.Key, "/\x00") {
				return fmt.Errorf("%s: broker.passwordSecretRef needs the name of a Secret (lower-case letters, digits, '-' and '.') and a key", where)
			}
		}
		for k := range s.Commands {
			if !containsString(sparkplugCommandKeys, k) {
				return fmt.Errorf("%s: commands.%s is not a command setting (known: %s)", where, k, strings.Join(sparkplugCommandKeys, ", "))
			}
		}
		// One broker, one tenant: a broker connection is the tenant trust boundary.
		if prev, ok := endpointTenant[endpoint]; ok && prev != s.Tenant {
			return fmt.Errorf("%s: broker %q already serves tenant %q and cannot also serve %q: a broker connection is the tenant trust boundary", where, s.Broker.URL, prev, s.Tenant)
		}
		endpointTenant[endpoint] = s.Tenant
		key := endpoint + "\x00" + s.HostId
		if prev, dup := stateIdentity[key]; dup {
			return fmt.Errorf("%s: broker %q and hostId %q collide with sources[%d]: two Host Applications cannot share a STATE identity on one broker", where, s.Broker.URL, s.HostId, prev)
		}
		stateIdentity[key] = i
	}
	return nil
}

// secretRefNamePattern is a Kubernetes Secret name (a DNS subdomain).
var secretRefNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// sparkplugPasswordEnv is the variable a source's password reaches the pod in.
func sparkplugPasswordEnv(n int) string { return fmt.Sprintf("DC_SPARKPLUG_BROKER_PW_%d", n) }

// sparkplugBrokerSecretData is what dcctl writes into the broker Secret: one key per source that
// carries a password of its own. Empty when none does.
func sparkplugBrokerSecretData(sources []SparkplugSource) map[string]string {
	data := map[string]string{}
	for i, s := range sources {
		if s.Broker.hasDcctlPassword() {
			data[sparkplugPasswordEnv(i+1)] = s.Broker.Password
		}
	}
	return data
}

// sparkplugProvisioning turns the parsed sources into the one value fragment the chart consumes:
// the sparkplug-ingest area's config (sources[] naming each password's variable) and the extraEnv
// that projects them from a Secret by NAME. It carries no password: the Secret is written by
// writeSparkplugBrokerSecret, outside the release. A source's variable is indexed by its
// position, so it is collision-free whatever the sources contain.
func sparkplugProvisioning(instance string, sources []SparkplugSource) (areaConfig map[string]interface{}) {
	secretName := sparkplugBrokerSecretName(instance)
	rendered := make([]interface{}, 0, len(sources))
	extraEnv := make([]interface{}, 0, len(sources))
	for i, s := range sources {
		broker := map[string]interface{}{"url": s.Broker.URL}
		if s.Broker.Username != "" {
			broker["username"] = s.Broker.Username
		}
		ref := s.Broker.PasswordSecretRef
		if s.Broker.hasDcctlPassword() || ref != nil {
			env := sparkplugPasswordEnv(i + 1)
			name, key := secretName, env
			if ref != nil {
				name, key = ref.Name, ref.Key
			}
			broker["passwordEnv"] = env
			extraEnv = append(extraEnv, map[string]interface{}{
				"name": env,
				"valueFrom": map[string]interface{}{
					"secretKeyRef": map[string]interface{}{"name": name, "key": key},
				},
			})
		}
		src := map[string]interface{}{"tenant": s.Tenant, "broker": broker}
		if s.HostId != "" {
			src["hostId"] = s.HostId
		}
		if len(s.Groups) > 0 {
			groups := make([]interface{}, len(s.Groups))
			for j, g := range s.Groups {
				groups[j] = g
			}
			src["groups"] = groups
		}
		if s.DeviceTypeToken != "" {
			src["deviceTypeToken"] = s.DeviceTypeToken
		}
		if s.AutoRegister {
			src["autoRegister"] = true
		}
		if len(s.Commands) > 0 {
			src["commands"] = s.Commands
		}
		rendered = append(rendered, src)
	}
	areaConfig = map[string]interface{}{"config": map[string]interface{}{"sources": rendered}}
	if len(extraEnv) > 0 {
		areaConfig["extraEnv"] = extraEnv
	}
	return areaConfig
}

// sparkplugCredentialsStamp is the value stamped on the sparkplug-ingest pods so a rotated
// password rolls them: a secretKeyRef is read once, at container start, and the Secret keeps its
// name when its content changes.
//
// 🔴 IT IS NOT A HASH OF THE PASSWORD, OF ANY OF ITS BYTES, OR OF A VALUE CARRYING THEM. It lands in
// the release values, which are readable, and any digest of a low-entropy secret there is an offline
// guessing oracle, salted or not. The API server already versions the Secret: its resourceVersion
// moves whenever the content does and stands still when it does not, so the stamp is derived from
// that, salted per instance with the declaration's UID and bound to the Secret's own UID. A run that
// rewrites the same password changes nothing and rolls nothing. An absent Secret has no stamp.
func sparkplugCredentialsStamp(instanceUID string, s *corev1.Secret) string {
	return credentialsStamp("sparkplug-broker-stamp", instanceUID, s)
}

// credentialsStamp is the version stamp every area credentials Secret uses, under a domain of its
// own so two stamps never collide. See sparkplugCredentialsStamp for why it is not a digest of the
// secret.
func credentialsStamp(domain, instanceUID string, s *corev1.Secret) string {
	if s == nil {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(domain + "\x00"))
	h.Write([]byte(instanceUID))
	h.Write([]byte{0})
	h.Write([]byte(s.Name))
	h.Write([]byte{0})
	h.Write([]byte(s.UID))
	h.Write([]byte{0})
	h.Write([]byte(s.ResourceVersion))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// syncSparkplugBrokerSecret settles the broker Secret for this run, before the release, and
// returns the stamp (sparkplugCredentialsStamp) for the pods.
//
//   - A run GIVEN sources (or told to withdraw them) is the authority: the Secret is rewritten to
//     hold exactly its passwords (a rotation replaces the old one in place), and removed when the
//     run has none, so a retired password does not linger.
//   - Any other run keeps what is there and only reads its identity for the stamp, so an upgrade
//     that is given no file leaves the password alone and the pods' stamp where it was.
func syncSparkplugBrokerSecret(ctx context.Context, typed kubernetes.Interface, st *State) (string, error) {
	name, ns := sparkplugBrokerSecretName(st.Instance), InstanceNamespace(st.Instance)
	owner := instanceOwner(st.Instance, st.InstanceUID)
	if st.SparkplugSourcesGiven {
		data := sparkplugBrokerSecretData(st.SparkplugSources)
		if len(data) == 0 {
			return "", deleteOwnedSecretIfOurs(ctx, typed, owner, ns, name)
		}
		if err := writeOwnedSecret(ctx, typed, owner, ownedSecret{
			Name: name, Namespace: ns, Type: corev1.SecretTypeOpaque,
			Labels: map[string]string{"devicechain.io/instance": st.Instance}, Data: data,
		}, time.Now); err != nil {
			return "", err
		}
	}
	s, err := typed.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading Secret %s/%s, which holds the Sparkplug broker passwords: %w", ns, name, err)
	}
	return sparkplugCredentialsStamp(st.InstanceUID, s), nil
}

// deleteOwnedSecretIfOurs removes a Secret dcctl wrote for this instance, and leaves one it did not.
func deleteOwnedSecretIfOurs(ctx context.Context, typed kubernetes.Interface, owner secretOwner, ns, name string) error {
	api := typed.CoreV1().Secrets(ns)
	existing, err := api.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("reading Secret %s/%s: %w", ns, name, err)
	}
	if reason := foreignReason(readOwnership(existing), owner); reason != "" {
		return &ErrForeignSecret{ns, name, reason}
	}
	if err := api.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("removing Secret %s/%s: %w", ns, name, err)
	}
	return nil
}

// withSparkplugCredentialsChecksum stamps the credentials stamp into the install values, on a copy of the
// area's block: vals shares sub-maps with the authoring values.
func withSparkplugCredentialsChecksum(vals map[string]interface{}, digest string) {
	if digest == "" {
		return
	}
	fa := mergedStringKeyMap(vals["functionalAreas"], map[string]interface{}{})
	fa["sparkplug-ingest"] = mergedStringKeyMap(fa["sparkplug-ingest"], map[string]interface{}{"credentialsChecksum": digest})
	vals["functionalAreas"] = fa
}

// withoutSparkplugSources returns the previous release's values without the leaves a run given
// the sources (or told to withdraw them) replaces, so the carry cannot put them back. It copies
// only the path it edits.
func withoutSparkplugSources(previous map[string]interface{}) map[string]interface{} {
	fa, ok := previous["functionalAreas"].(map[string]interface{})
	if !ok {
		return previous
	}
	area, ok := fa["sparkplug-ingest"].(map[string]interface{})
	if !ok {
		return previous
	}
	newArea := make(map[string]interface{}, len(area))
	for k, v := range area {
		if k == "extraEnv" || k == "credentialsChecksum" {
			continue
		}
		if k == "config" {
			if cfg, ok := v.(map[string]interface{}); ok {
				c2 := make(map[string]interface{}, len(cfg))
				for ck, cv := range cfg {
					if ck != "sources" {
						c2[ck] = cv
					}
				}
				v = c2
			}
		}
		newArea[k] = v
	}
	newFA := make(map[string]interface{}, len(fa))
	for k, v := range fa {
		newFA[k] = v
	}
	newFA["sparkplug-ingest"] = newArea
	out := make(map[string]interface{}, len(previous))
	for k, v := range previous {
		out[k] = v
	}
	out["functionalAreas"] = newFA
	return out
}

// releaseHoldsSparkplugSources says whether a release's values carry Sparkplug sources.
func releaseHoldsSparkplugSources(previous map[string]interface{}) bool {
	fa, _ := previous["functionalAreas"].(map[string]interface{})
	area, _ := fa["sparkplug-ingest"].(map[string]interface{})
	cfg, _ := area["config"].(map[string]interface{})
	sources, _ := cfg["sources"].([]interface{})
	return len(sources) > 0
}

// errSilentSparkplugWithdrawal is the refusal of a bootstrap that would drop the sources.
func errSilentSparkplugWithdrawal(instance string) error {
	return fmt.Errorf("instance %q already runs Sparkplug sources, and this bootstrap was given none, which would remove them "+
		"(a bootstrap composes the instance from its flags). Give --sparkplug-source <file> to keep or replace them, "+
		"or --withdraw-sparkplug-sources to remove them. Nothing has been changed", instance)
}

// sparkplugReleaseValues reads the instance's release values; a seam for the withdrawal check.
var sparkplugReleaseValues = func(st *State) (map[string]interface{}, error) {
	cfg, err := helmActionConfigFor(st.KubeContext)
	if err != nil {
		return nil, err
	}
	return previousReleaseValues(cfg, helmReleaseNameFor(st.Instance))
}

// CheckSparkplugFlags refuses the two ways of deciding the sources being given together.
func CheckSparkplugFlags(sources []SparkplugSource, withdraw bool) error {
	if len(sources) > 0 && withdraw {
		return fmt.Errorf("--sparkplug-source sets the Sparkplug sources and --withdraw-sparkplug-sources removes them; give one")
	}
	return nil
}

// precheckSparkplugWithdrawal refuses, before anything is written, a bootstrap that is given no
// sources over a release that holds some. An unreadable release is said, not refused: a fresh
// install has nothing to read, and refusing on a read error would block it.
func precheckSparkplugWithdrawal(ctx context.Context, st *State) error {
	if st.Evolving || st.SparkplugSourcesGiven || st.DryRun {
		return nil
	}
	previous, err := sparkplugReleaseValues(st)
	if err != nil {
		fmt.Println(color.YellowString("could not check the release for Sparkplug sources: %v", err))
		return nil
	}
	if releaseHoldsSparkplugSources(previous) {
		return errSilentSparkplugWithdrawal(st.Instance)
	}
	return nil
}
