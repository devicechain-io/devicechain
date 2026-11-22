// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-simulator/updater"
	"github.com/devicechain-io/dc-simulator/x509sim"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttpackets "github.com/eclipse/paho.mqtt.golang/packets"
	"github.com/rs/zerolog/log"
)

const (
	HarborpulseMQTTChargerType             = "hp-mqtt-charger"
	HarborpulseX509ChargerType             = "hp-x509-charger"
	HarborpulsePullLightType               = "hp-pull-light"
	HarborpulseMQTTChargerProfile          = "hp-mqtt-charger-profile"
	HarborpulseX509ChargerProfile          = "hp-x509-charger-profile"
	HarborpulsePullLightProfile            = "hp-pull-light-profile"
	HarborpulseConnectivityRule            = "hp-mqtt-connectivity"
	HarborpulseConnectivityAlarm           = "mqtt-charger-disconnected"
	HarborpulseConfigKey                   = "max_kw"
	HarborpulseMQTTDropFrom                = 12.0
	HarborpulseMQTTDropTo                  = 13.0
	HarborpulseX509RevokeHour              = 14.0
	HarborpulseConfigHour                  = 9.0
	HarborpulsePullHour                    = 10.0
	HarborpulseDefectMQTTWrongTopic        = "mqtt-wrong-topic"
	HarborpulseDefectX509RevocationSkipped = "x509-revocation-skipped"
	HarborpulseDefectMQTTNeverDisconnect   = "mqtt-never-disconnect"
	HarborpulseDefectConfigReportStale     = "config-report-stale"
	HarborpulseDefectPullPollingStopped    = "pull-polling-stopped"
	harborX509Prefix                       = "PAW-CERT-CHARGER-"
)

var harborS6Defects = []string{HarborpulseDefectMQTTWrongTopic, HarborpulseDefectX509RevocationSkipped,
	HarborpulseDefectMQTTNeverDisconnect, HarborpulseDefectConfigReportStale, HarborpulseDefectPullPollingStopped}

// These populations are separate from the six HTTP chargers and twelve campaign lamps.
// In particular they carry no hp_role=charger facet, so the OTA group's membership stays six.
func HarborpulseMQTTChargers() []string { return []string{"hp-charger-mqtt-1", "hp-charger-mqtt-2"} }
func HarborpulseX509Chargers() []string {
	return []string{x509sim.DeviceToken(harborX509Prefix + "pin-0001"), x509sim.DeviceToken(harborX509Prefix + "pin-0002")}
}
func HarborpulsePullLights() []string          { return []string{"hp-light-pull-1", "hp-light-pull-2"} }
func HarborpulseRevokedCharger() string        { return HarborpulseX509Chargers()[1] }
func HarborpulseRejectedConfigCharger() string { return HarborpulseRevokedCharger() }

// ScenarioS6 declares identities, not successful connections or report counters. The oracle
// reads credentials, presence, measurements, alarms, configuration and commands from the platform.
type ScenarioS6 struct {
	Anchor      string            `json:"anchor"`
	Credentials map[string]string `json:"credentials"`
}

type s6PullCommand struct {
	Token, Name    string
	Payload        json.RawMessage
	acted, success bool
}

type harborS6State struct {
	runMu                            sync.Mutex
	mu                               sync.Mutex
	newMQTT                          func(*mqtt.ClientOptions) mqtt.Client // nil in production; exercises the real dial options in tests
	ledger                           *ScenarioS6
	pki                              *x509sim.State
	clients                          map[string]mqtt.Client
	agents                           map[string]*updater.ConfigAgent
	dims                             map[string]float64
	queued                           map[string]bool
	pending                          map[string][]s6PullCommand
	day                              int
	initialized, revoked, configured bool
}

func (s *harborpulse) hasS6Defect(name string) bool { return slices.Contains(s.defects.names, name) }
func (s *harborpulse) s6Ledger() *ScenarioS6 {
	s.s6.mu.Lock()
	defer s.s6.mu.Unlock()
	if s.s6.ledger == nil {
		return nil
	}
	out := &ScenarioS6{Anchor: s.s6.ledger.Anchor, Credentials: map[string]string{}}
	for d, c := range s.s6.ledger.Credentials {
		out.Credentials[d] = c
	}
	return out
}

func (s *harborpulse) OwnsDeviceSession(token string) bool {
	return slices.Contains(HarborpulseMQTTChargers(), token) || slices.Contains(HarborpulseX509Chargers(), token) || slices.Contains(HarborpulsePullLights(), token)
}

