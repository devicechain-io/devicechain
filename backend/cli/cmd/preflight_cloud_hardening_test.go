// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// recorder is an API server that notes every request it is sent, with whether it carried
// credentials.
type recorder struct {
	mu   sync.Mutex
	seen []string
	auth []string
}

func (r *recorder) note(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, req.Method+" "+req.URL.Path)
	if a := req.Header.Get("Authorization"); a != "" {
		r.auth = append(r.auth, a)
	}
}

func (r *recorder) snapshot() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...), append([]string(nil), r.auth...)
}

const versionJSON = `{"major":"1","minor":"33","gitVersion":"v1.33.1"}`

// 🔴 FINDING 1: a redirect from the API server is never followed. client-go's own client
// follows one, and its auth wrapper puts the bearer token on every hop, so a redirect
// to another host would hand that host the kubeconfig's token.
func TestTheClusterClientFollowsNoRedirect(t *testing.T) {
	var third recorder
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		third.note(r)
		_, _ = io.WriteString(w, versionJSON)
	}))
	defer elsewhere.Close()
	var api recorder
	cluster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.note(r)
		code := http.StatusFound
		if r.Method == http.MethodPost {
			code = http.StatusTemporaryRedirect
		}
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, code)
	}))
	defer cluster.Close()

	cfg := &rest.Config{Host: cluster.URL, BearerToken: "tok-canary-5521"}
	kube, err := cloudKubeClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kube.Discovery().ServerVersion(); err == nil {
		t.Error("a redirected version request succeeded")
	}
	if _, err := kube.AuthorizationV1().SelfSubjectAccessReviews().Create(context.Background(),
		&authorizationv1.SelfSubjectAccessReview{}, metav1.CreateOptions{}); err == nil {
		t.Error("a redirected access review succeeded")
	}
	if seen, auth := third.snapshot(); len(seen) != 0 || len(auth) != 0 {
		t.Fatalf("the redirect target was sent %v, with credentials %v", seen, auth)
	}

	// Through the diagnostics, the redirected answer is advice, never a pass.
	p, err := cloudProbesFrom(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.http = cloudHTTPClient(newCloudFakes(t))
	var rows []installflow.PreflightObservation
	d := &doctor{observe: func(o installflow.PreflightObservation) { rows = append(rows, o) }}
	d.checkCloud(context.Background(), prodTarget, p)
	for _, o := range rows {
		if strings.HasPrefix(o.Text.String(), "API server 127.0.0.1") && o.Verdict == installflow.CheckPass {
			t.Errorf("a redirect reads PASS: %q", o.Text)
		}
	}
	if seen, _ := third.snapshot(); len(seen) != 0 {
		t.Fatalf("the redirect target was sent %v", seen)
	}
}

// A redirect back to the same, permitted path is not followed either: the server sees
// one request, not the ten a following client makes.
func TestARedirectLoopIsNotFollowed(t *testing.T) {
	var n atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Redirect(w, r, srv.URL+"/version", http.StatusFound)
	}))
	defer srv.Close()
	kube, err := cloudKubeClient(context.Background(), &rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = kube.Discovery().ServerVersion()
	if got := n.Load(); got != 1 {
		t.Fatalf("the server saw %d requests for one version probe", got)
	}
}

