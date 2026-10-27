// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-sparkplug-ingest/config"
)

// The command downlink of one Client (G19 S3).
//
// The dispatcher (package downlink) owns the policy: which command may be sent, in what order,
// under which limits. This file is what only the Client can answer, because it holds the broker
// session and the node sessions: who a device token is on the wire, whether the Host session is up
// and announced, and the publish itself, which is made on the session the Client already holds
// (there is no second connection, so there is no second credential and no second STATE).

// Errors the command publish answers. They are distinct because the dispatcher answers each
// differently: a session that is down parks the command for the next birth, an open breaker
// refuses it, and a finished term abandons it without a verdict.
var (
	// ErrSessionDown: there is no live, announced Host session to publish on.
	ErrSessionDown = errors.New("the Host session is not up")
	// ErrBreakerOpen: the source's command breaker is not admitting commands.
	ErrBreakerOpen = errors.New("the source's command breaker is open")
	// ErrTermEnded: the leadership term that was publishing has ended.
	ErrTermEnded = errors.New("the leadership term has ended")
	// ErrTableMoved: the node's BIRTH table was replaced between the last check and the publish, so
	// the command that was about to go out is stale. Nothing was published; the caller retracts
	// its record and asks again against the new table.
	ErrTableMoved = errors.New("the node's BIRTH table changed while the command was being recorded")
)

// PublishIndeterminateError is a publish that failed in a way that does not say whether the bytes
// reached the broker (a timeout waiting for the client library, a token error). The command may
// have been delivered, so it is NOT "not sent": the caller must not treat it as safe to retry.
type PublishIndeterminateError struct{ Err error }

func (e *PublishIndeterminateError) Error() string {
	return "the publish did not complete and may have reached the node: " + e.Err.Error()
}
func (e *PublishIndeterminateError) Unwrap() error { return e.Err }

// BeforeWireError wraps what the before-wire hook returned, so a caller can tell "my hook refused"
// from "the publish failed".
type BeforeWireError struct{ Err error }

func (e *BeforeWireError) Error() string { return "before-wire hook refused: " + e.Err.Error() }
func (e *BeforeWireError) Unwrap() error { return e.Err }

// CommandWire describes one command exactly as it goes on the wire: what the operator and the
// confirmation need to see, without parsing the protobuf again.
type CommandWire struct {
	Topic string
	// Alias is set when the BIRTH aliased the metric, and then Name is empty: an NCMD/DCMD carries
	// the alias only, never both.
	Alias    *uint64
	Name     string
	Datatype uint32
	// Value is the requested value as the caller offered it.
	Value any
	// SHA256 is the hex digest of the encoded payload bytes, and Size their length.
	SHA256 string
	Size   int
	// Epoch, Gen and BdSeq identify the BIRTH table the command was addressed against.
	Epoch    uint64
	Gen      uint64
	BdSeq    uint64
	HasBdSeq bool
	// PublishedAt is the payload timestamp, which is also the instant the confirmation window and
	// the clock-skew rule are measured from.
	PublishedAt time.Time
	// AtRequestedValue is set on a write when the node's last report of the metric, on the table the
	// command was addressed against, was already the requested value, and LastReportedAt is when it
	// says so. It is what lets a window that closes with nothing contrary seen say "already at the
	// value" instead of "not confirmed".
	AtRequestedValue bool
	LastReportedAt   time.Time
}

func wireOf(topic string, payload []byte, ref MetricRef, value any, at time.Time) CommandWire {
	sum := sha256.Sum256(payload)
	w := CommandWire{
		Topic: topic, Datatype: ref.Datatype, Value: value, SHA256: hex.EncodeToString(sum[:]), Size: len(payload),
		Epoch: ref.Epoch, Gen: ref.Gen, BdSeq: ref.BdSeq, HasBdSeq: ref.HasBdSeq, PublishedAt: at,
	}
	if ref.HasAlias {
		a := ref.Alias
		w.Alias = &a
	} else {
		w.Name = ref.Name
	}
	return w
}

