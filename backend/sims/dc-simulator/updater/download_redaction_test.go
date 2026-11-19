// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type capabilityTransport func(*http.Request) (*http.Response, error)

func (f capabilityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDownloadErrorsRedactCapabilityAndKeepClassification(t *testing.T) {
	const target = "https://updates.example/updates/download/fw?cap=private-capability&other=secret"
	sentinel := errors.New("dial failure at " + target)
	box := &grantBox{now: time.Now}
	box.set(grant{url: target, expires: time.Now().Add(time.Hour)})
	source := &httpSource{grants: box, client: &http.Client{Transport: capabilityTransport(func(r *http.Request) (*http.Response, error) {
		return nil, sentinel
	})}}
	_, err := source.ReadAt(make([]byte, 1), 0)
	if err == nil || strings.Contains(err.Error(), "private-capability") || strings.Contains(err.Error(), "?cap") || strings.Contains(err.Error(), "other=secret") {
		t.Fatalf("capability leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "https://updates.example/updates/download/fw") || !errors.Is(err, sentinel) {
		t.Fatalf("lost safe endpoint or cause: %v", err)
	}
	var transport *url.Error
	if !errors.As(err, &transport) {
		t.Fatal("url.Error classification lost")
	}
	for _, invalid := range []string{"http://[invalid?cap=private-capability", "://bad?cap=private-capability"} {
		box.set(grant{url: invalid, expires: time.Now().Add(time.Hour)})
		_, err := source.ReadAt(make([]byte, 1), 0)
		if err == nil || strings.Contains(err.Error(), "private-capability") {
			t.Fatalf("invalid URL leaked: %v", err)
		}
	}
}