// 🔴 FINDING 2: the cluster guard lets out exactly the diagnostics' own requests, each to
// its exact path under the configured server, and nothing a suffix, an encoding, a dot
// segment, another host or a proxy subresource dresses up as one.
func TestKubeRequestPermitted(t *testing.T) {
	plain, _ := url.Parse("https://api.example:6443")
	prefixed, _ := url.Parse("https://rancher.example/k8s/clusters/c-1")
	const proxySSAR = "/api/v1/namespaces/default/services/http:hook:80/proxy" + ssarPath
	for _, c := range []struct {
		server *url.URL
		method string
		raw    string
		ok     bool
	}{
		{plain, "GET", "https://api.example:6443/version", true},
		{plain, "POST", "https://api.example:6443" + ssarPath, true},
		{plain, "POST", "https://api.example:6443" + ssrPath, true},
		{prefixed, "POST", "https://rancher.example/k8s/clusters/c-1" + ssarPath, true},
		{prefixed, "GET", "https://rancher.example/k8s/clusters/c-1/version", true},

		{plain, "POST", "https://api.example:6443" + proxySSAR, false},
		{plain, "POST", "https://api.example:6443/k8s/clusters/c-1" + ssarPath, false},
		{prefixed, "POST", "https://rancher.example" + ssarPath, false},
		{prefixed, "GET", "https://rancher.example/version", false},
		{plain, "POST", "https://api.example:6443" + ssarPath + "/x", false},
		{plain, "POST", "https://api.example:6443" + ssarPath + "/", false},
		{plain, "POST", "https://api.example:6443/apis/authorization.k8s.io/v1/../v1/selfsubjectaccessreviews", false},
		{plain, "POST", "https://api.example:6443/apis/authorization.k8s.io%2Fv1/selfsubjectaccessreviews", false},
		{plain, "POST", "https://api.example:6443/apis/authorization.k8s.io/v1/selfsubjectaccess%72eviews", false},
		{plain, "POST", "https://api.example:6443//apis/authorization.k8s.io/v1/selfsubjectaccessreviews", false},
		{plain, "GET", "https://evil.example:6443/version", false},
		{plain, "GET", "https://api.example/version", false},
		{plain, "GET", "https://api.example:6443/api/v1/namespaces/default/secrets", false},
		{plain, "GET", "https://api.example:6443/api/v1/namespaces/default/services/x:80/proxy/version", false},
		{plain, "PUT", "https://api.example:6443" + ssarPath, false},
		{plain, "DELETE", "https://api.example:6443/version", false},
		{plain, "POST", "https://api.example:6443/version", false},
		{plain, "GET", "https://api.example:6443" + ssarPath, false},
	} {
		u, err := url.Parse(c.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := kubeRequestPermitted(c.server, c.method, u); got != c.ok {
			t.Errorf("server %s: %s %s permitted %v, want %v", c.server, c.method, c.raw, got, c.ok)
		}
	}
}

// 🔴 FINDING 3: a credential plugin is never run. client-go runs an exec plugin with no
// way to cancel it, so a hung one would outlive every bound and Exit. The context's
// authentication and permissions read NOT CHECKED, and the API server is asked only
// what it answers without credentials.
func TestACredentialPluginIsNeverRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the plugin is a shell script")
	}
	oldAPI := cloudAPITimeout
	t.Cleanup(func() { cloudAPITimeout = oldAPI })
	cloudAPITimeout = 300 * time.Millisecond

	var api recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.note(r)
		_, _ = io.WriteString(w, versionJSON)
	}))
	defer srv.Close()
	marker := filepath.Join(t.TempDir(), "plugin-ran")
	cfg := &rest.Config{Host: srv.URL, ExecProvider: &clientcmdapi.ExecConfig{
		Command: "sh", Args: []string{"-c", "touch " + marker + "; sleep 30"},
		APIVersion: "client.authentication.k8s.io/v1", InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
	}}
	p, err := cloudProbesFrom(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.http = cloudHTTPClient(newCloudFakes(t))
	var rows []installflow.PreflightObservation
	start := time.Now()
	d := &doctor{observe: func(o installflow.PreflightObservation) {
		if o.Observation == installflow.ObserveCheck {
			rows = append(rows, o)
		}
	}}
	d.checkCloud(context.Background(), prodTarget, p)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the run took %v", took)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the credential plugin ran (%v)", err)
	}
	if r := row(t, rows, "authentication"); r.Verdict != installflow.CheckNotChecked || !strings.Contains(r.Text.String(), "plugin") {
		t.Errorf("authentication reads %s %q", r.Verdict, r.Text)
	}
	for _, o := range rows {
		if strings.HasPrefix(o.Text.String(), "may ") && o.Verdict != installflow.CheckNotChecked {
			t.Errorf("%q reads %s", o.Text, o.Verdict)
		}
	}
	if _, auth := api.snapshot(); len(auth) != 0 {
		t.Errorf("the API server was sent credentials %v", auth)
	}
}

// connCounter counts a test server's connections opened and closed.
type connCounter struct{ opened, closed atomic.Int32 }

func (c *connCounter) track(_ net.Conn, s http.ConnState) {
	switch s {
	case http.StateNew:
		c.opened.Add(1)
	case http.StateClosed, http.StateHijacked:
		c.closed.Add(1)
	}
}

// settled waits for every connection the server saw to be closed.
func (c *connCounter) settled(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.opened.Load() > 0 && c.opened.Load() == c.closed.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d connection(s) opened, %d closed: a probe left one open", c.opened.Load(), c.closed.Load())
}

