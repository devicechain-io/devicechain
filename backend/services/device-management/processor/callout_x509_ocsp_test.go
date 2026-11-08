// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/devicechain-io/dc-microservice/x509trust/ocspclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ocsp"
)

// X.509 OCSP ON THE CONNECT PATH. The REAL callout decision over the REAL Api (SQLite); the
// responders are either a scripted checker (to drive each decision exactly) or the real client
// against an httptest OCSP responder (to prove the whole path). Every refusal is the control (the
// connect that is granted) with ONE thing changed, and is asserted against its own counter alone.

// scriptedOcsp answers every target by a function, and records what it was asked.
type scriptedOcsp struct {
	mu    sync.Mutex
	calls []ocspclient.Request
	by    func(t x509trust.OCSPTarget) (x509trust.OCSPOutcome, ocspclient.Reason)
}

func (s *scriptedOcsp) Check(_ context.Context, req ocspclient.Request) ocspclient.Outcome {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	out := ocspclient.Outcome{
		Results: x509trust.OCSPResults{},
		Reasons: map[x509trust.OCSPTargetKey]ocspclient.Reason{},
		Revoked: map[x509trust.OCSPTargetKey]*x509trust.OCSPAnswer{},
	}
	for _, t := range req.Targets {
		o, reason := s.by(t)
		switch o {
		case x509trust.OCSPGood, x509trust.OCSPRevoked:
			out.Results[t.Key()] = o
		default:
			out.Reasons[t.Key()] = reason
		}
	}
	return out
}

func (s *scriptedOcsp) called() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// ocspRig is a pinned device under root -> inter -> leaf with a responder wired in.
type ocspRig struct {
	*x509Fixture
	root, inter, leaf *testCert
	ocspMetrics       *OcspMetrics
}

