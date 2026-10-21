// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// Ending a device's live broker sessions (the revocation half of the platform MQTT broker
// being the authority on who published what).
//
// WHAT IT DOES. It pings the cluster, pages every server's connection list, picks out the
// connections whose MQTT client id belongs to one of the named devices of one tenant of THIS
// instance, and asks each owning server to disconnect each by connection id. It reuses the
// presence tap's own requester and its own parse (ParseDeviceClientIDFor), so the set of
// connections it considers "a device's" is the set presence considers, by construction.
//
// 🔑 IT KICKS EVERY SESSION OF THE DEVICE, DISCRIMINATED ONES INCLUDED, not just the plain
// client id. A device may hold several concurrent sessions ("{device}:{discriminator}"); a
// credential revoked while a second session is open must not leave that session publishing
// because the kick looked only for the plain id. The connection list therefore cannot be
// filtered by client id on the broker (that filter is an exact match); it is paged and matched
// here.
//
// 🔴 IT FAILS CLOSED ON AN INCOMPLETE VIEW. The list is a fan-out: each server describes only
// its own connections, so a server that did not answer is a server whose sessions were never
// looked at. Reporting "none found" for a cluster that was only partly consulted is the false
// success this exists to prevent, so anything short of every expected server answering is an
// error and the caller retries.
//
// The kick does not decide whether the device MAY reconnect: it ends the session, and the
// auth callout re-runs against whatever credential the device presents next, which the
// database (already committed by the caller) refuses if it was revoked. A device that holds a
// second, still-valid credential reconnects with it; one whose only credential was revoked
// does not.

// kickFirstPing is the first, short window of the cluster ping.
const kickFirstPing = 500 * time.Millisecond

// sysKick is the per-server kick endpoint: "$SYS.REQ.SERVER.{id}.KICK", body {"cid":N}.
const sysKick = "KICK"

// kickReply is the subset of a system-account request's reply this reads: the server wraps a
// handler's result in an envelope with an optional error.
type kickReply struct {
	Error *struct {
		Description string `json:"description"`
	} `json:"error"`
}

// connectionGone is the broker's answer to a kick of a connection that closed between the
// list and the kick. That is the goal state, not a failure.
const connectionGone = "no such client or leafnode id"

// KickDevices ends every live session of the named devices of tenant. It returns how many it
// ended, and an error unless the whole cluster was consulted and every session found was
// ended or already gone.
func KickDevices(ctx context.Context, r Requester, instanceId, tenant string, tokens []string,
	gather time.Duration) (int, error) {
	want := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		want[t] = true
	}
	// A short first ping, then the full window only if it came back incomplete. The ping is a
	// fan-out that waits its whole window, and a revocation is waited on by an operator: on a
	// healthy cluster every server answers within milliseconds, and waiting out the presence
	// tap's five-second window for each one would make every disable take five seconds.
	servers, expected, err := pingServers(ctx, r, min(gather, kickFirstPing))
	if err != nil || len(servers) < expected {
		servers, expected, err = pingServers(ctx, r, gather)
	}
	if err != nil {
		return 0, err
	}
	if len(servers) < expected {
		return 0, fmt.Errorf("only %d of %d broker servers answered, so some of the device's sessions "+
			"could not be looked for", len(servers), expected)
	}
	kicked := 0
	var failures []error
	for _, id := range servers {
		cids, err := findSessions(ctx, r, instanceId, tenant, want, id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, cid := range cids {
			if err := kickOne(ctx, r, id, cid); err != nil {
				failures = append(failures, err)
				continue
			}
			kicked++
		}
	}
	if len(failures) > 0 {
		return kicked, errors.Join(failures...)
	}
	return kicked, nil
}

// findSessions pages one server's connections and returns the connection ids belonging to a
// wanted device of tenant in this instance.
func findSessions(ctx context.Context, r Requester, instanceId, tenant string, want map[string]bool,
	serverID string) ([]uint64, error) {
	subject := fmt.Sprintf(sysServerDirect, serverID, "CONNZ")
	var cids []uint64
	offset := 0
	for page := 0; page < connzPageLimit; page++ {
		body, err := json.Marshal(map[string]any{"offset": offset, "limit": connzPageSize})
		if err != nil {
			return nil, err
		}
		msg, err := requestPage(ctx, r, subject, body)
		if err != nil {
			return nil, fmt.Errorf("requesting connections from server %s: %w", serverID, err)
		}
		reply := connzReply{}
		if err := json.Unmarshal(msg.Data, &reply); err != nil {
			return nil, fmt.Errorf("decoding the connection list from server %s: %w", serverID, err)
		}
		if reply.Error != nil {
			return nil, fmt.Errorf("server %s refused the connection list: %s", serverID, reply.Error.Description)
		}
		if reply.Data == nil {
			return nil, fmt.Errorf("server %s returned no connection data", serverID)
		}
		for _, c := range reply.Data.Connections {
			if c.MQTTClient == "" || c.Cid == 0 {
				continue
			}
			parts, err := messaging.ParseDeviceClientIDFor(instanceId, c.MQTTClient)
			if err != nil || parts.Tenant != tenant || !want[parts.DeviceToken] {
				continue
			}
			cids = append(cids, c.Cid)
		}
		offset += reply.Data.NumConns
		if reply.Data.NumConns == 0 || offset >= reply.Data.Total {
			return cids, nil
		}
	}
	return nil, fmt.Errorf("server %s did not finish listing connections within %d pages", serverID, connzPageLimit)
}

// kickOne ends one connection. A connection that is already gone counts as ended.
func kickOne(ctx context.Context, r Requester, serverID string, cid uint64) error {
	body, err := json.Marshal(map[string]any{"cid": cid})
	if err != nil {
		return err
	}
	msg, err := requestPage(ctx, r, fmt.Sprintf(sysServerDirect, serverID, sysKick), body)
	if err != nil {
		return fmt.Errorf("kicking connection %d on server %s: %w", cid, serverID, err)
	}
	reply := kickReply{}
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return fmt.Errorf("decoding the kick reply from server %s: %w", serverID, err)
	}
	if reply.Error != nil && !strings.Contains(strings.ToLower(reply.Error.Description), connectionGone) {
		return fmt.Errorf("server %s refused to kick connection %d: %s", serverID, cid, reply.Error.Description)
	}
	return nil
}
