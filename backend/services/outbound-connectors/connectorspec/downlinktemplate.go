// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DownlinkValues are the device and command values available to webhook templates.
type DownlinkValues struct {
	DevEUI        string
	FPort         int
	Payload       []byte
	Confirmed     bool
	CorrelationID string
}

// RenderedRequest contains the rendered relative URL, body and custom headers.
type RenderedRequest struct {
	URL, Body string
	Headers   map[string]string
}
type templatePart struct{ literal, name string }
type downlinkTemplate []templatePart

func tokenize(s string) (downlinkTemplate, error) {
	var parts downlinkTemplate
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			return append(parts, templatePart{literal: s}), nil
		}
		parts = append(parts, templatePart{literal: s[:i]})
		rest := s[i:]
		end := strings.Index(rest, "}}")
		if end < 0 {
			return nil, templateError(rest)
		}
		name := rest[2:end]
		switch name {
		case "devEui", "fPort", "payloadBase64", "payloadHex", "confirmed", "correlationId":
		default:
			return nil, templateError(rest[:end+2])
		}
		parts = append(parts, templatePart{name: name})
		s = rest[end+2:]
	}
}
func templateError(s string) error {
	if len(s) > 64 {
		s = s[:64]
	}
	return fmt.Errorf("invalid placeholder %q", s)
}
func (t downlinkTemplate) carries(name string) bool {
	for _, p := range t {
		if p.name == name {
			return true
		}
	}
	return false
}
func (t downlinkTemplate) render(v map[string]string, inURL bool) string {
	var b strings.Builder
	query := false
	for _, p := range t {
		if p.name == "" {
			b.WriteString(p.literal)
			if strings.Contains(p.literal, "?") {
				query = true
			}
			continue
		}
		s := v[p.name]
		if inURL {
			if query {
				s = url.QueryEscape(s)
			} else {
				s = url.PathEscape(s)
			}
		}
		b.WriteString(s)
	}
	return b.String()
}

// WebhookSpec is a parsed, validated HTTP downlink configuration.
type WebhookSpec struct {
	Method, ContentType, AuthHeader, AuthPrefix string
	url, body                                   downlinkTemplate
	headers                                     map[string]downlinkTemplate
	baseURL                                     *url.URL
}

// CarriesCorrelation says whether the sender can echo the platform's downlink identifier.
func (s *WebhookSpec) CarriesCorrelation() bool {
	if s == nil {
		return false
	}
	if s.url.carries("correlationId") || s.body.carries("correlationId") {
		return true
	}
	for _, t := range s.headers {
		if t.carries("correlationId") {
			return true
		}
	}
	return false
}

// Render substitutes each token once, escaping URL values for their path or query context.
func (s *WebhookSpec) Render(values DownlinkValues) (RenderedRequest, error) {
	v := map[string]string{"devEui": strings.ToUpper(values.DevEUI), "fPort": strconv.Itoa(values.FPort), "payloadBase64": base64.StdEncoding.EncodeToString(values.Payload), "payloadHex": hex.EncodeToString(values.Payload), "confirmed": strconv.FormatBool(values.Confirmed), "correlationId": values.CorrelationID}
	r := RenderedRequest{URL: s.url.render(v, true), Body: s.body.render(v, false), Headers: make(map[string]string)}
	if _, err := JoinWebhookURL(s.baseURL, r.URL); err != nil {
		return RenderedRequest{}, err
	}
	for k, t := range s.headers {
		val := t.render(v, false)
		if controls(val) {
			return RenderedRequest{}, fmt.Errorf("header %s contains control characters", k)
		}
		r.Headers[k] = val
	}
	return r, nil
}

