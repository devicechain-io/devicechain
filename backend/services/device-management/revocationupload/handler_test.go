// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package revocationupload

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/require"
)

// pki is a certificate and its key.
type pki struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

var serial int64 = time.Now().UnixNano()

func newCA(t testing.TB, parent *pki, cn string) *pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serial++
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, MaxPathLen: -1,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	issuer, signer := tmpl, key
	if parent != nil {
		issuer, signer = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, &key.PublicKey, signer)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &pki{cert: cert, key: key}
}

func (p *pki) pem() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.cert.Raw}))
}

func (p *pki) crl(t testing.TB, number int64, revoked ...*big.Int) []byte {
	t.Helper()
	tmpl := &x509.RevocationList{Number: big.NewInt(number), ThisUpdate: time.Now().Add(-time.Hour), NextUpdate: time.Now().Add(72 * time.Hour)}
	for _, s := range revoked {
		tmpl.RevokedCertificateEntries = append(tmpl.RevokedCertificateEntries, x509.RevocationListEntry{SerialNumber: s, RevocationTime: time.Now().Add(-time.Hour)})
	}
	der, err := x509.CreateRevocationList(rand.Reader, tmpl, p.cert, p.key)
	require.NoError(t, err)
	return der
}

type env struct {
	t       *testing.T
	api     *model.Api
	issuer  *auth.Issuer
	val     *auth.Validator
	srv     *httptest.Server
	h       *Handler
	ca      *pki
	deleted map[string]bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := putest.NewSQLiteDB(t, &model.DeviceTrustAnchor{}, &model.DeviceCredential{}, &model.PendingSessionKick{}, &model.PendingRevocationKick{}, &model.PendingRevocationSerial{},
		&model.DeviceRevocationIssuer{}, &model.DeviceRevocationList{}, &model.DeviceRevokedCertificate{}, &model.DeviceCredentialChainLink{})
	api := model.NewApi(&rdb.RdbManager{Database: db})
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	e := &env{t: t, api: api, issuer: auth.NewIssuer(key, "https://as.example.com", time.Hour, time.Hour),
		val: auth.NewValidator(&key.PublicKey), ca: newCA(t, nil, "Maker Root"), deleted: map[string]bool{}}
	e.h = New(api, func() *auth.Validator { return e.val }, nil, func(tenant string) bool { return e.deleted[tenant] })
	mux := http.NewServeMux()
	Register(mux, e.h)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	for _, tenant := range []string{"acme", "globex"} {
		_, err := api.CreateDeviceTrustAnchor(ctxFor(tenant), &model.DeviceTrustAnchorCreateRequest{Token: "maker", CertificatePem: e.ca.pem()})
		require.NoError(t, err)
	}
	return e
}

func ctxFor(tenant string) context.Context { return core.WithTenant(context.Background(), tenant) }

func (e *env) token(tenant string, authorities ...auth.Authority) string {
	as := make([]string, len(authorities))
	for i, a := range authorities {
		as[i] = string(a)
	}
	tok, err := e.issuer.IssueTenantAccess(tenant, "a@b.c", nil, as, false, "j-"+tenant)
	require.NoError(e.t, err)
	return tok.Token
}

func (e *env) writer(tenant string) string { return e.token(tenant, auth.DeviceWrite) }

func (e *env) url(anchor string) string { return e.srv.URL + "/trust-anchors/" + anchor + "/crl" }

func (e *env) put(bearer, anchor string, body io.Reader, headers ...string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPut, e.url(anchor), body)
	require.NoError(e.t, err)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (e *env) counts(tenant string) (lists, entries int64) {
	e.t.Helper()
	db := e.api.RDB.DB(ctxFor(tenant))
	require.NoError(e.t, db.Model(&model.DeviceRevocationList{}).Count(&lists).Error)
	require.NoError(e.t, db.Model(&model.DeviceRevokedCertificate{}).Count(&entries).Error)
	return
}