func (s *harborpulse) addS6Manifest(m *SimManifest) {
	metric := []MetricSpec{{Key: HarborpulseChargerPowerKey, Name: "Charging power", DataType: "DOUBLE", Unit: "kW"}}
	rule := map[string]any{"name": "MQTT charger disconnected", "type": "connectivity",
		"severity": SeverityWarning, "actions": ruleActions(HarborpulseConnectivityRule, HarborpulseConnectivityAlarm, "", "", nil)}
	definition, err := json.Marshal(rule)
	if err != nil {
		panic(err)
	}
	m.Profiles = append(m.Profiles,
		ProfileSpec{Token: HarborpulseMQTTChargerProfile, Name: "Harborpulse MQTT Charger", Category: "energy", Metrics: metric,
			DetectionRules: []DetectionRuleSpec{{Token: HarborpulseConnectivityRule, Name: "MQTT charger disconnected", Definition: string(definition), ReadsNoMetric: true, Enabled: true}}},
		ProfileSpec{Token: HarborpulseX509ChargerProfile, Name: "Harborpulse Certificate Charger", Category: "energy", Metrics: metric,
			ConfigurationKeys: []ConfigurationKeySpec{{Key: HarborpulseConfigKey, ValueType: "DOUBLE"}}},
		ProfileSpec{Token: HarborpulsePullLightProfile, Name: "Harborpulse Pull Lamp", Category: "lighting",
			Metrics:  []MetricSpec{{Key: HarborpulseDimKey, Name: "Dim level", DataType: "DOUBLE", Unit: "%"}},
			Commands: []CommandSpec{{Token: "hp-pull-set-dim", CommandKey: HarborpulseSetDimKey, Name: "Set dim level", ParameterSchema: harborpulseSetDimSchema}}})
	m.DeviceTypes = append(m.DeviceTypes,
		DeviceTypeSpec{Token: HarborpulseMQTTChargerType, Name: "MQTT Charger", ProfileToken: HarborpulseMQTTChargerProfile},
		DeviceTypeSpec{Token: HarborpulseX509ChargerType, Name: "Certificate Charger", ProfileToken: HarborpulseX509ChargerProfile},
		DeviceTypeSpec{Token: HarborpulsePullLightType, Name: "Pull Lamp", ProfileToken: HarborpulsePullLightProfile, CommandChannel: "HTTP_PULL"})
	for _, group := range []struct {
		typ    string
		tokens []string
	}{
		{HarborpulseMQTTChargerType, HarborpulseMQTTChargers()}, {HarborpulseX509ChargerType, HarborpulseX509Chargers()}, {HarborpulsePullLightType, HarborpulsePullLights()},
	} {
		p := PopulationSpec{OfType: group.typ, Count: len(group.tokens), FixedCount: true}
		for i, token := range group.tokens {
			ext := token
			if group.typ == HarborpulseX509ChargerType {
				ext = fmt.Sprintf("%spin-%04d", harborX509Prefix, i+1)
			}
			p.Instances = append(p.Instances, InstanceSpec{Token: token, ExternalId: ext, Areas: []string{HarborpulseNorthQuay}})
		}
		m.Populations = append(m.Populations, p)
	}
}

const mutationS6Declaration = `mutation($token:String!,$keys:[ConfigurationKeyInput!]!){setDeviceProfileConfigurationDeclaration(token:$token,keys:$keys){token}}`
const mutationS6TypeChannel = `mutation($token:String!,$channel:String!){updateDeviceType(token:$token,request:{commandChannel:$channel}){token}}`

// ResolveDeviceState preserves older chart-route handshakes. Custom routes must name
// their state endpoint explicitly rather than guessing at another service's URL.
func (e *Endpoints) ResolveDeviceState() error {
	if strings.TrimSpace(e.DeviceStateGraphQL) != "" {
		return nil
	}
	base := strings.TrimRight(e.DeviceMgmtGraphQL, "/")
	const suffix = "/api/device-management/graphql"
	if !strings.HasSuffix(base, suffix) {
		return fmt.Errorf("no endpoints.deviceStateGraphQL and cannot derive it from this device-management URL")
	}
	e.DeviceStateGraphQL = strings.TrimSuffix(base, suffix) + "/api/device-state/graphql"
	return nil
}