func newOcspRig(t *testing.T, enabled bool, checker OcspChecker) *ocspRig {
	t.Helper()
	ms := &core.Microservice{InstanceId: "inst-1", FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	om := NewOcspMetrics(ms)
	f := newX509FixtureWith(t, fixtureOpts{configure: func(t *testing.T, resp *CalloutResponder, _ *model.Api) {
		if checker != nil || !enabled {
			resp.WithOcsp(enabled, checker, om)
		}
	}})
	r := &ocspRig{x509Fixture: f, ocspMetrics: om}
	r.root = newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	r.inter = newTestCert(t, r.root, certOpts{cn: "Line A", ca: true})
	r.leaf = newTestCert(t, r.inter, certOpts{cn: "SN-1"})
	f.anchor("maker", r.root)
	f.device("dev-1")
	f.pin("c-1", "dev-1", "maker", r.leaf, r.inter)
	return r
}

func (r *ocspRig) setOcsp(mode, url string) {
	r.t.Helper()
	upd := map[string]any{"ocsp_mode": mode}
	if url != "" {
		upd["ocsp_responder_url"] = url
	}
	require.NoError(r.t, r.api.RDB.DB(r.ctx).Model(&model.DeviceTrustAnchor{}).Where("token = ?", "maker").Updates(upd).Error)
}

func (r *ocspRig) allowed(reason string) float64 {
	return testutil.ToFloat64(r.ocspMetrics.Allowed.WithLabelValues(reason))
}

func (r *ocspRig) grants(result string) {
	r.t.Helper()
	before := r.counts()
	userJWT, why := r.x509Authorize("dev-1", r.leaf, r.inter)
	require.Empty(r.t, why, "refused: %s", why)
	require.NotEmpty(r.t, userJWT)
	r.moved(before, result)
}

func (r *ocspRig) refuses(result string) {
	r.t.Helper()
	before := r.counts()
	userJWT, why := r.x509Authorize("dev-1", r.leaf, r.inter)
	require.Empty(r.t, userJWT, "granted a connect that must be refused as %s", result)
	require.Equal(r.t, genericAuthFailure, why, "the wire answer must be the generic denial")
	r.moved(before, result)
}

func allGood(x509trust.OCSPTarget) (x509trust.OCSPOutcome, ocspclient.Reason) {
	return x509trust.OCSPGood, ""
}

func nothing(x509trust.OCSPTarget) (x509trust.OCSPOutcome, ocspclient.Reason) {
	return x509trust.OCSPNoAnswer, ocspclient.ReasonTimeout
}

// 🔑 OFF IS BYTE-FOR-BYTE TODAY. No checker call, no lookup, nothing. The control right after it
// is the same connect with the mode on, which DOES ask: so the zero is the mode's doing, not a
// checker that was never wired.
func TestOcspOffRunsNothing(t *testing.T) {
	sc := &scriptedOcsp{by: allGood}
	r := newOcspRig(t, true, sc)
	r.grants(x509ResultOK)
	require.Zero(t, sc.called(), "an OFF anchor reached the OCSP checker")
	r.setOcsp("OFF", "") // an explicit OFF is the same as none
	r.grants(x509ResultOK)
	require.Zero(t, sc.called())

	r.setOcsp("ENFORCE_KNOWN", "")
	r.grants(x509ResultOK)
	require.Equal(t, 1, sc.called(), "the control: with the mode on, the checker is asked")
	assert.Len(t, sc.calls[0].Targets, 2, "the leaf and the intermediate are both asked")
}

// With the real client and a transport that fails the test on any dial, an OFF anchor sends no
// byte; and the same connect with the mode on does dial (the control).
func TestOcspOffSendsNoByte(t *testing.T) {
	var dials atomic.Int64
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		dials.Add(1)
		return nil, errors.New("no network in this test")
	})
	hc := ocspclient.NewHTTPClient(rt)
	client := ocspclient.New(ocspclient.Config{FetchDeadline: time.Second, MaxInFlight: 8, MaxAge: time.Hour, CacheEntries: 10, MaxHostsPerTenant: 4}, hc, hc, ocspclient.Hooks{})
	r := newOcspRig(t, true, client)
	r.root = newTestCert(t, nil, certOpts{cn: "Root 2", ca: true, ocsp: []string{"http://ocsp.example.test/"}})
	r.inter = newTestCert(t, r.root, certOpts{cn: "Inter 2", ca: true, ocsp: []string{"http://ocsp.example.test/"}})
	r.leaf = newTestCert(t, r.inter, certOpts{cn: "SN-2", ocsp: []string{"http://ocsp.example.test/"}})
	r.anchor("maker-2", r.root)
	r.device("dev-2")
	r.pin("c-2", "dev-2", "maker-2", r.leaf, r.inter)

	before := r.counts()
	_, why := r.x509Authorize("dev-2", r.leaf, r.inter)
	require.Empty(t, why)
	r.moved(before, x509ResultOK)
	require.Zero(t, dials.Load(), "an OFF anchor made a request")

	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&model.DeviceTrustAnchor{}).Where("token = ?", "maker-2").Update("ocsp_mode", "ENFORCE_KNOWN").Error)
	before = r.counts()
	_, why = r.x509Authorize("dev-2", r.leaf, r.inter)
	require.Empty(t, why, "ENFORCE_KNOWN admits when no responder answers")
	r.moved(before, x509ResultOK)
	require.NotZero(t, dials.Load(), "the control: the mode on does dial")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOcspRevokedRefuses(t *testing.T) {
	revoke := func(who *testCert) func(x509trust.OCSPTarget) (x509trust.OCSPOutcome, ocspclient.Reason) {
		return func(tg x509trust.OCSPTarget) (x509trust.OCSPOutcome, ocspclient.Reason) {
			if sameCert(tg.Cert, who.cert) {
				return x509trust.OCSPRevoked, ""
			}
			return x509trust.OCSPGood, ""
		}
	}
	for _, mode := range []string{"ENFORCE_KNOWN", "REQUIRE_CURRENT"} {
		t.Run(mode+": the leaf", func(t *testing.T) {
			sc := &scriptedOcsp{by: allGood}
			r := newOcspRig(t, true, sc)
			r.setOcsp(mode, "http://responder.test/")
			r.grants(x509ResultOK) // the control
			sc.by = revoke(r.leaf)
			r.refuses(x509ResultOcspRevoked)
		})
		t.Run(mode+": the intermediate", func(t *testing.T) {
			sc := &scriptedOcsp{by: allGood}
			r := newOcspRig(t, true, sc)
			r.setOcsp(mode, "http://responder.test/")
			sc.by = revoke(r.inter)
			r.refuses(x509ResultOcspRevoked)
		})
		// 🔑 REVOKED WINS OVER AN EARLY FAILURE: the leaf's check timed out, the intermediate's says
		// revoked. The refusal is ocsp_revoked, not "unavailable" (REQUIRE_CURRENT) and not a pass
		// (ENFORCE_KNOWN).
		t.Run(mode+": leaf unanswered, intermediate revoked", func(t *testing.T) {
			sc := &scriptedOcsp{}
			r := newOcspRig(t, true, sc)
			r.setOcsp(mode, "http://responder.test/")
			sc.by = func(tg x509trust.OCSPTarget) (x509trust.OCSPOutcome, ocspclient.Reason) {
				if sameCert(tg.Cert, r.inter.cert) {
					return x509trust.OCSPRevoked, ""
				}
				return x509trust.OCSPNoAnswer, ocspclient.ReasonTimeout
			}
			r.refuses(x509ResultOcspRevoked)
		})
	}
}

