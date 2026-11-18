// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/integrationcodec"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// ttsIDShape is The Things Stack's identifier grammar, checked again before an id is put in a path.
var ttsIDShape = regexp.MustCompile(`^[a-z0-9](?:-?[a-z0-9]){0,35}$`)

// TTS is the The Things Stack v3 application-server client: POST
// {base}/api/v3/as/applications/{application}/devices/{device}/down/push, with the API key as a bearer.
//
// 🔴 THE PUSH ANSWERS WITH AN EMPTY OBJECT, NOT AN ID. The Things Stack names no downlink at enqueue,
// so the platform names it: every downlink carries a correlation id of its own (the platform's
// prefix and 128 random bits), which the network server repeats on the ack, nack, sent and failed
// events of that downlink. That id is returned in place of a queue item id and is what a report is
// matched by. An event's own correlation ids are the network server's trace and are never used.
type TTS struct {
	client *http.Client
	rand   func([]byte) (int, error)
}

// NewTTS builds the client over the egress guard's transport. Redirects are never followed.
func NewTTS(g *egress.Guard) *TTS {
	if g == nil {
		g = egress.NewGuard(nil)
	}
	return NewTTSWithTransport(g.Transport())
}

// NewTTSWithTransport is NewTTS over a given transport (tests).
func NewTTSWithTransport(rt http.RoundTripper) *TTS {
	return &TTS{client: &http.Client{
		Transport:     rt,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, rand: rand.Read}
}

type ttsDownlinkItem struct {
	FPort          int      `json:"f_port"`
	FrmPayload     string   `json:"frm_payload"`
	Confirmed      bool     `json:"confirmed"`
	Priority       string   `json:"priority"`
	CorrelationIDs []string `json:"correlation_ids"`
	// ConfirmedRetry bounds how many times the Application Server re-queues the downlink after a nack.
	// Left unset, the Application Server's own configuration decides (8 by default): a command a device
	// did not acknowledge would be sent up to eight more times, each a fresh downlink the device may act
	// on, long after DeviceChain has closed the command.
	ConfirmedRetry *ttsConfirmedRetry `json:"confirmed_retry,omitempty"`
}

type ttsConfirmedRetry struct {
	MaxAttempts int `json:"max_attempts"`
}

// ttsConfirmedMaxAttempts is the retry bound sent with a confirmed downlink. The network server
// accepts 1 to 100, so one re-queue after the first nack is the least it will take.
const ttsConfirmedMaxAttempts = 1

// Enqueue implements Enqueuer. deviceID is the device's tts.device-id. The downlink is pushed (it
// joins the end of the device's queue). The network server has no per-item expiry on this call, so
// expires only bounds the send through ctx, which the adapter has already done.
func (c *TTS) Enqueue(ctx context.Context, t connectorspec.LoRaWANDownlinkTarget, deviceID string, p Params, _ time.Time) (string, error) {
	if t.NetworkServer != connectorspec.NetworkServerTTSV3 || t.BaseURL == nil {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: fmt.Sprintf("the network server type %q has no downlink client in this build", t.NetworkServer)}
	}
	// The pattern counts dash-separated groups, not characters, so the length is checked on its own.
	if len(deviceID) > 36 || len(t.ApplicationID) > 36 ||
		!ttsIDShape.MatchString(deviceID) || !ttsIDShape.MatchString(t.ApplicationID) {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: "the application or device id is not a valid The Things Stack identifier"}
	}
	var raw [16]byte
	if _, err := c.rand(raw[:]); err != nil {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: "could not name the downlink"}
	}
	key := integrationcodec.DownlinkCorrelationPrefix + hex.EncodeToString(raw[:])
	item := ttsDownlinkItem{
		FPort: p.FPort, FrmPayload: base64.StdEncoding.EncodeToString(p.Data), Confirmed: p.Confirmed,
		Priority: "NORMAL", CorrelationIDs: []string{key},
	}
	if p.Confirmed {
		item.ConfirmedRetry = &ttsConfirmedRetry{MaxAttempts: ttsConfirmedMaxAttempts}
	}
	body, err := json.Marshal(map[string][]ttsDownlinkItem{"downlinks": {item}})
	if err != nil {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: "could not encode the downlink"}
	}
	// The path is built from the validated ids and the connector's own base URL only.
	u := *t.BaseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v3/as/applications/" + url.PathEscape(t.ApplicationID) +
		"/devices/" + url.PathEscape(deviceID) + "/down/push"
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: "could not build the request"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.APIKey)

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
		// The push answers an empty object; what it says is not read. A 2xx is acceptance.
		return key, nil
	case resp.StatusCode >= 500 || resp.StatusCode < 400 || resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == 499 || ambiguousCode(data, readErr):
		return "", &SendError{Reach: cmddownlink.ReachUnanswered,
			Text: fmt.Sprintf("the network server did not accept the downlink cleanly (HTTP %d%s); it may have been queued", resp.StatusCode, grpcName(data, readErr))}
	default:
		return "", &SendError{Reach: cmddownlink.ReachUnsent,
			Text: fmt.Sprintf("the network server refused the downlink (HTTP %d%s)", resp.StatusCode, grpcName(data, readErr))}
	}
}

// Router sends each downlink through the client for its link's network server. A network server
// with no client in this build is refused UNSENT, never sent through another's.
type Router struct {
	ChirpStack Enqueuer
	TTS        Enqueuer
	Webhook    Enqueuer
}

// Enqueue implements Enqueuer.
func (r Router) Enqueue(ctx context.Context, t connectorspec.LoRaWANDownlinkTarget, device string, p Params, expires time.Time) (string, error) {
	switch t.NetworkServer {
	case connectorspec.NetworkServerChirpStackV4:
		if r.ChirpStack != nil {
			return r.ChirpStack.Enqueue(ctx, t, device, p, expires)
		}
	case connectorspec.NetworkServerWebhook:
		if r.Webhook != nil {
			return r.Webhook.Enqueue(ctx, t, device, p, expires)
		}
	case connectorspec.NetworkServerTTSV3:
		if r.TTS != nil {
			return r.TTS.Enqueue(ctx, t, device, p, expires)
		}
	}
	return "", &SendError{Reach: cmddownlink.ReachUnsent, Text: fmt.Sprintf("the network server type %q has no downlink client in this build", t.NetworkServer)}
}
