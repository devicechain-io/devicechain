// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/cmdreceiver"
	"github.com/devicechain-io/dc-simulator/x509sim"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttpackets "github.com/eclipse/paho.mqtt.golang/packets"
)

type s6CompletedToken struct {
	done chan struct{}
	err  error
}

func (t s6CompletedToken) Wait() bool                     { return true }
func (t s6CompletedToken) WaitTimeout(time.Duration) bool { return true }
func (t s6CompletedToken) Done() <-chan struct{}          { return t.done }
func (t s6CompletedToken) Error() error                   { return t.err }

type s6WireClient struct {
	mqtt.Client   // Any unexpected method fails loudly through the nil embedded client.
	topics        []string
	bodies        [][]byte
	disconnects   int
	refuseConnect bool
	connectErr    error
}

func (c *s6WireClient) IsConnectionOpen() bool { return !c.refuseConnect }
func (c *s6WireClient) Connect() mqtt.Token {
	done := make(chan struct{})
	close(done)
	var err error
	if c.refuseConnect {
		err = mqttpackets.ConnErrors[mqttpackets.ErrRefusedNotAuthorised]
		if c.connectErr != nil {
			err = c.connectErr
		}
	}
	return s6CompletedToken{done: done, err: err}
}
func (c *s6WireClient) Publish(topic string, _ byte, _ bool, payload interface{}) mqtt.Token {
	c.topics = append(c.topics, topic)
	c.bodies = append(c.bodies, append([]byte(nil), payload.([]byte)...))
	done := make(chan struct{})
	close(done)
	return s6CompletedToken{done: done}
}
func (c *s6WireClient) Disconnect(uint) { c.disconnects++ }