// commandIndex is the reverse index of one Client: device token -> the Sparkplug identity it is on
// this session. It is filled from the resolve each accepted BIRTH already makes, and an entry goes
// when its death is accepted, when the node's death cascades over it, and at the end of the Host
// session. Because a session that is down holds no entries, "session down" reads as "not born".
type commandIndex struct {
	mu      sync.RWMutex
	byToken map[string]CommandTarget
	byExtID map[string]string
}

func newCommandIndex() *commandIndex {
	return &commandIndex{byToken: map[string]CommandTarget{}, byExtID: map[string]string{}}
}

// targetOfExternalId parses the slash-joined external id SparkplugExternalId builds.
func targetOfExternalId(ext string) (CommandTarget, bool) {
	parts := strings.Split(ext, "/")
	switch len(parts) {
	case 2:
		if parts[0] != "" && parts[1] != "" {
			return CommandTarget{Group: parts[0], Node: parts[1]}, true
		}
	case 3:
		if parts[0] != "" && parts[1] != "" && parts[2] != "" {
			return CommandTarget{Group: parts[0], Node: parts[1], Device: parts[2]}, true
		}
	}
	return CommandTarget{}, false
}

// add records token as the device with external id ext, and reports whether the token was not
// already there for that identity.
func (x *commandIndex) add(ext, token string) (CommandTarget, bool, bool) {
	t, ok := targetOfExternalId(ext)
	if !ok {
		return CommandTarget{}, false, false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if old, had := x.byExtID[ext]; had && old != token {
		delete(x.byToken, old)
	}
	prev, had := x.byToken[token]
	if had {
		// The token moved to another identity: the old identity must not keep pointing at it.
		if oldExt := prev.String(); oldExt != ext {
			delete(x.byExtID, oldExt)
		}
	}
	x.byToken[token] = t
	x.byExtID[ext] = token
	return t, !had || prev != t, true
}

func (x *commandIndex) remove(ext string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if tok, ok := x.byExtID[ext]; ok {
		delete(x.byToken, tok)
		delete(x.byExtID, ext)
	}
}

func (x *commandIndex) locate(token string) (CommandTarget, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	t, ok := x.byToken[token]
	return t, ok
}

func (x *commandIndex) clear() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.byToken = map[string]CommandTarget{}
	x.byExtID = map[string]string{}
}

func (x *commandIndex) size() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.byToken)
}

// TokenResolver maps a device's external id to its DeviceChain token. *adapter.Registrar satisfies
// it; the Client uses it only to learn the token of a device a BIRTH has just resolved, which the
// registrar's cache already holds.
type TokenResolver interface {
	ResolveToken(ctx context.Context, tenant, externalId string, policy IngestPolicy) (token string, ok bool, err error)
}

// CommandHooks are the two callbacks the dispatcher attaches to a Client.
type CommandHooks struct {
	// Born is called on the receive goroutine when a node or device is announced on an ENABLED
	// source, with whether the token is newly in the source's index. It must not block.
	Born func(tenant, token string, added bool)
	// Tripped is called, off the receive goroutine, when the breaker opens, with the instant the
	// session was lost.
	Tripped func(at time.Time)
	// Reported is called on the receive goroutine, after the session tracker's lock is released,
	// with what an accepted message's metrics said (and the NBIRTH it was part of) on an ENABLED
	// source. It must not block: matching is in-memory and anything slower is the hook's to hand
	// off.
	Reported func(tenant string, reports []Report)
}

// Tenant is the tenant every message on this connection is attributed to.
func (c *Client) Tenant() string { return c.tenant }

// SourceID is the source's ingest identity ("sparkplug:<hostId>"), the one its devices carry. A
// command's awaiting entry records it, so a report is matched only against the commands published
// on the same broker.
func (c *Client) SourceID() string { return c.policy.Source }

// Commands is the source's command-downlink configuration.
func (c *Client) Commands() config.SourceCommands { return c.commands }

// SetCommandHooks attaches the dispatcher's callbacks. Set at wiring time before Connect.
func (c *Client) SetCommandHooks(h CommandHooks) { c.hooks = h }

