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
// enters that document: it is rendered into a Secret the chart owns, projected into the pod as
// an environment variable, and the source names the variable (broker.passwordEnv). The file
// dcctl reads may carry the password, or name a file that does; nothing here prints it, and an
// error names a source by its position and tenant only.
//
// It follows the LwM2M identities path (lwm2m.go) for the same reason that path exists: what
// the operator hands dcctl is not recorded anywhere dcctl can read it back, so the rendered
// blocks are CARRIED from the previous release by every later upgrade (carriedValuePaths) and
// replaced only by a run that is given the file again. Re-running `dcctl bootstrap` WITHOUT the
// flag is the withdrawal, exactly as it is for the LwM2M identities.

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

// sparkplugBrokerSecretName is the one Secret dcctl renders (via extraSecrets) to hold every
// source's broker password, one key per source.
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
}

// String and GoString keep the password out of any %v / %+v / %#v of a source that ends up in
// an error or a log line.
func (b SparkplugBroker) String() string {
	return fmt.Sprintf("{url:%s username:%s password:%s}", b.URL, b.Username, redactedIfSet(b.Password))
}
func (b SparkplugBroker) GoString() string { return b.String() }

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
		if s.Broker.Password != "" && s.Broker.Username == "" {
			return fmt.Errorf("%s: broker.password is set without broker.username", where)
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

// sparkplugPasswordEnv is the variable a source's password reaches the pod in.
func sparkplugPasswordEnv(n int) string { return fmt.Sprintf("DC_SPARKPLUG_BROKER_PW_%d", n) }

// sparkplugProvisioning turns the parsed sources into the two value fragments the chart
// consumes: the extraSecrets entry holding every password (nil when no source has one) and the
// sparkplug-ingest area's config (sources[] naming each password's variable) with the extraEnv
// that projects them. A source's password variable is indexed by its position, so it is
// collision-free whatever the sources contain.
func sparkplugProvisioning(instance string, sources []SparkplugSource) (secret map[string]interface{}, areaConfig map[string]interface{}) {
	secretName := sparkplugBrokerSecretName(instance)
	stringData := map[string]interface{}{}
	rendered := make([]interface{}, 0, len(sources))
	extraEnv := make([]interface{}, 0, len(sources))
	for i, s := range sources {
		broker := map[string]interface{}{"url": s.Broker.URL}
		if s.Broker.Username != "" {
			broker["username"] = s.Broker.Username
		}
		if s.Broker.Password != "" {
			env := sparkplugPasswordEnv(i + 1)
			stringData[env] = s.Broker.Password
			broker["passwordEnv"] = env
			extraEnv = append(extraEnv, map[string]interface{}{
				"name": env,
				"valueFrom": map[string]interface{}{
					"secretKeyRef": map[string]interface{}{"name": secretName, "key": env},
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
	if len(stringData) > 0 {
		secret = map[string]interface{}{"name": secretName, "stringData": stringData}
	}
	areaConfig = map[string]interface{}{"config": map[string]interface{}{"sources": rendered}}
	if len(extraEnv) > 0 {
		areaConfig["extraEnv"] = extraEnv
	}
	return secret, areaConfig
}
