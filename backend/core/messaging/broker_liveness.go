// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// How long a broker connection that died WITHOUT being closed stays in use.
//
// A NATS server whose machine stops abruptly (a reset or lost node, a kernel panic) or that a
// network partition cuts off closes nothing: no FIN, no RST. Its clients' sockets stay open
// and are simply never answered, and the client finds out only from its own liveness checks.
// With nats.go's defaults those are a 2-minute ping with two unanswered pings allowed, so an
// idle connection is given up after up to six minutes. A BUSY one is no better: a write that
// times out is returned to its caller and the connection is KEPT, and the ping timer queues on
// the connection lock behind every blocked writer, each holding it for the default one-minute
// flusher timeout. A hard reset of the node holding event-sources' connection stopped all HTTP
// ingest for five and a half minutes that way, and Kubernetes recorded nothing, because the
// machine was back before the node was ever marked NotReady.
//
// Two bounds close that, and both are needed:
//
//   - the ping (BrokerPingInterval, BrokerMaxPingsOutstanding) gives up an idle or lightly
//     loaded connection: nats.go raises ErrStaleConnection on the tick that finds more than
//     MaxPingsOut pings unanswered, so (MaxPingsOut+1) x PingInterval after the last PONG;
//   - the write timeout (BrokerWriteTimeout) gives up a loaded one: every write nats.go makes
//     carries a FlusherTimeout deadline, and the connection the dialer below returns CLOSES
//     ITSELF when such a write times out. The read loop then sees a closed socket, which
//     nats.go treats as a lost connection, and every writer queued on the lock fails at once
//     instead of waiting out a deadline of its own.
//
// The two compose rather than overlap: a writer that starts blocking just before the third
// tick delays that tick by up to one write timeout, so BrokerDeadConnectionBound, the worst
// case, is the ping bound PLUS the write timeout. After it, the client reconnects on its own
// (ReconnectWait plus a dial); a single broker URL behind a Service can route that dial to the
// lost server until Kubernetes stops routing to it, which costs a dial timeout per miss.
//
// These are PLATFORM CONSTANTS, not configuration: they bound a failure mode, and a knob would
// let an operator set them back to minutes. 10 s is the broker server's own default deadline
// for writing to a client, so a healthy server is never given up for a stall it would itself
// tolerate; and one PING/PONG per connection every 10 s is negligible for the broker at the
// few dozen connections an instance holds.
//
// Long-lived connections NOT covered here, and why:
//
//   - the paho MQTT clients (event-sources' external source, sparkplug-ingest's host, the
//     edge agent's uplink) keep paho's defaults: a 30 s keep-alive, a 10 s ping timeout and
//     NO write timeout. That bounds an IDLE or receive-mostly connection to a dead peer at
//     roughly 40 s, but NOT a loaded publisher: paho writes its PINGREQ straight to the
//     socket with no deadline, so once a publisher has filled the socket against a dead
//     peer that write blocks for good and the ping timeout is never checked. The edge
//     agent's uplink, which publishes upstream, is the one exposed. Bounding it needs
//     SetWriteTimeout on those clients, which is not done here and is an open follow-up;
//   - outbound-connectors' MQTT client sets its own keep-alive and write timeout;
//   - event-sources' presence canary is per probe and short-lived;
//   - a Postgres connection is not on the ingest path (event-sources opens no database).
const (
	// BrokerPingInterval is how often the client pings its server.
	BrokerPingInterval = 10 * time.Second
	// BrokerMaxPingsOutstanding unanswered pings are tolerated; the next tick gives the
	// connection up. NOT a zero-able knob: nats.go rewrites a MaxPingsOut of 0 to its own
	// default (2) rather than reading it as "none".
	BrokerMaxPingsOutstanding = 2
	// BrokerWriteTimeout bounds one socket write. A write that makes no progress for this
	// long closes the connection (stallClosingConn), so the client reconnects. It must stay
	// above zero: at zero nats.go installs no write deadline, and nothing would ever time out.
	BrokerWriteTimeout = 10 * time.Second
	// BrokerDeadConnectionBound is the longest a connection to a server that has stopped
	// answering stays CONNECTED: (MaxPingsOutstanding+1) ping intervals after the last PONG
	// for an idle connection, plus one write timeout when a blocked writer delays the tick
	// that would have given it up.
	BrokerDeadConnectionBound = (BrokerMaxPingsOutstanding+1)*BrokerPingInterval + BrokerWriteTimeout

	// brokerDialTimeout bounds one dial. It is nats.go's own default: a CustomDialer replaces
	// the library's dialer and so has to carry the timeout itself. It is FIXED, and two
	// things the library's dialer does are therefore not done: honouring a caller-set
	// nats.Timeout (no DeviceChain connection sets one; a connection that starts to must
	// change this too), and dividing the timeout among the addresses a hostname resolves
	// to (each gets the whole timeout here, so a name resolving to N dead addresses costs
	// up to N dial timeouts per attempt rather than one).
	brokerDialTimeout = nats.DefaultTimeout
)

