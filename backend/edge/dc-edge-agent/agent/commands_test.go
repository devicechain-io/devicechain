// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-edge-agent/config"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// ---- harness ----------------------------------------------------------------------------

// fakeAdapter stands in for the local protocol adapter when a test has to make local
// delivery fail, block, or be counted. fn decides each call's outcome.
type fakeAdapter struct {
	mu    sync.Mutex
	calls []string // child + "|" + envelope
	fn    func(n int, child string, env []byte) error
}

func (f *fakeAdapter) Deliver(_ context.Context, child string, env []byte) error {
	f.mu.Lock()
	f.calls = append(f.calls, child+"|"+string(env))
	n := len(f.calls)
	f.mu.Unlock()
	if f.fn == nil {
		return nil
	}
	return f.fn(n, child, env)
}

// snapshot is a copy of the recorded calls, safe to range over while deliveries continue.
func (f *fakeAdapter) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeAdapter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// cmdConfig is a config with the downlink on, tuned so retries and redeliveries are quick.
func cmdConfig(cloudAddr string, storeDir string) config.Configuration {
	return config.Configuration{
		InstanceId: "test",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: 0, StoreDir: storeDir},
		Uplink:     config.UplinkConfiguration{Tenant: "tenant1", GatewayToken: "gw1", BrokerURL: "tcp://" + cloudAddr, ConnectTimeoutSeconds: 2},
		Commands:   config.CommandsConfiguration{Enabled: true, MaxAgeSeconds: 120, RetryDelaySeconds: 1},
	}
}

// cmdRig is one cloud broker, one agent with the downlink on, and a child device on the
// agent's local listener.
type cmdRig struct {
	t         *testing.T
	cloudAddr string
	cloud     mqtt.Client
	a         *Agent
	agentAddr string
	storeDir  string
	stop      func()

	agentTweak func(*Agent)

	responses chan mqtt.Message // what the cloud receives on the gateway responses topic
	childGot  chan []byte       // what the child receives on its command topic
}

func newCmdRig(t *testing.T, adapter ChildAdapter, tweak func(*config.Configuration)) *cmdRig {
	t.Helper()
	return newCmdRigAgent(t, adapter, tweak, nil)
}

// newCmdRigAgent is newCmdRig with a hook that configures the agent before it starts.
func newCmdRigAgent(t *testing.T, adapter ChildAdapter, tweak func(*config.Configuration), agentTweak func(*Agent)) *cmdRig {
	t.Helper()
	cloudPort := startCloudBroker(t)
	r := &cmdRig{t: t, cloudAddr: fmt.Sprintf("127.0.0.1:%d", cloudPort), storeDir: dctest.JetStreamStoreDir(t)}
	r.cloud = mqttConnect(t, r.cloudAddr, "cloud-pub")
	r.subscribeResponses()
	r.agentTweak = agentTweak
	r.boot(adapter, tweak)
	return r
}

func (r *cmdRig) subscribeResponses() {
	r.responses = make(chan mqtt.Message, 64)
	sub := mqttConnect(r.t, r.cloudAddr, "cloud-resp-sub")
	if err := messaging.SubscribeMqttConfirmed(sub, "test/tenant1/gateways/gw1/devices/+/responses", 1,
		func(_ mqtt.Client, m mqtt.Message) { r.responses <- m }, 5*time.Second); err != nil {
		r.t.Fatalf("subscribe responses: %v", err)
	}
}

// boot starts (or restarts) the agent on the rig's store.
func (r *cmdRig) boot(adapter ChildAdapter, tweak func(*config.Configuration)) {
	r.t.Helper()
	cfg := cmdConfig(r.cloudAddr, r.storeDir)
	cfg.Local.ListenPort = freePort(r.t)
	if tweak != nil {
		tweak(&cfg)
	}
	r.a = newAgent(r.t, cfg, func(a *Agent) {
		a.cmdAckWaitOverride = 3 * time.Second
		if r.agentTweak != nil {
			r.agentTweak(a)
		}
		if adapter != nil {
			a.adapterOverride = adapter
		}
	})
	stop := runReady(r.t, r.a)
	var once sync.Once
	r.stop = func() { once.Do(stop) } // a restart stops the old agent; Cleanup must not stop it again
	r.t.Cleanup(r.stop)
	r.agentAddr = fmt.Sprintf("127.0.0.1:%d", cfg.Local.ListenPort)
	waitUntil(r.t, 15*time.Second, r.a.uplink.Connected)
}

// restart stops the agent and boots a new one on the same store.
func (r *cmdRig) restart(adapter ChildAdapter, tweak func(*config.Configuration)) {
	r.stop()
	r.boot(adapter, tweak)
}

// child connects a local device that listens for its commands.
func (r *cmdRig) child(id string) mqtt.Client {
	c := mqttConnect(r.t, r.agentAddr, "child-"+id)
	r.childGot = make(chan []byte, 16)
	if err := messaging.SubscribeMqttConfirmed(c, "test/tenant1/device-commands/"+id, 1,
		func(_ mqtt.Client, m mqtt.Message) { r.childGot <- append([]byte(nil), m.Payload()...) }, 5*time.Second); err != nil {
		r.t.Fatalf("child subscribe: %v", err)
	}
	return c
}

func cmdEnvelope(token, device, nonce string, extra string) []byte {
	return []byte(fmt.Sprintf(`{"token":%q,"deviceToken":%q,"name":"reboot","dispatchNonce":%q%s}`, token, device, nonce, extra))
}