func requireStatus(t *testing.T, resp *http.Response, want int) string {
	t.Helper()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, want, resp.StatusCode, string(body))
	return string(body)
}

func TestPutStoresACrlGivenAsPemOrDer(t *testing.T) {
	e := newEnv(t)
	der := e.ca.crl(t, 1, big.NewInt(1001), big.NewInt(1002))

	resp := e.put(e.writer("acme"), "maker", bytes.NewReader(der))
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(requireStatus(t, resp, 200)), &got))
	require.Equal(t, "1", got["crlNumber"])
	require.EqualValues(t, 2, got["entryCount"])
	require.EqualValues(t, 2, got["newlyRevoked"])
	require.Equal(t, false, got["unchanged"])
	lists, entries := e.counts("acme")
	require.EqualValues(t, 1, lists)
	require.EqualValues(t, 2, entries)

	pemBody := pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: e.ca.crl(t, 2, big.NewInt(1001))})
	resp = e.put(e.writer("acme"), "maker", bytes.NewReader(pemBody))
	require.NoError(t, json.Unmarshal([]byte(requireStatus(t, resp, 200)), &got))
	require.EqualValues(t, 0, got["newlyRevoked"])
	lists, entries = e.counts("acme")
	require.EqualValues(t, 1, lists, "replaced in place")
	require.EqualValues(t, 1, entries)

	l, ent := e.counts("globex")
	require.Zero(t, l+ent, "another tenant's anchor of the same CA was not touched")
}

func TestPutAcceptsAChunkedBody(t *testing.T) {
	e := newEnv(t)
	der := e.ca.crl(t, 1, big.NewInt(1001))
	resp := e.put(e.writer("acme"), "maker", io.MultiReader(bytes.NewReader(der)))
	requireStatus(t, resp, 200)
}

func TestPutWithAnIssuerChainInHeaders(t *testing.T) {
	e := newEnv(t)
	mid := newCA(t, e.ca, "Mid CA")
	low := newCA(t, mid, "Low CA")
	der := low.crl(t, 1, big.NewInt(77))
	hdr := func(c *pki) string { return base64.StdEncoding.EncodeToString(c.cert.Raw) }

	requireStatus(t, e.put(e.writer("acme"), "maker", bytes.NewReader(der)), 422)
	lists, _ := e.counts("acme")
	require.Zero(t, lists, "the anchor cannot vouch for a CA two levels down without the chain")

	requireStatus(t, e.put(e.writer("acme"), "maker", bytes.NewReader(der),
		IssuerCertificateHeader, hdr(low), IssuerCertificateHeader, hdr(mid)), 200)
	lists, entries := e.counts("acme")
	require.EqualValues(t, 1, lists)
	require.EqualValues(t, 1, entries)

	t.Run("a certificate header that is not a certificate is the store's refusal", func(t *testing.T) {
		resp := e.put(e.writer("acme"), "maker", bytes.NewReader(e.ca.crl(t, 9)),
			IssuerCertificateHeader, base64.StdEncoding.EncodeToString([]byte("hello")))
		requireStatus(t, resp, 422)
	})
}

// 🔑 THE CHAIN HEADERS ARE JUDGED BEFORE THE BODY IS READ, and each cap is exactly a 400 (a mutant
// that widens one reaches the body and the panicking reader says so).
func TestIssuerChainHeaderCapsAreRefusedBeforeTheBodyIsRead(t *testing.T) {
	e := newEnv(t)
	for name, headers := range map[string][]string{
		"not base64":       {"***"},
		"empty":            {""},
		"whitespace":       {"   "},
		"oversized":        {strings.Repeat("A", 8<<10+4)}, // a literal: the cap under test must not size its own probe
		"one over the cap": manyHeaderValues(x509trust.MaxPresentedCerts),
		"too many":         manyHeaderValues(x509trust.MaxPresentedCerts + 20), // well past the cap, and under any widened one
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/trust-anchors/x/crl", panicBody{t})
			req.SetPathValue("token", "maker")
			req.ContentLength = 100
			req.Header.Set("Authorization", "Bearer "+e.writer("acme"))
			for _, h := range headers {
				req.Header.Add(IssuerCertificateHeader, h)
			}
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			lists, entries := e.counts("acme")
			require.Zero(t, lists+entries)
		})
	}
	t.Run("the control: exactly the cap is not refused for its count", func(t *testing.T) {
		_, status, _ := issuerChainPem(manyHeaderValues(maxIssuerCertHeaders))
		require.Zero(t, status)
		_, status, _ = issuerChainPem([]string{strings.Repeat("A", 8<<10)})
		require.Zero(t, status)
	})
}