func TestHarborpulseS6UsesMQTTForChargersAndHTTPForPullLamps(t *testing.T) {
	for _, defect := range []string{"", HarborpulseDefectMQTTWrongTopic, HarborpulseDefectMQTTNeverDisconnect, HarborpulseDefectPullPollingStopped} {
		t.Run(defect, func(t *testing.T) {
			s := harborWith(t)
			if defect != "" {
				s = harborWith(t, defect)
			}
			m := s.Manifest()
			if err := m.Validate(); err != nil {
				t.Fatal(err)
			}
			if len(m.Expand(1)) != 43 {
				t.Fatalf("fleet=%d, want 37 original plus six additive devices", len(m.Expand(1)))
			}
			polls, telemetry := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") || len(auth) <= len("Bearer ") {
						t.Error("pull request lacks bearer")
					}
					polls++
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"commands":[]}`)
					return
				}
				telemetry++
				w.WriteHeader(http.StatusAccepted)
			}))
			defer srv.Close()
			rt := &Runtime{InstanceId: "instance", Tenant: "tenant", HTTPClient: srv.Client(), Endpoints: Endpoints{Ingress: srv.URL}}
			s.s6.initialized, s.s6.configured = true, true
			s.s6.clients, s.s6.dims, s.s6.queued = map[string]mqtt.Client{}, map[string]float64{}, map[string]bool{}
			clients := map[string]*s6WireClient{}
			for _, d := range m.Expand(1) {
				if !s.OwnsDeviceSession(d.Token) {
					continue
				}
				rt.Devices = append(rt.Devices, d)
				if d.DeviceTypeToken == HarborpulsePullLightType {
					s.s6.dims[d.Token], s.s6.queued[d.Token] = 100, true
				} else {
					c := &s6WireClient{}
					clients[d.Token], s.s6.clients[d.Token] = c, c
				}
			}
			// The planted window is a real session close, independently of telemetry suppression.
			elapsed := 6 * time.Minute // 12:00 on the default compressed day
			if err := s.tickS6(context.Background(), rt, time.Now(), elapsed); err != nil {
				t.Fatal(err)
			}
			if telemetry != 2 {
				t.Fatalf("HTTP telemetry=%d, want only the two pull lamps", telemetry)
			}
			wantPolls := 2
			if defect == HarborpulseDefectPullPollingStopped {
				wantPolls = 0
			}
			if polls != wantPolls {
				t.Fatalf("HTTP polls=%d want=%d", polls, wantPolls)
			}
			for device, c := range clients {
				if device == HarborpulseMQTTChargers()[0] && defect != HarborpulseDefectMQTTNeverDisconnect {
					if c.disconnects != 1 || len(c.topics) != 0 {
						t.Fatalf("session drop=%d, publishes=%d", c.disconnects, len(c.topics))
					}
					continue
				}
				target := device
				if defect == HarborpulseDefectMQTTWrongTopic && device == HarborpulseMQTTChargers()[1] {
					target = HarborpulseHotCharger
				}
				want := messaging.SubjectToMqttTopic(messaging.DeviceEventsSubject(rt.InstanceId, rt.Tenant, target))
				if len(c.topics) != 1 || c.topics[0] != want || !bytes.Contains(c.bodies[0], []byte("7.2")) {
					t.Fatalf("%s wire=%v, want MQTT %s and power=7.2", device, c.topics, want)
				}
			}
		})
	}
}

func TestHarborpulseS6PullAppliesBeforeReporting(t *testing.T) {
	s := harborWith(t)
	s.s6.dims = map[string]float64{HarborpulsePullLights()[0]: 100}
	var order []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer lamp-secret" || strings.Contains(r.URL.String(), "lamp-secret") {
			t.Error("incorrect pull authentication")
		}
		switch {
		case r.Method == http.MethodGet:
			order = append(order, "poll")
			_, _ = io.WriteString(w, `{"commands":[{"token":"cmd-1","name":"set-dim","payload":{"level_pct":35}}]}`)
		case strings.HasSuffix(r.URL.Path, "/ack"):
			order = append(order, "ack")
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/response"):
			order = append(order, "response")
			var body struct{ Success bool }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Success || s.s6.dims[HarborpulsePullLights()[0]] != 35 {
				t.Error("reported success before acting")
			}
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected pull route %s", r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	defer srv.Close()
	rt := &Runtime{InstanceId: "instance", Tenant: "tenant", HTTPClient: srv.Client(), Endpoints: Endpoints{Ingress: srv.URL}}
	if err := s.pollS6Lamp(context.Background(), rt, DeviceInstance{Token: HarborpulsePullLights()[0], CredentialId: "lamp-secret"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "poll,ack,response" {
		t.Fatalf("pull order=%v", order)
	}
}

type s6OwnedProbe struct {
	Sim
	owner   *harborpulse
	devices []DeviceInstance
}

func (p *s6OwnedProbe) Bootstrap(_ context.Context, rt *Runtime) error {
	rt.Devices = p.devices
	return nil
}
func (p *s6OwnedProbe) OwnsDeviceSession(token string) bool { return p.owner.OwnsDeviceSession(token) }

type s6ProbeFarEnd struct{ devices []string }

func (f *s6ProbeFarEnd) Subscribe(_ context.Context, token, _ string) error {
	f.devices = append(f.devices, token)
	return nil
}
func (f *s6ProbeFarEnd) Close()                     {}
func (f *s6ProbeFarEnd) Report() cmdreceiver.Report { return cmdreceiver.Report{} }

func TestHarborpulseS6SessionOwnershipPreservesOriginalFarEnd(t *testing.T) {
	s := harborWith(t)
	fe := &s6ProbeFarEnd{}
	probe := &s6OwnedProbe{Sim: s, owner: s, devices: s.Manifest().Expand(1)}
	rt := &Runtime{InstanceId: "instance", Tenant: "tenant", MqttBroker: "ssl://localhost:1883", NewFarEnd: func(_, _, _ string, _ *tls.Config) CommandFarEnd { return fe }}
	lc := NewLifecycle(probe, rt)
	defer lc.Close()
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fe.devices) != 33 {
		t.Fatalf("generic MQTT far end subscribed %d devices, want original 33 HTTP devices", len(fe.devices))
	}
	for _, token := range fe.devices {
		if s.OwnsDeviceSession(token) {
			t.Fatalf("generic far end competes for S6 device %s", token)
		}
	}
}

type s6RoundTripper func(*http.Request) (*http.Response, error)

func (f s6RoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHarborpulseS6DeclarationPrecedesPublishAndCanBeCleared(t *testing.T) {
	_, rt := newFakeProfileServer(t)
	base := rt.HTTPClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	var operations []string
	var declarations [][]ConfigurationKeySpec
	rt.HTTPClient.Transport = s6RoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Body == nil {
			return base.RoundTrip(req)
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(raw))
		var body struct {
			Query     string
			Variables struct{ Keys []ConfigurationKeySpec }
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		if strings.Contains(body.Query, "setDeviceProfileConfigurationDeclaration") {
			operations = append(operations, "declaration")
			declarations = append(declarations, body.Variables.Keys)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":{"setDeviceProfileConfigurationDeclaration":{"token":"profile"}}}`))}, nil
		}
		if strings.Contains(body.Query, "publishDeviceProfile(") {
			operations = append(operations, "publish")
		}
		return base.RoundTrip(req)
	})
	// TenantSession keeps its own HTTP client; wire the recorder into that client too.
	rt.Session = userclient.NewTenantSession(rt.HTTPClient, rt.Endpoints.UserGraphQL, "sim@example.invalid", "pw", rt.Tenant)
	p := widgetlabProfileSpec(t)
	p.ConfigurationKeys = []ConfigurationKeySpec{{Key: HarborpulseConfigKey, ValueType: "DOUBLE"}}
	if err := ensureProfile(context.Background(), rt, p); err != nil {
		t.Fatal(err)
	}
	if strings.Join(operations, ",") != "declaration,publish" {
		t.Fatalf("configuration publication order=%v", operations)
	}
	if err := ensureProfile(context.Background(), rt, p); err != nil {
		t.Fatal(err)
	}
	p.ConfigurationKeys = []ConfigurationKeySpec{}
	if err := ensureProfile(context.Background(), rt, p); err != nil {
		t.Fatal(err)
	}
	if len(declarations) != 3 || len(declarations[0]) != 1 || len(declarations[2]) != 0 || declarations[0][0].ValueType != "DOUBLE" {
		t.Fatalf("declarations=%v", declarations)
	}
	if strings.Join(operations, ",") != "declaration,publish,declaration,declaration,publish" {
		t.Fatalf("did not converge content=%v", operations)
	}
}

