// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package server is the LwM2M ingest CoAP/UDP+DTLS transport substrate (ADR-075,
// slice L0). It stands up a DTLS-PSK-authenticated CoAP server — pion/dtls (MIT) for
// the DTLS 1.2 termination, plgd-dev/go-coap (Apache-2.0) for CoAP over it — that
// carries many concurrent long-lived sessions, authenticates every device against a
// fail-closed PSK credential map, and (by default) issues a DTLS Connection ID (RFC
// 9146) so a session survives a client's source-address change without a fresh
// handshake.
//
// This is the transport ONLY. The LwM2M semantic layer — the /rd registration
// interface, presence, Observe→measurement decoding, downlink — lands in later
// slices (L1+) as handlers on the mux this server owns. What L0 proves is that the
// substrate is sound: it carries many sessions, it fails closed on authentication,
// and its CID configuration behaves as claimed (the ADR-075 Gate-2 proof).
//
// The peer-address update that makes CID roaming safe is enforced by pion, not here:
// pion rebinds a session's remote address only after a CID-tagged record AEAD-decrypts
// AND passes anti-replay as the latest record (conn.go, per RFC 9146 §6), so a forged
// or replayed record from a spoofed source cannot redirect a session. server_test.go
// proves that property against the exact DTLS config this package builds.
package server

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	piondtls "github.com/pion/dtls/v3"
	coapdtls "github.com/plgd-dev/go-coap/v3/dtls"
	dtlsserver "github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapnet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v3/options"
	udpclient "github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// healthPath is the trivial CoAP resource L0 answers to prove the full DTLS→CoAP
// stack round-trips. It carries no LwM2M semantics; the real /rd interface arrives in
// L1. It is deliberately NOT /.well-known/core (which a real client probes) so it
// cannot be mistaken for a conformant resource-discovery response.
const healthPath = "/dc/health"

// Metrics are the optional Prometheus instruments the transport updates. Any nil
// field is skipped, so the server is usable in tests without a registry. None is
// labeled by tenant — the transport has no tenant at this layer, and a per-identity
// label on a device-driven counter is the cardinality risk the governance lesson
// (ADR-023) warns against.
type Metrics struct {
	// Handshakes counts completed DTLS handshakes (each is a new authenticated
	// session). The CID roaming proof asserts this does NOT advance across a source
	// address change.
	Handshakes prometheus.Counter
	// HandshakeFailures counts handshakes refused because the client presented a PSK
	// identity not in the credential map — the fail-closed authentication floor. A
	// known identity presenting a wrong key fails later in the DTLS Finished exchange
	// (pion-side), not here; this counter is specifically the unknown-identity signal.
	HandshakeFailures prometheus.Counter
	// ActiveSessions is the live DTLS session count.
	ActiveSessions prometheus.Gauge
	// SessionsRejected counts new sessions refused because MaxSessions was reached.
	SessionsRejected prometheus.Counter
	// Requests counts CoAP requests handled, labeled by response code.
	Requests *prometheus.CounterVec
}

// Config is the resolved transport configuration. Credentials is the resolved
// identity->pre-shared-key map (from config.ResolveCredentials); an empty map is a
// valid inert posture that authenticates no one. CIDLength > 0 enables the DTLS
// Connection ID. MaxSessions is the live-session ceiling (must be >= 1). IdleTimeout
// of 0 disables idle-session reaping.
type Config struct {
	Addr             string
	Credentials      map[string][]byte
	CIDLength        int
	HandshakeTimeout time.Duration
	MaxSessions      int
	IdleTimeout      time.Duration
}

// Server is the running CoAP/DTLS transport. Build it with New, then Serve (blocks)
// and Stop. It is safe to Stop once; a second Stop is a no-op.
type Server struct {
	cfg     Config
	metrics Metrics

	listener *coapnet.DTLSListener
	coap     *dtlsserver.Server

	active   int64 // live session count (atomic), mirrored to the ActiveSessions gauge
	stopOnce sync.Once
}

