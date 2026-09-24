// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// validPort parses an explicit, numeric TCP port in 1-65535.
func validPort(p string) error {
	if p == "" {
		return fmt.Errorf("an explicit port is required")
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
		return fmt.Errorf("port %q is not a number in 1-65535", p)
	}
	return nil
}

// validHost refuses an empty host and the characters that would make a host mean
// something other than one host: a separator that turns one entry into two, a path, a
// userinfo marker, or whitespace a client might trim differently than we did.
func validHost(h string) error {
	if h == "" {
		return fmt.Errorf("a host is required")
	}
	if strings.ContainsAny(h, ",/\\@ \t\r\n") {
		return fmt.Errorf("host %q contains a character a host cannot", h)
	}
	return nil
}

// validHostPort validates a Kafka broker address: exactly host:port, no scheme, one
// broker per entry.
func validHostPort(a string) error {
	if strings.Contains(a, "://") || strings.Contains(a, ",") {
		return fmt.Errorf("%q must be a single host:port (no scheme, one broker per entry)", a)
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		return fmt.Errorf("%q is not host:port: %w", a, err)
	}
	if err := validHost(host); err != nil {
		return fmt.Errorf("%q: %w", a, err)
	}
	if err := validPort(port); err != nil {
		return fmt.Errorf("%q: %w", a, err)
	}
	return nil
}

// validHTTPURL parses an http or https URL with a host and no userinfo, query or
// fragment. It is the shape check for the AWS endpoint override and the SQS queue URL.
func validHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%q is not a URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%q must be an http or https URL", raw)
	}
	if u.Opaque != "" {
		return nil, fmt.Errorf("%q must be scheme://host", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%q must not carry userinfo", raw)
	}
	if err := validHost(u.Hostname()); err != nil {
		return nil, fmt.Errorf("%q: %w", raw, err)
	}
	if p := u.Port(); p != "" {
		if err := validPort(p); err != nil {
			return nil, fmt.Errorf("%q: %w", raw, err)
		}
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("%q must not carry a query or fragment", raw)
	}
	return u, nil
}