// send publishes a command the way the platform will: QoS 1 on the gateway commands topic.
func (r *cmdRig) send(device string, env []byte) {
	r.t.Helper()
	tok := r.cloud.Publish("test/tenant1/gateways/gw1/devices/"+device+"/commands", 1, false, env)
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		r.t.Fatalf("cloud publish: %v", tok.Error())
	}
}

func respond(c mqtt.Client, t *testing.T, device, body string) {
	t.Helper()
	tok := c.Publish("test/tenant1/command-responses/"+device, 1, false, []byte(body))
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("child respond: %v", tok.Error())
	}
}

type cloudResponse struct {
	CommandToken  string `json:"commandToken"`
	Success       bool   `json:"success"`
	Error         string `json:"error"`
	Reach         string `json:"reach"`
	DispatchNonce string `json:"dispatchNonce"`
	Payload       any    `json:"payload"`
}

func (r *cmdRig) nextResponse(d time.Duration) (cloudResponse, string) {
	r.t.Helper()
	select {
	case m := <-r.responses:
		var cr cloudResponse
		if err := json.Unmarshal(m.Payload(), &cr); err != nil {
			r.t.Fatalf("response not JSON: %v (%s)", err, m.Payload())
		}
		return cr, m.Topic()
	case <-time.After(d):
		r.t.Fatal("the cloud received no command response")
		return cloudResponse{}, ""
	}
}

func (r *cmdRig) noResponse(d time.Duration) {
	r.t.Helper()
	select {
	case m := <-r.responses:
		r.t.Fatalf("the cloud received an unexpected response: %s", m.Payload())
	case <-time.After(d):
	}
}

func (r *cmdRig) ledger(token string) *ledgerEntry {
	r.t.Helper()
	le, _, err := r.a.cmd.get(token)
	if err != nil {
		r.t.Fatalf("ledger %q: %v", token, err)
	}
	return le
}

// ---- tests ------------------------------------------------------------------------------

