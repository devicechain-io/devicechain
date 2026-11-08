// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"fmt"
	"net/url"
	"strings"
)

// The network servers a lorawan_downlink connector can name. A value here that the downlink
// adapter has no client for is a recognized vocabulary member and not executable: it is refused at
// dispatch as a terminal UNSENT, never sent as something else.
const (
	NetworkServerChirpStackV4 = "chirpstack_v4"
)

// LoRaWANDownlinkType is the connector type that links an inbound integration to its network
// server's downlink API.
const LoRaWANDownlinkType = "lorawan_downlink"

// maxIntegrationTokenLen bounds the integration reference. Device-management's token grammar is far
// narrower; this is only the ceiling a hostile config cannot exceed.
const maxIntegrationTokenLen = 128

// lorawanConfig is the DeviceChain-facing config of a lorawan_downlink connector. The credential
// (the network server's API token) is NEVER in it; Build attaches it from the sealed secret.
type lorawanConfig struct {
	// NetworkServer selects the API dialect. Required.
	NetworkServer string `json:"networkServer"`
	// BaseURL is the network server's REST API root, e.g. https://chirpstack.example.com. A path
	// prefix is allowed (a reverse proxy mount); a query, fragment or userinfo is not.
	BaseURL string `json:"baseUrl"`
	// Integration is the token of the inbound integration whose devices this link serves. Required.
	Integration string `json:"integration"`
}

// LoRaWANDownlinkTarget is a validated network-server link. It is NOT a publish destination: the
// REACT publish sender has no client for it and refuses it terminally. The downlink adapter is its
// only user.
type LoRaWANDownlinkTarget struct {
	NetworkServer string
	// BaseURL has no trailing slash.
	BaseURL     *url.URL
	Integration string
	// APIKey is the network server's API token, in memory only.
	APIKey string
}

func (LoRaWANDownlinkTarget) isTarget() {}

func parseLoRaWANDownlink(config []byte) (Target, error) {
	var c lorawanConfig
	if err := decodeStrict(LoRaWANDownlinkType, config, &c); err != nil {
		return nil, err
	}
	switch c.NetworkServer {
	case NetworkServerChirpStackV4:
	case "":
		return nil, fmt.Errorf("%s config: networkServer is required (chirpstack_v4)", LoRaWANDownlinkType)
	default:
		return nil, fmt.Errorf("%s config: networkServer %q is not one of chirpstack_v4", LoRaWANDownlinkType, c.NetworkServer)
	}
	if c.Integration == "" || len(c.Integration) > maxIntegrationTokenLen ||
		strings.ContainsAny(c.Integration, " \t\r\n:/\\") {
		return nil, fmt.Errorf("%s config: integration must be the token of an inbound integration", LoRaWANDownlinkType)
	}
	if strings.TrimSpace(c.BaseURL) != c.BaseURL || c.BaseURL == "" {
		return nil, fmt.Errorf("%s config: baseUrl is required and must not carry surrounding whitespace", LoRaWANDownlinkType)
	}
	u, err := validHTTPURL(c.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s config: baseUrl: %w", LoRaWANDownlinkType, err)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return LoRaWANDownlinkTarget{NetworkServer: c.NetworkServer, BaseURL: u, Integration: c.Integration}, nil
}

func withLoRaWANDownlinkSecret(t Target, secret string) (Target, error) {
	d := t.(LoRaWANDownlinkTarget)
	// The downlink API is authenticated, always: a link with no credential would send every
	// command unauthenticated, or be refused by the server after the egress. Refuse it here.
	if secret == "" {
		return nil, fmt.Errorf("%s config: the network server's API token (the connector secret) is required", LoRaWANDownlinkType)
	}
	d.APIKey = secret
	return d, nil
}
