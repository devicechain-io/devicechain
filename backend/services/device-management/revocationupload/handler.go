// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package revocationupload is the streaming CRL upload endpoint:
//
//	PUT /trust-anchors/{token}/crl
//
// served on device-management's own mux (the API Ingress forwards /api/device-management/...
// with the prefix stripped). The body is the CRL, DER or PEM. The GraphQL mutation carries the
// same operation for lists up to its (tighter) size bound; this endpoint takes lists up to the
// instance's callout.revocation.maxCrlBytes and is what dcctl uses.
//
// The order of the handler is the design, and each step is there because the one before it must
// not have cost anything (the artifact upload in update-management is the pattern):
//
//  1. Authenticate, authorize and look the trust anchor up BEFORE the body is touched. A caller
//     without a token, without device:write, or naming an anchor that is not theirs is answered
//     without a byte of its body being read or buffered.
//  2. Refuse a declared Content-Length over the bound, again before the body is read.
//  3. Take one of the replica's write slots. None free is a 503 with Retry-After, with the
//     connection closed, so the body on its way is neither read nor drained.
//  4. Read the body through a reader that counts as it goes and fails the moment the body
//     would pass the bound, with an idle deadline re-armed on every read and a deadline on the
//     whole. A Content-Length that lies, or none at all (chunked), therefore cannot get more
//     than the bound plus one byte into memory.
//  5. Hand the bytes to the same validator and writer the GraphQL mutation uses
//     (model.Api.PutDeviceRevocationList): signature, issuer chain, every acceptance rule, the
//     CRL-number high-water mark under the anchor and issuer locks.
//
// The CRL signer's certificate chain (empty when the anchor itself signed) travels in repeated
// X-Issuer-Certificate headers, one base64 DER certificate each, the signer first.
package revocationupload

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Path is the route pattern the handler is registered under.
const Path = "PUT /trust-anchors/{token}/crl"

// IssuerCertificateHeader carries one certificate of the CRL signer's chain, base64 DER.
const IssuerCertificateHeader = "X-Issuer-Certificate"

const (
	// idleTimeout is how long the connection may go without delivering a byte before the upload
	// is abandoned. It is re-armed on every read.
	idleTimeout = 30 * time.Second
	// totalTimeout bounds the whole read of the body.
	totalTimeout = 5 * time.Minute
	// minRate is the throughput floor of a body that has been streaming for a while: a sender that
	// averages less than this over a whole rateWindow (after the first window, which is the warm-up)
	// is abandoned, so one slow sender cannot hold a write slot for the whole-upload deadline.
	// 16 KiB/s moves the largest default CRL in about two minutes.
	minRate    = 16 << 10
	rateWindow = 30 * time.Second
	// maxIssuerCertHeaders and maxIssuerCertHeaderBytes bound the chain headers: a signer and the
	// CAs up to the anchor, each a certificate.
	maxIssuerCertHeaders     = x509trust.MaxPresentedCerts - 1
	maxIssuerCertHeaderBytes = 8 << 10
)

// ValidatorSource yields the live token validator, bound late; nil means authentication is not
// available yet and every request is refused.
type ValidatorSource func() *auth.Validator

// PolicySource yields the token-audience policy, read per request. A nil source, or one that
// returns nil, is the CLOSED policy.
type PolicySource func() *auth.PlanePolicy

// Handler serves the CRL upload endpoint.
type Handler struct {
	api       *model.Api
	validator ValidatorSource
	policy    PolicySource
	// tenantDeleted reports a tenant that has been through the delete door; nil means none.
	tenantDeleted func(tenant string) bool
	// idle is the per-read stall allowance and total the whole-body allowance; fields so a test
	// can shorten them.
	idle  time.Duration
	total time.Duration
	// minRate and rateWindow are the throughput floor; fields so a test can shorten them.
	minRate    int64
	rateWindow time.Duration
	// slots bounds the uploads this process works on at once.
	slots chan struct{}
}

// New builds the handler. The number of slots is the api's MaxConcurrentWrites, so the handler
// never waits for the model's own slot behind another REST upload.
func New(api *model.Api, validator ValidatorSource, policy PolicySource, tenantDeleted func(string) bool) *Handler {
	return &Handler{api: api, validator: validator, policy: policy, tenantDeleted: tenantDeleted,
		idle: idleTimeout, total: totalTimeout, minRate: minRate, rateWindow: rateWindow, slots: make(chan struct{}, api.RevocationLimits().MaxConcurrentWrites)}
}

