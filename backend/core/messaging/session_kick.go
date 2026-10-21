// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	nats "github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-microservice/core"
)

// Ending a device's live broker sessions when its authority is withdrawn.
//
// A device authenticates ONCE, at CONNECT, and the broker then holds its session for as long
// as the JWT the auth callout minted lives. On the platform MQTT broker the identity of every
// later publish is the session's, not anything in the message body, so withdrawing a device's
// authority (a credential disabled, rotated or deleted; the device disabled or deleted) means
// nothing to a session that is already open until something ENDS that session. The broker can
// end one: a system-account request names the server and the connection. But the system
// account is a separate login this file's callers do not all hold, so ending a session is a
// REQUEST to the service that does hold it (event-sources), and this file is the wire between
// the two. It sits here, as the purge request does, because the subject and the payload have
// to agree on both sides, and the way that goes wrong is a request nobody answers.
//
// # Why a request and not an event
//
// "Fire and forget" is the wrong shape for revocation. The caller must learn that the sessions
// are gone, or that they could not be ended, so that it never reports a revocation it did not
// carry out: the reply is the answer, and no reply is an error.

const (
	// sessionKickSubjectLeaf names the operation. The instance id sits before it, so one
	// broker serving several instances (ADR-048) keeps their requests apart.
	sessionKickSubjectLeaf = ".session-kick"

	// SessionKickQueue is the queue group the responders share, so one replica of the
	// responding service answers each request.
	SessionKickQueue = "session-kick"

	// MaxSessionKickTokens bounds one request. A credential or device change names one or two
	// devices; the bound only stops a malformed request from asking the broker to be scanned
	// for an unbounded set.
	MaxSessionKickTokens = 64

	// sessionKickAttemptTimeout bounds ONE attempt. The responder pings the cluster and pages
	// every server's connection list, which on a large fleet is not instant.
	sessionKickAttemptTimeout = 20 * time.Second
	// sessionKickAttempts is how many times the requester asks before it gives up and returns
	// the error to its caller, who reports it.
	sessionKickAttempts = 3
	// sessionKickBackoff separates attempts.
	sessionKickBackoff = time.Second
)

// SessionKickSubject is the subject a session-kick request is sent on, for one instance:
// "$DC.{instance}.session-kick". Both sides derive it here. It sits under controlSubjectRoot
// for the reason tenant_purge_detect.go gives: outside every stream's capture space, so no
// stream can store a request and replay it later.
func SessionKickSubject(instanceId string) string {
	return controlSubjectRoot + instanceId + sessionKickSubjectLeaf
}

// SessionKickRequest asks for every live session of the named devices of one tenant to be
// ended. Tokens are device tokens, not client ids: the responder knows how a device's
// session is named on the broker.
type SessionKickRequest struct {
	Tenant string   `json:"tenant"`
	Tokens []string `json:"tokens"`
}

// SessionKickReply is the responder's answer. OK is true only when every server of the
// broker cluster was consulted and every session found was ended: a reply that cannot say
// that carries the reason in Error, and the requester treats it as a failure.
type SessionKickReply struct {
	OK     bool   `json:"ok"`
	Kicked int    `json:"kicked,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ErrSessionKickUnavailable marks a revocation whose sessions could not be confirmed ended:
// no responder answered, the responder could not reach the broker's system account, or the
// broker cluster could not be consulted in full. The authority has been withdrawn (the
// database says so, and the next CONNECT is refused); what is NOT known is whether a session
// that was open at the time is still publishing, and the caller must say so.
var ErrSessionKickUnavailable = errors.New("the device's live broker sessions could not be confirmed ended")

// ValidateSessionKick checks a request on both sides of the wire.
func ValidateSessionKick(req SessionKickRequest) error {
	if err := core.ValidateToken(req.Tenant); err != nil {
		return fmt.Errorf("session kick names an invalid tenant: %w", err)
	}
	if len(req.Tokens) == 0 || len(req.Tokens) > MaxSessionKickTokens {
		return fmt.Errorf("session kick names %d devices, want 1 to %d", len(req.Tokens), MaxSessionKickTokens)
	}
	for _, t := range req.Tokens {
		if err := core.ValidateToken(t); err != nil {
			return fmt.Errorf("session kick names an invalid device token: %w", err)
		}
	}
	return nil
}

// SessionKickRequester sends session-kick requests over a connection to the data-plane
// account.
type SessionKickRequester struct {
	nc         *nats.Conn
	instanceID string
	// Attempts and Backoff are exported for tests, which shorten them; the defaults are the
	// constants above.
	Attempts       int
	Backoff        time.Duration
	AttemptTimeout time.Duration
}

// NewSessionKickRequester builds a requester for one instance.
func NewSessionKickRequester(nc *nats.Conn, instanceId string) *SessionKickRequester {
	return &SessionKickRequester{
		nc: nc, instanceID: instanceId,
		Attempts: sessionKickAttempts, Backoff: sessionKickBackoff, AttemptTimeout: sessionKickAttemptTimeout,
	}
}

// KickDeviceSessions ends every live session of the given devices of tenant, retrying a
// failed attempt, and returns nil ONLY on a confirmed reply. Any other outcome is an error
// wrapping ErrSessionKickUnavailable: nothing here ever reports success it did not observe.
func (r *SessionKickRequester) KickDeviceSessions(ctx context.Context, tenant string, tokens []string) error {
	req := SessionKickRequest{Tenant: tenant, Tokens: tokens}
	if err := ValidateSessionKick(req); err != nil {
		return err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	attempts := max(r.Attempts, 1)
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w: %v", ErrSessionKickUnavailable, errors.Join(last, ctx.Err()))
			case <-time.After(r.Backoff):
			}
		}
		last = r.once(ctx, body)
		if last == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: %v", ErrSessionKickUnavailable, last)
}

func (r *SessionKickRequester) once(ctx context.Context, body []byte) error {
	if r.nc == nil {
		return errors.New("no broker connection")
	}
	ctx, cancel := context.WithTimeout(ctx, r.AttemptTimeout)
	defer cancel()
	msg, err := r.nc.RequestWithContext(ctx, SessionKickSubject(r.instanceID), body)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return errors.New("no service is answering session-kick requests; event-sources needs the NATS " +
				"system-account login to end live device sessions")
		}
		return err
	}
	var reply SessionKickReply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return fmt.Errorf("unreadable session-kick reply: %w", err)
	}
	if !reply.OK {
		if reply.Error == "" {
			return errors.New("the responder declined without a reason")
		}
		return errors.New(reply.Error)
	}
	return nil
}
