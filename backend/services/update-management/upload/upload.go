// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package upload is the artifact content endpoint: an authenticated, streaming
// PUT /updates/artifacts/{token}/content that stores an artifact's bytes in the object
// store and verifies them against the size and digest declared when the artifact was
// created.
//
// The order of the handler is the design, and each step is there because the one before
// it must not have cost anything:
//
//  1. Authenticate, authorize and look the artifact up BEFORE the body is touched. A
//     caller without a token, without artifact:write, or naming an artifact that is not
//     uploadable, is answered without a byte of its body being read or buffered.
//  2. Refuse a declared Content-Length that is not the declared size, again before the
//     body is read.
//  3. Stream the body into the store through a reader that counts and hashes as it goes
//     and fails the moment the body runs past the declared size. A Content-Length that
//     lies (or no Content-Length at all, as with chunked encoding) therefore cannot get
//     more than the declared size plus one byte stored.
//  4. Judge the stored object by what was counted and hashed, never by what was
//     claimed. Any disagreement deletes the object and leaves the artifact UPLOADING.
//  5. Only then flip the artifact to AVAILABLE, with a conditional update, so two
//     concurrent uploads cannot both win.
package upload

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/rs/zerolog/log"
)

// Path is the route pattern the handler is registered under.
const Path = "PUT /updates/artifacts/{token}/content"

const (
	// idleTimeout is how long the connection may go without delivering a byte before the
	// upload is abandoned. It is re-armed on every read, so a large, steady upload is
	// never cut by it while a stalled one is.
	idleTimeout   = 30 * time.Second
	cleanupBudget = 30 * time.Second
)

// ValidatorSource yields the live token validator, which is bound late (the service
// starts before the auth bootstrap succeeds); nil means authentication is not available
// yet and every request is refused.
type ValidatorSource func() *auth.Validator

// Handler serves the artifact content endpoint.
type Handler struct {
	api       *model.Api
	validator ValidatorSource
	// idle is the per-read stall allowance; a field so a test can shorten it.
	idle time.Duration
	// window is the whole-upload allowance for an artifact of a given size; a field so a
	// test can shorten it. The default is model.UploadWindow, which is also what a claim's
	// expiry is derived from, so the two cannot drift apart.
	window func(size int64) time.Duration
	// slots bounds the uploads this process streams at once: one token per upload in flight.
	slots chan struct{}
}

// New builds the handler. api carries the object store and the catalogue, and
// maxConcurrent bounds the uploads this process streams at once (it must be positive:
// an unbounded handler is the thing the bound exists to prevent).
func New(api *model.Api, validator ValidatorSource, maxConcurrent int) *Handler {
	if maxConcurrent <= 0 {
		panic("upload.New: maxConcurrent must be positive")
	}
	return &Handler{api: api, validator: validator, idle: idleTimeout,
		window: model.UploadWindow, slots: make(chan struct{}, maxConcurrent)}
}

