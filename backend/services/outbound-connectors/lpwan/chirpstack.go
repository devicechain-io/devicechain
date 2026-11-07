// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/egress"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// maxResponseBytes caps what a network server may send back; a hostile or broken server cannot hold
// memory past it.
const maxResponseBytes = 64 << 10

// SendError is a failed enqueue, classified by how far the command got.
type SendError struct {
	// Reach is cmddownlink.ReachUnsent (proven not enqueued, safe to retry) or
	// cmddownlink.ReachUnanswered (the request may have been enqueued).
	Reach string
	// Text is safe to show the tenant: it never holds the credential or the payload.
	Text string
	// Blocked is true when the egress guard refused the destination.
	Blocked bool
}

func (e *SendError) Error() string { return e.Text }

// Enqueuer puts one downlink on a network server's queue and returns the server's id for it.
type Enqueuer interface {
	Enqueue(ctx context.Context, t connectorspec.LoRaWANDownlinkTarget, devEUI string, p Params, expires time.Time) (queueItemID string, err error)
}

// ChirpStack is the ChirpStack v4 REST client (the chirpstack-rest-api proxy's
// POST /api/devices/{devEui}/queue).
type ChirpStack struct {
	client *http.Client
}

// NewChirpStack builds the client over the egress guard's transport. A nil guard is a guard with no
// allowances. Redirects are never followed: a network server that redirects is answered, not chased
// to an address the guard has not judged against the connector's stored host.
func NewChirpStack(g *egress.Guard) *ChirpStack {
	if g == nil {
		g = egress.NewGuard(nil)
	}
	return NewChirpStackWithTransport(g.Transport())
}

// NewChirpStackWithTransport is NewChirpStack over a given transport (tests).
func NewChirpStackWithTransport(rt http.RoundTripper) *ChirpStack {
	return &ChirpStack{client: &http.Client{
		Transport:     rt,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type csQueueItem struct {
	DevEui    string `json:"devEui"`
	FPort     int    `json:"fPort"`
	Confirmed bool   `json:"confirmed"`
	Data      string `json:"data"`
	// ExpiresAt makes the network server drop the item if the device has not collected it by then,
	// so a downlink does not actuate after the platform has given up on the command.
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// Enqueue implements Enqueuer. ctx must carry the send's deadline.
func (c *ChirpStack) Enqueue(ctx context.Context, t connectorspec.LoRaWANDownlinkTarget, devEUI string, p Params, expires time.Time) (string, error) {
	if t.NetworkServer != connectorspec.NetworkServerChirpStackV4 || t.BaseURL == nil {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: fmt.Sprintf("the network server type %q has no downlink client in this build", t.NetworkServer)}
	}
	if !validDevEUI(devEUI) {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: "the device's DevEUI is not 16 hexadecimal digits"}
	}
	item := csQueueItem{
		DevEui: devEUI, FPort: p.FPort, Confirmed: p.Confirmed,
		Data: base64.StdEncoding.EncodeToString(p.Data),
	}
	if !expires.IsZero() {
		item.ExpiresAt = expires.UTC().Format(time.RFC3339)
	}
	body, err := json.Marshal(map[string]csQueueItem{"queueItem": item})
	if err != nil {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: "could not encode the downlink"}
	}
	// The path is built from the validated DevEUI and the connector's own base URL only.
	u := *t.BaseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/api/devices/" + url.PathEscape(devEUI) + "/queue"
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: "could not build the request"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.APIKey)

	// Whether the request reached the wire decides unsent versus unanswered when the exchange fails.
	var wrote atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		// Any write attempt past the headers counts, successful or not: a request that failed
		// part-way may still have reached the server whole, so only a failure BEFORE the headers
		// went out proves nothing was sent.
		WroteHeaders: func() { wrote.Store(true) },
		WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) },
	}))

	resp, err := c.client.Do(req)
	if err != nil {
		return "", classifyTransport(err, wrote.Load())
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if readErr == nil && len(data) > maxResponseBytes {
		readErr = errors.New("response too large")
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if readErr != nil {
			return "", &SendError{Reach: cmddownlink.ReachUnanswered, Text: "the network server accepted the downlink but its answer could not be read"}
		}
		var ok struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(data, &ok) != nil || ok.ID == "" {
			return "", &SendError{Reach: cmddownlink.ReachUnanswered, Text: "the network server answered success without a queue item id"}
		}
		return ok.ID, nil
	case resp.StatusCode >= 500 || resp.StatusCode < 400 || resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == 499 || ambiguousCode(data, readErr):
		// A server failure, a timeout, or a redirect we do not follow (a proxy in front answered):
		// nothing proves it was not queued.
		return "", &SendError{Reach: cmddownlink.ReachUnanswered,
			Text: fmt.Sprintf("the network server did not accept the downlink cleanly (HTTP %d%s); it may have been queued", resp.StatusCode, grpcName(data, readErr))}
	default:
		// 4xx: a documented rejection, the server did not queue it.
		return "", &SendError{Reach: cmddownlink.ReachUnsent,
			Text: fmt.Sprintf("the network server refused the downlink (HTTP %d%s)", resp.StatusCode, grpcName(data, readErr))}
	}
}