func TestOcspIncompleteIsTheModesChoice(t *testing.T) {
	t.Run("ENFORCE_KNOWN admits, counts the reason, and says nothing on the wire", func(t *testing.T) {
		sc := &scriptedOcsp{by: nothing}
		r := newOcspRig(t, true, sc)
		r.setOcsp("ENFORCE_KNOWN", "")
		before := r.allowed("timeout")
		r.grants(x509ResultOK)
		assert.Equal(t, before+1, r.allowed("timeout"))
	})
	t.Run("REQUIRE_CURRENT refuses with ocsp_unavailable", func(t *testing.T) {
		sc := &scriptedOcsp{by: nothing}
		r := newOcspRig(t, true, sc)
		r.setOcsp("REQUIRE_CURRENT", "http://responder.test/")
		r.refuses(x509ResultOcspUnavailable)
	})
	t.Run("REQUIRE_CURRENT with the intermediate unanswered refuses; the control has both good", func(t *testing.T) {
		sc := &scriptedOcsp{by: allGood}
		r := newOcspRig(t, true, sc)
		r.setOcsp("REQUIRE_CURRENT", "http://responder.test/")
		r.grants(x509ResultOK)
		sc.by = func(tg x509trust.OCSPTarget) (x509trust.OCSPOutcome, ocspclient.Reason) {
			if sameCert(tg.Cert, r.inter.cert) {
				return x509trust.OCSPNoAnswer, ocspclient.ReasonBreakerOpen
			}
			return x509trust.OCSPGood, ""
		}
		r.refuses(x509ResultOcspUnavailable)
	})
}

// The master switch: ENFORCE_KNOWN runs as OFF (no checker call), REQUIRE_CURRENT refuses.
func TestOcspMasterSwitchOff(t *testing.T) {
	t.Run("ENFORCE_KNOWN is admitted unchecked and counted", func(t *testing.T) {
		sc := &scriptedOcsp{by: allGood}
		r := newOcspRig(t, false, sc)
		r.setOcsp("ENFORCE_KNOWN", "")
		before := r.allowed("disabled")
		r.grants(x509ResultOK)
		assert.Zero(t, sc.called(), "a checker was called with the switch off")
		assert.Equal(t, before+1, r.allowed("disabled"))
	})
	t.Run("REQUIRE_CURRENT refuses", func(t *testing.T) {
		sc := &scriptedOcsp{by: allGood}
		r := newOcspRig(t, false, sc)
		r.setOcsp("REQUIRE_CURRENT", "http://responder.test/")
		r.refuses(x509ResultOcspUnavailable)
		assert.Zero(t, sc.called())
	})
	t.Run("a responder never given OCSP at all behaves as the switch off", func(t *testing.T) {
		r := newOcspRig(t, false, nil)
		r.setOcsp("REQUIRE_CURRENT", "http://responder.test/")
		r.refuses(x509ResultOcspUnavailable)
	})
}

func TestOcspUnknownStoredModeRefuses(t *testing.T) {
	sc := &scriptedOcsp{by: allGood}
	r := newOcspRig(t, true, sc)
	r.setOcsp("BOGUS", "")
	r.refuses(x509ResultError)
	assert.Zero(t, sc.called())
}

// The cheap local checks run first: a manually revoked certificate is refused as cert_revoked and
// costs no OCSP fetch.
func TestOcspIsAskedAfterTheLocalRevocationChecks(t *testing.T) {
	sc := &scriptedOcsp{by: allGood}
	r := newOcspRig(t, true, sc)
	r.setOcsp("ENFORCE_KNOWN", "")
	fp := x509trust.Fingerprint(r.leaf.cert)
	_, err := r.api.RevokeDeviceCertificate(r.ctx, &model.DeviceCertificateRevokeRequest{AnchorToken: "maker", FingerprintSha256: &fp})
	require.NoError(t, err)
	r.refuses(x509ResultCertRevoked)
	require.Zero(t, sc.called(), "a certificate already refused cost an OCSP fetch")
}

