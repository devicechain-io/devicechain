// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package download is the artifact download endpoint: GET /updates/download/{artifact}?cap=…,
// the one HTTP surface a device reaches to fetch the bytes of an update.
//
// A device is not authenticated by anything this handler can check synchronously (there is no
// device-credential seam outside device-management), so the request carries a CAPABILITY instead:
// a signed, expiring grant the platform minted over the device's already-authenticated MQTT
// session, naming one artifact for one device under one assignment. It is not the tenant's
// credential and it opens nothing else.
//
// The order of the handler is the design, and every refusal happens before the first byte of the
// artifact is read:
//
//  1. Take a download slot. Unauthenticated work is bounded before any of it is done.
//  2. Verify the capability's signature, then its expiry and its binding to the artifact in the
//     path. A forged, mangled, wrong-tenant or wrong-artifact capability is one answer (403).
//  3. Check that the database still agrees: the assignment is the device's current one, its
//     attempt is live and not being cancelled, the artifact is the assignment's own and still
//     available (403 / 410).
//  4. Only then open the object, for exactly the requested window, and stream it. The artifact is
//     never buffered: the body is copied in small chunks with a write deadline re-armed on each, so
//     a stalled client costs one slot for one idle period and no memory.
//
// Range requests are honoured with a single `bytes=` range (206), which is what lets a device
// that lost its connection at 40% continue from where it was. The ETag is the artifact's digest, so
// If-Range makes a resume after the artifact changed (it cannot, artifacts are immutable) fall
// back to the whole object rather than splice two versions.
package download

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// Path is the route pattern the handler is registered under. A GET pattern also serves HEAD.
const Path = "GET /updates/download/{artifact}"

const (
	// idleTimeout is how long a write may stall before the download is abandoned. It is re-armed
	// on every chunk, so a slow but moving download is never cut by it and a stalled one is.
	idleTimeout = 30 * time.Second
	chunkSize   = 32 << 10
	// maxEmptyReads is how many consecutive empty, error-free reads end a download.
	maxEmptyReads = 8
)

// Refusal and outcome labels of the download metrics: a closed vocabulary.
const (
	ResultServed       = "served"
	ResultAborted      = "aborted"
	ResultCapMalformed = "capability_malformed"
	ResultCapInvalid   = "capability_invalid"
	ResultCapExpired   = "capability_expired"
	ResultStale        = "stale"
	ResultDisabled     = "device_disabled"
	ResultGone         = "gone"
	ResultBadRange     = "range_not_satisfiable"
	ResultBusy         = "busy"
	ResultError        = "error"
)

// Results lists every label value, so each series exists at zero from startup.
var Results = []string{ResultServed, ResultAborted, ResultCapMalformed, ResultCapInvalid, ResultCapExpired,
	ResultStale, ResultDisabled, ResultGone, ResultBadRange, ResultBusy, ResultError}

// Metrics are the download endpoint's instruments. Nil-safe.
type Metrics struct {
	Requests *prometheus.CounterVec
	Bytes    prometheus.Counter
}

func (m *Metrics) result(r string) {
	if m != nil && m.Requests != nil {
		m.Requests.WithLabelValues(r).Inc()
	}
}

func (m *Metrics) sent(n int64) {
	if m != nil && m.Bytes != nil && n > 0 {
		m.Bytes.Add(float64(n))
	}
}

// Handler serves the artifact download endpoint.
type Handler struct {
	api     *model.Api
	metrics *Metrics
	// idle is the per-write stall allowance; a field so a test can shorten it.
	idle time.Duration
	// window is the whole-download allowance for a response of a given length; a field so a test
	// can shorten it. A client that takes a byte every few seconds never trips the idle deadline,
	// so without a ceiling on the whole transfer it would hold a download slot for as long as it
	// liked. The default is the upload endpoint own window, so the two endpoints agree on the
	// slowest rate they tolerate.
	window func(length int64) time.Duration
	// slots bounds the downloads this process serves at once: one token per request in flight.
	slots chan struct{}
}

// New builds the handler. maxConcurrent bounds the downloads this process streams at once (it must
// be positive: an unbounded handler is the thing the bound exists to prevent).
func New(api *model.Api, maxConcurrent int, metrics *Metrics) *Handler {
	if maxConcurrent <= 0 {
		panic("download.New: maxConcurrent must be positive")
	}
	return &Handler{api: api, metrics: metrics, idle: idleTimeout, window: model.UploadWindow,
		slots: make(chan struct{}, maxConcurrent)}
}

