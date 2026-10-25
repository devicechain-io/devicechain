// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package selfreg is the device's half of self-registration: it plays a device against
// POST /provisioning/{tenant}/register, following the client contract the endpoint documents.
// dcctl's `device register` is a thin command over it, and a simulator or test harness that
// needs a device to enroll uses the same code, so the contract has one implementation on this
// side of the wire.
//
// # The contract this implements
//
//   - The device generates its own credential material (32 random bytes, base64url) and
//     PERSISTS IT BEFORE THE FIRST SEND. A retry, a crash and a restart therefore all present
//     the same material, and a registration that succeeded but whose answer was lost is
//     recognised by the platform as a replay instead of being refused as a second device.
//   - 429 and 503 are waited out: Retry-After plus a uniform random jitter of 0 to 100% of it,
//     then the same material again.
//   - A network error, a timeout and a 5xx that is not an operator's problem back off
//     exponentially from 1 s to 5 min with full jitter, the same material each time.
//   - 400, 401, 403, 404, 409, 413 and 422, and the 5xx that say the operator must act
//     (501, and 500 PROVISIONING_MISCONFIGURED), are NOT retried: they need a person, or are
//     a firmware defect, and retrying only adds load.
package selfreg

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	mathrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The bounds of the material, as the endpoint enforces them.
const (
	MaterialMinLen = 22
	MaterialMaxLen = 256
	// MaterialBytes is how much entropy a generated material carries: 256 bits, which is 43
	// base64url characters.
	MaterialBytes = 32
)

const (
	backoffFloor = time.Second
	backoffCeil  = 5 * time.Minute
	// maxResponseBytes bounds what is read of an answer.
	maxResponseBytes = 64 << 10
	attemptTimeout   = 30 * time.Second
)

// Request is what the device presents.
type Request struct {
	Tenant     string
	Key        string
	Secret     string
	ExternalId string
	// Name and Metadata are optional and apply only to a device the registration creates.
	Name     string
	Metadata json.RawMessage
}

// Options are the client's seams. Only BaseURL and MaterialFile are required.
type Options struct {
	// BaseURL is the device-management API root, e.g. https://dc.example.com/api/device-management.
	BaseURL string
	// MaterialFile is where the credential material lives. It is created (mode 0600) before the
	// first send and read, not regenerated, when it already exists.
	MaterialFile string
	HTTPClient   *http.Client
	// MaxAttempts bounds the sends; zero keeps trying until the context ends.
	MaxAttempts int
	// Sleep waits for d or the context; a test replaces it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Rand returns a uniform value in [0, 1); a test replaces it.
	Rand func() float64
	// Logf reports a retry; nil is silent.
	Logf func(format string, args ...any)
}

// Result is a successful registration.
type Result struct {
	// Outcome is CREATED, ADOPTED or REPLAYED.
	Outcome        string
	Tenant         string
	DeviceToken    string
	ExternalId     string
	CredentialType string
	// ClientId is the MQTT client id the device must connect with.
	ClientId string
	// Material is the device's credential: the MQTT username is "{tenant}:{material}" and there
	// is no password. The platform holds only its digest.
	Material string
	// MaterialFile is where Material is kept.
	MaterialFile string
	// MaterialCreated is true when this run generated the material, false when it was already
	// on disk (a retry of an earlier run).
	MaterialCreated bool
	Attempts        int
}

