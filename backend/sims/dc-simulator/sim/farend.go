// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-simulator/cmdreceiver"
)

// CommandFarEnd is the device side of the two-way command contract (ADR-043) for a
// scenario whose manifest declares one: every provisioned device subscribed to its
// own command topic and answering what arrives, so a command issued against it
// completes QUEUED -> SENT -> SUCCESSFUL.
//
// It is an interface rather than a concrete *cmdreceiver.Receiver for one reason,
// and it is the reason that matters here: the wiring — is a far end attached at
// all, does it cover every device, does a missing broker fail loudly — is the part
// that was missing, and none of it can be tested against a real MQTT gateway in a
// unit test. A substitutable factory lets the WIRING be gated on the same terms as
// everything else in this package, instead of being the one seam that is only ever
// exercised by hand against a live cluster.
type CommandFarEnd interface {
	// Subscribe attaches one device, returning only once its subscription is
	// confirmed — a device that connects but is never acked is reported as an
	// error, never as a quietly listening one.
	Subscribe(ctx context.Context, deviceToken, credentialId string) error
	// Report is the far end's receive/respond evidence, surfaced on /status.
	Report() cmdreceiver.Report
	// Close disconnects every device.
	Close()
}

// FarEndFactory builds a CommandFarEnd for one instance/tenant against a broker.
// Runtime carries one so a test can substitute a fake; production leaves it nil and
// gets newMqttFarEnd.
type FarEndFactory func(instanceId, tenant, broker string, tlsConfig *tls.Config) CommandFarEnd

// newMqttFarEnd is the real far end: a cmdreceiver over the NATS MQTT gateway.
func newMqttFarEnd(instanceId, tenant, broker string, tlsConfig *tls.Config) CommandFarEnd {
	return cmdreceiver.New(instanceId, tenant, broker, tlsConfig)
}

// brokerTLSSchemes are every URL scheme paho establishes a TLS connection for.
//
// 🔴 It is paho's list, not a plausible subset of it, and the difference is a real
// failure: paho dials `mqtts://` over TLS and — given a nil config — verifies
// against the system roots, which a local bring-up's self-signed gateway cert does
// not chain to. A short list that recognised only ssl/tls would leave the operator's
// RECORDED trust decision unapplied while dcctl had already printed that the
// certificate would not be verified, so the connection fails with an x509 error that
// contradicts both the record and the message. Kept in step with paho's
// openConnection switch (netconn.go); `wss` handles a TLS config the same way.
var brokerTLSSchemes = []string{"ssl://", "tls://", "mqtts://", "mqtt+ssl://", "tcps://", "wss://"}

// brokerTLSConfig returns the TLS config for a broker URL, or nil for a plaintext
// one. paho uses a config only for a TLS-scheme broker, and then needs one — a nil
// config verifies against the system roots.
func brokerTLSConfig(broker string, insecure bool) *tls.Config {
	tlsScheme := false
	for _, s := range brokerTLSSchemes {
		if strings.HasPrefix(broker, s) {
			tlsScheme = true
			break
		}
	}
	if !tlsScheme {
		return nil
	}
	// #nosec G402 -- InsecureSkipVerify is the caller's explicit handshake field, off
	// unless dcctl (or a hand-edited record) asked for it; a deployment reachable at
	// its certificate's SAN leaves it false and gets full verification.
	return &tls.Config{InsecureSkipVerify: insecure}
}