func countingServer(h http.Handler) (*httptest.Server, *connCounter) {
	c := &connCounter{}
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ConnState = c.track
	srv.Start()
	return srv, c
}

// 🔴 FINDING 4: a probe leaves no connection behind, so Rechecks do not pile idle
// connections (and their goroutines) up. The server holds every connection open as long
// as the client lets it.
func TestTheRegistryProbeLeavesNoConnection(t *testing.T) {
	srv, conns := countingServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	srv.Config.SetKeepAlivesEnabled(true)
	c := cloudHTTPClient(nil)
	for range 3 {
		req, _ := http.NewRequest(http.MethodHead, srv.URL+"/v2/", nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	conns.settled(t)
}

func TestTheClusterClientLeavesNoConnection(t *testing.T) {
	srv, conns := countingServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, versionJSON)
	}))
	defer srv.Close()
	for range 3 { // three Rechecks, three clients
		kube, err := cloudKubeClient(context.Background(), &rest.Config{Host: srv.URL, BearerToken: "t"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := kube.Discovery().ServerVersion(); err != nil {
			t.Fatal(err)
		}
	}
	conns.settled(t)
}

// 🔴 FINDING 5: authentication never reads PASS for a context the API server treats as
// anonymous: one with no credentials, or one SelfSubjectReview says is anonymous. A 403
// on an access review proves nothing about credentials.
func TestAnAnonymousContextIsNeverAuthenticated(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "authorization.k8s.io", Resource: "selfsubjectaccessreviews"},
		"", errors.New("anonymous"))
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "authentication.k8s.io", Resource: "selfsubjectreviews"}, "")
	for _, c := range []struct {
		name  string
		setup func(*cloudFakes)
		pass  bool
	}{
		{"no credentials, reviews forbidden", func(f *cloudFakes) { f.creds, f.ssarErr = credNone, forbidden }, false},
		{"no credentials, reviews answered", func(f *cloudFakes) { f.creds = credNone }, false},
		{"no credentials, review says a user", func(f *cloudFakes) { f.creds, f.ssrUser = credNone, "alice" }, false},
		{"review says anonymous", func(f *cloudFakes) { f.ssrUser = "system:anonymous" }, false},
		{"review says unauthenticated", func(f *cloudFakes) {
			f.ssrUser, f.ssrGroups = "someone", []string{"system:unauthenticated"}
		}, false},
		{"no review, reviews forbidden", func(f *cloudFakes) { f.ssrErr, f.ssarErr = notFound, forbidden }, false},
		{"review says a user", func(*cloudFakes) {}, true},
		// Without the API server's word on who the caller is, an answered access review
		// is not taken as proof: it can be answered for nobody.
		{"no review, reviews answered", func(f *cloudFakes) { f.ssrErr = notFound }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newCloudFakes(t)
			c.setup(f)
			rows, _ := runCloud(t, f, testServer)
			r := row(t, rows, "authentication")
			if (r.Verdict == installflow.CheckPass) != c.pass {
				t.Errorf("authentication reads %s %q", r.Verdict, r.Text)
			}
		})
	}
}

// A server address with a path prefix (an API front-end proxying several clusters) is
// still asked, at its own prefix, and only there.
func TestAPrefixedServerIsAskedUnderItsPrefix(t *testing.T) {
	const prefix = "/k8s/clusters/c-1"
	var api recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.note(r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case prefix + "/version":
			_, _ = io.WriteString(w, versionJSON)
		case prefix + ssarPath:
			_, _ = io.WriteString(w, `{"kind":"SelfSubjectAccessReview","apiVersion":"authorization.k8s.io/v1","status":{"allowed":true}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	kube, err := cloudKubeClient(context.Background(), &rest.Config{Host: srv.URL + prefix})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kube.Discovery().ServerVersion(); err != nil {
		t.Errorf("version under the prefix: %v", err)
	}
	r, err := kube.AuthorizationV1().SelfSubjectAccessReviews().Create(context.Background(),
		&authorizationv1.SelfSubjectAccessReview{}, metav1.CreateOptions{})
	if err != nil || !r.Status.Allowed {
		t.Errorf("access review under the prefix: %v %+v", err, r)
	}
	if seen, _ := api.snapshot(); len(seen) != 2 || seen[0] != "GET "+prefix+"/version" || seen[1] != "POST "+prefix+ssarPath {
		t.Errorf("the server saw %v", seen)
	}
}
