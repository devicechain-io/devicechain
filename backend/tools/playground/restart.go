// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// resetAnnotation is the pod-template annotation a reset stamps. Changing the template
// is what makes Kubernetes roll the Deployment, and a rolled driver starts fresh, so it
// rotates. Nothing reads the value.
const resetAnnotation = "playground.devicechain.io/reset-at"

// Restarter asks the API server to roll the driver Deployment. It is the nightly
// CronJob's whole job; the policy lives in the driver, not here.
type Restarter struct {
	// APIBase is https://host:port of the API server.
	APIBase    string
	Token      string
	Namespace  string
	Deployment string
	Client     *http.Client
}

// Restart patches the Deployment's pod template and fails unless the API server accepted
// it. A restart that "succeeded" without the patch landing would be a night with no
// reset and a green CronJob.
func (r Restarter) Restart(ctx context.Context, now time.Time) error {
	if r.Namespace == "" || r.Deployment == "" || r.APIBase == "" || r.Token == "" {
		return fmt.Errorf("restart needs an API server, a token, a namespace and a deployment")
	}
	body := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`,
		resetAnnotation, now.UTC().Format(time.RFC3339))
	url := fmt.Sprintf("%s/apis/apps/v1/namespaces/%s/deployments/%s", strings.TrimRight(r.APIBase, "/"), r.Namespace, r.Deployment)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader([]byte(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/strategic-merge-patch+json")
	c := r.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("patching deployment %s/%s: %s: %s", r.Namespace, r.Deployment, resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// InClusterRestarter reads the pod's own service account.
func InClusterRestarter(deployment string) (Restarter, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		return Restarter{}, errNotInCluster
	}
	tok, err := os.ReadFile(saDir + "/token")
	if err != nil {
		return Restarter{}, err
	}
	ns, err := os.ReadFile(saDir + "/namespace")
	if err != nil {
		return Restarter{}, err
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return Restarter{}, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return Restarter{}, fmt.Errorf("no certificate in %s/ca.crt", saDir)
	}
	return Restarter{
		APIBase:    "https://" + hostPort(host, port),
		Token:      strings.TrimSpace(string(tok)),
		Namespace:  strings.TrimSpace(string(ns)),
		Deployment: deployment,
		Client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		}},
	}, nil
}

func hostPort(host, port string) string {
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" {
		return host
	}
	return host + ":" + port
}