func TestHarborpulseS6MutationDocumentsMatchServedSchema(t *testing.T) {
	schema := servedSchema(t, "device-management")
	for _, d := range []struct {
		doc  string
		vars map[string]any
	}{
		{mutationS6Declaration, map[string]any{"token": "p", "keys": []any{map[string]any{"key": "max_kw", "valueType": "DOUBLE"}}}},
		{mutationS6TypeChannel, map[string]any{"token": "t", "channel": "HTTP_PULL"}},
	} {
		if errs := schema.ValidateWithVariables(d.doc, d.vars); len(errs) > 0 {
			t.Errorf("%s: %v", d.doc, errs)
		}
	}
}

func TestHarborpulseS6ConfigAgentAndReportDefectUseTheDeviceWire(t *testing.T) {
	for _, device := range HarborpulseX509Chargers() {
		for _, defect := range []string{"", HarborpulseDefectConfigReportStale} {
			t.Run(device+defect, func(t *testing.T) {
				s := harborWith(t)
				if defect != "" {
					s = harborWith(t, defect)
				}
				client := &s6WireClient{}
				agent, err := s.configAgentS6(&Runtime{InstanceId: "instance", Tenant: "tenant"}, DeviceInstance{Token: device}, client)
				if err != nil {
					t.Fatal(err)
				}
				if err := agent.Sync(context.Background()); err != nil {
					t.Fatal(err)
				}
				document := `{"max_kw":11}`
				hash := sha256.Sum256([]byte(document))
				desired, err := json.Marshal(map[string]any{"v": 1, "kind": "config.revision", "revision": 2, "digest": "sha256:" + hex.EncodeToString(hash[:]), "document": document})
				if err != nil {
					t.Fatal(err)
				}
				if err := agent.HandleDesired(context.Background(), desired); err != nil {
					t.Fatal(err)
				}
				wantMessages := 3
				if defect != "" {
					wantMessages = 1
				}
				if len(client.bodies) != wantMessages {
					t.Fatalf("reports=%d want=%d", len(client.bodies), wantMessages)
				}
				for _, topic := range client.topics {
					if topic != "instance/tenant/device-reports/"+device {
						t.Fatalf("wrong report topic %s", topic)
					}
				}
				if defect == "" {
					var report struct {
						Status   string
						Revision int
						Errors   []string
					}
					if err := json.Unmarshal(client.bodies[2], &report); err != nil {
						t.Fatal(err)
					}
					want := "APPLIED"
					if device == HarborpulseRejectedConfigCharger() {
						want = "REJECTED"
					}
					if report.Status != want || report.Revision != 2 || want == "REJECTED" && len(report.Errors) == 0 {
						t.Fatalf("configuration report=%+v want=%s@2", report, want)
					}
				}
			})
		}
	}
}

