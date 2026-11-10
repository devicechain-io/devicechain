// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/integrationcodec"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// Webhook sends HTTP downlinks through the guarded transport.
type Webhook struct {
	client *http.Client
	rand   func([]byte) (int, error)
}

// NewWebhook builds the client over the egress guard's transport. Redirects are never followed.
func NewWebhook(g *egress.Guard) *Webhook {
	if g == nil {
		g = egress.NewGuard(nil)
	}
	return NewWebhookWithTransport(g.Transport())
}

// NewWebhookWithTransport is NewWebhook over a given transport (tests).
func NewWebhookWithTransport(rt http.RoundTripper) *Webhook {
	return &Webhook{client: &http.Client{
		Transport:     rt,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, rand: rand.Read}
}

// Enqueue sends a rendered webhook request under the adapter's context deadline.
func (c *Webhook) Enqueue(ctx context.Context, t connectorspec.LoRaWANDownlinkTarget, deviceID string, p Params, _ time.Time) (string, error) {
	if t.NetworkServer != connectorspec.NetworkServerWebhook || t.Webhook == nil || t.BaseURL == nil {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: fmt.Sprintf("the network server type %q has no downlink client in this build", t.NetworkServer)}
	}
	rawEUI, err := hex.DecodeString(deviceID)
	if err != nil || len(rawEUI) != 8 {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: "the device has no valid DevEUI"}
	}
	var raw [16]byte
	if n, err := c.rand(raw[:]); err != nil || n != len(raw) {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: "could not name the downlink"}
	}
	key := integrationcodec.DownlinkCorrelationPrefix + hex.EncodeToString(raw[:])
	rendered, err := t.Webhook.Render(connectorspec.DownlinkValues{DevEUI: deviceID, FPort: p.FPort, Payload: p.Data, Confirmed: p.Confirmed, CorrelationID: key})
	if err != nil {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: "could not render the downlink request"}
	}
	u, err := connectorspec.JoinWebhookURL(t.BaseURL, rendered.URL)
	if err != nil || u.Scheme != t.BaseURL.Scheme || u.Host != t.BaseURL.Host {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: "could not build the downlink URL"}
	}
	req, err := http.NewRequestWithContext(ctx, t.Webhook.Method, u.String(), strings.NewReader(rendered.Body))
	if err != nil {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: "could not build the request"}
	}
	if rendered.Body != "" {
		req.Header.Set("Content-Type", t.Webhook.ContentType)
	}
	req.Header.Set("Accept", "*/*")
	for k, v := range rendered.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set(t.Webhook.AuthHeader, t.Webhook.AuthPrefix+t.APIKey)
	var wrote atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
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
		readErr = fmt.Errorf("response too large")
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// The response body has no queue-id contract. Any 2xx is acceptance.
		return key, nil
	case resp.StatusCode >= 500 || resp.StatusCode < 400 || resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == 499 || readErr != nil:
		return "", &SendError{Reach: cmddownlink.ReachUnanswered,
			Text: fmt.Sprintf("the network server did not accept the downlink cleanly (HTTP %d); it may have been queued", resp.StatusCode)}
	default:
		return "", &SendError{Reach: cmddownlink.ReachUnsent,
			Text: fmt.Sprintf("the network server refused the downlink (HTTP %d)", resp.StatusCode)}
	}
}