// TestCommandRoundTrip: a command published for a child on the gateway topic reaches the
// child on its golden command topic with the SAME bytes, and the child's answer reaches the
// cloud on the gateway response topic. This is the whole path through the real local MQTT
// adapter, the durable inbox, the ledger and the response stream.
func TestCommandRoundTrip(t *testing.T) {
	r := newCmdRig(t, nil, nil)
	child := r.child("dev1")

	env := cmdEnvelope("cmd-1", "dev1", "n1", "")
	r.send("dev1", env)

	select {
	case got := <-r.childGot:
		if string(got) != string(env) {
			t.Errorf("child received %q, want the platform's bytes %q unchanged", got, env)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the child never received the command")
	}
	respond(child, t, "dev1", `{"commandToken":"cmd-1","success":true,"payload":{"ok":1},"dispatchNonce":"n1"}`)

	cr, topic := r.nextResponse(15 * time.Second)
	if topic != "test/tenant1/gateways/gw1/devices/dev1/responses" {
		t.Errorf("answer topic = %q, want the gateway namespace response topic", topic)
	}
	if cr.CommandToken != "cmd-1" || !cr.Success || cr.DispatchNonce != "n1" {
		t.Errorf("answer = %+v", cr)
	}
	waitUntil(t, 5*time.Second, func() bool { return r.ledger("cmd-1").State == stAnswered })
}

// TestRepeatedCommandReachesTheChildOnce: the cloud delivering the same command again (a QoS 1
// duplicate, or a re-dispatch with a new nonce) never actuates the child twice, and the answer
// to the original delivery is addressed to the dispatch the cloud is now waiting on.
func TestRepeatedCommandReachesTheChildOnce(t *testing.T) {
	r := newCmdRig(t, nil, nil)
	child := r.child("dev1")

	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	select {
	case <-r.childGot:
	case <-time.After(15 * time.Second):
		t.Fatal("the child never received the command")
	}
	// Same token, same nonce: a plain duplicate.
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	// Same token, new nonce: the cloud re-dispatched.
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n2", ""))
	waitUntil(t, 10*time.Second, func() bool { return r.ledger("cmd-1").Nonce == "n2" })

	select {
	case got := <-r.childGot:
		t.Fatalf("the child received the command a second time: %s", got)
	case <-time.After(2 * time.Second):
	}
	if n := r.a.cmd.delivered.Load(); n != 1 {
		t.Errorf("delivered = %d, want 1", n)
	}

	// The child answers the dispatch it actually received (n1); the cloud is waiting on n2.
	respond(child, t, "dev1", `{"commandToken":"cmd-1","success":true,"dispatchNonce":"n1"}`)
	cr, _ := r.nextResponse(15 * time.Second)
	if cr.DispatchNonce != "n2" {
		t.Errorf("answer nonce = %q, want n2 (the dispatch the cloud is waiting on)", cr.DispatchNonce)
	}
}

// TestChildMustQuoteTheNonceItWasGiven: an answer whose nonce is not the delivered one is
// refused, not laundered into a valid-looking one by the restamp.
func TestChildMustQuoteTheNonceItWasGiven(t *testing.T) {
	r := newCmdRig(t, nil, nil)
	child := r.child("dev1")
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	<-r.childGot

	respond(child, t, "dev1", `{"commandToken":"cmd-1","success":true,"dispatchNonce":"invented"}`)
	respond(child, t, "dev1", `{"commandToken":"cmd-1","success":true}`)
	waitUntil(t, 10*time.Second, func() bool { return r.a.cmd.respRefused.Load() == 2 })
	r.noResponse(time.Second)

	// The correct answer still goes through afterwards.
	respond(child, t, "dev1", `{"commandToken":"cmd-1","success":true,"dispatchNonce":"n1"}`)
	if cr, _ := r.nextResponse(15 * time.Second); cr.DispatchNonce != "n1" {
		t.Errorf("nonce = %q", cr.DispatchNonce)
	}
	// A second answer for the same command is dropped, not forwarded again.
	waitUntil(t, 5*time.Second, func() bool { return r.ledger("cmd-1").State == stAnswered })
	respond(child, t, "dev1", `{"commandToken":"cmd-1","success":false,"dispatchNonce":"n1"}`)
	waitUntil(t, 10*time.Second, func() bool { return r.a.cmd.respRefused.Load() == 3 })
	r.noResponse(time.Second)
	if n := r.refusedReason("command_responses_refused_total", "duplicate"); n != 1 {
		t.Errorf("duplicate refusals = %d, want 1", n)
	}
}

// TestRedispatchAfterAnswerReplaysTheStoredAnswer: the child already answered; the cloud
// re-dispatches with a new nonce and gets the stored answer restamped, with the child NOT
// actuated again.
func TestRedispatchAfterAnswerReplaysTheStoredAnswer(t *testing.T) {
	r := newCmdRig(t, nil, nil)
	child := r.child("dev1")
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	<-r.childGot
	respond(child, t, "dev1", `{"commandToken":"cmd-1","success":true,"payload":{"v":7},"dispatchNonce":"n1"}`)
	r.nextResponse(15 * time.Second)
	waitUntil(t, 5*time.Second, func() bool { return r.ledger("cmd-1").State == stAnswered })

	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n2", ""))
	cr, _ := r.nextResponse(15 * time.Second)
	if cr.DispatchNonce != "n2" || !cr.Success || cr.CommandToken != "cmd-1" {
		t.Errorf("replayed answer = %+v, want the stored answer addressed to n2", cr)
	}
	select {
	case got := <-r.childGot:
		t.Fatalf("the child was actuated again: %s", got)
	case <-time.After(time.Second):
	}
}

// TestExpiredCommandIsNeverDeliveredAndTheCloudIsTold: a command past its deadline is not
// handed to the adapter, and the cloud receives an UNSENT answer quoting its nonce.
func TestExpiredCommandIsNeverDeliveredAndTheCloudIsTold(t *testing.T) {
	fa := &fakeAdapter{}
	r := newCmdRig(t, fa, nil)
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", fmt.Sprintf(`,"expiresAt":%q`, past)))

	cr, _ := r.nextResponse(15 * time.Second)
	if cr.Success || cr.Reach != "UNSENT" || cr.DispatchNonce != "n1" || cr.CommandToken != "cmd-1" || cr.Error == "" {
		t.Errorf("answer = %+v, want a failed UNSENT answer quoting n1", cr)
	}
	if fa.count() != 0 {
		t.Errorf("the adapter was called %d times for an expired command", fa.count())
	}
	if n := r.a.cmd.expired.Load(); n != 1 {
		t.Errorf("expired = %d, want 1", n)
	}
}

// TestDeadlineIsFixedAtAcceptance: a later dispatch of the same token cannot extend the
// command's life.
func TestDeadlineIsFixedAtAcceptance(t *testing.T) {
	fa := &fakeAdapter{fn: func(int, string, []byte) error { return fmt.Errorf("%w: down", errNotSubmitted) }}
	r := newCmdRig(t, fa, nil)
	soon := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", fmt.Sprintf(`,"expiresAt":%q`, soon)))
	waitUntil(t, 10*time.Second, func() bool { le, _, err := r.a.cmd.get("cmd-1"); return err == nil && !le.Deadline.IsZero() })
	first := r.ledger("cmd-1").Deadline

	later := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n2", fmt.Sprintf(`,"expiresAt":%q`, later)))
	waitUntil(t, 10*time.Second, func() bool { return r.ledger("cmd-1").Nonce == "n2" })
	if got := r.ledger("cmd-1").Deadline; !got.Equal(first) {
		t.Errorf("deadline moved from %v to %v on a re-dispatch", first, got)
	}
	if first.After(time.Now().Add(2 * time.Minute)) {
		t.Errorf("deadline %v is later than maxAgeSeconds (120s) allows", first)
	}
}

// TestFailedLocalDeliveryRetriesUntilItSucceeds: a delivery that failed BEFORE anything was
// sent releases the claim and is retried; the child ends up with exactly one delivery.
func TestFailedLocalDeliveryRetriesUntilItSucceeds(t *testing.T) {
	fa := &fakeAdapter{fn: func(n int, _ string, _ []byte) error {
		if n < 3 {
			return fmt.Errorf("%w: local broker down", errNotSubmitted)
		}
		return nil
	}}
	r := newCmdRig(t, fa, nil)
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	waitUntil(t, 20*time.Second, func() bool { return r.a.cmd.delivered.Load() == 1 })
	if fa.count() != 3 {
		t.Errorf("adapter calls = %d, want 3 (two refusals then success)", fa.count())
	}
	if st := r.ledger("cmd-1").State; st != stDelivered {
		t.Errorf("state = %s, want delivered", st)
	}
}

// TestUncertainDeliveryIsNeverRetried: a publish whose outcome is unknown may have reached the
// device, so it is not sent again, however long the command stays unanswered.
func TestUncertainDeliveryIsNeverRetried(t *testing.T) {
	fa := &fakeAdapter{fn: func(int, string, []byte) error { return errors.New("timed out waiting for the PUBACK") }}
	r := newCmdRig(t, fa, nil)
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	waitUntil(t, 10*time.Second, func() bool { le, _, err := r.a.cmd.get("cmd-1"); return err == nil && le.State == stDelivered })
	time.Sleep(3 * time.Second) // longer than the retry delay and the redelivery wait
	if fa.count() != 1 {
		t.Errorf("adapter calls = %d, want exactly 1", fa.count())
	}
}

// TestQueuedCommandSurvivesARestart: a command that could not be delivered stays in the
// durable inbox across an agent restart and is delivered by the next boot, once.
func TestQueuedCommandSurvivesARestart(t *testing.T) {
	down := &fakeAdapter{fn: func(int, string, []byte) error { return fmt.Errorf("%w: down", errNotSubmitted) }}
	r := newCmdRig(t, down, nil)
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	waitUntil(t, 10*time.Second, func() bool { return down.count() >= 1 })

	up := &fakeAdapter{}
	r.restart(up, nil)
	waitUntil(t, 20*time.Second, func() bool { return up.count() == 1 })
	waitUntil(t, 5*time.Second, func() bool { return r.ledger("cmd-1").State == stDelivered })
	time.Sleep(2 * time.Second)
	if up.count() != 1 {
		t.Errorf("adapter calls after restart = %d, want 1", up.count())
	}
}

// TestAnAgentThatDiedMidDeliveryDoesNotActuateAgain: the ledger says a previous run claimed the
// command and never finished. Whether the child has it is unknowable, so it is NOT sent again
// and the cloud is told the command may have been acted on.
func TestAnAgentThatDiedMidDeliveryDoesNotActuateAgain(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Bool
	hang := &fakeAdapter{fn: func(int, string, []byte) error {
		started.Store(true)
		<-release // never returns during this run: the process "dies" holding the claim
		return nil
	}}
	r := newCmdRig(t, hang, nil)
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	waitUntil(t, 10*time.Second, started.Load)
	if st := r.ledger("cmd-1").State; st != stDelivering {
		t.Fatalf("state = %s, want delivering", st)
	}

	fresh := &fakeAdapter{}
	r.restart(fresh, nil)
	cr, _ := r.nextResponse(30 * time.Second)
	close(release)
	if cr.Success || cr.Reach != "UNANSWERED" || cr.DispatchNonce != "n1" {
		t.Errorf("answer = %+v, want a failed UNANSWERED answer", cr)
	}
	if fresh.count() != 0 {
		t.Errorf("the restarted agent delivered the command %d times", fresh.count())
	}
}

// TestAnswerSurvivesAnUplinkOutage: a child answers while the cloud is unreachable; the answer
// is held durably and forwarded after the link returns.
func TestAnswerSurvivesAnUplinkOutage(t *testing.T) {
	cloudStore := dctest.JetStreamStoreDir(t)
	srv := newCloudBroker(t, -1, cloudStore)
	cloudPort := dctest.MQTTPort(t, srv)
	r := &cmdRig{t: t, cloudAddr: fmt.Sprintf("127.0.0.1:%d", cloudPort), storeDir: dctest.JetStreamStoreDir(t)}
	r.cloud = mqttConnect(t, r.cloudAddr, "cloud-pub")
	r.boot(nil, nil)
	child := r.child("dev1")
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	<-r.childGot

	srv.Shutdown()
	waitUntil(t, 15*time.Second, func() bool { return !r.a.uplink.Connected() })
	respond(child, t, "dev1", `{"commandToken":"cmd-1","success":true,"dispatchNonce":"n1"}`)
	time.Sleep(time.Second)

	srv2 := newCloudBroker(t, cloudPort, cloudStore)
	t.Cleanup(srv2.Shutdown)
	r.subscribeResponses()
	waitUntil(t, 30*time.Second, r.a.uplink.Connected)
	cr, _ := r.nextResponse(30 * time.Second)
	if cr.CommandToken != "cmd-1" || !cr.Success {
		t.Errorf("answer = %+v", cr)
	}
}

// TestCommandsAndAnswersThatShouldNotPassAreRefused covers the guards on both directions.
func TestCommandsAndAnswersThatShouldNotPassAreRefused(t *testing.T) {
	fa := &fakeAdapter{}
	r := newCmdRig(t, fa, nil)

	// A body naming a different device than the topic: refused, and the cloud is told.
	r.send("dev1", cmdEnvelope("cmd-mis", "dev2", "nm", ""))
	cr, _ := r.nextResponse(15 * time.Second)
	if cr.Reach != "UNSENT" || cr.DispatchNonce != "nm" || cr.CommandToken != "cmd-mis" {
		t.Errorf("mismatch answer = %+v", cr)
	}
	// Garbage and the gateway's own token: acknowledged and counted, never delivered.
	r.send("dev1", []byte(`not json`))
	r.send("dev1", []byte(`{"token":"bad token!","deviceToken":"dev1","dispatchNonce":"x"}`))
	r.send("gw1", cmdEnvelope("cmd-self", "gw1", "ns", ""))
	waitUntil(t, 10*time.Second, func() bool { return r.a.cmd.refused.Load() >= 4 })
	r.noResponse(time.Second)

	// A token reused by another child is refused.
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	waitUntil(t, 10*time.Second, func() bool { return fa.count() == 1 })
	r.send("dev2", cmdEnvelope("cmd-1", "dev2", "n9", ""))
	waitUntil(t, 10*time.Second, func() bool { return r.a.cmd.refused.Load() >= 5 })
	if fa.count() != 1 {
		t.Errorf("adapter calls = %d, want 1 (the reused token must not reach dev2)", fa.count())
	}

	// A child answering a command that is not its own, and an unknown command.
	c2 := mqttConnect(t, r.agentAddr, "child-dev2")
	respond(c2, t, "dev2", `{"commandToken":"cmd-1","success":true,"dispatchNonce":"n1"}`)
	respond(c2, t, "dev2", `{"commandToken":"nobody","success":true,"dispatchNonce":"n1"}`)
	respond(c2, t, "dev2", `[1,2,3]`)
	waitUntil(t, 10*time.Second, func() bool { return r.a.cmd.respRefused.Load() == 3 })
	r.noResponse(time.Second)
	if r.ledger("cmd-1").State == stAnswered {
		t.Error("another device's answer settled the command")
	}
}

// TestLocalClientCannotInjectACommand: a local MQTT client publishing to any look-alike of the
// agent's own subjects reaches neither the delivery path nor the answer path.
func TestLocalClientCannotInjectACommand(t *testing.T) {
	fa := &fakeAdapter{}
	r := newCmdRig(t, fa, nil)
	evil := mqttConnect(t, r.agentAddr, "evil")
	env := cmdEnvelope("cmd-x", "dev1", "n1", "")
	for _, topic := range []string{"$EDGE/CMD/IN/dev1", "EDGE/CMD/IN/dev1", "$EDGE/CMD/OUT/dev1", "edge/commands/dev1"} {
		evil.Publish(topic, 1, false, env).WaitTimeout(3 * time.Second)
	}
	evil.Publish("$EDGE/CMD/OUT/dev1", 1, false, []byte(`{"commandToken":"cmd-x","success":true,"dispatchNonce":"n1"}`)).WaitTimeout(3 * time.Second)
	time.Sleep(2 * time.Second)
	if fa.count() != 0 {
		t.Errorf("a local publish became a command: %d adapter calls", fa.count())
	}
	r.noResponse(500 * time.Millisecond)
	if n := r.a.cmd.refused.Load(); n < 1 {
		t.Errorf("forged inbox messages were not counted (refused = %d)", n)
	}
	waitUntil(t, 5*time.Second, func() bool {
		info, err := r.a.js.StreamInfo(cmdInboxStream)
		return err == nil && info.State.Msgs == 0
	})
	if fi, err := os.Stat(filepath.Join(r.storeDir, commandSealFile)); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("command seal: %v / %v, want an owner-only file", fi, err)
	}
}