// JoinWebhookURL appends a template path to the base prefix without changing its authority.
func JoinWebhookURL(base *url.URL, path string) (*url.URL, error) {
	if base == nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "\\") {
		return nil, fmt.Errorf("urlTemplate must be an absolute path without an authority or backslash")
	}
	rel, err := url.Parse(path)
	if err != nil || rel.IsAbs() || rel.Host != "" || rel.User != nil || rel.Fragment != "" || strings.Contains(path, "#") {
		return nil, fmt.Errorf("urlTemplate must not contain an authority, userinfo or fragment")
	}
	decoded := rel.Path
	if strings.Contains(decoded, "\\") {
		return nil, fmt.Errorf("urlTemplate must not contain a backslash")
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == ".." {
			return nil, fmt.Errorf("urlTemplate must not contain ..")
		}
	}
	u, err := url.Parse(strings.TrimRight(base.String(), "/") + path)
	if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("urlTemplate changed baseUrl scheme or host")
	}
	return u, nil
}
func controls(s string) bool { return strings.ContainsFunc(s, unicode.IsControl) }
func headerToken(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}
func forbiddenHeader(s string, auth bool) bool {
	switch strings.ToLower(s) {
	case "host", "content-length", "transfer-encoding", "connection", "content-type", "cookie", "set-cookie":
		return true
	case "authorization", "proxy-authorization":
		return !auth
	}
	return false
}
func parseWebhook(c lorawanConfig, base *url.URL) (*WebhookSpec, error) {
	s := &WebhookSpec{Method: "POST", ContentType: "application/json", AuthHeader: "Authorization", AuthPrefix: "Bearer ", headers: make(map[string]downlinkTemplate), baseURL: base}
	if c.URLTemplate == nil || *c.URLTemplate == "" {
		return nil, fmt.Errorf("urlTemplate is required")
	}
	if len(*c.URLTemplate) > 1024 {
		return nil, fmt.Errorf("urlTemplate exceeds 1024 bytes")
	}
	body := ""
	if c.BodyTemplate != nil {
		body = *c.BodyTemplate
	}
	if len(body) > 8192 {
		return nil, fmt.Errorf("bodyTemplate exceeds 8192 bytes")
	}
	if c.Method != nil {
		s.Method = *c.Method
	}
	switch s.Method {
	case "POST", "PUT", "PATCH":
	default:
		return nil, fmt.Errorf("method must be POST, PUT or PATCH")
	}
	if c.ContentType != nil {
		s.ContentType = *c.ContentType
	}
	switch s.ContentType {
	case "application/json", "text/plain", "application/x-www-form-urlencoded":
	default:
		return nil, fmt.Errorf("unsupported contentType")
	}
	if c.AuthHeader != nil {
		s.AuthHeader = *c.AuthHeader
	}
	if !headerToken(s.AuthHeader) || forbiddenHeader(s.AuthHeader, true) {
		return nil, fmt.Errorf("invalid authHeader")
	}
	if c.AuthPrefix != nil {
		s.AuthPrefix = *c.AuthPrefix
	}
	if utf8.RuneCountInString(s.AuthPrefix) > 32 || controls(s.AuthPrefix) {
		return nil, fmt.Errorf("authPrefix exceeds 32 characters or contains control characters")
	}
	var err error
	s.url, err = tokenize(*c.URLTemplate)
	if err != nil {
		return nil, err
	}
	s.body, err = tokenize(body)
	if err != nil {
		return nil, err
	}
	if !s.url.carries("devEui") && !s.body.carries("devEui") {
		return nil, fmt.Errorf("urlTemplate or bodyTemplate must carry {{devEui}}")
	}
	if len(c.Headers) > 16 {
		return nil, fmt.Errorf("headers exceeds 16 entries")
	}
	seen := make(map[string]bool)
	for k, v := range c.Headers {
		lower := strings.ToLower(k)
		if !headerToken(k) || forbiddenHeader(k, false) || strings.EqualFold(k, s.AuthHeader) || seen[lower] {
			return nil, fmt.Errorf("forbidden or invalid custom header %q", k)
		}
		seen[lower] = true
		if len(v) > 512 || controls(v) {
			return nil, fmt.Errorf("header %s exceeds 512 bytes or contains control characters", k)
		}
		s.headers[k], err = tokenize(v)
		if err != nil {
			return nil, err
		}
	}
	r, err := s.Render(DownlinkValues{DevEUI: "0011223344556677", FPort: 1, Payload: []byte{251, 255}, Confirmed: true, CorrelationID: "dc:00112233445566778899aabbccddeeff"})
	if err != nil {
		return nil, err
	}
	if s.ContentType == "application/json" && r.Body != "" && !json.Valid([]byte(r.Body)) {
		return nil, fmt.Errorf("bodyTemplate does not render valid JSON")
	}
	return s, nil
}
