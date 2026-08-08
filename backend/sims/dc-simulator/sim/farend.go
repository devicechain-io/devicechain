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

// attachCommandFarEnd readies the scenario's command channel: it brings up an
// IN-PROCESS far end for FarEndInternal, and for FarEndExternal it attaches nothing
// while still enforcing everything the OTHER process needs from this one. Idempotent:
// a second call (Reset re-runs Bootstrap) leaves an already-attached far end alone
// rather than opening a second connection per device.
//
// 🔴 It FAILS the bootstrap when a declared far end cannot be reached — no broker
// configured, or (internal) any device that does not come back subscribed. Degrading
// to "run without it" is the failure this whole seam exists to remove: the scenario
// would come up green, the board would render a control widget, and every command
// issued from it would sit at SENT until it expired a week later. There is an
// explicit opt-out (FarEndDisabled) for an operator who consciously accepts that,
// because a conscious choice is reported on /status and a silent degrade is not.
func (l *Lifecycle) attachCommandFarEnd(ctx context.Context) error {
	switch l.farEndMode {
	case FarEndNone:
		return nil
	case FarEndInternal, FarEndExternal:
		// Both declare a far end, so both go through the opt-out and the broker gate
		// below; only internal reaches the attach at the bottom.
	default:
		// Unreachable through a validated manifest — Manifest.Validate rejects any
		// mode outside the closed set — and refused here anyway, because the branch
		// that would otherwise run for an unknown mode is "attach a receiver", i.e.
		// answer commands on behalf of a scenario whose author wrote something this
		// build does not understand.
		return fmt.Errorf("scenario %q declares an unknown command far-end mode %q",
			l.name, l.farEndMode)
	}

	// The opt-out means ONE thing in every mode that declares a far end: the operator
	// accepts that this scenario's commands will not be answered. So it skips the
	// attach AND relaxes the broker requirement below — for external too, where
	// nothing was going to be attached but the acceptance is the same statement, and
	// where the broker gate would otherwise be a refusal with no escape hatch.
	if l.rt.FarEndDisabled {
		return nil
	}

	// 🔴 The broker is required by BOTH far-end modes, and external is the one that
	// looks like it should be exempt and is not. This process does not dial the
	// broker in external mode — but it is where the presentation client LEARNS the
	// address, handed out with the rest of the scenario's config, so an empty value
	// here is an empty value there.
	//
	// The polarity is worth stating plainly, because it reads backwards: an external
	// far end needs this MORE than an internal one. An internal one that cannot dial
	// fails here, loudly, in the log the operator is already watching. An external one
	// that cannot dial fails in another process, on another machine, as a scene that
	// simply never receives anything — while this side's bootstrap, /status and board
	// are all green and every command it dispatches reaches SENT and expires.
	broker := strings.TrimSpace(l.rt.MqttBroker)
	if broker == "" {
		return fmt.Errorf("scenario %q declares a %q command far end but the handshake carries "+
			"no endpoints.mqttBroker, so nothing can reach the device command plane and its "+
			"command widget would enqueue commands nothing answers. Re-create the sim with a "+
			"current dcctl, or start with --no-command-far-end to run the scenario knowingly "+
			"without an answered command channel", l.name, l.farEndMode)
	}

	if l.farEndMode == FarEndExternal {
		// Everything the external far end needs from this process is now in place, and
		// what it does NOT need is a receiver here. The device is a presentation client
		// holding its own MQTT session under the same device credential; a cmdreceiver
		// alongside it would not be a safety net, it would take the command away from
		// the real far end — MQTT 3.1.1 has no shared subscriptions, so the two
		// sessions fight over one client id, and whichever wins, this process answers
		// SUCCESSFUL for work only the other one can do.
		//
		// The silence is the residual hazard, which is why commandFarEndStatus reports
		// the mode: from inside the simulator, "an external client will answer" and
		// "nothing will answer" are the same absence, and only one of them is a bug.
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
//
// Mode is the third fact, and it is what makes the report readable at all once a far
// end can live outside this process. declared:true attached:false has TWO causes now
// — an internal far end that was suppressed, and an external one this process was
// never going to attach — and they are opposite verdicts on the same board: one
// means the Send button is dead, the other means it works and this process simply
// cannot see it. Nothing else on /status can tell them apart, because from in here
// they are the same absence.
// 🔴 The json tags here ARE the contract — /status is read by dcctl, by scripts and
// by a human with curl, none of which can see the Go field names. Renaming one is a
// breaking change that compiles cleanly and passes any test that goes through the
// struct, so TestStatusWireNamesAreTheContract decodes the real response into a map
// and asserts the literal key strings.
type farEndStatus struct {
	Mode     CommandFarEndMode   `json:"mode"`
	Declared bool                `json:"declared"`
	Attached bool                `json:"attached"`
	Disabled bool                `json:"disabled,omitempty"`
	Broker   string              `json:"broker,omitempty"`
	Evidence *cmdreceiver.Report `json:"evidence,omitempty"`
}

// commandFarEndStatus snapshots the far end for /status.
func (l *Lifecycle) commandFarEndStatus() farEndStatus {
	st := farEndStatus{
		Mode: l.farEndMode,
		// "Declared" is unchanged in meaning — this scenario expects its commands to
		// be answered — which is true of both far-end modes and neither's business
		// to say WHERE.
		Declared: l.farEndMode != FarEndNone,
		Broker:   l.rt.MqttBroker,
	}
	// Disabled is the operator's acceptance that this scenario's commands go
	// unanswered, which is one statement in every mode that has a far end to lose —
	// so it is reported for internal and external alike. It is withheld for
	// FarEndNone because there the flag accepts nothing: that scenario never had a
	// command channel, and reporting it as "disabled" would invent a control surface
	// it does not have. main.go still warns in that case, so the flag never passes
	// entirely without comment.
	st.Disabled = l.farEndMode != FarEndNone && l.rt.FarEndDisabled
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