// New builds the CoAP/DTLS server: it resolves the bind address, constructs the
// DTLS-PSK (and optionally CID) listener over the exact config buildServerDTLSConfig
// produces, and wires the CoAP mux plus the session-accounting hooks. It does NOT
// begin accepting — call Serve for that. A bad bind address or listener construction
// fails here so a misconfiguration crashes startup rather than a silent non-serving
// pod.
func New(cfg Config, metrics Metrics) (*Server, error) {
	if cfg.MaxSessions < 1 {
		return nil, fmt.Errorf("server: MaxSessions must be >= 1 (got %d)", cfg.MaxSessions)
	}
	s := &Server{cfg: cfg, metrics: metrics}

	dtlsCfg := buildServerDTLSConfig(cfg, s.onAuthFailure)
	listener, err := coapnet.NewDTLSListener("udp", cfg.Addr, dtlsCfg)
	if err != nil {
		return nil, fmt.Errorf("server: cannot listen on %q: %w", cfg.Addr, err)
	}
	s.listener = listener

	router := mux.NewRouter()
	if err := router.Handle(healthPath, mux.HandlerFunc(s.handleHealth)); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("server: cannot register health handler: %w", err)
	}

	opts := []dtlsserver.Option{
		options.WithMux(router),
		options.WithOnNewConn(s.onNewConn),
		// Surface go-coap's internal transport/handshake errors through the structured
		// logger instead of its default fmt.Println. These are frequent-and-expected
		// (every unknown-identity refusal is one), so they log at debug; the counters
		// carry the operational signal.
		options.WithErrors(func(err error) {
			log.Debug().Err(err).Msg("LwM2M CoAP/DTLS transport error (a handshake refusal or a transport-level error).")
		}),
	}
	if cfg.HandshakeTimeout > 0 {
		// Bound a single DTLS handshake so a stalled or half-open handshake cannot pin a
		// serve goroutine (the unauthenticated-datagram DoS surface, ADR-075 §7).
		opts = append(opts, options.WithDTLSHandshakeTimeout(cfg.HandshakeTimeout))
	}
	if cfg.IdleTimeout > 0 {
		// Reap a session idle past the timeout, bounding the memory a fleet of idle
		// sessions holds (ADR-075 M6). onInactive closes the connection, which fires the
		// AddOnClose hook that decrements the active count.
		opts = append(opts, options.WithInactivityMonitor(cfg.IdleTimeout, func(c *udpclient.Conn) {
			log.Debug().Str("peer", c.RemoteAddr().String()).Msg("Reaping idle LwM2M DTLS session.")
			_ = c.Close()
		}))
	} else {
		// CRITICAL: go-coap's DefaultConfig installs a 16-SECOND inactivity monitor that
		// closes any session with no inbound message for 16s. Left in place that would
		// silently sever a healthy-but-quiet device — and defeat the entire point of CID
		// for queue-mode sleepers, which are idle for minutes to hours by design. There is
		// no stock "disable" option, so when no idle timeout is configured we MUST actively
		// override the default with a nil monitor.
		opts = append(opts, disableInactivityMonitor{})
	}
	s.coap = coapdtls.NewServer(opts...)
	return s, nil
}

// buildServerDTLSConfig produces the pion DTLS server config the listener runs on. It
// is the single source of the server's DTLS posture, so both New and the tests build
// from it — the CID/auth proofs then exercise exactly what production runs. The PSK
// callback is fail-closed: an identity absent from the credential map is refused (and
// counted via onAuthFailure), which is the authentication floor the L1 tenancy seam
// binds tenant + device identity onto.
func buildServerDTLSConfig(cfg Config, onAuthFailure func()) *piondtls.Config {
	dtlsCfg := &piondtls.Config{
		PSK: func(identity []byte) ([]byte, error) {
			if key, ok := cfg.Credentials[string(identity)]; ok && len(key) > 0 {
				return key, nil
			}
			if onAuthFailure != nil {
				onAuthFailure()
			}
			// Do not echo the identity: it is attacker-controlled and would let a probe
			// enumerate provisioned identities by differential error. The datagram-level
			// cookie exchange (HelloVerifyRequest, on by default in pion's server flow)
			// already blunts spoofed-source handshake amplification.
			return nil, fmt.Errorf("dtls: unknown PSK identity")
		},
		// The LwM2M-mandated constrained cipher suite (OMA LwM2M 1.x security).
		CipherSuites: []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}
	if cfg.CIDLength > 0 {
		// Enabling CID makes pion route inbound records by Connection ID rather than by
		// UDP 5-tuple, so a session survives a client's source-address change. pion only
		// rebinds the peer address after a CID record validates (AEAD + anti-replay), so
		// this does not open a redirection vector — proven in server_test.go.
		dtlsCfg.ConnectionIDGenerator = piondtls.RandomCIDGenerator(cfg.CIDLength)
	}
	return dtlsCfg
}