func manyHeaderValues(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "AAAA"
	}
	return out
}

// 🔑 THE ORDER IS THE DESIGN: every refusal below happens WITHOUT the body being read.
type panicBody struct{ t *testing.T }

func (b panicBody) Read([]byte) (int, error) {
	b.t.Helper()
	b.t.Error("the request body was read before the request was authorized")
	return 0, io.EOF
}
func (panicBody) Close() error { return nil }

func TestRefusalsHappenWithoutReadingTheBody(t *testing.T) {
	e := newEnv(t)
	svc, err := e.issuer.IssueService("some-service", []string{string(auth.DeviceWrite)}, "j-svc")
	require.NoError(t, err)
	scope, err := auth.NewScope([]string{"customer:some-customer"})
	require.NoError(t, err)
	scoped, err := e.issuer.IssueScopedTenantAccess("acme", "a@b.c", nil, []string{string(auth.DeviceWrite)}, scope, "j-scp")
	require.NoError(t, err)
	e.deleted["initech"] = true

	for _, tc := range []struct {
		name   string
		bearer string
		anchor string
		cl     int64
		want   int
	}{
		{"no token", "", "maker", 100, http.StatusUnauthorized},
		{"garbage token", "not.a.jwt", "maker", 100, http.StatusUnauthorized},
		{"service token", svc.Token, "maker", 100, http.StatusUnauthorized},
		{"no authority", e.token("acme"), "maker", 100, http.StatusForbidden},
		{"read-only authority", e.token("acme", auth.DeviceRead), "maker", 100, http.StatusForbidden},
		{"customer-scoped membership", scoped.Token, "maker", 100, http.StatusForbidden},
		{"deleted tenant", e.writer("initech"), "maker", 100, http.StatusForbidden},
		{"unknown anchor", e.writer("acme"), "ghost", 100, http.StatusNotFound},
		{"another tenant's anchor", e.writer("hooli"), "maker", 100, http.StatusNotFound},
		{"bad token grammar", e.writer("acme"), "Not A Token!", 100, http.StatusBadRequest},
		{"content-length too large", e.writer("acme"), "maker", 1 << 30, http.StatusRequestEntityTooLarge},
		{"content-length just over the bound", e.writer("acme"), "maker", int64(e.api.RevocationLimits().MaxCrlBytes) + 1, http.StatusRequestEntityTooLarge},
		{"empty body", e.writer("acme"), "maker", 0, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/trust-anchors/x/crl", panicBody{t})
			req.SetPathValue("token", tc.anchor)
			req.ContentLength = tc.cl
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			if tc.cl != 0 {
				require.Equal(t, "close", rec.Header().Get("Connection"), "an unread body must not be drained")
			}
			lists, entries := e.counts("acme")
			require.Zero(t, lists+entries)
		})
	}
	t.Run("a bad chain header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/trust-anchors/maker/crl", panicBody{t})
		req.SetPathValue("token", "maker")
		req.ContentLength = 100
		req.Header.Set("Authorization", "Bearer "+e.writer("acme"))
		req.Header.Set(IssuerCertificateHeader, "***")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