// APIError is a refusal the platform gave, after which retrying would not help or the
// attempts ran out.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("registration refused: HTTP %d", e.Status)
	}
	return fmt.Sprintf("registration refused: HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

// GenerateMaterial returns fresh credential material: MaterialBytes random bytes, base64url
// without padding.
func GenerateMaterial() (string, error) {
	b := make([]byte, MaterialBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate credential material: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidateMaterial reports whether material is acceptable to the endpoint.
func ValidateMaterial(material string) error {
	if n := len(material); n < MaterialMinLen || n > MaterialMaxLen {
		return fmt.Errorf("credential material must be %d to %d characters, got %d", MaterialMinLen, MaterialMaxLen, n)
	}
	for i := 0; i < len(material); i++ {
		c := material[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return errors.New("credential material must be base64url: only letters, digits, '-' and '_'")
		}
	}
	return nil
}

// LoadOrCreateMaterial returns the material kept in path, creating it first when there is
// none. The file is created exclusively with mode 0600, written and synced before this
// returns, so a caller that sends afterwards has it on disk. An existing file is read and
// validated; one that is not valid material is an error, never silently replaced, because
// replacing it would strand a device whose credential the platform already holds.
func LoadOrCreateMaterial(path string) (material string, created bool, err error) {
	if path == "" {
		return "", false, errors.New("a material file is required: the credential must be saved before it is sent")
	}
	if existing, rerr := os.ReadFile(path); rerr == nil {
		m := strings.TrimSpace(string(existing))
		if verr := ValidateMaterial(m); verr != nil {
			return "", false, fmt.Errorf("%s does not hold valid credential material (%v); move it aside if it is stale", path, verr)
		}
		return m, false, nil
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return "", false, fmt.Errorf("read %s: %w", path, rerr)
	}
	m, err := GenerateMaterial()
	if err != nil {
		return "", false, err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", false, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another run created it between our read and our create: use theirs.
			return LoadOrCreateMaterial(path)
		}
		return "", false, fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.WriteString(m + "\n"); err != nil {
		f.Close()
		return "", false, fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", false, fmt.Errorf("sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return "", false, fmt.Errorf("close %s: %w", path, err)
	}
	return m, true, nil
}

// Register registers the device, saving its material first and following the retry contract.
func Register(ctx context.Context, req Request, opts Options) (*Result, error) {
	if opts.BaseURL == "" || req.Tenant == "" || req.Key == "" || req.Secret == "" || req.ExternalId == "" {
		return nil, errors.New("a registration needs a base URL, tenant, provision key, provision secret and external id")
	}
	if opts.HTTPClient == nil {
		// The transport is stated: this client runs on a device or a workstation, outside the
		// platform, and an operator behind a proxy expects the standard environment (HTTPS_PROXY)
		// to apply, which the platform-internal httptransport.New() deliberately refuses.
		opts.HTTPClient = &http.Client{Timeout: attemptTimeout, Transport: http.DefaultTransport}
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepContext
	}
	if opts.Rand == nil {
		opts.Rand = mathrand.Float64
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}

	// Saved BEFORE any send. Everything after this point may be retried, repeated by a new
	// run, or interrupted, and none of it changes what the device presents.
	material, created, err := LoadOrCreateMaterial(opts.MaterialFile)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(wireRequest{ProvisionKey: req.Key, ProvisionSecret: req.Secret, ExternalId: req.ExternalId,
		CredentialSecret: material, Name: nilIfEmpty(req.Name), Metadata: req.Metadata})
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(opts.BaseURL, "/") + "/provisioning/" + url.PathEscape(req.Tenant) + "/register"

	backoffs := 0
	for attempt := 1; ; attempt++ {
		status, header, payload, sendErr := send(ctx, opts.HTTPClient, endpoint, body)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var wait time.Duration
		switch {
		case sendErr != nil:
			wait = backoff(backoffs, opts.Rand)
			backoffs++
			opts.Logf("attempt %d: %v; retrying in %s", attempt, sendErr, wait.Round(time.Millisecond))
		case status == http.StatusOK || status == http.StatusCreated:
			return success(payload, material, opts.MaterialFile, created, attempt)
		default:
			apiErr := parseError(status, payload)
			retry, afterHeader := classify(status, apiErr.Code, header)
			if !retry {
				return nil, apiErr
			}
			if afterHeader > 0 {
				wait = afterHeader + time.Duration(opts.Rand()*float64(afterHeader))
			} else {
				wait = backoff(backoffs, opts.Rand)
				backoffs++
			}
			opts.Logf("attempt %d: %s; retrying in %s", attempt, apiErr, wait.Round(time.Millisecond))
		}
		if opts.MaxAttempts > 0 && attempt >= opts.MaxAttempts {
			if sendErr != nil {
				return nil, fmt.Errorf("giving up after %d attempts: %w", attempt, sendErr)
			}
			return nil, fmt.Errorf("giving up after %d attempts: %w", attempt, parseError(status, payload))
		}
		if err := opts.Sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

type wireRequest struct {
	ProvisionKey     string          `json:"provisionKey"`
	ProvisionSecret  string          `json:"provisionSecret"`
	ExternalId       string          `json:"externalId"`
	CredentialSecret string          `json:"credentialSecret"`
	Name             *string         `json:"name,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type wireResponse struct {
	Outcome        string `json:"outcome"`
	Tenant         string `json:"tenant"`
	DeviceToken    string `json:"deviceToken"`
	ExternalId     string `json:"externalId"`
	CredentialType string `json:"credentialType"`
	Mqtt           struct {
		ClientId string `json:"clientId"`
	} `json:"mqtt"`
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func send(ctx context.Context, c *http.Client, endpoint string, body []byte) (int, http.Header, []byte, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(hreq)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, payload, nil
}

func success(payload []byte, material, file string, created bool, attempts int) (*Result, error) {
	var w wireResponse
	if err := json.Unmarshal(payload, &w); err != nil || w.Outcome == "" || w.DeviceToken == "" || w.Mqtt.ClientId == "" {
		return nil, fmt.Errorf("the platform accepted the registration but its answer is not the documented one: %q", truncate(payload))
	}
	return &Result{Outcome: w.Outcome, Tenant: w.Tenant, DeviceToken: w.DeviceToken, ExternalId: w.ExternalId,
		CredentialType: w.CredentialType, ClientId: w.Mqtt.ClientId, Material: material, MaterialFile: file,
		MaterialCreated: created, Attempts: attempts}, nil
}

func parseError(status int, payload []byte) *APIError {
	e := &APIError{Status: status}
	var w struct{ Code, Message string }
	if json.Unmarshal(payload, &w) == nil {
		e.Code, e.Message = w.Code, w.Message
	}
	if e.Code == "" {
		e.Message = truncate(payload)
	}
	return e
}

func truncate(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

// classify decides whether a refusal is worth retrying and, for the ones that name a wait,
// returns it.
func classify(status int, code string, h http.Header) (retry bool, retryAfter time.Duration) {
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true, parseRetryAfter(h.Get("Retry-After"))
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusGatewayTimeout:
		return true, 0
	case http.StatusInternalServerError:
		// A misconfigured profile is the operator's to fix; any other 500 is the platform's own
		// failure, which a retry can outlast.
		return code != "PROVISIONING_MISCONFIGURED", 0
	default:
		return false, 0
	}
}

func parseRetryAfter(v string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

// backoff is exponential from backoffFloor to backoffCeil with full jitter: a uniform wait
// between zero and the current step.
func backoff(n int, rnd func() float64) time.Duration {
	step := float64(backoffFloor) * math.Pow(2, float64(n))
	if step > float64(backoffCeil) || math.IsInf(step, 0) {
		step = float64(backoffCeil)
	}
	return time.Duration(rnd() * step)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