// The request carries the tenant, the anchor and the anchor's own responder, and nothing the
// device presented but the verified certificates.
func TestOcspRequestShape(t *testing.T) {
	sc := &scriptedOcsp{by: allGood}
	r := newOcspRig(t, true, sc)
	r.setOcsp("ENFORCE_KNOWN", "http://responder.test/ocsp")
	r.grants(x509ResultOK)
	require.Equal(t, 1, sc.called())
	req := sc.calls[0]
	assert.Equal(t, x509Tenant, req.Tenant)
	assert.Equal(t, "http://responder.test/ocsp", req.OverrideURL)
	assert.NotEmpty(t, req.Anchor)
	assert.Equal(t, r.leaf.cert, req.Targets[0].Cert)
	assert.Equal(t, r.inter.cert, req.Targets[0].Issuer)
}

// ─── the whole path: the real client against httptest OCSP responders ───

// liveResponder answers for certificates issued by signer (set once the CA exists, because the
// certificates need the responder's URL first).
type liveResponder struct {
	*httptest.Server
	signer atomic.Pointer[testCert]
	hits   atomic.Int64
	status atomic.Int32
	down   atomic.Bool
}

func newLiveResponder(t *testing.T) *liveResponder {
	lr := &liveResponder{}
	lr.status.Store(int32(ocsp.Good))
	lr.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		lr.hits.Add(1)
		if lr.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(req.Body)
		parsed, err := ocsp.ParseRequest(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		signer := lr.signer.Load()
		tmpl := ocsp.Response{Status: int(lr.status.Load()), SerialNumber: parsed.SerialNumber,
			ThisUpdate: time.Now().Add(-time.Minute), NextUpdate: time.Now().Add(time.Hour)}
		if tmpl.Status == ocsp.Revoked {
			tmpl.RevokedAt, tmpl.RevocationReason = time.Now().Add(-time.Hour), ocsp.KeyCompromise
		}
		der, err := ocsp.CreateResponse(signer.cert, signer.cert, tmpl, signer.key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/ocsp-response")
		_, _ = w.Write(der)
	}))
	t.Cleanup(lr.Close)
	return lr
}

// liveRig is root -> inter -> leaf where each certificate's AIA names the responder of its issuer,
// with the real client (plain transports) wired into the callout.
type liveRig struct {
	*ocspRig
	rootResp, interResp *liveResponder
}

func newLiveRig(t *testing.T, cfg ocspclient.Config) *liveRig {
	t.Helper()
	rootResp, interResp := newLiveResponder(t), newLiveResponder(t)
	plain := ocspclient.NewHTTPClient(http.DefaultTransport)
	if cfg.FetchDeadline == 0 {
		cfg.FetchDeadline = 2 * time.Second
	}
	cfg.MaxInFlight, cfg.MaxAge, cfg.CacheEntries, cfg.MaxHostsPerTenant = 16, 6*time.Hour, 100, 8
	r := newOcspRig(t, true, ocspclient.New(cfg, plain, plain, ocspclient.Hooks{}))
	r.root = newTestCert(t, nil, certOpts{cn: "Live Root", ca: true})
	r.inter = newTestCert(t, r.root, certOpts{cn: "Live Inter", ca: true, ocsp: []string{rootResp.URL}})
	r.leaf = newTestCert(t, r.inter, certOpts{cn: "SN-LIVE", ocsp: []string{interResp.URL}})
	rootResp.signer.Store(r.root)
	interResp.signer.Store(r.inter)
	r.anchor("live", r.root)
	r.device("dev-live")
	r.pin("c-live", "dev-live", "live", r.leaf, r.inter)
	return &liveRig{ocspRig: r, rootResp: rootResp, interResp: interResp}
}

func (l *liveRig) set(mode string) {
	l.t.Helper()
	require.NoError(l.t, l.api.RDB.DB(l.ctx).Model(&model.DeviceTrustAnchor{}).Where("token = ?", "live").Update("ocsp_mode", mode).Error)
}

func (l *liveRig) connect(device string) (string, string, map[string]float64) {
	before := l.counts()
	userJWT, why := l.x509Authorize(device, l.leaf, l.inter)
	return userJWT, why, before
}