// attachCommandFarEnd brings up the scenario's command far end, if it declares one.
// Idempotent: a second call (Reset re-runs Bootstrap) leaves an already-attached far
// end alone rather than opening a second connection per device.
//
// 🔴 It FAILS the bootstrap when a declared far end cannot be brought up — no broker
// configured, or any device that does not come back subscribed. Degrading to "run
// without it" is the failure this whole seam exists to remove: the scenario would
// come up green, the board would render a control widget, and every command issued
// from it would sit at SENT until it expired a week later. There is an explicit
// opt-out (FarEndDisabled) for an operator who consciously accepts that, because a
// conscious choice is reported on /status and a silent degrade is not.
func (l *Lifecycle) attachCommandFarEnd(ctx context.Context) error {
	if !l.farEndDeclared {
		return nil
	}

	// The check below is check-then-act — it releases the lock, then builds and
	// subscribes across a network round trip before storing. That is only safe
	// because its ONE caller, Lifecycle.Bootstrap, holds bootstrapMu for its whole
	// duration; see that field for what two overlapping attaches would do. Anything
	// that calls this from outside Bootstrap must take bootstrapMu first.
	l.mu.Lock()
	already := l.farEnd != nil
	l.mu.Unlock()
	if already {
		return nil
	}

	if l.rt.FarEndDisabled {
		return nil
	}

	broker := strings.TrimSpace(l.rt.MqttBroker)
	if broker == "" {
		return fmt.Errorf("scenario %q needs a command far end but the handshake carries no "+
			"endpoints.mqttBroker: its command widget would enqueue commands nothing answers. "+
			"Re-create the sim with a current dcctl, or start with --no-command-far-end to run "+
			"the scenario knowingly without one", l.name)
	}

	// A far end over no devices attaches nothing, sets l.farEnd, and reports
	// attached:true — a silent no-op of exactly the class this seam exists to kill.
	// It is how a mis-ordered attach (before Provision, when rt.Devices is still
	// empty) would look: entirely healthy.
	if len(l.rt.Devices) == 0 {
		return fmt.Errorf("scenario %q declares a command far end but no devices are "+
			"provisioned, so it would subscribe nothing and still report itself attached", l.name)
	}

	newFarEnd := l.rt.NewFarEnd
	if newFarEnd == nil {
		newFarEnd = newMqttFarEnd
	}
	fe := newFarEnd(l.rt.InstanceId, l.rt.Tenant, broker, brokerTLSConfig(broker, l.rt.MqttTLSInsecure))

	for _, d := range l.rt.Devices {
		if err := fe.Subscribe(ctx, d.Token, d.CredentialId); err != nil {
			// Close what did attach: leaving half a cohort connected to a broker on a
			// failed bootstrap leaks connections across a Reset loop.
			fe.Close()
			return fmt.Errorf("command far end: %w", err)
		}
	}

	l.mu.Lock()
	l.farEnd = fe
	l.mu.Unlock()
	return nil
}

// farEndStatus is the /status view of the command channel. Declared and Attached are
// reported SEPARATELY on purpose: "this scenario needs a far end" and "it has one"
// are different facts, and collapsing them into one boolean is how a disabled far
// end reads as a scenario that never wanted one.
type farEndStatus struct {
	Declared bool                `json:"declared"`
	Attached bool                `json:"attached"`
	Disabled bool                `json:"disabled,omitempty"`
	Broker   string              `json:"broker,omitempty"`
	Evidence *cmdreceiver.Report `json:"evidence,omitempty"`
}

// commandFarEndStatus snapshots the far end for /status.
func (l *Lifecycle) commandFarEndStatus() farEndStatus {
	st := farEndStatus{
		Declared: l.farEndDeclared,
		Disabled: l.rt.FarEndDisabled,
		Broker:   l.rt.MqttBroker,
	}
	l.mu.Lock()
	fe := l.farEnd
	l.mu.Unlock()
	if fe != nil {
		st.Attached = true
		rep := fe.Report()
		st.Evidence = &rep
	}
	return st
}

// Close releases the command far end's broker connections. Called on process
// shutdown, NOT from Stop: a real device keeps listening for commands while it is
// not reporting, and a far end that dropped off on Stop would make a command issued
// against a stopped sim expire — reintroducing, in a narrower window, exactly the
// failure this seam removes.
func (l *Lifecycle) Close() {
	l.mu.Lock()
	fe := l.farEnd
	l.farEnd = nil
	l.mu.Unlock()
	if fe != nil {
		fe.Close()
	}
}