// Register mounts the handler on mux.
func Register(mux *http.ServeMux, api *model.Api, validator ValidatorSource, maxConcurrent int) {
	mux.Handle(Path, New(api, validator, maxConcurrent))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Authenticate. Nothing below this block runs for an unauthenticated caller, and
	// nothing in it reads the body.
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	ctx := auth.WithClaims(r.Context(), claims)
	ctx = core.WithTenant(ctx, claims.Tenant)
	if err := auth.Authorize(ctx, auth.ArtifactWrite); err != nil {
		refuse(w, r, http.StatusForbidden, "forbidden")
		return
	}
	if h.api.Blobs == nil {
		refuse(w, r, http.StatusServiceUnavailable, "object store not configured")
		return
	}
	if err := h.api.RefuseDeletedTenant(ctx); err != nil {
		refuse(w, r, http.StatusForbidden, err.Error())
		return
	}
	token := r.PathValue("token")
	if err := core.ValidateToken(token); err != nil {
		refuse(w, r, http.StatusBadRequest, "invalid artifact token")
		return
	}
	artifact, err := h.api.ArtifactRowByToken(ctx, token)
	if err != nil {
		log.Error().Err(err).Msg("artifact lookup for upload failed")
		refuse(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	if artifact == nil {
		refuse(w, r, http.StatusNotFound, "no such artifact")
		return
	}
	if artifact.State != model.StateUploading {
		refuse(w, r, http.StatusConflict, model.ErrNotUploadable(artifact).Error())
		return
	}

	// 2. A declared length is checked before the first byte is read. -1 (unknown, e.g.
	// chunked) is allowed and enforced during the stream instead.
	if r.ContentLength >= 0 && r.ContentLength != artifact.SizeBytes {
		status := http.StatusBadRequest
		if r.ContentLength > artifact.SizeBytes {
			status = http.StatusRequestEntityTooLarge
		}
		refuse(w, r, status, "Content-Length does not match the artifact's declared size of "+
			strconv.FormatInt(artifact.SizeBytes, 10)+" bytes")
		return
	}

	// Admission, still before the body: a bounded number of uploads per process, then one
	// per artifact across every replica. Both refuse with the connection closed, so
	// neither costs a byte of the body that was on its way.
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "5")
		refuse(w, r, http.StatusServiceUnavailable, "too many uploads are in progress; retry shortly")
		return
	}
	if err := h.api.ClaimUpload(ctx, artifact); err != nil {
		var re *model.RefusalError
		if errors.As(err, &re) {
			refuse(w, r, http.StatusConflict, re.Msg)
			return
		}
		log.Error().Err(err).Str("artifact", token).Msg("claiming an artifact upload failed")
		refuse(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	defer h.release(artifact)

	// 3. Stream.
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(h.window(artifact.SizeBytes))
	sr := &streamReader{
		src:      r.Body,
		hash:     sha256.New(),
		max:      artifact.SizeBytes,
		ctx:      r.Context(),
		rc:       rc,
		idle:     h.idle,
		deadline: deadline,
	}
	id, err := randomObjectID()
	if err != nil {
		refuse(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	ref, err := h.api.Blobs.Put(ctx,
		blob.Key{Tenant: claims.Tenant, Purpose: model.BlobPurpose, ID: id},
		sr, blob.PutOptions{ContentType: artifact.MediaType, MaxSize: artifact.SizeBytes})
	if err != nil {
		// Put cleans up after itself on failure, so nothing is stored.
		switch {
		case errors.Is(err, blob.ErrTooLarge) || errors.Is(err, errBodyTooLong):
			refuse(w, r, http.StatusRequestEntityTooLarge, "the body is longer than the artifact's declared size")
		case errors.Is(err, errDeadline), isTimeout(err):
			refuse(w, r, http.StatusRequestTimeout, "the upload stalled or ran too slowly")
		default:
			log.Warn().Err(err).Str("artifact", token).Msg("artifact upload failed")
			refuse(w, r, http.StatusBadRequest, "the body could not be read")
		}
		return
	}

	// 4. Judge what was stored by what was counted and hashed.
	got := model.DigestPrefix + hex.EncodeToString(sr.hash.Sum(nil))
	switch {
	case sr.n != artifact.SizeBytes:
		h.discard(ref, token)
		refuse(w, r, http.StatusBadRequest, "the body was "+strconv.FormatInt(sr.n, 10)+
			" bytes; the artifact declares "+strconv.FormatInt(artifact.SizeBytes, 10))
		return
	case got != artifact.Digest:
		h.discard(ref, token)
		refuse(w, r, http.StatusUnprocessableEntity, "the content's SHA-256 does not match the artifact's declared digest")
		return
	}

	// 5. Commit.
	if err := h.api.CompleteUpload(ctx, artifact, ref); err != nil {
		h.discard(ref, token)
		var re *model.RefusalError
		if errors.As(err, &re) {
			refuse(w, r, http.StatusConflict, re.Msg)
			return
		}
		log.Error().Err(err).Str("artifact", token).Msg("recording a verified artifact upload failed")
		refuse(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"token":     artifact.Token,
		"state":     model.StateAvailable,
		"sizeBytes": strconv.FormatInt(artifact.SizeBytes, 10),
		"digest":    artifact.Digest,
	})
}

// release gives the artifact's upload lease back. It runs on a context detached from the
// request for the same reason discard does: the usual reason to be here with a dead request
// is a client that went away, and the lease must still be freed so a retry is not made to
// wait out its expiry.
func (h *Handler) release(a *model.Artifact) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), cleanupBudget)
	defer cancel()
	ctx = core.WithTenant(ctx, a.TenantId)
	if err := h.api.ReleaseUpload(ctx, a); err != nil {
		log.Warn().Err(err).Str("artifact", a.Token).Msg("could not release an artifact upload lease; it expires on its own")
	}
}

// discard deletes an object that failed verification. It runs on a context detached from
// the request, because the usual reason to be here with a dead request context is a
// client that went away mid-upload, and that is exactly when the object must still go.
// A failed delete is logged loudly: it leaves an unreferenced object nothing will find.
func (h *Handler) discard(ref blob.Ref, token string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), cleanupBudget)
	defer cancel()
	if err := h.api.Blobs.Delete(ctx, ref); err != nil && !errors.Is(err, blob.ErrNotFound) {
		log.Error().Err(err).Str("artifact", token).Str("ref", ref.String()).
			Msg("could not delete an artifact object that failed verification; it is orphaned")
	}
}