// TestFullInboxRefusesDurablyAndTellsTheCloud: when the inbox is full a new command is not
// accepted, the cloud receives an UNSENT answer, an older command is NOT evicted, and a later
// redelivery of the refused command is still refused rather than accepted.
func TestFullInboxRefusesDurablyAndTellsTheCloud(t *testing.T) {
	var allow atomic.Bool
	fa := &fakeAdapter{fn: func(int, string, []byte) error {
		if allow.Load() {
			return nil
		}
		return fmt.Errorf("%w: down", errNotSubmitted)
	}}
	r := newCmdRig(t, fa, func(c *config.Configuration) { c.Commands.InboxMaxBytes = config.MinCommandStreamBytes })
	pad := fmt.Sprintf(`,"payload":%q`, strings.Repeat("x", 200*1024))

	var refusedToken string
	for i := 0; i < 12 && refusedToken == ""; i++ {
		tok := fmt.Sprintf("cmd-%d", i)
		r.send("dev1", cmdEnvelope(tok, "dev1", "n1", pad))
		select {
		case m := <-r.responses:
			var body cloudResponse
			_ = json.Unmarshal(m.Payload(), &body)
			if body.Reach == "UNSENT" && body.CommandToken == tok {
				refusedToken = tok
			}
		case <-time.After(1500 * time.Millisecond):
		}
	}
	if refusedToken == "" {
		t.Fatal("the inbox never filled: no command was refused")
	}
	if n := r.a.cmd.refused.Load(); n < 1 {
		t.Errorf("refused = %d", n)
	}
	// Nothing older was evicted: every accepted command is still queued.
	info, err := r.a.js.StreamInfo(cmdInboxStream)
	if err != nil || info.State.Msgs == 0 {
		t.Fatalf("inbox = %+v / %v", info, err)
	}

	// The cloud redelivers the refused command after the box recovers: still refused.
	allow.Store(true)
	r.send("dev1", cmdEnvelope(refusedToken, "dev1", "n1", pad))
	waitUntil(t, 10*time.Second, func() bool {
		le, _, err := r.a.cmd.get(refusedToken)
		// Refused is terminal: "expired" until the owed UNSENT answer is forwarded, "answered" after.
		return err == nil && (le.State == stExpired || le.State == stAnswered)
	})
	time.Sleep(2 * time.Second)
	for _, c := range fa.snapshot() {
		if strings.Contains(c, `"token":"`+refusedToken+`"`) {
			t.Errorf("a refused command was delivered: the redelivery was accepted")
		}
	}
}

