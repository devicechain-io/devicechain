// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateLoRaWANDownlink(t *testing.T) {
	good := []string{
		`{"networkServer":"chirpstack_v4","baseUrl":"https://cs.example.com","integration":"cs1"}`,
		`{"networkServer":"chirpstack_v4","baseUrl":"http://chirpstack.svc:8090/","integration":"cs-1"}`,
		`{"networkServer":"chirpstack_v4","baseUrl":"https://example.com/chirp/","integration":"a"}`,
	}
	for _, c := range good {
		require.NoError(t, ValidateConfig(LoRaWANDownlinkType, []byte(c)), c)
	}
	bad := map[string]string{
		"no networkServer":      `{"baseUrl":"https://x.io","integration":"i"}`,
		"unknown networkServer": `{"networkServer":"ttn","baseUrl":"https://x.io","integration":"i"}`,
		"no baseUrl":            `{"networkServer":"chirpstack_v4","integration":"i"}`,
		"baseUrl scheme":        `{"networkServer":"chirpstack_v4","baseUrl":"ftp://x.io","integration":"i"}`,
		"baseUrl userinfo":      `{"networkServer":"chirpstack_v4","baseUrl":"https://u:p@x.io","integration":"i"}`,
		"baseUrl query":         `{"networkServer":"chirpstack_v4","baseUrl":"https://x.io/?a=1","integration":"i"}`,
		"baseUrl fragment":      `{"networkServer":"chirpstack_v4","baseUrl":"https://x.io/#f","integration":"i"}`,
		"baseUrl whitespace":    `{"networkServer":"chirpstack_v4","baseUrl":" https://x.io","integration":"i"}`,
		"baseUrl no host":       `{"networkServer":"chirpstack_v4","baseUrl":"https://","integration":"i"}`,
		"no integration":        `{"networkServer":"chirpstack_v4","baseUrl":"https://x.io"}`,
		"integration with sep":  `{"networkServer":"chirpstack_v4","baseUrl":"https://x.io","integration":"a:b"}`,
		"integration slash":     `{"networkServer":"chirpstack_v4","baseUrl":"https://x.io","integration":"a/b"}`,
		"integration too long":  `{"networkServer":"chirpstack_v4","baseUrl":"https://x.io","integration":"` + strings.Repeat("a", 129) + `"}`,
		"unknown field":         `{"networkServer":"chirpstack_v4","baseUrl":"https://x.io","integration":"i","apiKey":"secret"}`,
		"trailing data":         `{"networkServer":"chirpstack_v4","baseUrl":"https://x.io","integration":"i"} {}`,
	}
	for name, c := range bad {
		require.Error(t, ValidateConfig(LoRaWANDownlinkType, []byte(c)), name)
	}
}

func TestBuildLoRaWANDownlinkRequiresTheCredential(t *testing.T) {
	cfg := []byte(`{"networkServer":"chirpstack_v4","baseUrl":"https://cs.example.com/","integration":"cs1"}`)
	_, err := Build(LoRaWANDownlinkType, cfg, "")
	require.Error(t, err, "a link with no credential would send every command unauthenticated")

	tg, err := Build(LoRaWANDownlinkType, cfg, "api-key")
	require.NoError(t, err)
	d := tg.(LoRaWANDownlinkTarget)
	require.Equal(t, "api-key", d.APIKey)
	require.Equal(t, "https://cs.example.com", d.BaseURL.String())
	require.Equal(t, "cs1", d.Integration)
	require.Equal(t, NetworkServerChirpStackV4, d.NetworkServer)
	require.NotContains(t, err2s(Build(LoRaWANDownlinkType, []byte(`{"networkServer":"x"}`), "api-key")), "api-key")
}

func err2s(_ Target, err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestLoRaWANDownlinkIsASupportedType(t *testing.T) {
	require.True(t, Supported(LoRaWANDownlinkType))
}

func TestValidateTTSv3Link(t *testing.T) {
	for _, c := range []string{
		`{"networkServer":"tts_v3","baseUrl":"https://eu1.cloud.thethings.network","integration":"tts1","applicationId":"my-app"}`,
		`{"networkServer":"tts_v3","baseUrl":"https://tts.example.com/","integration":"t","applicationId":"app"}`,
	} {
		require.NoError(t, ValidateConfig(LoRaWANDownlinkType, []byte(c)), c)
	}
	for name, c := range map[string]string{
		"no applicationId":      `{"networkServer":"tts_v3","baseUrl":"https://x.io","integration":"i"}`,
		"uppercase":             `{"networkServer":"tts_v3","baseUrl":"https://x.io","integration":"i","applicationId":"MyApp"}`,
		"too short":             `{"networkServer":"tts_v3","baseUrl":"https://x.io","integration":"i","applicationId":"ab"}`,
		"double dash":           `{"networkServer":"tts_v3","baseUrl":"https://x.io","integration":"i","applicationId":"my--app"}`,
		"trailing dash":         `{"networkServer":"tts_v3","baseUrl":"https://x.io","integration":"i","applicationId":"my-app-"}`,
		"path injection":        `{"networkServer":"tts_v3","baseUrl":"https://x.io","integration":"i","applicationId":"a/../b"}`,
		"too long":              `{"networkServer":"tts_v3","baseUrl":"https://x.io","integration":"i","applicationId":"` + strings.Repeat("a", 37) + `"}`,
		"chirpstack with appid": `{"networkServer":"chirpstack_v4","baseUrl":"https://x.io","integration":"i","applicationId":"my-app"}`,
	} {
		require.Error(t, ValidateConfig(LoRaWANDownlinkType, []byte(c)), name)
	}
	tg, err := Build(LoRaWANDownlinkType, []byte(`{"networkServer":"tts_v3","baseUrl":"https://x.io","integration":"i","applicationId":"my-app"}`), "key")
	require.NoError(t, err)
	require.Equal(t, "my-app", tg.(LoRaWANDownlinkTarget).ApplicationID)
}
