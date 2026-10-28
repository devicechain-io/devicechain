// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// errNoSystemLoginForIssuerPin is the refusal to start a callout that would grant certificate
// connects with no way to tell a genuine broker request from a forged one. The remedy is either
// the system login (the bring-up mints it on every instance that runs the callout) or an explicit
// callout.issuerPinDisabled.
var errNoSystemLoginForIssuerPin = errors.New("the device auth callout pins certificate connects to the broker's " +
	"live server ids, which it learns over the NATS system account, but no system-account login " +
	"(infrastructure.nats.auth.sysUser / sysPassword) is configured; configure it, or set " +
	"callout.issuerPinDisabled to run without the pin")

// issuerPinRuntime is the pin and the system-account connection it learns from.
type issuerPinRuntime struct {
	pin  *processor.IssuerPin
	conn *nats.Conn
}

// stop ends the periodic refresh and closes the connection.
func (r *issuerPinRuntime) stop() {
	if r == nil {
		return
	}
	r.pin.Stop()
	r.conn.Close()
}

// wireCalloutIssuerPin builds the issuer pin over a connection of its own to the system account
// and attaches it to the responder. It returns nil, nil when the operator turned the pin off.
//
// The connection retries forever (and the pin refuses every certificate connect while it is down),
// so the service can start while the broker is mid-roll; the first dial failing is not a startup
// failure, a missing login is.
func wireCalloutIssuerPin(ctx context.Context, resp *processor.CalloutResponder, cfg *config.DeviceManagementConfiguration,
	nc mscfg.NatsConfiguration, metrics *processor.IssuerPinMetrics) (*issuerPinRuntime, error) {
	if cfg.Callout.IssuerPinDisabled {
		log.Warn().Msg("callout.issuerPinDisabled is set: certificate connects are NOT pinned to the broker's live " +
			"server ids. The broker's publish-deny on the callout subject is the only defence against a forged request.")
		return nil, nil
	}
	if nc.Auth.SysUser == "" || nc.Auth.SysPassword == "" {
		return nil, errNoSystemLoginForIssuerPin
	}
	conn, err := dialIssuerPinConnection(nc)
	if err != nil {
		return nil, err
	}
	pin, err := processor.NewIssuerPin(processor.IssuerPinConfig{
		Gather:             processor.NewSystemGather(conn, cfg.Callout.IssuerPinGatherWindowDuration()),
		Connected:          conn.IsConnected,
		RefreshInterval:    cfg.Callout.IssuerPinRefreshDuration(),
		MinRefreshInterval: cfg.Callout.IssuerPinMinRefreshDuration(),
		Metrics:            metrics,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	resp.WithIssuerPin(pin)
	pin.Start(ctx)
	log.Info().Dur("refresh", cfg.Callout.IssuerPinRefreshDuration()).Dur("minRefresh", cfg.Callout.IssuerPinMinRefreshDuration()).
		Int("servers", len(pin.Members())).
		Msg("Device auth-callout pins certificate connects to the broker's live server ids.")
	return &issuerPinRuntime{pin: pin, conn: conn}, nil
}

// dialIssuerPinConnection opens the connection to the system account as the system login.
func dialIssuerPinConnection(cfg mscfg.NatsConfiguration) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.Name("device-management-callout-issuer-pin"),
		nats.UserInfo(cfg.Auth.SysUser, cfg.Auth.SysPassword),
		nats.MaxReconnects(-1),
		// The likeliest first dial is the one that fails (the bring-up rolls the broker in the same
		// run that restarts the services). With this option nats.Connect returns a connection in
		// RECONNECTING state and a nil error; the pin reads IsConnected, so that is a pin that
		// refuses certificate connects until the dial lands, which is the intended behaviour.
		nats.RetryOnFailedConnect(true),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Warn().Err(err).Msg("Lost the NATS system-account connection; certificate connects are refused until it returns.")
		}),
		nats.ReconnectHandler(func(*nats.Conn) {
			log.Info().Msg("Reconnected to the NATS system account; certificate connects can be pinned again.")
		}),
	}
	opts = append(opts, messaging.BrokerLivenessOptions("device-management-callout-issuer-pin", nil)...)
	tlsCfg, err := cfg.TLSConfig(cfg.Hostname)
	if err != nil {
		return nil, err
	}
	if tlsCfg != nil {
		opts = append(opts, nats.Secure(tlsCfg))
	}
	conn, err := nats.Connect(fmt.Sprintf("nats://%s:%d", cfg.Hostname, cfg.Port), opts...)
	if err != nil {
		return nil, fmt.Errorf("dialling the NATS system account for the callout's issuer pin: %w", err)
	}
	// Give the first dial a moment so a healthy start does not refuse its first connects, but do
	// not hold the service up on a broker that is not there.
	deadline := time.Now().Add(5 * time.Second)
	for !conn.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	return conn, nil
}
