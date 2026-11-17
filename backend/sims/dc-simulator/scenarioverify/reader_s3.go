// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"fmt"
	"time"
)

// The documents the verifier sends for the S3 beats: deliveries out of the tenant, notifications,
// and the LoRaWAN gauges' decoding. Each is validated against its served schema by
// documents_test.go.
const (
	mutationRotateSigning = `mutation ScenarioRotateSigning($handle: String!) {
  rotateWebhookSigningSecret(handle: $handle) { handle secret rotating }
}`

	mutationEndSigningRotation = `mutation ScenarioEndSigningRotation($handle: String!) {
  endWebhookSigningRotation(handle: $handle) { handle rotating }
}`

	queryNotificationAttempts = `query ScenarioNotificationAttempts($c: NotificationAttemptSearchCriteria!) {
  notificationAttempts(criteria: $c) {
    results { attemptedAt alarmToken trigger channelToken status recipients errorCode }
    pagination { totalRecords }
  }
}`

	mutationAcknowledgeAlarm = `mutation ScenarioAcknowledge($token: String!) {
  acknowledgeAlarm(token: $token) { token }
}`

	queryDecoderHealth = `query ScenarioDecoderHealth($profile: String!, $minutes: Int) {
  payloadDecoderHealth(profileToken: $profile, minutes: $minutes) { decoded ignored failed suppressed lastError }
}`

	queryFailedEvents = `query ScenarioFailedEvents($c: FailedEventSearchCriteria!) {
  failedEvents(criteria: $c) {
    results { occurredTime reason deviceToken error }
    pagination { totalRecords }
  }
}`

	attemptPageSize = 200
	failedPageSize  = 200
	// failedReasonDecode is event-management's FailedEventReason for an uplink the tenant's
	// decoder could not decode. A hand mirror: it is SENT as a filter, so a rename is refused by
	// the server's enum rather than matching nothing.
	failedReasonDecode = "DECODE_FAILED"
)

// Attempt is one notification attempt.
type Attempt struct {
	At         time.Time `json:"at"`
	AlarmToken string    `json:"alarmToken"`
	Trigger    string    `json:"trigger"`
	Channel    string    `json:"channel"`
	Status     string    `json:"status"`
	ErrorCode  string    `json:"errorCode,omitempty"`
}

// DecoderHealth is a profile's decoder tallies.
type DecoderHealth struct {
	Decoded, Ignored, Failed, Suppressed float64
	LastError                            string
}

// DecodeFailure is one DECODE_FAILED record.
type DecodeFailure struct {
	At     time.Time `json:"at"`
	Device string    `json:"device"`
	Error  string    `json:"error"`
}

// RotateSigningSecret replaces the secret under handle with a new one this verifier holds, and
// ends the overlap so every later delivery is signed by it alone. It is the one write the verifier
// makes to set the day up: the platform shows a signing secret once, to whoever minted it, and the
// simulator does not keep the one it minted.
func (r *Reader) RotateSigningSecret(ctx context.Context, handle string) (string, error) {
	if r.endpoints.OutboundConnectorsGraphQL == "" {
		return "", fmt.Errorf("the handshake carries no outboundConnectorsGraphQL endpoint")
	}
	var rotated struct {
		RotateWebhookSigningSecret struct {
			Secret string `json:"secret"`
		} `json:"rotateWebhookSigningSecret"`
	}
	if err := r.session.Query(ctx, r.endpoints.OutboundConnectorsGraphQL, mutationRotateSigning,
		map[string]any{"handle": handle}, &rotated); err != nil {
		return "", fmt.Errorf("rotate the signing secret %s: %w", handle, err)
	}
	var ended struct {
		EndWebhookSigningRotation struct {
			Rotating bool `json:"rotating"`
		} `json:"endWebhookSigningRotation"`
	}
	if err := r.session.Query(ctx, r.endpoints.OutboundConnectorsGraphQL, mutationEndSigningRotation,
		map[string]any{"handle": handle}, &ended); err != nil {
		return "", fmt.Errorf("end the signing rotation %s: %w", handle, err)
	}
	if rotated.RotateWebhookSigningSecret.Secret == "" || ended.EndWebhookSigningRotation.Rotating {
		return "", fmt.Errorf("the signing rotation of %s did not settle on one new secret", handle)
	}
	return rotated.RotateWebhookSigningSecret.Secret, nil
}

