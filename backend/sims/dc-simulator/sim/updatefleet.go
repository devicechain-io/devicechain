// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/devicechain-io/dc-simulator/otafleet"
)

// UpdateFleet is the simulated OTA-updatable devices of a sim: every provisioned device on the
// update protocol against the instance's broker. It is an interface for the reason CommandFarEnd
// is one: the attach wiring (does it cover every device, does a missing broker fail loudly) is the
// part that matters, and a substitutable factory lets a test gate it without a broker.
type UpdateFleet interface {
	// Attach puts the device at position index of the fleet on the update protocol, returning once
	// its subscription is confirmed (an injected-offline device returns without connecting).
	Attach(ctx context.Context, deviceToken, credentialID string, index int) error
	// Report is the fleet's /status view.
	Report() otafleet.Report
	// Close disconnects every device.
	Close()
}

// UpdateFleetFactory builds an UpdateFleet for one instance and tenant against a broker.
type UpdateFleetFactory func(instanceID, tenant, broker string, tlsConfig *tls.Config, opts otafleet.Options) (UpdateFleet, error)

func newMqttUpdateFleet(instanceID, tenant, broker string, tlsConfig *tls.Config, opts otafleet.Options) (UpdateFleet, error) {
	return otafleet.New(instanceID, tenant, broker, tlsConfig, opts)
}

// downloadDialAddr is the host:port the artifact download dials. When the sim reaches the instance
// through an ADDRESS (a port-forward, a load balancer) while the instance routes on a hostname,
// the capability URL in a download grant names that hostname, which would not resolve here: the
// download then dials the address the handshake's endpoints name and still presents the hostname.
// Empty means the URL's own host is dialled.
func downloadDialAddr(rt *Runtime) (string, error) {
	if rt.HostHeader == "" {
		return "", nil
	}
	raw := rt.Endpoints.UserGraphQL
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("the handshake carries a host header but its endpoint %q names no address to dial for downloads", raw)
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

// attachUpdateFleet gives every provisioned device an update client when the operator asked for
// them (--ota). Like attachCommandFarEnd it runs inside Bootstrap, holding bootstrapMu, and is
// idempotent across a Reset.
//
// It FAILS the bootstrap rather than running without them: a sim that was asked for update devices
// and has none would sit green while a rollout aimed at it waits out its deadlines.
func (l *Lifecycle) attachUpdateFleet(ctx context.Context) error {
	opts := l.rt.UpdateFleet
	if opts == nil {
		return nil
	}
	if l.rt.ExternalDevicePlane {
		return fmt.Errorf("scenario %q was started with --external-device-plane, which makes an outside client "+
			"every device; this process cannot also be the devices that take updates", l.name)
	}
	broker := strings.TrimSpace(l.rt.MqttBroker)
	if broker == "" {
		return fmt.Errorf("scenario %q was asked for update devices but the handshake carries no "+
			"endpoints.mqttBroker, so nothing can reach the device update channel. Re-create the sim with a "+
			"current dcctl", l.name)
	}
	l.mu.Lock()
	already := l.updateFleet != nil
	l.mu.Unlock()
	if already {
		return nil
	}
	if len(l.rt.Devices) == 0 {
		return fmt.Errorf("scenario %q was asked for update devices but no devices are provisioned", l.name)
	}
	if total := opts.Faults.Total(); total > len(l.rt.Devices) {
		return fmt.Errorf("scenario %q has %d devices but %d are to misbehave; a fault count the fleet cannot "+
			"hold would silently inject fewer faults than asked for", l.name, len(l.rt.Devices), total)
	}

	o := *opts
	addr, err := downloadDialAddr(l.rt)
	if err != nil {
		return err
	}
	o.DownloadDialAddr, o.DownloadInsecure = addr, l.rt.MqttTLSInsecure

	factory := l.rt.NewUpdateFleet
	if factory == nil {
		factory = newMqttUpdateFleet
	}
	fleet, err := factory(l.rt.InstanceId, l.rt.Tenant, broker, brokerTLSConfig(broker, l.rt.MqttTLSInsecure, l.rt.HostHeader), o)
	if err != nil {
		return fmt.Errorf("update devices: %w", err)
	}
	for i, d := range l.rt.Devices {
		if err := fleet.Attach(ctx, d.Token, d.CredentialId, i); err != nil {
			fleet.Close() // half a fleet left connected would leak across a Reset loop
			return fmt.Errorf("update devices: %w", err)
		}
	}
	l.mu.Lock()
	l.updateFleet = fleet
	l.mu.Unlock()
	return nil
}

// updateFleetStatus snapshots the fleet for /status; nil when there is none.
func (l *Lifecycle) updateFleetStatus() *otafleet.Report {
	l.mu.Lock()
	f := l.updateFleet
	l.mu.Unlock()
	if f == nil {
		return nil
	}
	r := f.Report()
	return &r
}

func (l *Lifecycle) closeUpdateFleet() {
	l.mu.Lock()
	f := l.updateFleet
	l.updateFleet = nil
	l.mu.Unlock()
	if f != nil {
		f.Close()
	}
}