// SetTokenResolver binds the resolver that builds the reverse index. Set at wiring time before
// Connect. A client with no resolver keeps no index, and every command to it reads as not located.
func (c *Client) SetTokenResolver(r TokenResolver) { c.resolver = r }

// Locate resolves a device token to its Sparkplug identity on this session.
func (c *Client) Locate(token string) (CommandTarget, bool) { return c.cmdIndex.locate(token) }

// LookupMetric resolves a metric name against the target's BIRTH table.
func (c *Client) LookupMetric(target CommandTarget, name string) (MetricRef, error) {
	return c.sessions.LookupMetric(target, name)
}

// SessionUp reports whether the Host session is live and has announced ONLINE.
func (c *Client) SessionUp() bool { return c.hostOnline.Load() }

// BreakerState is the source's command breaker state.
func (c *Client) BreakerState() BreakerState {
	return c.breaker.State()
}

// IndexSize is the number of devices the source can currently address; for tests and metrics.
func (c *Client) IndexSize() int { return c.cmdIndex.size() }

// reported hands the dispatcher what an accepted message said. It runs after Observe returned, so
// no session lock is held.
func (c *Client) reported(reports []Report) {
	if len(reports) > 0 && c.hooks.Reported != nil {
		c.hooks.Reported(c.tenant, reports)
	}
}

// BreakerSample is a source's breaker state at this moment, for the scrape-time collector.
func (c *Client) BreakerSample() (tenant, source string, state BreakerState) {
	return c.tenant, c.policy.Source, c.breaker.State()
}

// trackCommandTargets updates the reverse index from the presence events an accepted message
// asserted. Connected events add the device (resolving its token through the registrar's cache),
// and disconnects remove it. It runs on the receive goroutine, in message order.
func (c *Client) trackCommandTargets(ctx context.Context, events []PresenceEvent) {
	if c.resolver == nil {
		return
	}
	for _, ev := range events {
		if !ev.Connected {
			c.cmdIndex.remove(ev.ExternalId)
			continue
		}
		token, ok, err := c.resolver.ResolveToken(ctx, c.tenant, ev.ExternalId, c.policy)
		if err != nil {
			// The device is not addressable until its next birth; a command to it parks.
			log.Debug().Err(err).Str("tenant", c.tenant).Str("externalId", ev.ExternalId).
				Msg("Could not resolve a born device's token; commands to it wait for its next birth.")
			continue
		}
		if !ok {
			continue
		}
		_, added, valid := c.cmdIndex.add(ev.ExternalId, token)
		if valid && c.commands.Enabled && c.hooks.Born != nil {
			c.hooks.Born(c.tenant, token, added)
		}
	}
}

// commandReady checks what must hold for a command to be published now, without consuming the
// breaker's probe slot.
func (c *Client) commandReady(ctx context.Context) error {
	if ctx.Err() != nil {
		return ErrTermEnded
	}
	if !c.hostOnline.Load() {
		return ErrSessionDown
	}
	if c.BreakerState() == BreakerOpen {
		return ErrBreakerOpen
	}
	return nil
}

// PublishMetric writes one value to a metric the target's BIRTH declared.
//
// ref is what validation resolved earlier, and it is checked again here against the CURRENT table:
// a rebirth that landed in between re-addresses the command by the new alias, or refuses it if the
// metric is gone or changed type, rather than sending a stale one. beforeWire, when set, is called
// once with the command exactly as it will go out, after that last check and immediately before
// the publish, and the publish does not happen if it returns an error. That is where the caller
// records what it is about to send, so a record exists for every command that reaches the wire.
func (c *Client) PublishMetric(ctx context.Context, ref MetricRef, value any, beforeWire func(CommandWire) error) (CommandWire, error) {
	if err := c.commandReady(ctx); err != nil {
		return CommandWire{}, err
	}
	cur, err := c.sessions.RevalidateMetric(ref)
	if err != nil {
		return CommandWire{}, err
	}
	at := c.now()
	payload, err := BuildMetricCommand(cur, value, at.UnixMilli())
	if err != nil {
		return CommandWire{}, err
	}
	wire := wireOf(cur.Target.Topic(), payload, cur, value, at)
	// Is the node already at this value? Read from the table just now, for a node that has not
	// re-declared since (a replaced table says nothing about what the node holds). A reserved
	// control metric (Reboot) is a trigger, not a value, and is never "already there".
	if !isReservedMetric(cur.Name) {
		if want, werr := RequestedWire(cur.Datatype, value); werr == nil {
			if last, lastAt, ok := c.sessions.LastReported(cur); ok && last == want {
				wire.AtRequestedValue, wire.LastReportedAt = true, lastAt
			}
		}
	}
	return c.putOnWire(ctx, cur.Target, &cur, payload, wire, beforeWire)
}