// rawPut sends a PUT with exactly the given Content-Length line and body bytes and then goes
// quiet, returning the response the server gives without being sent the rest.
func rawPut(t *testing.T, srv *httptest.Server, anchor, bearer, lengthHeader string, body []byte) int {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	var sb strings.Builder
	fmt.Fprintf(&sb, "PUT /trust-anchors/%s/crl HTTP/1.1\r\nHost: t\r\n", anchor)
	if bearer != "" {
		fmt.Fprintf(&sb, "Authorization: Bearer %s\r\n", bearer)
	}
	fmt.Fprintf(&sb, "%s\r\n\r\n", lengthHeader)
	_, err = conn.Write(append([]byte(sb.String()), body...))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err, "no response: the server is waiting on the body")
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestUnauthenticatedAndUnauthorizedStalledUploadsAreAnsweredAtOnce(t *testing.T) {
	e := newEnv(t)
	for name, tc := range map[string]struct {
		bearer string
		want   int
	}{
		"unauthenticated": {"", http.StatusUnauthorized},
		"unauthorized":    {e.token("acme", auth.DeviceRead), http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			got := rawPut(t, e.srv, "maker", tc.bearer, "Content-Length: 5000", nil)
			require.Less(t, time.Since(started), 2*time.Second, "the server waited on a body it should never read")
			require.Equal(t, tc.want, got)
		})
	}
}

func TestAnAuthorizedStalledUploadIsAbandonedAndStoresNothing(t *testing.T) {
	e := newEnv(t)
	e.h.idle = 150 * time.Millisecond
	started := time.Now()
	got := rawPut(t, e.srv, "maker", e.writer("acme"), "Content-Length: 5000", []byte("0123456789"))
	require.Less(t, time.Since(started), 3*time.Second)
	require.Equal(t, http.StatusRequestTimeout, got)
	lists, entries := e.counts("acme")
	require.Zero(t, lists+entries)
}

// 🔑 THE SIZE CAP APPLIES DURING THE STREAM: a body with no declared length, or a lying one, is cut
// at the bound plus one byte and never reaches the parser.
func TestAChunkedBodyPastTheBoundIsCutAndStoresNothing(t *testing.T) {
	e := newEnv(t)
	lim := model.DefaultRevocationLimits()
	lim.MaxCrlBytes = 64 << 10
	lim.MaxGraphqlCrlBytes = 64 << 10
	require.NoError(t, e.api.SetRevocationLimits(lim))

	src := &endless{}
	req, err := http.NewRequest(http.MethodPut, e.url("maker"), io.LimitReader(src, 8<<20))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+e.writer("acme"))
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	} else {
		// The server may close the connection before the client finishes sending.
		require.True(t, strings.Contains(err.Error(), "reset") || strings.Contains(err.Error(), "broken pipe") ||
			strings.Contains(err.Error(), "aborted") || strings.Contains(err.Error(), "EOF"), "%v", err)
	}
	lists, entries := e.counts("acme")
	require.Zero(t, lists+entries)
}

// The cap reader never pulls more than one byte past the limit from its source, however much the
// source could supply.
func TestCapReaderConsumesAtMostLimitPlusOne(t *testing.T) {
	src := &endless{}
	cr := &capReader{src: src, max: 1000, ctx: context.Background(), rc: http.NewResponseController(httptest.NewRecorder()),
		idle: time.Second, deadline: time.Now().Add(time.Minute)}
	_, err := io.Copy(io.Discard, cr)
	require.ErrorIs(t, err, errBodyTooLong)
	require.EqualValues(t, 1001, src.served, "exactly max+1 bytes are read before the stream is cut")
}

func TestCapReaderStopsAtTheWholeUploadDeadline(t *testing.T) {
	cr := &capReader{src: &endless{}, max: 1 << 30, ctx: context.Background(), rc: http.NewResponseController(httptest.NewRecorder()),
		idle: time.Second, deadline: time.Now().Add(-time.Second)}
	_, err := io.Copy(io.Discard, cr)
	require.ErrorIs(t, err, errDeadline)
}

