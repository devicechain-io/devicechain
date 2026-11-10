// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// TracingConfiguration is operator-set FIXED configuration. The endpoint is not
// tenant supplied, so the core/egress guard does not apply.
type TracingConfiguration struct {
	Enabled       bool
	Protocol      string
	Endpoint      string
	Insecure      bool
	SamplingRatio *float64
}

func (c *TracingConfiguration) ApplyDefaults() {
	if c.Protocol == "" {
		c.Protocol = "grpc"
	}
	if c.Enabled && c.SamplingRatio == nil {
		ratio := 0.1
		c.SamplingRatio = &ratio
	}
}

func (c TracingConfiguration) Ratio() float64 {
	if c.SamplingRatio == nil {
		return 0.1
	}
	return *c.SamplingRatio
}

func (c TracingConfiguration) Validate() error {
	if c.Protocol != "" && c.Protocol != "grpc" && c.Protocol != "http" {
		return fmt.Errorf("infrastructure.tracing.protocol must be grpc or http")
	}
	if r := c.Ratio(); math.IsNaN(r) || math.IsInf(r, 0) || r < 0 || r > 1 {
		return fmt.Errorf("infrastructure.tracing.samplingRatio must be between 0 and 1")
	}
	if c.Endpoint == "" {
		if c.Enabled {
			return fmt.Errorf("infrastructure.tracing.endpoint is required when enabled")
		}
		return nil
	}
	if strings.ContainsAny(c.Endpoint, " \t\r\n") {
		return fmt.Errorf("infrastructure.tracing.endpoint contains whitespace")
	}
	if c.Protocol == "http" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(c.Endpoint, "#") || strings.HasSuffix(u.Host, ":") || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("infrastructure.tracing.endpoint must be an HTTP(S) URL without credentials, query or fragment")
		}
		if u.Scheme == "http" && !c.Insecure {
			return fmt.Errorf("infrastructure.tracing.insecure must be true for plaintext HTTP")
		}
		if u.Scheme == "https" && c.Insecure {
			return fmt.Errorf("infrastructure.tracing.insecure conflicts with HTTPS")
		}
		if u.Port() != "" {
			if !validTracingPort(u.Port()) {
				return fmt.Errorf("infrastructure.tracing.endpoint has invalid port")
			}
		}
		return nil
	}
	host, port, err := net.SplitHostPort(c.Endpoint)
	if err != nil || host == "" || !validTracingPort(port) || strings.ContainsAny(host, "/@?# \t\n\r") {
		return fmt.Errorf("infrastructure.tracing.endpoint must be host:port for grpc")
	}
	return nil
}

func validTracingPort(port string) bool {
	p, err := strconv.Atoi(port)
	return err == nil && p >= 1 && p <= 65535 && strconv.Itoa(p) == port
}