// PublishRebirth asks a node to rebirth (NCMD "Node Control/Rebirth"), addressed by name as the
// protocol requires of this one command. The node of a device target is used. It marks the rebirth
// as commanded, so the NBIRTH that answers it is taken as the answer it is.
func (c *Client) PublishRebirth(ctx context.Context, target CommandTarget, beforeWire func(CommandWire) error) (CommandWire, error) {
	node := CommandTarget{Group: target.Group, Node: target.Node}
	if err := c.commandReady(ctx); err != nil {
		return CommandWire{}, err
	}
	at := c.now()
	payload, err := rebirthCommand(at.UnixMilli())
	if err != nil {
		return CommandWire{}, err
	}
	ref := MetricRef{Target: node, Name: nodeControlRebirthMetric, Datatype: datatypeBoolean}
	wire := wireOf(node.Topic(), payload, ref, true, at)
	out, err := c.putOnWire(ctx, node, nil, payload, wire, beforeWire)
	if err == nil {
		c.sessions.MarkCommandedRebirth(node.Group, node.Node)
	}
	return out, err
}

// putOnWire is the shared tail of every command publish: the before-wire hook, the last checks, and
// the publish itself.
func (c *Client) putOnWire(ctx context.Context, target CommandTarget, ref *MetricRef, payload []byte, wire CommandWire, beforeWire func(CommandWire) error) (CommandWire, error) {
	if beforeWire != nil {
		if err := beforeWire(wire); err != nil {
			return wire, &BeforeWireError{Err: err}
		}
	}
	// The last look, after the record exists: the term may have ended and the session may have
	// dropped while it was being written. Admit takes the half-open probe slot, so it is the
	// final gate and is given back if the publish does not happen.
	if ctx.Err() != nil {
		return wire, ErrTermEnded
	}
	if !c.hostOnline.Load() {
		return wire, ErrSessionDown
	}
	// And the table the alias came from must still be the node's current one: a rebirth can land
	// while the record is written, and the check is taken under the tracker's lock, after the
	// record and immediately before the publish. A rebirth is addressed by name and has no table.
	if ref != nil && !c.sessions.Current(*ref) {
		return wire, ErrTableMoved
	}
	if !c.breaker.Admit() {
		return wire, ErrBreakerOpen
	}
	if err := c.publishCommand(target, payload); err != nil {
		if errors.Is(err, errCommandNotConnected) {
			c.breaker.Release()
			return wire, ErrSessionDown
		}
		// Anything else leaves the outcome unknown: the bytes may have reached the broker. It
		// counts as a publish for the breaker's watch, and a half-open probe slot stays taken.
		c.breaker.Published()
		return wire, &PublishIndeterminateError{Err: err}
	}
	c.breaker.Published()
	return wire, nil
}

// noteSessionLost is called when a Host session ends. A session that ends right after a command
// publish is read as the broker refusing the command; see Breaker.
func (c *Client) noteSessionLost() {
	c.hostOnline.Store(false)
	at := c.now()
	if !c.breaker.Disconnected() {
		return
	}
	if c.metrics.CommandBreakerTrips != nil {
		c.metrics.CommandBreakerTrips.Inc()
	}
	log.Warn().Str("tenant", c.tenant).Str("broker", c.broker.URL).
		Msg("The broker dropped the Host session right after a command was published; it most likely refused the command (check the source's publish permission on NCMD/DCMD topics). Commands to this source are paused and fail fast; ingest is unaffected.")
	if c.hooks.Tripped != nil {
		c.hooks.Tripped(at)
	}
}