type endless struct{ served int64 }

func (e *endless) Read(p []byte) (int, error) {
	e.served += int64(len(p))
	return len(p), nil
}

// A declared length that is honest but over the bound is refused before the body is read; one
// that is under the bound but whose body is longer is cut by the stream.
func TestALyingContentLengthIsCutByTheStream(t *testing.T) {
	e := newEnv(t)
	lim := model.DefaultRevocationLimits()
	lim.MaxCrlBytes = 64 << 10
	lim.MaxGraphqlCrlBytes = 64 << 10
	require.NoError(t, e.api.SetRevocationLimits(lim))
	got := rawPut(t, e.srv, "maker", e.writer("acme"), "Content-Length: 100",
		bytes.Repeat([]byte("a"), 100))
	require.Equal(t, http.StatusUnprocessableEntity, got, "an honest 100-byte body that is not a CRL is refused by the parser, not the cap")
}

func TestAdmissionPastTheSlotLimitIsRefusedBeforeTheBodyIsRead(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < cap(e.h.slots); i++ {
		e.h.slots <- struct{}{}
	}
	req := httptest.NewRequest(http.MethodPut, "/trust-anchors/maker/crl", panicBody{t})
	req.SetPathValue("token", "maker")
	req.ContentLength = 100
	req.Header.Set("Authorization", "Bearer "+e.writer("acme"))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "5", rec.Header().Get("Retry-After"))
	require.Equal(t, "close", rec.Header().Get("Connection"))
	<-e.h.slots
	resp := e.put(e.writer("acme"), "maker", bytes.NewReader(e.ca.crl(t, 1)))
	requireStatus(t, resp, 200) // the control: with a slot free it is accepted
}

func TestSlotsAreReleasedAfterEachUpload(t *testing.T) {
	e := newEnv(t)
	for i := int64(1); i <= int64(cap(e.h.slots))*3; i++ {
		requireStatus(t, e.put(e.writer("acme"), "maker", bytes.NewReader(e.ca.crl(t, i))), 200)
	}
	require.Zero(t, len(e.h.slots))
}

func TestStoreRefusalsAreMappedToStatuses(t *testing.T) {
	e := newEnv(t)
	requireStatus(t, e.put(e.writer("acme"), "maker", bytes.NewReader(e.ca.crl(t, 5))), 200)
	body := requireStatus(t, e.put(e.writer("acme"), "maker", bytes.NewReader(e.ca.crl(t, 4))), 422)
	require.Contains(t, body, model.RuleCrlNumberNotNewer, "the refusal names the rule")
	stranger := newCA(t, nil, "Stranger")
	body = requireStatus(t, e.put(e.writer("acme"), "maker", bytes.NewReader(stranger.crl(t, 9))), 422)
	require.Contains(t, body, "wrong_issuer")
	requireStatus(t, e.put(e.writer("acme"), "maker", strings.NewReader("garbage")), 422)
}

func TestOtherMethodsAreNotRouted(t *testing.T) {
	e := newEnv(t)
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		req, _ := http.NewRequest(m, e.url("maker"), nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Contains(t, []int{404, 405}, resp.StatusCode, m)
	}
}