// authenticate verifies a tenant ACCESS token (never a service token) and returns its
// claims, writing the refusal itself on any failure.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	const prefix = "Bearer "
	authz := r.Header.Get("Authorization")
	if len(authz) <= len(prefix) || !strings.EqualFold(authz[:len(prefix)], prefix) {
		refuse(w, r, http.StatusUnauthorized, "missing bearer token")
		return nil, false
	}
	v := h.validator()
	if v == nil {
		refuse(w, r, http.StatusUnauthorized, "authentication is not available")
		return nil, false
	}
	claims, err := v.Validate(authz[len(prefix):])
	if err != nil {
		refuse(w, r, http.StatusUnauthorized, "invalid token")
		return nil, false
	}
	return claims, true
}

// refuse answers without reading the request body and closes the connection when there
// is one, so the server neither buffers nor drains what the caller was about to send.
func refuse(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if r.ContentLength != 0 {
		w.Header().Set("Connection", "close")
	}
	http.Error(w, msg, status)
}

func randomObjectID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

var (
	errBodyTooLong = errors.New("upload: body is longer than the declared size")
	errDeadline    = errors.New("upload: whole-upload deadline passed")
)

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// streamReader passes the body through while counting and hashing it. It is the second
// line of the size defence (the store's MaxSize is the third): it fails the read as soon
// as the body would exceed max, so an over-long or lying body is cut at max+1 bytes and
// the surplus is never hashed or stored.
type streamReader struct {
	src      io.Reader
	hash     hash.Hash
	n        int64
	max      int64
	ctx      context.Context
	rc       *http.ResponseController
	idle     time.Duration
	deadline time.Time
}

func (s *streamReader) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	now := time.Now()
	if now.After(s.deadline) {
		return 0, errDeadline
	}
	// Re-arm the idle deadline, never past the whole-upload one. A recorder that cannot
	// set a deadline is served without.
	d := now.Add(s.idle)
	if d.After(s.deadline) {
		d = s.deadline
	}
	_ = s.rc.SetReadDeadline(d)

	// Never read more than one byte past the limit, so the overflow is detected without
	// consuming (or hashing) an unbounded surplus.
	if room := s.max - s.n + 1; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := s.src.Read(p)
	if n > 0 {
		s.n += int64(n)
		if s.n > s.max {
			return 0, errBodyTooLong
		}
		s.hash.Write(p[:n])
	}
	if err == io.EOF {
		_ = s.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}