// Register mounts the handler on mux.
func Register(mux *http.ServeMux, api *model.Api, maxConcurrent int, metrics *Metrics) {
	mux.Handle(Path, New(api, maxConcurrent, metrics))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Admission, before anything is looked up.
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "5")
		h.refuse(w, http.StatusServiceUnavailable, ResultBusy, "too many downloads are in progress; retry shortly")
		return
	}

	artifactToken := r.PathValue("artifact")
	caps := r.URL.Query()["cap"]
	if core.ValidateToken(artifactToken) != nil || len(caps) != 1 {
		h.refuse(w, http.StatusForbidden, ResultCapMalformed, "download refused")
		return
	}

	// 2. The capability.
	c, err := h.api.VerifyCapability(r.Context(), caps[0], artifactToken)
	switch {
	case err == nil:
	case errors.Is(err, model.ErrCapabilityExpired):
		h.refuse(w, http.StatusForbidden, ResultCapExpired, "download capability expired; request a new one")
		return
	case errors.Is(err, model.ErrCapabilityMalformed):
		h.refuse(w, http.StatusForbidden, ResultCapMalformed, "download refused")
		return
	case errors.Is(err, model.ErrCapabilityInvalid):
		h.refuse(w, http.StatusForbidden, ResultCapInvalid, "download refused")
		return
	default:
		log.Error().Err(err).Msg("verifying a download capability failed")
		h.refuse(w, http.StatusInternalServerError, ResultError, "internal error")
		return
	}

	// 3. The state it refers to.
	ctx := core.WithTenant(r.Context(), c.Tenant)
	target, err := h.api.ResolveDownload(ctx, c)
	switch {
	case err == nil:
	case errors.Is(err, model.ErrDownloadStale):
		h.refuse(w, http.StatusForbidden, ResultStale, "download refused")
		return
	case errors.Is(err, model.ErrDownloadDisabled):
		// The device was disabled after it was granted this capability. Same answer as a stale
		// one on the wire: the device learns nothing it could not learn by asking again.
		h.refuse(w, http.StatusForbidden, ResultDisabled, "download refused")
		return
	case errors.Is(err, model.ErrDownloadGone):
		h.refuse(w, http.StatusGone, ResultGone, "the artifact is no longer available")
		return
	default:
		log.Error().Err(err).Str("artifact", artifactToken).Msg("resolving a download failed")
		h.refuse(w, http.StatusInternalServerError, ResultError, "internal error")
		return
	}
	if h.api.Blobs == nil {
		h.refuse(w, http.StatusServiceUnavailable, ResultError, "object store not configured")
		return
	}

	// 4. Serve.
	size := target.Artifact.SizeBytes
	etag := `"` + target.Artifact.Digest + `"`
	start, length, partial, rangeErr := resolveRange(r, size, etag)
	if rangeErr != nil {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
		h.refuse(w, http.StatusRequestedRangeNotSatisfiable, ResultBadRange, rangeErr.Error())
		return
	}
	ref, err := blob.ParseRef(target.Ref)
	if err != nil {
		log.Error().Err(err).Str("artifact", artifactToken).Msg("an available artifact has an unreadable object reference")
		h.refuse(w, http.StatusInternalServerError, ResultError, "internal error")
		return
	}
	body, info, err := h.api.Blobs.OpenRange(ctx, ref, start, length)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			log.Error().Str("artifact", artifactToken).Msg("an available artifact's object is missing from the store")
		} else if !errors.Is(err, blob.ErrRangeNotSatisfiable) {
			log.Error().Err(err).Str("artifact", artifactToken).Msg("opening an artifact object failed")
		}
		status, result := http.StatusInternalServerError, ResultError
		if errors.Is(err, blob.ErrRangeNotSatisfiable) {
			w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
			status, result = http.StatusRequestedRangeNotSatisfiable, ResultBadRange
		}
		h.refuse(w, status, result, "the content could not be read")
		return
	}
	defer body.Close()
	if info.Size != size {
		log.Error().Str("artifact", artifactToken).Int64("stored", info.Size).Int64("declared", size).
			Msg("an available artifact's stored size disagrees with its declared size")
		h.refuse(w, http.StatusInternalServerError, ResultError, "internal error")
		return
	}

	hdr := w.Header()
	hdr.Set("Accept-Ranges", "bytes")
	hdr.Set("ETag", etag)
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Content-Type", target.Artifact.MediaType)
	hdr.Set("Content-Length", strconv.FormatInt(length, 10))
	status := http.StatusOK
	if partial {
		status = http.StatusPartialContent
		hdr.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, size))
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		h.metrics.result(ResultServed)
		return
	}
	sent := h.stream(w, body, length)
	h.metrics.sent(sent)
	if sent == length {
		h.metrics.result(ResultServed)
	} else {
		h.metrics.result(ResultAborted)
	}
}