// Serve accepts connections until Stop is called. It blocks. A returned error other
// than the normal shutdown is a real serve failure.
func (s *Server) Serve() error {
	log.Info().Str("addr", s.Addr().String()).Int("cidLength", s.cfg.CIDLength).
		Int("maxSessions", s.cfg.MaxSessions).Int("credentials", len(s.cfg.Credentials)).
		Msg("LwM2M CoAP/DTLS transport listening.")
	return s.coap.Serve(s.listener)
}

// Stop halts accepting and closes the listener. Idempotent.
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		s.coap.Stop()
		if err := s.listener.Close(); err != nil {
			log.Warn().Err(err).Msg("Error closing the LwM2M DTLS listener.")
		}
	})
}

// Addr returns the bound listener address (useful when the config port was 0).
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// onNewConn fires once per completed DTLS handshake. It enforces the live-session
// ceiling (a handshake past MaxSessions is accepted by DTLS but immediately closed and
// counted as rejected — so the table is transiently exceeded by one, then bounded) and
// maintains the active-session count. The gauge is moved with Inc/Dec (each internally
// atomic) rather than Set(atomicCount): two concurrent closes doing atomic-add then
// Set could land the stale Set last and pin the gauge above the true count until the
// next session event — a permanent drift in a quiet deployment.
func (s *Server) onNewConn(c *udpclient.Conn) {
	if s.metrics.Handshakes != nil {
		s.metrics.Handshakes.Inc()
	}
	newActive := atomic.AddInt64(&s.active, 1)
	if s.metrics.ActiveSessions != nil {
		s.metrics.ActiveSessions.Inc()
	}
	c.AddOnClose(func() {
		atomic.AddInt64(&s.active, -1)
		if s.metrics.ActiveSessions != nil {
			s.metrics.ActiveSessions.Dec()
		}
	})
	if newActive > int64(s.cfg.MaxSessions) {
		if s.metrics.SessionsRejected != nil {
			s.metrics.SessionsRejected.Inc()
		}
		log.Warn().Str("peer", c.RemoteAddr().String()).Int("maxSessions", s.cfg.MaxSessions).
			Msg("Refusing new LwM2M session: live-session ceiling reached.")
		_ = c.Close() // fires AddOnClose → decrements active back down
	}
}

// onAuthFailure counts a refused (unknown-identity) handshake.
func (s *Server) onAuthFailure() {
	if s.metrics.HandshakeFailures != nil {
		s.metrics.HandshakeFailures.Inc()
	}
}

// disableInactivityMonitor is a go-coap DTLS-server option that replaces the default
// 16-second idle reaper with a nil monitor, so a quiet session is never closed for
// inactivity. See the CRITICAL note at its use site in New.
type disableInactivityMonitor struct{}

func (disableInactivityMonitor) DTLSServerApply(cfg *dtlsserver.Config) {
	cfg.CreateInactivityMonitor = func() udpclient.InactivityMonitor {
		return inactivity.NewNilMonitor[*udpclient.Conn]()
	}
}

// handleHealth answers the L0 health probe with 2.05 Content, proving the DTLS→CoAP
// path round-trips end to end. Real LwM2M handlers replace this surface in L1.
func (s *Server) handleHealth(w mux.ResponseWriter, _ *mux.Message) {
	if s.metrics.Requests != nil {
		s.metrics.Requests.WithLabelValues(codes.Content.String()).Inc()
	}
	if err := w.SetResponse(codes.Content, message.TextPlain, nil); err != nil {
		log.Warn().Err(err).Msg("Failed to write LwM2M health response.")
	}
}