func (s *harborpulse) bootstrapS6(ctx context.Context, rt *Runtime) error {
	s.s6.runMu.Lock()
	defer s.s6.runMu.Unlock()
	if s.s6.initialized {
		return nil
	}
	if rt.MqttBroker == "" {
		return fmt.Errorf("harborpulse S6 requires endpoints.mqttBroker")
	}
	if err := rt.Endpoints.ResolveDeviceState(); err != nil {
		return err
	}
	if !strings.HasPrefix(rt.MqttBroker, "ssl://") && !strings.HasPrefix(rt.MqttBroker, "tls://") {
		return fmt.Errorf("harborpulse S6 certificate chargers require a TLS MQTT broker")
	}
	root := os.Getenv("DC_SIM_HARBORPULSE_PKI_DIR")
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return fmt.Errorf("harborpulse PKI cache: %w", err)
		}
		root = filepath.Join(cache, "devicechain", "harborpulse-pki")
	}
	st, err := x509sim.OpenOrMint(filepath.Join(root, rt.InstanceId, rt.Tenant), x509sim.Options{
		Prefix: harborX509Prefix, AnchorToken: "hp-charger-trust", Pinned: 2})
	if err != nil {
		return err
	}
	if err := x509sim.Register(ctx, rt.Session, x509sim.RegisterConfig{Endpoint: rt.Endpoints.DeviceMgmtGraphQL, DeviceTypeToken: HarborpulseX509ChargerType}, st); err != nil {
		return err
	}
	s.s6.pki = st
	ledger := &ScenarioS6{Anchor: st.Manifest.AnchorToken, Credentials: map[string]string{}}
	for _, d := range rt.deviceSnapshot() {
		if slices.Contains(HarborpulseX509Chargers(), d.Token) {
			// No token fallback is available to a certificate charger, even though Provision
			// creates the standard deterministic credential before scenario-specific setup.
			if err := setHarborCredentialEnabled(ctx, rt, d.CredentialToken, false); err != nil {
				return err
			}
			credential := x509sim.CredentialToken(d.Token)
			if err := setHarborCredentialEnabled(ctx, rt, credential, true); err != nil {
				return err
			}
			ledger.Credentials[d.Token] = credential
		}
	}
	for _, token := range HarborpulseX509Chargers() {
		if err := ensureAttribute(ctx, rt, NumericAttributeSpec{Device: token, Key: HarborpulseConfigKey, Value: 22}); err != nil {
			return err
		}
	}
	s.s6.mu.Lock()
	s.s6.ledger = ledger
	s.s6.mu.Unlock()
	s.s6.clients, s.s6.agents = map[string]mqtt.Client{}, map[string]*updater.ConfigAgent{}
	s.s6.dims, s.s6.queued = map[string]float64{}, map[string]bool{}
	for _, d := range HarborpulsePullLights() {
		s.s6.dims[d] = 100
	}
	if !rt.ExternalDevicePlane {
		for _, d := range rt.deviceSnapshot() {
			if d.DeviceTypeToken == HarborpulseMQTTChargerType || d.DeviceTypeToken == HarborpulseX509ChargerType {
				if err := s.connectS6(ctx, rt, d); err != nil {
					s.closeS6Clients()
					return err
				}
			}
		}
	}
	s.s6.initialized = true
	return nil
}

type harborConfigTransport struct {
	send func(context.Context, []byte) error
}

func (t harborConfigTransport) Send(ctx context.Context, b []byte) error { return t.send(ctx, b) }

func s6MQTTWait(ctx context.Context, token mqtt.Token) error {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("harborpulse MQTT operation timed out")
	case <-token.Done():
		return token.Error()
	}
}