func TestHarborpulseS6RevocationControlChangesOnlyTheCredentialOperation(t *testing.T) {
	for _, defect := range []string{"", HarborpulseDefectX509RevocationSkipped} {
		t.Run(defect, func(t *testing.T) {
			updates := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Query     string
					Variables struct {
						Token   string
						Request struct{ Enabled bool }
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				var data map[string]any
				if strings.Contains(req.Query, "updateDeviceCredential(") {
					updates++
					if req.Variables.Token != x509sim.CredentialToken(HarborpulseRevokedCharger()) || req.Variables.Request.Enabled {
						t.Error("wrong credential revocation")
					}
					data = map[string]any{"updateDeviceCredential": map[string]any{"token": req.Variables.Token}}
				} else {
					data, _ = (&fakeProfileServer{}).dispatch(req.Query, nil)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer srv.Close()
			rt := &Runtime{HTTPClient: srv.Client(), Endpoints: Endpoints{DeviceMgmtGraphQL: srv.URL}}
			rt.Session = userclient.NewTenantSession(srv.Client(), srv.URL, "sim@example.com", "password", "tenant")
			s := harborWith(t)
			if defect != "" {
				s = harborWith(t, defect)
			}
			client := &s6WireClient{}
			s.s6.clients = map[string]mqtt.Client{HarborpulseRevokedCharger(): client}
			if err := s.revokeS6(context.Background(), rt); err != nil {
				t.Fatal(err)
			}
			want := 1
			if defect != "" {
				want = 0
			}
			if updates != want || !s.s6.revoked || client.disconnects != 1 {
				t.Fatalf("updates=%d, revoked=%t, disconnects=%d", updates, s.s6.revoked, client.disconnects)
			}
		})
	}
}

func TestHarborpulseS6RefusedChargerRetriesWithItsCertificate(t *testing.T) {
	s := harborWith(t)
	st, err := x509sim.OpenOrMint(t.TempDir(), x509sim.Options{Prefix: harborX509Prefix, Pinned: 2})
	if err != nil {
		t.Fatal(err)
	}
	s.s6.pki = st
	s.s6.initialized, s.s6.configured, s.s6.revoked = true, true, true
	attempts := 0
	device := DeviceInstance{Token: HarborpulseRevokedCharger(), DeviceTypeToken: HarborpulseX509ChargerType, CredentialId: "disabled-fallback"}
	s.s6.newMQTT = func(opts *mqtt.ClientOptions) mqtt.Client {
		attempts++
		if opts.Username != "tenant:@x509" || opts.Password != "" || opts.ClientID != "instance:tenant:"+device.Token || opts.TLSConfig == nil || len(opts.TLSConfig.Certificates) != 1 {
			t.Error("certificate connect lost its real x509 identity or client certificate")
		}
		return &s6WireClient{refuseConnect: true}
	}
	rt := &Runtime{InstanceId: "instance", Tenant: "tenant", MqttBroker: "ssl://broker.test:1883", Devices: []DeviceInstance{device}}
	for i := 0; i < 2; i++ {
		if err := s.tickS6(context.Background(), rt, time.Now(), 10*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if attempts != 2 {
		t.Fatalf("revoked certificate was tried %d times, want retries after refusal", attempts)
	}
}

func TestHarborpulseS6OldStateEndpointAndSceneSlots(t *testing.T) {
	e := Endpoints{DeviceMgmtGraphQL: "https://instance.test/api/device-management/graphql"}
	if err := e.ResolveDeviceState(); err != nil || e.DeviceStateGraphQL != "https://instance.test/api/device-state/graphql" {
		t.Fatalf("state route=%s error=%v", e.DeviceStateGraphQL, err)
	}
	e = Endpoints{DeviceMgmtGraphQL: "https://instance.test/custom"}
	if err := e.ResolveDeviceState(); err == nil {
		t.Fatal("guessed a state endpoint for a custom route")
	}
	seen := map[string]bool{}
	for _, d := range HarborpulseSceneLayout().Devices {
		if d.Quay != HarborpulseNorthQuay {
			continue
		}
		key := fmt.Sprintf("%s/%d", d.Kind, d.Index)
		if seen[key] {
			t.Fatalf("scene slot reused: %s", key)
		}
		seen[key] = true
	}
}

func TestS6PullRetriesWorkHiddenAfterAck(t *testing.T) {
	for _, failAt := range []string{"ack", "response"} {
		t.Run(failAt, func(t *testing.T) {
			s := harborWith(t)
			d := DeviceInstance{Token: HarborpulsePullLights()[0], CredentialId: "secret"}
			s.s6.dims = map[string]float64{d.Token: 100}
			hidden, failed, completed := false, false, false
			acks, responses := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "ingress.test" {
					t.Errorf("Host=%q, want ingress.test", r.Host)
				}
				if r.Method == http.MethodGet {
					if hidden {
						w.WriteHeader(http.StatusNoContent)
					} else {
						_, _ = io.WriteString(w, `{"commands":[{"token":"cmd","name":"set-dim","payload":{"level_pct":35}}]}`)
					}
					return
				}
				stage := "response"
				if strings.HasSuffix(r.URL.Path, "/ack") {
					stage, hidden = "ack", true // Server committed even if its reply is lost.
					acks++
				} else {
					responses++
				}
				if stage == failAt && !failed {
					failed = true
					w.WriteHeader(503)
					return
				}
				if stage == "response" {
					completed = true
				}
				w.WriteHeader(204)
			}))
			defer srv.Close()
			rt := &Runtime{InstanceId: "i", Tenant: "t", HostHeader: "ingress.test", HTTPClient: srv.Client(), Endpoints: Endpoints{Ingress: srv.URL}}
			if err := s.pollS6Lamp(context.Background(), rt, d); err == nil {
				t.Fatal("lost reply was hidden")
			}
			if err := s.pollS6Lamp(context.Background(), rt, d); err != nil {
				t.Fatal(err)
			}
			if !completed || s.s6.dims[d.Token] != 35 || acks != 2 {
				t.Fatalf("completed=%t dim=%v acks=%d responses=%d", completed, s.s6.dims[d.Token], acks, responses)
			}
		})
	}
}

func TestS6PostDisableConnectFailureDefersProofToVerifier(t *testing.T) {
	s := harborWith(t)
	st, err := x509sim.OpenOrMint(t.TempDir(), x509sim.Options{Prefix: harborX509Prefix, Pinned: 2})
	if err != nil {
		t.Fatal(err)
	}
	s.s6.pki = st
	s.s6.initialized, s.s6.configured, s.s6.revoked = true, true, true
	s.s6.newMQTT = func(*mqtt.ClientOptions) mqtt.Client {
		return &s6WireClient{refuseConnect: true, connectErr: fmt.Errorf("network Error : %w", io.EOF)}
	}
	rt := &Runtime{InstanceId: "instance", Tenant: "tenant", MqttBroker: "ssl://broker.test:1883", Devices: []DeviceInstance{{Token: HarborpulseRevokedCharger(), DeviceTypeToken: HarborpulseX509ChargerType}}}
	if err := s.tickS6(context.Background(), rt, time.Now(), 10*time.Minute); err != nil {
		t.Fatalf("post-disable EOF must leave refusal proof to the platform oracle: %v", err)
	}
}

func TestS6BeatsRepeatAfterMidnight(t *testing.T) {
	s := harborWith(t)
	s.s6.initialized, s.s6.configured, s.s6.revoked = true, true, true
	s.s6.dims, s.s6.queued = map[string]float64{}, map[string]bool{}
	var commands []string
	var values []string
	enabled := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"commands":[]}`)
			return
		}
		if strings.Contains(r.URL.Path, "/events") {
			w.WriteHeader(202)
			return
		}
		var req struct {
			Query     string
			Variables struct {
				Token   string
				Request map[string]any
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var data map[string]any
		switch {
		case strings.Contains(req.Query, "createCommand("):
			token, _ := req.Variables.Request["token"].(string)
			commands = append(commands, token)
			data = map[string]any{"createCommand": map[string]any{"command": map[string]any{"token": token}}}
		case strings.Contains(req.Query, "updateDeviceCredential("):
			if req.Variables.Request["enabled"] == true {
				enabled++
			}
			data = map[string]any{"updateDeviceCredential": map[string]any{"token": req.Variables.Token}}
		case strings.Contains(req.Query, "setEntityAttribute("):
			value, _ := req.Variables.Request["value"].(string)
			values = append(values, value)
			data = map[string]any{}
		default:
			data, _ = (&fakeProfileServer{}).dispatch(req.Query, nil)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	rt := &Runtime{InstanceId: "instance", Tenant: "tenant", HTTPClient: srv.Client(), Endpoints: Endpoints{Ingress: srv.URL, DeviceMgmtGraphQL: srv.URL, CommandMgmtGraphQL: srv.URL}, Devices: []DeviceInstance{{Token: HarborpulsePullLights()[0], DeviceTypeToken: HarborpulsePullLightType}}}
	rt.Session = userclient.NewTenantSession(srv.Client(), srv.URL, "sim@example.com", "password", "tenant")
	s.s6.dims[HarborpulsePullLights()[0]] = 35
	start := time.Now()
	for _, elapsed := range []time.Duration{4 * time.Minute, 18 * time.Minute, 27 * time.Minute, 28 * time.Minute} {
		if err := s.tickS6(context.Background(), rt, start.Add(elapsed), elapsed); err != nil {
			t.Fatal(err)
		}
		if elapsed == 18*time.Minute && (s.s6.revoked || s.s6.configured || s.s6.dims[HarborpulsePullLights()[0]] != 100 || s.s6.queued[HarborpulsePullLights()[0]]) {
			t.Fatal("midnight did not restore the initial state")
		}
	}
	if enabled != 2 || strings.Join(values, ",") != "22,22,11,11" || len(commands) != 2 || commands[0] == commands[1] {
		t.Fatalf("enabled=%d values=%v commands=%v", enabled, values, commands)
	}
}

func TestS6ResponseRetryDoesNotApplyTheCommandAgain(t *testing.T) {
	s := harborWith(t)
	d := DeviceInstance{Token: HarborpulsePullLights()[0]}
	s.s6.dims = map[string]float64{d.Token: 100}
	polls, responses, acks := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			polls++
			if polls == 1 {
				_, _ = io.WriteString(w, `{"commands":[{"token":"cmd","name":"set-dim","payload":{"level_pct":35}}]}`)
			} else {
				_, _ = io.WriteString(w, `{"commands":[]}`)
			}
		case strings.HasSuffix(r.URL.Path, "/ack"):
			acks++
			w.WriteHeader(204)
		default:
			responses++
			if responses == 1 {
				w.WriteHeader(503)
			} else {
				w.WriteHeader(204)
			}
		}
	}))
	defer srv.Close()
	rt := &Runtime{HTTPClient: srv.Client(), Endpoints: Endpoints{Ingress: srv.URL}}
	if err := s.pollS6Lamp(context.Background(), rt, d); err == nil {
		t.Fatal("response failure hidden")
	}
	if s.s6.dims[d.Token] != 35 {
		t.Fatal("command was not applied before its first response")
	}
	s.s6.dims[d.Token] = 77 // A newer action or a day reset must survive a response retry.
	for i := 0; i < 2; i++ {
		if err := s.pollS6Lamp(context.Background(), rt, d); err != nil {
			t.Fatal(err)
		}
	}
	if s.s6.dims[d.Token] != 77 || responses != 2 || acks != 2 {
		t.Fatalf("dim=%v responses=%d acks=%d", s.s6.dims[d.Token], responses, acks)
	}
}

func TestS6PullPreservesCommandArrivalOrder(t *testing.T) {
	s := harborWith(t)
	d := DeviceInstance{Token: HarborpulsePullLights()[0]}
	s.s6.dims = map[string]float64{d.Token: 100}
	var responded []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"commands":[{"token":"d","name":"set-dim","payload":{"level_pct":10}},{"token":"c","name":"set-dim","payload":{"level_pct":20}},{"token":"b","name":"set-dim","payload":{"level_pct":30}},{"token":"a","name":"set-dim","payload":{"level_pct":40}}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/response") {
			parts := strings.Split(r.URL.Path, "/")
			responded = append(responded, parts[len(parts)-2])
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	rt := &Runtime{HTTPClient: srv.Client(), Endpoints: Endpoints{Ingress: srv.URL}}
	if err := s.pollS6Lamp(context.Background(), rt, d); err != nil {
		t.Fatal(err)
	}
	if strings.Join(responded, ",") != "d,c,b,a" || s.s6.dims[d.Token] != 40 {
		t.Fatalf("responses=%v dim=%v", responded, s.s6.dims[d.Token])
	}
}