// BrokerLivenessOptions returns the options that bound how long a broker connection that died
// without being closed stays in use (see BrokerDeadConnectionBound). EVERY long-lived
// connection a DeviceChain process opens to NATS appends them; they live here, once, so the
// connections cannot drift apart.
//
// connection names the connection in the log line a stalled write produces (a service holds
// more than one). onWriteStall, when non-nil, is called once for each connection a stalled
// write closed, so the connection's DisconnectErrHandler, which nats.go calls after it, can
// tell that disconnect from the others. What guarantees "after" is NOT the closer calling
// onWriteStall before it closes: every write nats.go makes on a connection in use runs under
// the connection's lock, which the read loop's processOpErr also takes before it queues the
// handler, so the handler is queued only once the stalled write has returned, whichever order
// the closer used. Do not reason from the closer's order.
//
// A stall can also close a connection that never became the live one (a CONNECT or the
// reconnect's flush of buffered publishes, both of which nats.go writes under the same write
// timeout), and no DisconnectErrHandler follows for that one; the caller must therefore also
// drop whatever onWriteStall recorded when a connection is (re)established.
//
// TLS is unaffected: nats.go wraps the dialed connection in tls.Client, a TLS write that
// reaches the deadline surfaces this connection's timeout, and the dialer does not implement
// SkipTLSHandshake, so the handshake still runs.
func BrokerLivenessOptions(connection string, onWriteStall func()) []nats.Option {
	return []nats.Option{
		nats.PingInterval(BrokerPingInterval),
		nats.MaxPingsOutstanding(BrokerMaxPingsOutstanding),
		nats.FlusherTimeout(BrokerWriteTimeout),
		nats.SetCustomDialer(&stallClosingDialer{
			dialer:     net.Dialer{Timeout: brokerDialTimeout},
			connection: connection,
			onStall:    onWriteStall,
		}),
	}
}

type stallClosingDialer struct {
	dialer     net.Dialer
	connection string
	onStall    func()
}

// Dial implements nats.CustomDialer.
func (d *stallClosingDialer) Dial(network, address string) (net.Conn, error) {
	c, err := d.dialer.Dial(network, address)
	if err != nil {
		return nil, err
	}
	return &stallClosingConn{Conn: c, connection: d.connection, onStall: d.onStall}, nil
}

// stallClosingConn closes itself the first time a TRAFFIC write times out: one whose deadline
// is the FlusherTimeout that nats.go arms around every write it makes on a connection in use.
// Closing is what turns a stalled write into a reconnect. nats.go keeps a connection whose
// write failed, but its read loop treats the closed socket as a lost connection.
//
// 🔴 ONLY THAT DEADLINE COUNTS, and the deadline is told apart by how far ahead it was armed.
// Two shorter ones reach this connection too, and neither means a connection in use died:
// nats.go's whole-connect deadline (Opts.Timeout, 2 s), under which a TLS handshake that
// times out already fails the connect, and crypto/tls's close_notify on Close (5 s), which
// runs while nats.go is ALREADY giving the connection up for another reason (a stale ping, a
// requested shutdown) and would otherwise be counted as a second death. Those are passed
// through untouched. The threshold is half the write timeout: well above both, well below the
// write timeout itself, which is armed a few nanoseconds before it is read.
//
// The CONNECT itself is NOT under the 2 s deadline: nats.go writes it through the same
// writer as all later traffic, which re-arms the full write timeout around it, so a CONNECT
// that stalls is closed here like any other write. That is harmless (the connect fails
// either way, and a CONNECT is a few hundred bytes), but it is one of the closes that no
// DisconnectErrHandler follows; see BrokerLivenessOptions.
type stallClosingConn struct {
	net.Conn
	connection string
	onStall    func()
	once       sync.Once
	// armed is how far ahead, in nanoseconds, the current write deadline was set (0: none).
	armed atomic.Int64
}

func (c *stallClosingConn) SetDeadline(t time.Time) error {
	c.arm(t)
	return c.Conn.SetDeadline(t)
}

func (c *stallClosingConn) SetWriteDeadline(t time.Time) error {
	c.arm(t)
	return c.Conn.SetWriteDeadline(t)
}

func (c *stallClosingConn) arm(t time.Time) {
	var ahead time.Duration
	if !t.IsZero() {
		ahead = time.Until(t)
	}
	c.armed.Store(int64(ahead))
}

func (c *stallClosingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	var ne net.Error
	if err != nil && errors.As(err, &ne) && ne.Timeout() &&
		time.Duration(c.armed.Load()) > BrokerWriteTimeout/2 {
		c.once.Do(func() {
			log.Warn().Str("area", c.connection).Str("server", c.RemoteAddr().String()).
				Dur("write_timeout", BrokerWriteTimeout).
				Msg("A write to the NATS server made no progress; closing the connection so the client " +
					"reconnects. The server, its node or the network to it has stopped answering, or " +
					"the server has stopped reading")
			if c.onStall != nil {
				c.onStall()
			}
			_ = c.Conn.Close()
		})
	}
	return n, err
}