func TestNoValidatorYetIsRefused(t *testing.T) {
	e := newEnv(t)
	h := New(e.api, func() *auth.Validator { return nil }, nil, nil)
	req := httptest.NewRequest(http.MethodPut, "/trust-anchors/maker/crl", panicBody{t})
	req.SetPathValue("token", "maker")
	req.ContentLength = 10
	req.Header.Set("Authorization", "Bearer "+e.writer("acme"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// 🔑 THE AUDIENCE RULE: a token minted for another resource is the same 401 as an invalid one, and
// the body is not read. A nil policy is the closed one, so a wiring that forgot it refuses.
func TestATokenForAnotherAudienceIsRefusedBeforeTheBodyIsRead(t *testing.T) {
	e := newEnv(t)
	tok, err := e.issuer.IssueOAuthAccess("acme", "a@b.c", nil, []string{string(auth.DeviceWrite)}, "scope",
		[]string{"https://elsewhere.example.com/resource"}, false, "client", "j-aud")
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/trust-anchors/x/crl", panicBody{t})
	req.SetPathValue("token", "maker")
	req.ContentLength = 100
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	lists, entries := e.counts("acme")
	require.Zero(t, lists+entries)
}

// ─── the throughput floor ───────────────────────────────────────────────────

// trickle yields one byte every gap, for ever.
type trickle struct{ gap time.Duration }

func (tr trickle) Read(p []byte) (int, error) {
	time.Sleep(tr.gap)
	p[0] = 'a'
	return 1, nil
}

// 🔑 A SENDER THAT KEEPS THE CONNECTION ALIVE BUT SLOW is abandoned once a whole window after the
// warm-up averages under the floor, with a 408, so it cannot hold the write slot for the whole-upload
// deadline. The idle timeout never fires: a byte arrives every few milliseconds.
func TestASlowSenderIsAbandonedAndFreesItsSlot(t *testing.T) {
	e := newEnv(t)
	e.h.minRate, e.h.rateWindow = 10<<10, 150*time.Millisecond
	e.h.idle = 5 * time.Second
	req := httptest.NewRequest(http.MethodPut, "/trust-anchors/x/crl", trickle{gap: 10 * time.Millisecond})
	req.SetPathValue("token", "maker")
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+e.writer("acme"))
	rec := httptest.NewRecorder()
	started := time.Now()
	e.h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusRequestTimeout, rec.Code, rec.Body.String())
	require.Less(t, time.Since(started), 3*time.Second, "abandoned after about two windows, not at the whole-upload deadline")
	require.Zero(t, len(e.h.slots), "the slot is free again")
	lists, entries := e.counts("acme")
	require.Zero(t, lists+entries)
}

func TestTheThroughputFloorSparesAFastSenderAndTheWarmUpWindow(t *testing.T) {
	newReader := func(src io.Reader) *capReader {
		return &capReader{src: src, max: 1 << 30, ctx: context.Background(), rc: http.NewResponseController(httptest.NewRecorder()),
			idle: time.Minute, deadline: time.Now().Add(time.Minute), minRate: 1 << 10, window: 100 * time.Millisecond, winStart: time.Now()}
	}
	t.Run("a fast sender passes every window", func(t *testing.T) {
		cr := newReader(io.LimitReader(&endless{}, 8<<20))
		for i := 0; i < 20; i++ {
			_, err := io.CopyN(io.Discard, cr, 256<<10)
			require.NoError(t, err)
			time.Sleep(20 * time.Millisecond)
		}
	})
	t.Run("a slow first window is the warm-up", func(t *testing.T) {
		cr := newReader(trickle{gap: 5 * time.Millisecond})
		deadline := time.Now().Add(120 * time.Millisecond) // inside the first window plus a little
		var err error
		buf := make([]byte, 1)
		for time.Now().Before(deadline) && err == nil {
			_, err = cr.Read(buf)
		}
		require.NoError(t, err, "the first window is not judged")
	})
	t.Run("a slow second window is judged", func(t *testing.T) {
		cr := newReader(trickle{gap: 5 * time.Millisecond})
		var err error
		buf := make([]byte, 1)
		for i := 0; i < 200 && err == nil; i++ {
			_, err = cr.Read(buf)
		}
		require.ErrorIs(t, err, errTooSlow)
	})
	t.Run("a disabled window never judges", func(t *testing.T) {
		cr := newReader(trickle{gap: 5 * time.Millisecond})
		cr.window = 0
		for i := 0; i < 60; i++ {
			_, err := cr.Read(make([]byte, 1))
			require.NoError(t, err)
		}
	})
}
