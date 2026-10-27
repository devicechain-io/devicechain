// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"os"
	"time"
)

// NewHTTPClient builds the client the download uses. TLS is always verified: the instance CA, if
// given, is added to the system roots, and there is no way to switch verification off. A
// redirect is followed only if it passes the same origin pin as the grant did, so a server cannot
// bounce the device to another host.
func NewHTTPClient(caFile, serverName string, origin *url.URL, allowInsecure bool) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("no certificate found in " + caFile)
		}
	}
	tr := &http.Transport{
		TLSClientConfig:       &tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS12},
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 3 {
				return errors.New("too many redirects")
			}
			// The redirect target is not a grant, so apply the origin and scheme rules, not the path rule.
			t := *req.URL
			t.Path = downloadPathFragment
			if err := CheckGrantURL(origin, allowInsecure, t.String()); err != nil {
				return errors.New("redirect refused: " + err.Error())
			}
			return nil
		},
	}, nil
}