// Register mounts the handler on mux.
func Register(mux *http.ServeMux, h *Handler) { mux.Handle(Path, h) }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Authenticate. Nothing below this block runs for an unauthenticated caller, and nothing
	// in it reads the body.
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	ctx := auth.WithClaims(r.Context(), claims)
	ctx = core.WithTenant(ctx, claims.Tenant)
	// A customer-scoped membership is refused here, as it is for the mutation.
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		refuse(w, r, http.StatusForbidden, "forbidden")
		return
	}
	if h.tenantDeleted != nil && h.tenantDeleted(claims.Tenant) {
		refuse(w, r, http.StatusForbidden, "this tenant has been deleted")
		return
	}
	token := r.PathValue("token")
	if err := core.ValidateToken(token); err != nil {
		refuse(w, r, http.StatusBadRequest, "invalid trust anchor token")
		return
	}
	found, err := h.api.DeviceTrustAnchorsByToken(ctx, []string{token})
	if err != nil {
		log.Error().Err(err).Msg("trust anchor lookup for a CRL upload failed")
		refuse(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	if len(found) == 0 {
		refuse(w, r, http.StatusNotFound, "no such trust anchor")
		return
	}
	chainPem, status, msg := issuerChainPem(r.Header.Values(IssuerCertificateHeader))
	if status != 0 {
		refuse(w, r, status, msg)
		return
	}

	// 2. A declared length is checked before the first byte is read. -1 (unknown, e.g. chunked) is
	// allowed and enforced during the stream instead.
	maxBytes := int64(h.api.RevocationLimits().MaxCrlBytes)
	if r.ContentLength > maxBytes {
		refuse(w, r, http.StatusRequestEntityTooLarge, "the CRL is larger than the "+strconv.FormatInt(maxBytes, 10)+" byte limit")
		return
	}
	if r.ContentLength == 0 {
		refuse(w, r, http.StatusBadRequest, "the body is empty")
		return
	}

	// 3. Admission, still before the body.
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "5")
		refuse(w, r, http.StatusServiceUnavailable, "too many CRL uploads are in progress; retry shortly")
		return
	}

	// 4. Stream into memory, bounded.
	buf := &bytes.Buffer{}
	if r.ContentLength > 0 {
		buf.Grow(int(r.ContentLength))
	}
	cr := &capReader{src: r.Body, max: maxBytes, ctx: r.Context(), rc: http.NewResponseController(w),
		idle: h.idle, deadline: time.Now().Add(h.total),
		minRate: h.minRate, window: h.rateWindow, winStart: time.Now()}
	if _, err := io.Copy(buf, cr); err != nil {
		switch {
		case errors.Is(err, errBodyTooLong):
			refuse(w, r, http.StatusRequestEntityTooLarge, "the CRL is larger than the "+strconv.FormatInt(maxBytes, 10)+" byte limit")
		case errors.Is(err, errDeadline), errors.Is(err, errTooSlow), isTimeout(err):
			refuse(w, r, http.StatusRequestTimeout, "the upload stalled or ran too slowly")
		default:
			refuse(w, r, http.StatusBadRequest, "the body could not be read")
		}
		return
	}

	// 5. Validate and store, exactly as the mutation does.
	res, err := h.api.PutDeviceRevocationList(ctx, &model.RevocationListPutRequest{
		AnchorToken: token, Crl: buf.Bytes(), IssuerChainPem: chainPem,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuerKey":    res.List.IssuerKey,
		"issuerName":   res.List.IssuerName,
		"crlNumber":    res.List.CrlNumber,
		"thisUpdate":   res.List.ThisUpdate.UTC().Format(time.RFC3339),
		"nextUpdate":   res.List.NextUpdate.UTC().Format(time.RFC3339),
		"entryCount":   res.List.EntryCount,
		"crlSha256":    res.List.CrlSha256,
		"newlyRevoked": res.NewlyRevoked,
		"unchanged":    res.Unchanged,
	})
}

// fail answers an error of the store.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var lim *limit.Error
	switch class, refused := integrity.Refused(err); {
	case errors.Is(err, gorm.ErrRecordNotFound):
		refuse(w, r, http.StatusNotFound, "no such trust anchor")
	case refused && class == integrity.ClassInvalid:
		refuse(w, r, http.StatusUnprocessableEntity, err.Error())
	case errors.As(err, &lim):
		refuse(w, r, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		refuse(w, r, http.StatusServiceUnavailable, "the upload was interrupted; retry")
	default:
		log.Error().Err(err).Msg("storing a revocation list failed")
		refuse(w, r, http.StatusInternalServerError, "internal error")
	}
}