func classifyTransport(err error, wrote bool) *SendError {
	if errors.Is(err, egress.ErrBlocked) {
		return &SendError{Reach: cmddownlink.ReachUnsent, Blocked: true, Text: "the platform egress policy refused the network server's address"}
	}
	text := "the network server could not be reached"
	if wrote {
		return &SendError{Reach: cmddownlink.ReachUnanswered, Text: text + " after the request was sent; the downlink may have been queued"}
	}
	return &SendError{Reach: cmddownlink.ReachUnsent, Text: text}
}

// grpcCodes names the gRPC status codes ChirpStack's REST proxy puts in an error body's "code".
var grpcCodes = map[int]string{
	1: "CANCELLED", 2: "UNKNOWN", 3: "INVALID_ARGUMENT", 4: "DEADLINE_EXCEEDED", 5: "NOT_FOUND",
	6: "ALREADY_EXISTS", 7: "PERMISSION_DENIED", 8: "RESOURCE_EXHAUSTED", 9: "FAILED_PRECONDITION",
	10: "ABORTED", 11: "OUT_OF_RANGE", 12: "UNIMPLEMENTED", 13: "INTERNAL", 14: "UNAVAILABLE",
	15: "DATA_LOSS", 16: "UNAUTHENTICATED",
}

// ambiguousCode reports a gRPC status that does not prove the operation was rolled back: cancelled,
// unknown, deadline exceeded, aborted, internal, unavailable, data loss. The REST proxy can answer
// these with a 4xx after the queue insert completed. A body that cannot be read or decoded is
// ambiguous too; only an empty body or a decoded, unambiguous code is a clean refusal.
func ambiguousCode(data []byte, readErr error) bool {
	if readErr != nil {
		return true // the rejection's own words could not be read: nothing proves it rolled back
	}
	if len(data) == 0 {
		return false // a bare status (a rate limiter, a proxy's refusal) names no gRPC outcome to doubt
	}
	var m struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(data, &m) != nil {
		// A body that does not decode is a truncated or foreign answer, and a truncated ABORTED is
		// indistinguishable from a complete one. Reading it as a clean refusal would report the
		// command UNSENT (safe to retry) while the downlink may already be queued, and a retried
		// downlink actuates a physical device twice.
		return true
	}
	switch m.Code {
	case 1, 2, 4, 10, 13, 14, 15:
		return true
	}
	return false
}

// grpcName reads the gRPC status code out of an error body and returns ", NAME". It returns ONLY
// platform-owned text: a network server's own words are never echoed to the tenant, because a link
// pointed at an operator-allowed internal address would otherwise be a channel to read that
// address's responses back.
func grpcName(data []byte, readErr error) string {
	if readErr != nil || len(data) == 0 {
		return ""
	}
	var m struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	if n, ok := grpcCodes[m.Code]; ok {
		return ", " + n
	}
	return ""
}

func validDevEUI(s string) bool {
	if len(s) != 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