// refuse answers a refusal with a short plain-text body and counts it. Nothing of the artifact has
// been read by the time it is called.
func (h *Handler) refuse(w http.ResponseWriter, status int, result, msg string) {
	h.metrics.result(result)
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, msg, status)
}

// stream copies exactly length bytes from body to w, re-arming the write deadline on every chunk. It
// returns how many bytes it wrote. A short body (an object truncated under us) ends the response
// early, which net/http turns into a closed connection, because Content-Length was promised: the
// client sees a failed transfer, never a silently short artifact.
func (h *Handler) stream(w http.ResponseWriter, body io.Reader, length int64) int64 {
	rc := http.NewResponseController(w)
	buf := make([]byte, chunkSize)
	var sent int64
	idle := 0
	deadline := time.Now().Add(h.window(length))
	for sent < length {
		now := time.Now()
		if !now.Before(deadline) {
			log.Warn().Int64("sent", sent).Int64("length", length).Msg("an artifact download ran past its window and was cut")
			return sent
		}
		wd := now.Add(h.idle)
		if wd.After(deadline) {
			wd = deadline
		}
		_ = rc.SetWriteDeadline(wd)
		n := int64(len(buf))
		if rem := length - sent; rem < n {
			n = rem
		}
		got, err := body.Read(buf[:n])
		if got > 0 {
			idle = 0
			wrote, werr := w.Write(buf[:got])
			sent += int64(wrote)
			if werr != nil {
				return sent
			}
		} else if idle++; idle > maxEmptyReads {
			// A reader that keeps returning nothing without an error would loop for ever.
			return sent
		}
		if err != nil {
			if err != io.EOF {
				log.Warn().Err(err).Int64("sent", sent).Int64("length", length).Msg("reading an artifact object failed mid-download")
			}
			break
		}
	}
	_ = rc.SetWriteDeadline(time.Time{})
	return sent
}

// errUnsatisfiable is a range the artifact cannot serve.
var errUnsatisfiable = errors.New("the requested range cannot be satisfied")

// resolveRange decides which window of an object of the given size to serve. No Range header (or
// an If-Range that does not match the ETag, or a range unit this server does not know) is the whole
// object. Exactly one `bytes=` range is honoured; several, a malformed one, or one that starts at
// or past the end is unsatisfiable.
func resolveRange(r *http.Request, size int64, etag string) (start, length int64, partial bool, err error) {
	whole := func() (int64, int64, bool, error) { return 0, size, false, nil }
	spec := r.Header.Get("Range")
	if spec == "" {
		return whole()
	}
	if ir := r.Header.Get("If-Range"); ir != "" && ir != etag {
		return whole()
	}
	unit, rest, ok := strings.Cut(spec, "=")
	if !ok || !strings.EqualFold(strings.TrimSpace(unit), "bytes") {
		return whole()
	}
	rest = strings.TrimSpace(rest)
	if rest == "" || strings.Contains(rest, ",") {
		return 0, 0, false, errUnsatisfiable
	}
	first, last, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, 0, false, errUnsatisfiable
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	switch {
	case first == "":
		// A suffix: the last n bytes.
		n, perr := strconv.ParseInt(last, 10, 64)
		if perr != nil || n <= 0 || size == 0 {
			return 0, 0, false, errUnsatisfiable
		}
		if n > size {
			n = size
		}
		return size - n, n, true, nil
	default:
		s, perr := strconv.ParseInt(first, 10, 64)
		if perr != nil || s < 0 || s >= size {
			return 0, 0, false, errUnsatisfiable
		}
		e := size - 1
		if last != "" {
			v, perr := strconv.ParseInt(last, 10, 64)
			if perr != nil || v < s {
				return 0, 0, false, errUnsatisfiable
			}
			if v < e {
				e = v
			}
		}
		return s, e - s + 1, true, nil
	}
}
