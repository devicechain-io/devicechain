// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-sources/presence"
	"github.com/devicechain-io/dc-microservice/messaging"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// Ending a device's live broker sessions on request (the revocation half of event-sources no
// longer authenticating each platform-MQTT event by a credential in its body).
//
// A device on the platform broker is authenticated ONCE, at CONNECT, and its events carry no
// credential of their own (a body credential is refused), so a credential revoked or a device
// disabled while it is connected must END the session, or it keeps publishing until the JWT the
// auth callout minted expires. Ending a session needs the NATS system account, whose login this
// service already holds for the broker-presence tap; device-management, which knows when a
// credential changes, asks this service over a request/reply subject and learns the answer.
//
// 🔴 THIS SERVICE REFUSES TO START WITHOUT THE LOGIN WHEN IT INGESTS THE PLATFORM BROKER. With
// no way to end a session, a revocation could only be reported as not carried out, and a
// platform whose revocations all fail is worse than one that says up front it cannot run. The
// instance bring-up always mints the login; an instance without a platform-broker source has
// no sessions of ours and may go without.

// errNoSystemLoginForSessionKick is the startup refusal.
var errNoSystemLoginForSessionKick = errors.New("this service ingests the platform MQTT broker, whose device " +
	"sessions it must be able to end when a credential is revoked or a device is disabled, but no NATS " +
	"system-account login is configured (infrastructure.nats.auth.sysUser and sysPassword); re-run the " +
	"bring-up to mint one")

// sessionKickTimeout bounds one kick: a ping, a connection listing per server, and a kick per
// session. The requester's own attempt timeout (20s) is longer, so this answers first.
const sessionKickTimeout = 15 * time.Second

type sessionKickRuntime struct {
	conn *nats.Conn
	sub  *nats.Subscription
	once sync.Once
}

var sessionKick *sessionKickRuntime

// startSessionKick subscribes the responder. The subscription is on the DATA-PLANE connection
// (where device-management asks); the kicks themselves go over a connection of their own to
// the system account.
func startSessionKick(ctx context.Context) error {
	return startSessionKickOn(ctx, func() *nats.Conn { return NatsManager.Conn() })
}

// startSessionKickOn is startSessionKick over the data-plane connection dataConn returns, which
// a test supplies without building a broker manager.
func startSessionKickOn(ctx context.Context, dataConn func() *nats.Conn) error {
	infra := Microservice.InstanceConfiguration.Infrastructure
	required := GatewaySourceId != ""
	if infra.Nats.Auth.SysUser == "" || infra.Nats.Auth.SysPassword == "" {
		if required {
			return errNoSystemLoginForSessionKick
		}
		log.Info().Msg("No NATS system-account credential is configured, so this service cannot end device " +
			"broker sessions on request. It ingests no platform broker, so it has none to end.")
		return nil
	}
	conn, err := dialSystemAccount(ctx, infra.Nats, systemAccountConnectWait, nil)
	if err != nil {
		if required {
			return fmt.Errorf("could not reach the NATS system account to serve device session kicks: %w", err)
		}
		log.Warn().Err(err).Msg("Could not reach the NATS system account; device session kicks are not served.")
		return nil
	}
	gather := Configuration.BrokerPresence.InventoryGatherWindow()
	requester := presence.NewRequester(conn)
	instance := Microservice.InstanceId
	sub, err := messaging.QueueSubscribeSynced(dataConn(), messaging.SessionKickSubject(instance),
		messaging.SessionKickQueue, func(msg *nats.Msg) {
			answerSessionKick(msg, requester, instance, gather)
		})
	if err != nil {
		conn.Close()
		return fmt.Errorf("subscribing to device session kick requests: %w", err)
	}
	sessionKick = &sessionKickRuntime{conn: conn, sub: sub}
	return nil
}

// answerSessionKick handles one request. It ALWAYS replies, and replies OK only when the whole
// broker cluster was consulted and every session found was ended: the requester reports any
// other answer to its caller as a failed revocation.
func answerSessionKick(msg *nats.Msg, r presence.Requester, instance string, gather time.Duration) {
	reply := func(rep messaging.SessionKickReply) {
		body, err := json.Marshal(rep)
		if err == nil && msg.Reply != "" {
			err = msg.Respond(body)
		}
		if err != nil {
			log.Error().Err(err).Msg("Could not answer a device session kick request.")
		}
	}
	var req messaging.SessionKickRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		reply(messaging.SessionKickReply{Error: "unreadable session kick request"})
		return
	}
	if err := messaging.ValidateSessionKick(req); err != nil {
		reply(messaging.SessionKickReply{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionKickTimeout)
	defer cancel()
	kicked, err := presence.KickDevices(ctx, r, instance, req.Tenant, req.Tokens, gather)
	if err != nil {
		// Logged without the device tokens' sessions' details beyond counts: tokens are not secrets,
		// but the error text is the broker's own.
		log.Warn().Err(err).Str("tenant", req.Tenant).Int("devices", len(req.Tokens)).Int("kicked", kicked).
			Msg("Could not confirm a device's broker sessions ended.")
		reply(messaging.SessionKickReply{Kicked: kicked, Error: err.Error()})
		return
	}
	reply(messaging.SessionKickReply{OK: true, Kicked: kicked})
}

// stopSessionKick stops answering and closes the system-account connection.
func stopSessionKick() {
	rt := sessionKick
	if rt == nil {
		return
	}
	sessionKick = nil
	rt.once.Do(func() {
		_ = rt.sub.Unsubscribe()
		rt.conn.Close()
	})
}