// issuerChainPem turns the chain headers into the PEM the store takes. A non-zero status is the
// refusal. Each header is one base64 DER certificate, bounded, and at most maxIssuerCertHeaders of
// them.
func issuerChainPem(values []string) (string, int, string) {
	if len(values) > maxIssuerCertHeaders {
		return "", http.StatusBadRequest, "at most " + strconv.Itoa(maxIssuerCertHeaders) + " " + IssuerCertificateHeader + " headers"
	}
	var out strings.Builder
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) > maxIssuerCertHeaderBytes {
			return "", http.StatusBadRequest, IssuerCertificateHeader + " is too large"
		}
		der, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(der) == 0 {
			return "", http.StatusBadRequest, IssuerCertificateHeader + " must be a base64 DER certificate"
		}
		out.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	return out.String(), 0, ""
}

// authenticate verifies a tenant ACCESS token (never a service token) and returns its claims,
// writing the refusal itself on any failure.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	const prefix = "Bearer "
	authz := r.Header.Get("Authorization")
	if len(authz) <= len(prefix) || !strings.EqualFold(authz[:len(prefix)], prefix) {
		refuse(w, r, http.StatusUnauthorized, "missing bearer token")
		return nil, false
	}
	var v *auth.Validator
	if h.validator != nil {
		v = h.validator()
	}
	if v == nil {
		refuse(w, r, http.StatusUnauthorized, "authentication is not available")
		return nil, false
	}
	claims, err := v.Validate(authz[len(prefix):])
	if err != nil {
		refuse(w, r, http.StatusUnauthorized, "invalid token")
		return nil, false
	}
	// 🔴 AFTER Validate, NEVER BEFORE: the audience rule runs on a token whose signature, expiry
	// and type were verified, so a forged token never reaches it. A refusal is the same 401 an
	// invalid token gets.
	var p *auth.PlanePolicy
	if h.policy != nil {
		p = h.policy()
	}
	if err := p.Admit(claims, auth.PlaneUpload); err != nil {
		refuse(w, r, http.StatusUnauthorized, "invalid token")
		return nil, false
	}
	if claims.Tenant == "" {
		refuse(w, r, http.StatusUnauthorized, "invalid token")
		return nil, false
	}
	return claims, true
}

// refuse answers without reading the request body and closes the connection when there is one, so
// the server neither buffers nor drains what the caller was about to send.
func refuse(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if r.ContentLength != 0 {
		w.Header().Set("Connection", "close")
	}
	http.Error(w, msg, status)
}

var (
	errBodyTooLong = errors.New("revocationupload: body is longer than the limit")
	errDeadline    = errors.New("revocationupload: whole-upload deadline passed")
	errTooSlow     = errors.New("revocationupload: the body arrives below the minimum rate")
)

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// capReader passes the body through while counting it, and fails as soon as the body would exceed
// max: it never reads more than one byte past the limit, so an over-long or lying body is cut
// there and the surplus never reaches memory.
type capReader struct {
	src      io.Reader
	n        int64
	max      int64
	ctx      context.Context
	rc       *http.ResponseController
	idle     time.Duration
	deadline time.Time
	// The throughput floor: every window, the bytes that arrived in it must average at least minRate
	// a second, except in the first (warm-up) window. A zero window disables it.
	minRate  int64
	window   time.Duration
	winStart time.Time
	winBase  int64
	winsDone int
}

func (c *capReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	now := time.Now()
	if now.After(c.deadline) {
		return 0, errDeadline
	}
	if c.window > 0 && now.Sub(c.winStart) >= c.window {
		elapsed := now.Sub(c.winStart).Seconds()
		if c.winsDone > 0 && float64(c.n-c.winBase) < float64(c.minRate)*elapsed {
			return 0, errTooSlow
		}
		c.winStart, c.winBase = now, c.n
		c.winsDone++
	}
	d := now.Add(c.idle)
	if d.After(c.deadline) {
		d = c.deadline
	}
	_ = c.rc.SetReadDeadline(d)
	if room := c.max - c.n + 1; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := c.src.Read(p)
	if n > 0 {
		c.n += int64(n)
		if c.n > c.max {
			return 0, errBodyTooLong
		}
	}
	if err == io.EOF {
		_ = c.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}
