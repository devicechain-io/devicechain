// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func webhookConfig(extra map[string]any) []byte {
	c := map[string]any{"networkServer": "webhook", "baseUrl": "https://ns.example.com/api", "integration": "tok", "urlTemplate": "/devices/{{devEui}}"}
	for k, v := range extra {
		c[k] = v
	}
	b, _ := json.Marshal(c)
	return b
}
func TestWebhookValidation(t *testing.T) {
	for name, c := range map[string]map[string]any{
		"defaults":   {},
		"put":        {"method": "PUT"},
		"patch":      {"method": "PATCH"},
		"empty body": {"bodyTemplate": ""},
		"raw auth":   {"authPrefix": ""},
		"form":       {"contentType": "application/x-www-form-urlencoded", "bodyTemplate": "eui={{devEui}}"},
		"json":       {"urlTemplate": "/send", "bodyTemplate": `{"eui":"{{devEui}}"}`},
	} {
		t.Run(name, func(t *testing.T) { require.NoError(t, ValidateConfig(LoRaWANDownlinkType, webhookConfig(c))) })
	}
	bad := map[string]map[string]any{
		"unknown":           {"urlTemplate": "/{{devEui}}/{{unknown}}"},
		"whitespace":        {"urlTemplate": "/{{ devEui }}"},
		"unterminated":      {"urlTemplate": "/{{devEui"},
		"empty":             {"urlTemplate": "/{{}}"},
		"authority":         {"urlTemplate": "//evil.com/{{devEui}}"},
		"parent":            {"urlTemplate": "/../{{devEui}}"},
		"encoded parent":    {"urlTemplate": "/%2e%2e/{{devEui}}"},
		"backslash":         {"urlTemplate": `/a\{{devEui}}`},
		"relative":          {"urlTemplate": "devices/{{devEui}}"},
		"fragment":          {"urlTemplate": "/{{devEui}}#x"},
		"no device":         {"urlTemplate": "/send"},
		"long url":          {"urlTemplate": "/{{devEui}}" + strings.Repeat("x", 1024)},
		"long body":         {"bodyTemplate": strings.Repeat("x", 8193)},
		"long header":       {"headers": map[string]string{"X-Test": strings.Repeat("x", 513)}},
		"crlf":              {"headers": map[string]string{"X-Test": "x\r\ny"}},
		"bad header":        {"headers": map[string]string{"Bad Name": "value"}},
		"auth collision":    {"authHeader": "X-Key", "headers": map[string]string{"x-key": "override"}},
		"bad auth":          {"authHeader": "Host"},
		"auth newline":      {"authPrefix": "x\n"},
		"long auth":         {"authPrefix": strings.Repeat("x", 33)},
		"invalid json":      {"bodyTemplate": "{"},
		"delete":            {"method": "DELETE"},
		"native fields":     {"networkServer": "chirpstack_v4"},
		"tts fields":        {"networkServer": "tts_v3", "applicationId": "app"},
		"application":       {"applicationId": "app"},
		"empty application": {"applicationId": ""},
		"null application":  {"applicationId": nil},
		"unknown field":     {"surprise": true},
		"bad type":          {"contentType": "text/html"},
	}
	for _, h := range []string{"Host", "Content-Length", "Transfer-Encoding", "Connection", "Content-Type", "Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie"} {
		bad[h] = map[string]any{"headers": map[string]string{strings.ToLower(h): "value"}}
	}
	headers := map[string]string{}
	for i := 0; i < 17; i++ {
		headers["X-"+strings.Repeat("a", i+1)] = "v"
	}
	bad["many headers"] = map[string]any{"headers": headers}
	for name, c := range bad {
		t.Run(name, func(t *testing.T) { require.Error(t, ValidateConfig(LoRaWANDownlinkType, webhookConfig(c))) })
	}
	_, err := Build(LoRaWANDownlinkType, webhookConfig(nil), "")
	require.Error(t, err)
	for _, offender := range []string{"{{unknown}}", "{{ devEui }}", "{{devEui", "{{}}"} {
		err := ValidateConfig(LoRaWANDownlinkType, webhookConfig(map[string]any{"urlTemplate": "/" + offender}))
		require.ErrorContains(t, err, offender)
	}
}
func TestWebhookRender(t *testing.T) {
	target, err := Build(LoRaWANDownlinkType, webhookConfig(map[string]any{"urlTemplate": "/devices/{{devEui}}/{{payloadBase64}}?data={{payloadBase64}}&ref={{correlationId}}", "bodyTemplate": `{"dev":"{{devEui}}","port":{{fPort}},"b64":"{{payloadBase64}}","hex":"{{payloadHex}}","confirmed":{{confirmed}},"ref":"{{correlationId}}"}`, "headers": map[string]string{"X-Ref": "{{correlationId}}"}}), "secret")
	require.NoError(t, err)
	s := target.(LoRaWANDownlinkTarget).Webhook
	r, err := s.Render(DownlinkValues{DevEUI: "aabbccddeeff0011", FPort: 12, Payload: []byte{251, 255}, Confirmed: true, CorrelationID: "dc:0123456789abcdef0123456789abcdef"})
	require.NoError(t, err)
	require.Equal(t, "/devices/AABBCCDDEEFF0011/+%2F8=?data=%2B%2F8%3D&ref=dc%3A0123456789abcdef0123456789abcdef", r.URL)
	require.Equal(t, `{"dev":"AABBCCDDEEFF0011","port":12,"b64":"+/8=","hex":"fbff","confirmed":true,"ref":"dc:0123456789abcdef0123456789abcdef"}`, r.Body)
	require.Equal(t, map[string]string{"X-Ref": "dc:0123456789abcdef0123456789abcdef"}, r.Headers)
	require.True(t, s.CarriesCorrelation())
	_, err = s.Render(DownlinkValues{DevEUI: "0011223344556677", CorrelationID: "dc:bad\r\nX-Key: injected"})
	require.Error(t, err)

	r, err = s.Render(DownlinkValues{DevEUI: "0011223344556677", CorrelationID: "{{devEui}}"})
	require.NoError(t, err)
	require.Equal(t, "{{devEui}}", r.Headers["X-Ref"])
	require.Contains(t, r.Body, `"ref":"{{devEui}}"`)
	for name, c := range map[string]map[string]any{"none": {},
		"url":    {"urlTemplate": "/{{devEui}}?ref={{correlationId}}"},
		"body":   {"bodyTemplate": `{"ref":"{{correlationId}}"}`},
		"header": {"headers": map[string]string{"X-Ref": "{{correlationId}}"}}} {
		tg, e := Build(LoRaWANDownlinkType, webhookConfig(c), "secret")
		require.NoError(t, e)
		require.Equal(t, name != "none", tg.(LoRaWANDownlinkTarget).Webhook.CarriesCorrelation(), name)
	}
}