// TestDisabledDownlinkChangesNothing: without commands.enabled the agent subscribes to nothing,
// creates nothing and exports no command series.
func TestDisabledDownlinkChangesNothing(t *testing.T) {
	cloudPort := startCloudBroker(t)
	cloudAddr := fmt.Sprintf("127.0.0.1:%d", cloudPort)
	cfg := cmdConfig(cloudAddr, dctest.JetStreamStoreDir(t))
	cfg.Commands = config.CommandsConfiguration{}
	cfg.Local.ListenPort = freePort(t)
	a := startAgent(t, cfg)
	waitUntil(t, 15*time.Second, a.uplink.Connected)

	if a.cmd != nil || a.uplink.onCommand != nil {
		t.Fatal("the downlink state exists with commands disabled")
	}
	for _, s := range []string{cmdInboxStream, cmdResponsesStream} {
		if _, err := a.js.StreamInfo(s); !errors.Is(err, nats.ErrStreamNotFound) {
			t.Errorf("stream %s: %v, want not found", s, err)
		}
	}
	if _, err := a.js.KeyValue(cmdLedgerBucket); err == nil {
		t.Error("the ledger bucket exists")
	}
	code, body := httpGet(t, "http://"+a.metrics.addr+"/metrics")
	if code != 200 || strings.Contains(body, "commands_") || strings.Contains(body, "command_") {
		t.Errorf("metrics expose command series with the downlink off (status %d)", code)
	}
	// A command published to the gateway topic goes nowhere.
	pub := mqttConnect(t, cloudAddr, "cloud-pub")
	pub.Publish("test/tenant1/gateways/gw1/devices/dev1/commands", 1, false, cmdEnvelope("c", "dev1", "n", "")).Wait()
}