func TestOcspEndToEnd(t *testing.T) {
	t.Run("good answers admit, are cached, and a revoked leaf is refused", func(t *testing.T) {
		l := newLiveRig(t, ocspclient.Config{})
		l.set("ENFORCE_KNOWN")
		jwt, why, before := l.connect("dev-live")
		require.Empty(t, why)
		require.NotEmpty(t, jwt)
		l.moved(before, x509ResultOK)
		require.Equal(t, int64(1), l.interResp.hits.Load())
		require.Equal(t, int64(1), l.rootResp.hits.Load(), "the intermediate is checked too")

		_, why, _ = l.connect("dev-live")
		require.Empty(t, why)
		require.Equal(t, int64(1), l.interResp.hits.Load(), "the second connect is served from the cache")

		// A revoked answer is not cached: flip the leaf's responder and a fresh certificate is refused.
		l.interResp.status.Store(int32(ocsp.Revoked))
		fresh := newTestCert(t, l.inter, certOpts{cn: "SN-FRESH", ocsp: []string{l.interResp.URL}})
		l.device("dev-fresh")
		l.pin("c-fresh", "dev-fresh", "live", fresh, l.inter)
		before = l.counts()
		userJWT, why := l.x509Authorize("dev-fresh", fresh, l.inter)
		require.Empty(t, userJWT)
		require.Equal(t, genericAuthFailure, why)
		l.moved(before, x509ResultOcspRevoked)
	})
	t.Run("a down responder: ENFORCE_KNOWN admits, REQUIRE_CURRENT refuses", func(t *testing.T) {
		l := newLiveRig(t, ocspclient.Config{})
		l.interResp.down.Store(true)
		l.set("ENFORCE_KNOWN")
		before := l.allowed("bad_response")
		_, why, b := l.connect("dev-live")
		require.Empty(t, why, "ENFORCE_KNOWN must admit with the leaf's responder down")
		l.moved(b, x509ResultOK)
		assert.Equal(t, before+1, l.allowed("bad_response"))

		l.set("REQUIRE_CURRENT")
		require.NoError(t, l.api.RDB.DB(l.ctx).Model(&model.DeviceTrustAnchor{}).Where("token = ?", "live").
			Update("ocsp_responder_url", l.interResp.URL).Error)
		fresh := newTestCert(t, l.inter, certOpts{cn: "SN-FRESH2", ocsp: []string{l.interResp.URL}})
		l.device("dev-fresh2")
		l.pin("c-fresh2", "dev-fresh2", "live", fresh, l.inter)
		b = l.counts()
		userJWT, why := l.x509Authorize("dev-fresh2", fresh, l.inter)
		require.Empty(t, userJWT)
		require.Equal(t, genericAuthFailure, why)
		l.moved(b, x509ResultOcspUnavailable)
	})
	t.Run("a slow responder never holds a connect past the deadline", func(t *testing.T) {
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
		}))
		t.Cleanup(slow.Close)
		l := newLiveRig(t, ocspclient.Config{FetchDeadline: 250 * time.Millisecond})
		fresh := newTestCert(t, l.inter, certOpts{cn: "SN-SLOW", ocsp: []string{slow.URL}})
		l.device("dev-slow")
		l.pin("c-slow", "dev-slow", "live", fresh, l.inter)
		l.set("ENFORCE_KNOWN")
		start := time.Now()
		userJWT, why := l.x509Authorize("dev-slow", fresh, l.inter)
		require.Empty(t, why)
		require.NotEmpty(t, userJWT)
		require.Less(t, time.Since(start), 2*time.Second, "the connect waited on a slow responder")
	})
}

// 🔑 THE JIT GATE: a never-registered certificate its CA says is revoked is refused BEFORE anything
// is spent or written (no device, no credential, no allowance), exactly as a CRL-revoked one is; its
// good twin registers (the control).
func TestOcspJitRefusesARevokedCertificateBeforeRegistering(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	sc := &scriptedOcsp{by: allGood}
	r.resp.WithOcsp(true, sc, nil)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&model.DeviceTrustAnchor{}).Where("token = ?", "maker").
		Update("ocsp_mode", "ENFORCE_KNOWN").Error)

	revoked := r.ca.simDevice(t, "SN-REVOKED", time.Hour)
	twin := r.ca.simDevice(t, "SN-TWIN", time.Hour)
	sc.by = func(tg x509trust.OCSPTarget) (x509trust.OCSPOutcome, ocspclient.Reason) {
		if sameCert(tg.Cert, revoked.cert) {
			return x509trust.OCSPRevoked, ""
		}
		return x509trust.OCSPGood, ""
	}
	r.refuses(x509ResultOcspRevoked, r.clientID("SN-REVOKED"), revoked)
	assert.Empty(t, r.tenants.charged(), "a revoked certificate costs the tenant no registration allowance")
	r.grants(x509ResultJitCreated, r.clientID("SN-TWIN"), twin)
}