// Attempts reads every notification attempt for one alarm.
func (r *Reader) Attempts(ctx context.Context, alarmToken string) ([]Attempt, error) {
	if r.endpoints.NotificationMgmtGraphQL == "" {
		return nil, fmt.Errorf("the handshake carries no notificationMgmtGraphQL endpoint")
	}
	var out []Attempt
	for page := 1; page <= maxPages; page++ {
		var resp struct {
			NotificationAttempts struct {
				Results []struct {
					AttemptedAt  string  `json:"attemptedAt"`
					AlarmToken   string  `json:"alarmToken"`
					Trigger      string  `json:"trigger"`
					ChannelToken string  `json:"channelToken"`
					Status       string  `json:"status"`
					ErrorCode    *string `json:"errorCode"`
				} `json:"results"`
				Pagination struct {
					TotalRecords *int `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"notificationAttempts"`
		}
		vars := map[string]any{"c": map[string]any{"pageNumber": page, "pageSize": attemptPageSize, "alarmToken": alarmToken}}
		if err := r.session.Query(ctx, r.endpoints.NotificationMgmtGraphQL, queryNotificationAttempts, vars, &resp); err != nil {
			return nil, fmt.Errorf("read notification attempts of %s: %w", alarmToken, err)
		}
		if resp.NotificationAttempts.Pagination.TotalRecords == nil {
			return nil, fmt.Errorf("read notification attempts of %s: the server returned a null totalRecords", alarmToken)
		}
		for _, a := range resp.NotificationAttempts.Results {
			at, err := time.Parse(time.RFC3339Nano, a.AttemptedAt)
			if err != nil {
				return nil, fmt.Errorf("notification attempt of %s: attemptedAt %q: %w", alarmToken, a.AttemptedAt, err)
			}
			code := ""
			if a.ErrorCode != nil {
				code = *a.ErrorCode
			}
			out = append(out, Attempt{At: at, AlarmToken: a.AlarmToken, Trigger: a.Trigger, Channel: a.ChannelToken, Status: a.Status, ErrorCode: code})
		}
		if len(out) >= *resp.NotificationAttempts.Pagination.TotalRecords || len(resp.NotificationAttempts.Results) < attemptPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("read notification attempts of %s: more than %d pages", alarmToken, maxPages)
}

// Acknowledge acknowledges one alarm.
func (r *Reader) Acknowledge(ctx context.Context, token string) error {
	var resp struct {
		AcknowledgeAlarm struct {
			Token string `json:"token"`
		} `json:"acknowledgeAlarm"`
	}
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, mutationAcknowledgeAlarm, map[string]any{"token": token}, &resp); err != nil {
		return fmt.Errorf("acknowledge %s: %w", token, err)
	}
	return nil
}

// DecoderHealth reads a profile's decoder tallies over the last minutes.
func (r *Reader) DecoderHealth(ctx context.Context, profile string, minutes int) (DecoderHealth, error) {
	var resp struct {
		PayloadDecoderHealth struct {
			Decoded    float64 `json:"decoded"`
			Ignored    float64 `json:"ignored"`
			Failed     float64 `json:"failed"`
			Suppressed float64 `json:"suppressed"`
			LastError  string  `json:"lastError"`
		} `json:"payloadDecoderHealth"`
	}
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryDecoderHealth,
		map[string]any{"profile": profile, "minutes": minutes}, &resp); err != nil {
		return DecoderHealth{}, fmt.Errorf("read the decoder health of %s: %w", profile, err)
	}
	h := resp.PayloadDecoderHealth
	return DecoderHealth{Decoded: h.Decoded, Ignored: h.Ignored, Failed: h.Failed, Suppressed: h.Suppressed, LastError: h.LastError}, nil
}

// DecodeFailures reads the DECODE_FAILED records of one device.
func (r *Reader) DecodeFailures(ctx context.Context, device string) ([]DecodeFailure, error) {
	var out []DecodeFailure
	for page := 1; page <= maxPages; page++ {
		var resp struct {
			FailedEvents struct {
				Results []struct {
					OccurredTime string  `json:"occurredTime"`
					Reason       string  `json:"reason"`
					DeviceToken  *string `json:"deviceToken"`
					Error        string  `json:"error"`
				} `json:"results"`
				Pagination struct {
					TotalRecords *int `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"failedEvents"`
		}
		vars := map[string]any{"c": map[string]any{"pageNumber": page, "pageSize": failedPageSize, "reason": failedReasonDecode, "deviceToken": device}}
		if err := r.session.Query(ctx, r.events, queryFailedEvents, vars, &resp); err != nil {
			return nil, fmt.Errorf("read the decode failures of %s: %w", device, err)
		}
		if resp.FailedEvents.Pagination.TotalRecords == nil {
			return nil, fmt.Errorf("read the decode failures of %s: the server returned a null totalRecords", device)
		}
		for _, f := range resp.FailedEvents.Results {
			if f.Reason != failedReasonDecode || f.DeviceToken == nil || *f.DeviceToken != device {
				return nil, fmt.Errorf("read the decode failures of %s: the server returned a %s record of %v", device, f.Reason, f.DeviceToken)
			}
			at, err := time.Parse(time.RFC3339Nano, f.OccurredTime)
			if err != nil {
				return nil, fmt.Errorf("decode failure of %s: occurredTime %q: %w", device, f.OccurredTime, err)
			}
			out = append(out, DecodeFailure{At: at, Device: device, Error: f.Error})
		}
		if len(out) >= *resp.FailedEvents.Pagination.TotalRecords || len(resp.FailedEvents.Results) < failedPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("read the decode failures of %s: more than %d pages", device, maxPages)
}