// TestCommandMetricsAreExported pins the series operators alert on.
func TestCommandMetricsAreExported(t *testing.T) {
	r := newCmdRig(t, nil, nil)
	child := r.child("dev1")
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	<-r.childGot
	respond(child, t, "dev1", `{"commandToken":"cmd-1","success":true,"dispatchNonce":"n1"}`)
	r.nextResponse(15 * time.Second)
	waitUntil(t, 5*time.Second, func() bool { return r.a.cmd.respForwarded.Load() == 1 })

	_, body := httpGet(t, "http://"+r.a.metrics.addr+"/metrics")
	for _, want := range []string{
		"devicechain_edge_commands_received_total 1",
		"devicechain_edge_commands_delivered_total 1",
		"devicechain_edge_command_responses_forwarded_total 1",
		`devicechain_edge_commands_refused_total{reason="inbox_full"} 0`,
		`devicechain_edge_command_responses_refused_total{reason="not_owner"} 0`,
		"devicechain_edge_command_inbox_depth",
		"devicechain_edge_command_responses_dropped_total 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

// TestResponseTopicStaysInTheGatewaysNamespace mirrors the event constructor's discipline.
func TestResponseTopicStaysInTheGatewaysNamespace(t *testing.T) {
	up := testUplink(t)
	got, err := up.ResponseTopic("dev1")
	if err != nil || got != "test/tenant1/gateways/gw1/devices/dev1/responses" {
		t.Errorf("ResponseTopic = %q, %v", got, err)
	}
	for _, bad := range []string{"", "a/b", "a.b", "+", "#", "dev 1"} {
		if _, err := up.ResponseTopic(bad); err == nil {
			t.Errorf("ResponseTopic(%q) accepted", bad)
		}
	}
	if _, err := up.ResponseTopic("gw1"); !errors.Is(err, ErrGatewayAddressedItself) {
		t.Errorf("the gateway's own token: %v", err)
	}
}

// TestCommandsFilterIsTheGatewaysOwn pins the subscription the broker grant must permit.
func TestCommandsFilterIsTheGatewaysOwn(t *testing.T) {
	up := testUplink(t)
	if got, want := up.commandsFilter(), "test/tenant1/gateways/gw1/devices/+/commands"; got != want {
		t.Errorf("commandsFilter = %q, want %q", got, want)
	}
}

// TestUplinkIgnoresACommandOnAnotherGatewaysTopic: the topic's gateway must be this one.
func TestUplinkIgnoresACommandOnAnotherGatewaysTopic(t *testing.T) {
	up := testUplink(t)
	var called atomic.Bool
	up.onCommand = func(string, []byte) bool { called.Store(true); return true }
	up.handleCommand(nil, fakeMessage{topic: "test/tenant1/gateways/other/devices/dev1/commands"})
	up.handleCommand(nil, fakeMessage{topic: "test/tenant1/gateways/gw1/devices/dev1/events"})
	up.handleCommand(nil, fakeMessage{topic: "test/tenant1/gateways/gw1/devices/a.b/commands"})
	if called.Load() {
		t.Error("a command on a foreign topic reached the agent")
	}
	up.handleCommand(nil, fakeMessage{topic: "test/tenant1/gateways/gw1/devices/dev1/commands"})
	if !called.Load() {
		t.Error("a command on the gateway's own topic was dropped")
	}
}

type fakeMessage struct {
	mqtt.Message
	topic string
}

func (f fakeMessage) Topic() string   { return f.topic }
func (f fakeMessage) Payload() []byte { return []byte("{}") }
func (f fakeMessage) Ack()            {}

// TestLedgerMutateIsCompareAndSet: a lost race is retried against the fresh entry rather than
// overwriting it.
func TestLedgerMutateIsCompareAndSet(t *testing.T) {
	r := newCmdRig(t, &fakeAdapter{}, nil)
	cp := r.a.cmd
	if err := cp.create("tok", &ledgerEntry{Device: "d", Nonce: "a", State: stAccepted}); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	le, err := cp.mutate("tok", func(e *ledgerEntry) bool {
		once.Do(func() { // another writer changes the entry between our read and our write
			_, _ = cp.mutate("tok", func(o *ledgerEntry) bool { o.Nonce = "other"; return true })
		})
		e.State = stDelivering
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if le.Nonce != "other" || le.State != stDelivering {
		t.Errorf("entry = %+v, want the concurrent write preserved and ours applied", le)
	}
}

// TestParseGatewayTopicRoundTrip keeps the uplink's subject handling honest against core.
func TestParseGatewayTopicRoundTrip(t *testing.T) {
	up := testUplink(t)
	subj := strings.ReplaceAll(strings.Replace(up.commandsFilter(), "+", "dev1", 1), "/", ".")
	g, c, ok := messaging.ParseGatewayChildSubject(subj, messaging.SegmentCommands)
	if !ok || g != "gw1" || c != "dev1" {
		t.Errorf("parse(%q) = %q, %q, %v", subj, g, c, ok)
	}
}

// TestDuplicateInboxMessageForADeliveredCommandIsDropped: the crash window between the inbox
// write and the ledger write can leave two inbox messages for one command. The second must be
// dropped (not delivered, not retried forever) once the first has been delivered.
func TestDuplicateInboxMessageForADeliveredCommandIsDropped(t *testing.T) {
	fa := &fakeAdapter{}
	r := newCmdRig(t, fa, nil)
	env := cmdEnvelope("cmd-1", "dev1", "n1", "")
	r.send("dev1", env)
	waitUntil(t, 10*time.Second, func() bool { return r.a.cmd.delivered.Load() == 1 })

	if err := r.a.sealedPublish(cmdInboxPrefix+"dev1", env, "second-copy"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, func() bool {
		info, err := r.a.js.StreamInfo(cmdInboxStream)
		return err == nil && info.State.Msgs == 0
	})
	if fa.count() != 1 {
		t.Errorf("adapter calls = %d, want 1", fa.count())
	}
	if r.a.cmd.duplicates.Load() == 0 {
		t.Error("the duplicate was not counted")
	}
}

// TestRefusalOfARepeatNeverSettlesALiveCommand: a malformed repeat of a command that is already
// in flight or delivered (here: the body names another device) must not tell the cloud the
// command was never delivered.
func TestRefusalOfARepeatNeverSettlesALiveCommand(t *testing.T) {
	fa := &fakeAdapter{}
	r := newCmdRig(t, fa, nil)
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	waitUntil(t, 10*time.Second, func() bool { return r.a.cmd.delivered.Load() == 1 })

	r.send("dev1", cmdEnvelope("cmd-1", "dev2", "n2", "")) // body names another device
	waitUntil(t, 10*time.Second, func() bool { return r.a.cmd.refused.Load() >= 1 })
	r.noResponse(1500 * time.Millisecond)
	if st := r.ledger("cmd-1").State; st != stDelivered {
		t.Errorf("state = %s, want delivered (a refused repeat settled the live command)", st)
	}
}

// TestAnInboxMessageForAnotherChildNeverTouchesTheLedgerEntry: the ledger holds the token for
// dev1; an inbox message for the same token under dev2 (the crash-window collision) is refused
// without claiming dev1's entry or reaching an adapter.
func TestAnInboxMessageForAnotherChildNeverTouchesTheLedgerEntry(t *testing.T) {
	fa := &fakeAdapter{fn: func(int, string, []byte) error { return fmt.Errorf("%w: down", errNotSubmitted) }}
	r := newCmdRig(t, fa, nil)
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	waitUntil(t, 10*time.Second, func() bool { return fa.count() >= 1 })
	before := fa.count()

	if err := r.a.sealedPublish(cmdInboxPrefix+"dev2", cmdEnvelope("cmd-1", "dev2", "n1", ""), "collide"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, func() bool { return r.a.cmd.refused.Load() >= 1 })
	for _, c := range fa.snapshot()[before:] {
		if strings.HasPrefix(c, "dev2|") {
			t.Errorf("the adapter was called for the colliding child: %s", c)
		}
	}
	if le := r.ledger("cmd-1"); le.Device != "dev1" {
		t.Errorf("ledger device = %q, want dev1", le.Device)
	}
}

// TestACommandIsNotHandedOverAfterItsDeadline: time spent claiming the command must not carry
// it past its deadline into the adapter.
func TestACommandIsNotHandedOverAfterItsDeadline(t *testing.T) {
	fa := &fakeAdapter{}
	r := newCmdRig(t, fa, nil)
	r.a.beforeDeliverHook = func() { time.Sleep(1500 * time.Millisecond) }
	soon := time.Now().Add(1200 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", fmt.Sprintf(`,"expiresAt":%q`, soon)))

	cr, _ := r.nextResponse(20 * time.Second)
	if cr.Reach != "UNSENT" || cr.DispatchNonce != "n1" {
		t.Errorf("answer = %+v, want UNSENT", cr)
	}
	if fa.count() != 0 {
		t.Errorf("the adapter was called %d times after the deadline", fa.count())
	}
}

// TestAReplayStillOwedIsDischargedByARedelivery: the ledger records that an answer is owed to
// a newer dispatch; the next sight of that dispatch pays it, so a failed or interrupted replay
// is never lost.
func TestAReplayStillOwedIsDischargedByARedelivery(t *testing.T) {
	r := newCmdRig(t, nil, nil)
	child := r.child("dev1")
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	<-r.childGot
	respond(child, t, "dev1", `{"commandToken":"cmd-1","success":true,"dispatchNonce":"n1"}`)
	r.nextResponse(15 * time.Second)
	waitUntil(t, 5*time.Second, func() bool { return r.ledger("cmd-1").State == stAnswered })

	// The state a crash between "nonce recorded" and "replay queued" leaves behind.
	if _, err := r.a.cmd.mutate("cmd-1", func(e *ledgerEntry) bool { e.Nonce = "n2"; e.ReplayPending = true; return true }); err != nil {
		t.Fatal(err)
	}
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n2", "")) // same nonce as recorded
	cr, _ := r.nextResponse(15 * time.Second)
	if cr.DispatchNonce != "n2" || !cr.Success {
		t.Errorf("replayed answer = %+v", cr)
	}
	waitUntil(t, 5*time.Second, func() bool { return !r.ledger("cmd-1").ReplayPending })
}

// TestACommandOnlyAcceptedOnceUnderConcurrentRepeats floods one token and checks one delivery.
func TestACommandOnlyReachesTheAdapterOnceUnderConcurrentRepeats(t *testing.T) {
	fa := &fakeAdapter{}
	r := newCmdRig(t, fa, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", "")) }()
	}
	wg.Wait()
	waitUntil(t, 10*time.Second, func() bool { return fa.count() >= 1 })
	time.Sleep(2 * time.Second)
	if fa.count() != 1 {
		t.Errorf("adapter calls = %d, want 1", fa.count())
	}
}

// TestAnAnswerThatFailedToForwardIsRetried: an answer is removed from the durable stream only
// after the cloud has taken it; a failed forward leaves it to be redelivered.
func TestAnAnswerThatFailedToForwardIsRetried(t *testing.T) {
	var attempts atomic.Int32
	r := newCmdRigAgent(t, nil, nil, func(a *Agent) {
		a.publishOverride = func(topic string, payload []byte) error {
			if strings.HasSuffix(topic, "/responses") && attempts.Add(1) == 1 {
				return errors.New("simulated cloud rejection")
			}
			return nil
		}
	})
	child := r.child("dev1")
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	<-r.childGot
	respond(child, t, "dev1", `{"commandToken":"cmd-1","success":true,"dispatchNonce":"n1"}`)

	waitUntil(t, 20*time.Second, func() bool { return attempts.Load() >= 2 })
	waitUntil(t, 10*time.Second, func() bool { return r.ledger("cmd-1").State == stAnswered })
}

// TestASyntheticAnswerForAWaitingCommandIsRefused: the agent only makes answers for commands it
// refused, expired, claimed or answered; a sealed answer for one still waiting is not one it wrote.
func TestASyntheticAnswerForAWaitingCommandIsRefused(t *testing.T) {
	fa := &fakeAdapter{fn: func(int, string, []byte) error { return fmt.Errorf("%w: down", errNotSubmitted) }}
	r := newCmdRig(t, fa, func(c *config.Configuration) { c.Commands.RetryDelaySeconds = 60 })
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	waitUntil(t, 10*time.Second, func() bool {
		le, _, err := r.a.cmd.get("cmd-1")
		return err == nil && fa.count() >= 1 && le.State == stAccepted
	})

	body := []byte(`{"commandToken":"cmd-1","success":false,"reach":"UNSENT","dispatchNonce":"n1"}`)
	if err := r.a.sealedPublish(cmdSyntheticPrefix+"dev1", body, "stray"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, func() bool { return r.a.cmd.respRefused.Load() >= 1 })
	r.noResponse(time.Second)
	if st := r.ledger("cmd-1").State; st == stAnswered {
		t.Error("a stray synthetic answer settled a waiting command")
	}
}

// refusedReason reads one labelled refusal counter from the metrics endpoint.
func (r *cmdRig) refusedReason(metric, reason string) int {
	r.t.Helper()
	_, body := httpGet(r.t, "http://"+r.a.metrics.addr+"/metrics")
	prefix := fmt.Sprintf("devicechain_edge_%s{reason=%q} ", metric, reason)
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			var n int
			fmt.Sscanf(strings.TrimPrefix(line, prefix), "%d", &n)
			return n
		}
	}
	r.t.Fatalf("metric %s{reason=%q} not exported", metric, reason)
	return -1
}

// TestLocalClientCannotForgeAnAnswer: a local MQTT client can publish to the agent's own
// answer subject, so the seal is the only thing standing between it and an answer the agent
// never made. The command here is genuinely delivered and waiting, with a correct nonce.
func TestLocalClientCannotForgeAnAnswer(t *testing.T) {
	r := newCmdRig(t, &fakeAdapter{}, nil)
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	waitUntil(t, 10*time.Second, func() bool { return r.a.cmd.delivered.Load() == 1 })

	evil := mqttConnect(t, r.agentAddr, "evil")
	evil.Publish("$EDGE/CMD/OUT/dev1", 1, false, []byte(`{"commandToken":"cmd-1","success":true,"dispatchNonce":"n1"}`)).WaitTimeout(3 * time.Second)
	waitUntil(t, 10*time.Second, func() bool { return r.refusedReason("command_responses_refused_total", "forged") == 1 })
	r.noResponse(time.Second)
	if st := r.ledger("cmd-1").State; st == stAnswered {
		t.Error("a forged answer settled the command")
	}
}

// TestAChildCannotAnswerACommandItWasNeverGiven: the command is queued but has not been handed
// to any adapter; an answer for it is a fabrication and is refused for that reason.
func TestAChildCannotAnswerACommandItWasNeverGiven(t *testing.T) {
	fa := &fakeAdapter{fn: func(int, string, []byte) error { return fmt.Errorf("%w: down", errNotSubmitted) }}
	// A long retry delay parks the command in "accepted" after its one failed attempt, so the
	// answer below meets a command that is certainly not in flight.
	r := newCmdRig(t, fa, func(c *config.Configuration) { c.Commands.RetryDelaySeconds = 60 })
	child := r.child("dev1")
	r.send("dev1", cmdEnvelope("cmd-1", "dev1", "n1", ""))
	waitUntil(t, 10*time.Second, func() bool {
		le, _, err := r.a.cmd.get("cmd-1")
		return err == nil && fa.count() >= 1 && le.State == stAccepted
	})

	respond(child, t, "dev1", `{"commandToken":"cmd-1","success":true,"dispatchNonce":"n1"}`)
	waitUntil(t, 10*time.Second, func() bool { return r.refusedReason("command_responses_refused_total", "not_dispatched") >= 1 })
	r.noResponse(time.Second)
}