// sameCert compares by encoding: the callout parses the chain a device presented, so the
// certificates it hands the checker are not the test's own pointers.
func sameCert(a, b *x509.Certificate) bool { return bytes.Equal(a.Raw, b.Raw) }

// 🔴 A path the CRL step dropped is still ASKED about, and a revoked answer on it refuses: a
// cross-signed intermediate the CA revoked must not be hidden because the CRL step happened to
// leave only the other path standing. The control is the same call with nothing revoked.
func TestOcspRevokedOnACrlDroppedPathStillRefuses(t *testing.T) {
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	interA := newTestCert(t, root, certOpts{cn: "Line A", ca: true})
	interB := newTestCert(t, root, certOpts{cn: "Line B", ca: true})
	leaf := newTestCert(t, interA, certOpts{cn: "SN-1"})
	all := &x509trust.Verified{Leaf: leaf.cert, Chains: [][]*x509.Certificate{
		{leaf.cert, interA.cert, root.cert},
		{leaf.cert, interB.cert, root.cert}, // the dropped path
	}}
	survivor := &x509trust.Verified{Leaf: leaf.cert, Chains: [][]*x509.Certificate{all.Chains[0]}}
	survivor.Chain = survivor.Chains[0]
	revokedB := interB.cert
	script := &scriptedOcsp{by: func(tg x509trust.OCSPTarget) (x509trust.OCSPOutcome, ocspclient.Reason) {
		if tg.Cert == revokedB {
			return x509trust.OCSPRevoked, ""
		}
		return x509trust.OCSPGood, ""
	}}
	resp := (&CalloutResponder{}).WithOcsp(true, script, nil)
	anchor := &model.DeviceTrustAnchor{}
	anchor.OcspMode.String, anchor.OcspMode.Valid = string(x509trust.OCSPEnforceKnown), true

	_, refusal := resp.applyOcsp(context.Background(), "acme", anchor, all, survivor)
	require.Equal(t, x509ResultOcspRevoked, refusal, "a revoked intermediate on the dropped path must refuse")
	require.Len(t, script.calls[0].Targets, 4, "every certificate on every verified path is asked about")

	// Control: nothing revoked, the survivor is admitted.
	revokedB = nil
	got, refusal := resp.applyOcsp(context.Background(), "acme", anchor, all, survivor)
	require.Empty(t, refusal)
	require.NotNil(t, got)
}

// REVIEW NIT. The reason a REQUIRE_CURRENT refusal is logged with is the FIRST unanswered target in
// path order (the leaf here), every time: never whichever one a map iteration happens to visit.
func TestOcspUnavailableLogsTheLeafsReasonFirst(t *testing.T) {
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	inter := newTestCert(t, root, certOpts{cn: "Line A", ca: true})
	leaf := newTestCert(t, inter, certOpts{cn: "SN-1"})
	v := &x509trust.Verified{Leaf: leaf.cert, Chains: [][]*x509.Certificate{{leaf.cert, inter.cert, root.cert}}}
	v.Chain = v.Chains[0]
	script := &scriptedOcsp{by: func(tg x509trust.OCSPTarget) (x509trust.OCSPOutcome, ocspclient.Reason) {
		if tg.Cert == leaf.cert {
			return x509trust.OCSPNoAnswer, ocspclient.ReasonTimeout
		}
		return x509trust.OCSPNoAnswer, ocspclient.ReasonBreakerOpen
	}}
	anchor := &model.DeviceTrustAnchor{}
	anchor.OcspMode.String, anchor.OcspMode.Valid = string(x509trust.OCSPRequireCurrent), true

	var buf bytes.Buffer
	saved := log.Logger
	log.Logger = zerolog.New(&buf)
	defer func() { log.Logger = saved }()
	for i := 0; i < 40; i++ {
		resp := (&CalloutResponder{now: time.Now}).WithOcsp(true, script, nil)
		_, refusal := resp.applyOcsp(context.Background(), "acme", anchor, v, v)
		require.Equal(t, x509ResultOcspUnavailable, refusal)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 40)
	for _, l := range lines {
		require.Contains(t, l, `"reason":"timeout"`, "logged reason must be the leaf's")
	}
}
