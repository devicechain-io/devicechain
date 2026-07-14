// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/branding"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/rs/zerolog/log"
)

// ContextBlobKey injects the object store (ADR-058) into the data-plane GraphQL
// request context so the setTenantLogo resolver can garbage-collect a replaced
// object-store logo. It is nil when the store is not configured.
const ContextBlobKey = gqlcore.ContextKey("blob")

// brandingLogoPath is the shared path for the tenant-logo object-store endpoints
// (ADR-058): GET streams the caller's object-store logo through an authorizing
// proxy; POST uploads a new one. The GraphQL logo field returns this path (with a
// cache-busting ?v=) for an object-store-backed logo.
const brandingLogoPath = "/branding/logo"

// BrandingLogoHandler serves the tenant branding-logo object-store endpoints. It
// authenticates every request with a tenant ACCESS token (never a service token):
// upload additionally requires branding:write; read requires only a valid session
// for the tenant (the logo is shown in that tenant's console shell). The store may
// be nil (not configured) — the endpoints then fail 503 and Tier-0 logos still work.
type BrandingLogoHandler struct {
	store     blob.Store
	identity  *identity.Manager
	validator *auth.Validator
}

// RegisterBrandingLogoHandler wires the branding-logo endpoints onto mux at
// brandingLogoPath. store may be nil when the object store is unconfigured.
func RegisterBrandingLogoHandler(mux *http.ServeMux, store blob.Store, mgr *identity.Manager, v *auth.Validator) {
	h := &BrandingLogoHandler{store: store, identity: mgr, validator: v}
	mux.Handle(brandingLogoPath, h)
}

func (h *BrandingLogoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.serveRead(w, r)
	case http.MethodPost:
		h.serveUpload(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// authenticate verifies a tenant access token and returns its claims. It writes the
// 401 and returns false on any failure, so callers just return.
func (h *BrandingLogoHandler) authenticate(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	const prefix = "Bearer "
	authz := r.Header.Get("Authorization")
	if len(authz) <= len(prefix) || authz[:len(prefix)] != prefix {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return nil, false
	}
	claims, err := h.validator.Validate(authz[len(prefix):])
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return nil, false
	}
	return claims, true
}

// serveUpload stores an uploaded raster logo in the object store and points the
// caller's branding_logo at it, GC-ing any previous object-store logo.
func (h *BrandingLogoHandler) serveUpload(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !claims.HasAuthority(auth.BrandingWrite) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.store == nil {
		http.Error(w, "object store not configured", http.StatusServiceUnavailable)
		return
	}

	// Read the body bounded by the Tier-1 ceiling + 1 byte so an over-size upload is
	// detected without buffering unbounded data.
	body, err := io.ReadAll(io.LimitReader(r.Body, branding.MaxUploadedLogoBytes+1))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		http.Error(w, "empty upload", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > branding.MaxUploadedLogoBytes {
		http.Error(w, "logo exceeds the maximum size", http.StatusRequestEntityTooLarge)
		return
	}
	// Sniff the real content type from the bytes — the declared Content-Type header
	// is not trusted. Only the raster allow-list is accepted (SVG stays https-only).
	sniffed := http.DetectContentType(body)
	ext, allowed := branding.UploadableLogoMIME[sniffed]
	if !allowed {
		http.Error(w, "unsupported image type (allowed: png, jpeg, webp)", http.StatusUnsupportedMediaType)
		return
	}

	ctx := auth.WithClaims(r.Context(), claims)
	id, err := randomLogoID(ext)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ref, err := h.store.Put(ctx,
		blob.Key{Tenant: claims.Tenant, Purpose: branding.LogoBlobPurpose, ID: id},
		bytes.NewReader(body),
		blob.PutOptions{ContentType: sniffed, MaxSize: branding.MaxUploadedLogoBytes})
	if err != nil {
		if errors.Is(err, blob.ErrTooLarge) {
			http.Error(w, "logo exceeds the maximum size", http.StatusRequestEntityTooLarge)
			return
		}
		log.Error().Err(err).Msg("branding logo Put failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	prev, err := h.identity.SetTenantLogo(ctx, claims.Tenant, ref.String())
	if err != nil {
		// The column was not updated — remove the just-stored blob so it does not
		// orphan, then fail.
		_ = h.store.Delete(ctx, ref)
		log.Error().Err(err).Msg("setting tenant logo reference failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	deleteReplacedLogoBlob(ctx, h.store, prev, ref.String())

	writeJSON(w, map[string]string{"logo": brandingLogoPath + "?v=" + id})
}

// serveRead streams the caller's object-store logo through the authorizing proxy.
// It only serves an object-store (blob://) logo; a Tier-0 https/data: logo is used
// by the client directly and 404s here.
func (h *BrandingLogoHandler) serveRead(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if h.store == nil {
		http.Error(w, "object store not configured", http.StatusServiceUnavailable)
		return
	}
	ctx := auth.WithClaims(r.Context(), claims)
	t, err := h.identity.TenantByToken(ctx, claims.Tenant)
	if err != nil {
		log.Error().Err(err).Msg("loading tenant for logo read failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if t.BrandingLogo == nil {
		http.NotFound(w, r)
		return
	}
	// The ref comes from the CALLER's own tenant row, so it is inherently the
	// caller's asset — the store additionally enforces the instance prefix on Open.
	ref, err := blob.ParseRef(*t.BrandingLogo)
	if err != nil {
		http.NotFound(w, r) // a Tier-0 logo is not served by the proxy
		return
	}
	rc, info, err := h.store.Open(ctx, ref)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Error().Err(err).Msg("opening logo blob failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", info.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	// The ?v= id changes on every upload, so a response may be cached briefly and
	// privately (it carries a tenant's logo, shown only to that tenant's users).
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, rc); err != nil {
		log.Warn().Err(err).Msg("streaming logo blob failed")
	}
}

// randomLogoID builds an unguessable object id with the given extension. The
// randomness keeps ids from colliding and from being enumerable.
func randomLogoID(ext string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]) + ext, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