func (s *harborpulse) connectS6(ctx context.Context, rt *Runtime, d DeviceInstance) error {
	if c := s.s6.clients[d.Token]; c != nil && c.IsConnectionOpen() {
		return nil
	}
	id, err := messaging.DeviceClientID(rt.InstanceId, rt.Tenant, d.Token)
	if err != nil {
		return err
	}
	tc := brokerTLSConfig(rt.MqttBroker, rt.MqttTLSInsecure, rt.HostHeader)
	opts := mqtt.NewClientOptions().AddBroker(rt.MqttBroker).SetClientID(id).
		SetCleanSession(true).SetAutoReconnect(false).SetConnectRetry(false).
		SetConnectTimeout(4 * time.Second).SetKeepAlive(15 * time.Second).SetOrderMatters(false)
	if d.DeviceTypeToken == HarborpulseX509ChargerType {
		opts.SetUsername(x509sim.Username(rt.Tenant))
		found := false
		for _, e := range s.s6.pki.Entries(x509sim.KindPinned) {
			if e.Token != d.Token {
				continue
			}
			pair, err := s.s6.pki.Pair(e)
			if err != nil {
				return err
			}
			tc.Certificates = append(tc.Certificates, pair)
			found = true
		}
		if !found {
			return fmt.Errorf("no certificate for %s", d.Token)
		}
	} else {
		opts.SetUsername(rt.Tenant + ":" + d.CredentialId)
	}
	opts.SetTLSConfig(tc)
	newClient := s.s6.newMQTT
	if newClient == nil {
		newClient = mqtt.NewClient
	}
	c := newClient(opts)
	if err := s6MQTTWait(ctx, c.Connect()); err != nil {
		c.Disconnect(0)
		return err
	}
	s.s6.clients[d.Token] = c
	if d.DeviceTypeToken != HarborpulseX509ChargerType {
		return nil
	}
	agent, err := s.configAgentS6(rt, d, c)
	if err != nil {
		return err
	}
	s.s6.agents[d.Token] = agent
	topic := fmt.Sprintf("%s/%s/device-desired/%s", rt.InstanceId, rt.Tenant, d.Token)
	var desiredMu sync.Mutex
	if err := messaging.SubscribeMqttConfirmed(c, topic, 1, func(_ mqtt.Client, m mqtt.Message) {
		desiredMu.Lock()
		defer desiredMu.Unlock()
		reportCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := agent.HandleDesired(reportCtx, m.Payload()); err != nil {
			// A failed report is retried by the next Sync, never presented as applied.
			log.Error().Err(err).Str("device", d.Token).Msg("harborpulse configuration report failed")
		}
	}, 5*time.Second); err != nil {
		c.Disconnect(0)
		delete(s.s6.clients, d.Token)
		return err
	}
	if err := agent.Sync(ctx); err != nil {
		c.Disconnect(0)
		delete(s.s6.clients, d.Token)
		return err
	}
	return nil
}

func (s *harborpulse) configAgentS6(rt *Runtime, d DeviceInstance, client mqtt.Client) (*updater.ConfigAgent, error) {
	return updater.OpenConfigAgent(updater.ConfigAgentConfig{Transport: harborConfigTransport{send: func(ctx context.Context, b []byte) error {
		if s.hasS6Defect(HarborpulseDefectConfigReportStale) && bytes.Contains(b, []byte(`"kind":"config.status"`)) {
			return nil
		}
		if client == nil {
			return fmt.Errorf("configuration report has no session")
		}
		topic := fmt.Sprintf("%s/%s/device-reports/%s", rt.InstanceId, rt.Tenant, d.Token)
		return s6MQTTWait(ctx, client.Publish(topic, 1, false, b))
	}}, Validate: func(document string) []string {
		if d.Token == HarborpulseRejectedConfigCharger() {
			return []string{"planted charger refuses the requested power cap"}
		}
		return nil
	}})
}

func (s *harborpulse) revokeS6(ctx context.Context, rt *Runtime) error {
	if !s.hasS6Defect(HarborpulseDefectX509RevocationSkipped) {
		if err := setHarborCredentialEnabled(ctx, rt, x509sim.CredentialToken(HarborpulseRevokedCharger()), false); err != nil {
			return err
		}
	}
	s.s6.revoked = true
	if c := s.s6.clients[HarborpulseRevokedCharger()]; c != nil {
		c.Disconnect(50)
	}
	return nil
}

func (s *harborpulse) tickS6(ctx context.Context, rt *Runtime, now time.Time, elapsed time.Duration) error {
	s.s6.runMu.Lock()
	defer s.s6.runMu.Unlock()
	if rt.ExternalDevicePlane {
		return nil
	}
	if !s.s6.initialized {
		for _, d := range rt.deviceSnapshot() {
			if s.OwnsDeviceSession(d.Token) {
				return fmt.Errorf("harborpulse S6 device plane has not been bootstrapped")
			}
		}
		return nil // original-fleet telemetry fixtures deliberately have no S6 population
	}
	day := harborDayOf(elapsed, s.dayLength)
	if day != s.s6.day {
		if err := s.resetS6Day(ctx, rt, day); err != nil {
			return err
		}
	}
	h := harborHourOf(elapsed, s.dayLength)
	var errs []error
	if h >= HarborpulseConfigHour && !s.s6.configured {
		var configErr error
		for _, d := range HarborpulseX509Chargers() {
			configErr = errors.Join(configErr, ensureAttribute(ctx, rt, NumericAttributeSpec{Device: d, Key: HarborpulseConfigKey, Value: 11}))
		}
		if configErr == nil {
			s.s6.configured = true
		}
		errs = append(errs, configErr)
	}
	if h >= HarborpulseX509RevokeHour && !s.s6.revoked {
		errs = append(errs, s.revokeS6(ctx, rt))
	}
	for _, d := range rt.deviceSnapshot() {
		if d.DeviceTypeToken == HarborpulsePullLightType {
			if h >= HarborpulsePullHour && !s.s6.queued[d.Token] {
				req := map[string]any{"token": fmt.Sprintf("hp-pull-%s-%d-%d", d.Token, s.startedAt().Unix(), day), "deviceToken": d.Token, "name": HarborpulseSetDimKey, "payload": `{"level_pct":35}`, "expiresAt": now.Add(s.dayLength).Format(time.RFC3339)}
				var out struct {
					CreateCommand struct {
						Command   *struct{ Token string }
						Rejection *struct{ Code string }
					}
				}
				if err := rt.Session.Query(ctx, rt.Endpoints.CommandMgmtGraphQL, mutationScenarioCommand, map[string]any{"request": req}, &out); err != nil {
					errs = append(errs, err)
				} else if out.CreateCommand.Command == nil {
					errs = append(errs, fmt.Errorf("pull lamp command rejected: %+v", out.CreateCommand.Rejection))
				} else {
					s.s6.queued[d.Token] = true
				}
			}
			if !s.hasS6Defect(HarborpulseDefectPullPollingStopped) {
				errs = append(errs, s.pollS6Lamp(ctx, rt, d))
			}
			errs = append(errs, EmitMeasurements(ctx, rt, d, map[string]float64{HarborpulseDimKey: s.s6.dims[d.Token]}))
			continue
		}
		if d.DeviceTypeToken != HarborpulseMQTTChargerType && d.DeviceTypeToken != HarborpulseX509ChargerType {
			continue
		}
		hour := harborHourOf(elapsed, s.dayLength)
		if d.Token == HarborpulseMQTTChargers()[0] && hour >= HarborpulseMQTTDropFrom && hour < HarborpulseMQTTDropTo && !s.hasS6Defect(HarborpulseDefectMQTTNeverDisconnect) {
			if c := s.s6.clients[d.Token]; c != nil {
				c.Disconnect(50)
			}
			continue
		}
		err := s.connectS6(ctx, rt, d)
		if d.Token == HarborpulseRevokedCharger() && s.s6.revoked && !s.hasS6Defect(HarborpulseDefectX509RevocationSkipped) {
			if err == nil {
				errs = append(errs, fmt.Errorf("revoked charger was admitted by the broker"))
			} else if !s6CertificateRefused(err) {
				errs = append(errs, fmt.Errorf("revoked charger reconnect failed without an authentication refusal: %w", err))
			}
			continue // Keep retrying the same certificate; the oracle separately requires a callout rejection.
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if a := s.s6.agents[d.Token]; a != nil {
			errs = append(errs, a.Sync(ctx))
		}
		stamp := now.UTC().Format(time.RFC3339Nano)
		payload, err := MeasurementPayload(map[string]float64{HarborpulseChargerPowerKey: 7.2}, stamp, 0)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		body, err := MQTTEventBody(d, "Measurement", stamp, payload)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		target, qos := d.Token, byte(1)
		if d.Token == HarborpulseMQTTChargers()[1] && s.hasS6Defect(HarborpulseDefectMQTTWrongTopic) {
			target, qos = HarborpulseHotCharger, 0
		}
		topic := messaging.SubjectToMqttTopic(messaging.DeviceEventsSubject(rt.InstanceId, rt.Tenant, target))
		if err := s6MQTTWait(ctx, s.s6.clients[d.Token].Publish(topic, qos, false, body)); err != nil {
			rt.Stats.Failed.Add(1)
			errs = append(errs, err)
		} else {
			rt.Stats.Emitted.Add(1)
			outcome := OutcomeAccepted
			if qos == 0 {
				outcome = OutcomeAmbiguous
			}
			rt.Identity.Record(outcome, d.Token, "Measurement", stamp)
		}
	}
	return errors.Join(errs...)
}

func (s *harborpulse) pollS6Lamp(ctx context.Context, rt *Runtime, d DeviceInstance) error {
	base := fmt.Sprintf("%s/%s/%s/devices/%s/commands", strings.TrimRight(rt.Endpoints.Ingress, "/"), rt.InstanceId, rt.Tenant, d.Token)
	var out struct {
		Commands *[]s6PullCommand `json:"commands"`
	}
	if s.s6.pending == nil {
		s.s6.pending = map[string][]s6PullCommand{}
	}
	pending := s.s6.pending[d.Token]
	var errs []error
	if err := s6HTTP(ctx, rt, d, http.MethodGet, base+"?wait=0", nil, &out); err != nil {
		errs = append(errs, err)
	} else if out.Commands == nil {
		errs = append(errs, fmt.Errorf("pull lamp %s received no command list", d.Token))
	} else {
		// Retain the entire delivery before acknowledging any of it: an ACK may commit
		// even when its HTTP reply is lost, and acknowledged commands leave polling.
		for _, c := range *out.Commands {
			if !slices.ContainsFunc(pending, func(p s6PullCommand) bool { return p.Token == c.Token }) {
				pending = append(pending, c)
			}
		}
	}
	// Store the complete ordered batch before ACK can remove anything from polling.
	s.s6.pending[d.Token] = pending
	for len(pending) > 0 {
		c := &pending[0]
		if err := s6HTTP(ctx, rt, d, http.MethodPost, base+"/"+c.Token+"/ack", nil, nil); err != nil {
			errs = append(errs, err)
			break
		}
		if !c.acted {
			level, valid := harborParam(c.Payload, HarborpulseLevelParam, 0, 100)
			c.success = c.Name == HarborpulseSetDimKey && valid
			if c.success {
				s.s6.dims[d.Token] = level
			}
			c.acted = true
		}
		// Retries report the original action; they must not overwrite newer device state.
		body, err := json.Marshal(map[string]any{"success": c.success})
		if err != nil {
			errs = append(errs, err)
			break
		}
		if err := s6HTTP(ctx, rt, d, http.MethodPost, base+"/"+c.Token+"/response", body, nil); err != nil {
			errs = append(errs, err)
			break
		}
		pending[0] = s6PullCommand{}
		pending = pending[1:]
		s.s6.pending[d.Token] = pending
	}
	return errors.Join(errs...)
}

func s6HTTP(ctx context.Context, rt *Runtime, d DeviceInstance, method, endpoint string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Host = rt.HostHeader
	req.Header.Set("Authorization", "Bearer "+d.CredentialId)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := rt.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	want := http.StatusNoContent
	if method == http.MethodGet {
		want = http.StatusOK
	}
	if resp.StatusCode != want {
		return fmt.Errorf("HTTP pull %s %s: status %d", method, d.Token, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(out)
	}
	return nil
}

func (s *harborpulse) closeS6Clients() {
	for _, c := range s.s6.clients {
		c.Disconnect(50)
	}
	s.s6.clients = map[string]mqtt.Client{}
}
func (s *harborpulse) CloseDeviceSessions() {
	s.s6.runMu.Lock()
	defer s.s6.runMu.Unlock()
	s.closeS6Clients()
}

// Reset only after every platform write succeeds. Retrying a partial reset is safe;
// pending pull deliveries deliberately survive midnight until their responses complete.
func (s *harborpulse) resetS6Day(ctx context.Context, rt *Runtime, day int) error {
	for _, device := range HarborpulseX509Chargers() {
		if err := setHarborCredentialEnabled(ctx, rt, x509sim.CredentialToken(device), true); err != nil {
			return err
		}
		if err := ensureAttribute(ctx, rt, NumericAttributeSpec{Device: device, Key: HarborpulseConfigKey, Value: 22}); err != nil {
			return err
		}
	}
	s.s6.dims, s.s6.queued = map[string]float64{}, map[string]bool{}
	for _, device := range HarborpulsePullLights() {
		s.s6.dims[device] = 100
	}
	s.s6.configured, s.s6.revoked = false, false
	s.s6.day = day
	return nil
}

func s6CertificateRefused(err error) bool {
	return errors.Is(err, mqttpackets.ConnErrors[mqttpackets.ErrRefusedNotAuthorised]) || errors.Is(err, mqttpackets.ConnErrors[mqttpackets.ErrRefusedBadUsernameOrPassword])
}
