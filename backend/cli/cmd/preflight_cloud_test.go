// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

// canaryErr is an error whose text a row must never quote: a client-go error can carry a
// URL with userinfo, a bearer token, or a terminal escape.
const canaryErr = "token=s3cr3t-canary-4417 https://admin:hunter2@api.example \x1b]0;pwned\x07"

// cloudFakes is a cluster and a network as the advisory diagnostics see them.
type cloudFakes struct {
	kube *fake.Clientset

	mu      sync.Mutex
	methods []string // every HTTP request the registry probes sent, as "METHOD url"

	lookupErr  error
	versionErr error
	ssarErr    error
	// creds is the context's kind of credentials (credStatic when unset); ssrUser,
	// ssrGroups and ssrErr are what a SelfSubjectReview answers (alice when unset).
	creds     credKind
	ssrUser   string
	ssrGroups []string
	ssrErr    error
	// ssrEmpty makes the SelfSubjectReview answer with no user at all.
	ssrEmpty bool
	// allow decides each access review; nil allows everything.
	allow   func(*authorizationv1.ResourceAttributes) (allowed bool, evalErr string)
	httpErr map[string]error // by host
	status  map[string]int   // by host; 200 when absent
	// hang makes every probe block until its context is done, or until release (then
	// succeed): a probe that is not bounded reads as a pass.
	hang    bool
	release chan struct{}
	once    sync.Once
}

// releaseAll lets every hung probe go.
func (f *cloudFakes) releaseAll() { f.once.Do(func() { close(f.release) }) }

func newCloudFakes(t *testing.T) *cloudFakes {
	f := &cloudFakes{kube: fake.NewClientset(), httpErr: map[string]error{}, status: map[string]int{},
		release: make(chan struct{})}
	t.Cleanup(f.releaseAll)
	f.kube.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{Major: "1", Minor: "33", GitVersion: "v1.33.1"}
	f.kube.PrependReactor("get", "version", func(ktesting.Action) (bool, runtime.Object, error) {
		f.block()
		return true, nil, f.versionErr
	})
	f.kube.PrependReactor("create", "selfsubjectreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		f.block()
		if f.ssrErr != nil {
			return true, nil, f.ssrErr
		}
		r := a.(ktesting.CreateAction).GetObject().(*authenticationv1.SelfSubjectReview).DeepCopy()
		r.Status.UserInfo.Username, r.Status.UserInfo.Groups = f.ssrUser, f.ssrGroups
		if r.Status.UserInfo.Username == "" && !f.ssrEmpty {
			r.Status.UserInfo.Username = "alice"
		}
		return true, r, nil
	})
	f.kube.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		f.block()
		if f.ssarErr != nil {
			return true, nil, f.ssarErr
		}
		r := a.(ktesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview).DeepCopy()
		r.Status.Allowed = true
		if f.allow != nil {
			r.Status.Allowed, r.Status.EvaluationError = f.allow(r.Spec.ResourceAttributes)
		}
		return true, r, nil
	})
	return f
}

// block is a hung server: it answers only when released, long after any bound.
func (f *cloudFakes) block() {
	if !f.hang {
		return
	}
	select {
	case <-f.release:
	case <-time.After(3 * time.Second):
	}
}

func (f *cloudFakes) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.methods = append(f.methods, r.Method+" "+r.URL.String())
	f.mu.Unlock()
	if f.hang {
		// Ignores the request's context, as a stuck dial can: only within() bounds it.
		f.block()
	}
	if err := f.httpErr[r.URL.Hostname()]; err != nil {
		return nil, err
	}
	code := f.status[r.URL.Hostname()]
	if code == 0 {
		code = http.StatusOK
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
}

func (f *cloudFakes) probes(server string) func(context.Context, cloudTarget) (cloudProbes, error) {
	return func(context.Context, cloudTarget) (cloudProbes, error) {
		return cloudProbes{server: server, kube: f.kube, creds: f.creds, http: cloudHTTPClient(f), tofuRegistry: "registry.opentofu.org",
			lookupHost: func(ctx context.Context, host string) ([]string, error) {
				if f.hang {
					f.block() // ignores ctx
				}
				if f.lookupErr != nil {
					return nil, f.lookupErr
				}
				return []string{"203.0.113.7"}, nil
			}}, nil
	}
}

// prodTarget is a Standard plan: every optional component of the cluster root.
var prodTarget = cloudTarget{KubeContext: "prod", ImageRegistry: bootstrap.DefaultImageRegistry,
	CertManager: true, CNPG: true, Monitoring: true}

// runCloud runs the advisory diagnostics against the fakes and returns its check rows.
func runCloud(t *testing.T, f *cloudFakes, server string) ([]installflow.PreflightObservation, installflow.PreflightResult) {
	t.Helper()
	rows, res, _ := runCloudTimed(t, f, server)
	return rows, res
}

// runCloudTimed is runCloud, with how long the run itself took. A hung fake is released
// before the no-mutation assertion, which reads the fake's log.
func runCloudTimed(t *testing.T, f *cloudFakes, server string) ([]installflow.PreflightObservation, installflow.PreflightResult, time.Duration) {
	t.Helper()
	var rows []installflow.PreflightObservation
	start := time.Now()
	r := cloudDiagnosticsRunner(prodTarget, f.probes(server))
	res, err := r.RunPreflight(context.Background(), func(o installflow.PreflightObservation) {
		if o.Observation == installflow.ObserveSection && o.Section != installplain.SectionCloud {
			t.Errorf("section %q", o.Section)
		}
		if o.Observation == installflow.ObserveCheck {
			rows = append(rows, o)
		}
	})
	took := time.Since(start)
	f.releaseAll()
	if err != nil {
		t.Fatalf("the advisory runner errored: %v", err)
	}
	if res.Fails != 0 {
		t.Fatalf("the advisory runner counted %d failure(s)", res.Fails)
	}
	for _, o := range rows {
		if o.Verdict == installflow.CheckFail {
			t.Errorf("an advisory row reads FAIL: %q", o.Text)
		}
		for _, s := range []string{o.Text.String(), o.Fix.String()} {
			if strings.Contains(s, "s3cr3t") || strings.Contains(s, "hunter2") || strings.Contains(s, "\x1b") {
				t.Errorf("a row quotes an error: %q", s)
			}
		}
	}
	assertReadOnly(t, f)
	return rows, res, took
}

// 🔴 THE NO-MUTATION ASSERTION: the fake cluster saw only reads and access reviews, and
// the fake network only HEAD and GET.
func assertReadOnly(t *testing.T, f *cloudFakes) {
	t.Helper()
	for _, a := range f.kube.Actions() {
		switch a.GetVerb() {
		case "get", "list", "watch":
		case "create":
			if r := a.GetResource().Resource; r != "selfsubjectaccessreviews" && r != "selfsubjectreviews" {
				t.Errorf("the diagnostics created %s", a.GetResource().Resource)
			}
		default:
			t.Errorf("the diagnostics sent %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.methods {
		if !strings.HasPrefix(m, "HEAD ") && !strings.HasPrefix(m, "GET ") {
			t.Errorf("a registry probe sent %s", m)
		}
	}
}

// row is the one row whose text starts with prefix.
func row(t *testing.T, rows []installflow.PreflightObservation, prefix string) installflow.PreflightObservation {
	t.Helper()
	for _, o := range rows {
		if strings.HasPrefix(o.Text.String(), prefix) {
			return o
		}
	}
	var texts []string
	for _, o := range rows {
		texts = append(texts, string(o.Verdict)+" "+o.Text.String())
	}
	t.Fatalf("no row starting %q in:\n%s", prefix, strings.Join(texts, "\n"))
	return installflow.PreflightObservation{}
}

const testServer = "https://api.prod.example:6443"

func TestCloudDiagnosticsOnAHealthyCluster(t *testing.T) {
	f := newCloudFakes(t)
	rows, res := runCloud(t, f, testServer)
	for _, prefix := range []string{"API server name api.prod.example resolves", "API server api.prod.example answered over verified TLS",
		"authentication: the API server accepts this context's credentials", "may create namespaces", "may create secrets",
		"may create customresourcedefinitions.apiextensions.k8s.io", "OpenTofu provider registry registry.opentofu.org reachable",
		"cert-manager chart repository charts.jetstack.io reachable"} {
		if r := row(t, rows, prefix); r.Verdict != installflow.CheckPass {
			t.Errorf("%q reads %s", prefix, r.Verdict)
		}
	}
	// Reaching the image registry from here is not the nodes reaching it: a fact.
	if r := row(t, rows, "image registry ghcr.io"); r.Verdict != installflow.CheckInfo {
		t.Errorf("the image registry reads %s", r.Verdict)
	}
	if res.Warns != 0 {
		t.Errorf("%d warning(s) on a healthy cluster", res.Warns)
	}
	if n := len(f.kube.Actions()); n != 3+len(cloudAccessNeeds) {
		t.Errorf("%d cluster requests, want the version, the self review, %d access reviews and the IngressClass list", n, len(cloudAccessNeeds))
	}
}

func TestCloudDiagnosticsAPushRegistryIsAVerdict(t *testing.T) {
	f := newCloudFakes(t)
	var rows []installflow.PreflightObservation
	r := cloudDiagnosticsRunner(cloudTarget{KubeContext: "prod", ImageRegistry: "registry.example/team", Build: true}, f.probes(testServer))
	_, _ = r.RunPreflight(context.Background(), func(o installflow.PreflightObservation) { rows = append(rows, o) })
	if r := row(t, rows, "image registry registry.example reachable"); r.Verdict != installflow.CheckPass {
		t.Errorf("the push registry reads %s", r.Verdict)
	}
}

// 🔴 THE NAMED MUTANT: an error reported as PASS. Every way each probe can fail reads
// WARN, UNKNOWN or NOT CHECKED, quotes none of the error, and never fails the run.
func TestCloudDiagnosticsErrorsAreNeverPass(t *testing.T) {
	gr := schema.GroupResource{Group: "authorization.k8s.io", Resource: "selfsubjectaccessreviews"}
	cases := []struct {
		name   string
		setup  func(*cloudFakes)
		rows   []string // the rows that must not pass
		verdit installflow.CheckVerdict
	}{
		{"dns not found", func(f *cloudFakes) {
			f.lookupErr = &net.DNSError{Err: "no such host " + canaryErr, Name: "api.prod.example", IsNotFound: true}
		}, []string{"API server name"}, installflow.CheckWarn},
		{"dns timeout", func(f *cloudFakes) {
			f.lookupErr = &net.DNSError{Err: canaryErr, IsTimeout: true}
		}, []string{"API server name"}, installflow.CheckUnknown},
		{"dns other", func(f *cloudFakes) { f.lookupErr = errors.New(canaryErr) }, []string{"API server name"}, installflow.CheckUnknown},
		{"tls untrusted", func(f *cloudFakes) {
			f.versionErr = x509.UnknownAuthorityError{}
		}, []string{"API server api.prod.example presented", "authentication", "may create"}, ""},
		{"refused", func(f *cloudFakes) {
			f.versionErr = &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
		}, []string{"API server api.prod.example refused", "authentication", "may create"}, ""},
		{"api other", func(f *cloudFakes) { f.versionErr = errors.New(canaryErr) },
			[]string{"API server api.prod.example could not", "authentication", "may create"}, ""},
		{"version unauthorized", func(f *cloudFakes) {
			f.versionErr = apierrors.NewUnauthorized(canaryErr)
		}, []string{"authentication", "may create"}, ""},
		{"reviews unauthorized", func(f *cloudFakes) {
			f.ssarErr = apierrors.NewUnauthorized(canaryErr)
		}, []string{"authentication: the API server refused", "may create"}, ""},
		{"reviews forbidden", func(f *cloudFakes) {
			f.ssarErr = apierrors.NewForbidden(gr, "", errors.New(canaryErr))
		}, []string{"may create"}, installflow.CheckUnknown},
		{"reviews other", func(f *cloudFakes) { f.ssarErr, f.ssrErr = errors.New(canaryErr), errors.New(canaryErr) },
			[]string{"authentication", "may create"}, installflow.CheckUnknown},
		{"review denied", func(f *cloudFakes) {
			f.allow = func(a *authorizationv1.ResourceAttributes) (bool, string) { return a.Resource != "secrets", "" }
		}, []string{"may create secrets"}, installflow.CheckWarn},
		{"review undecided", func(f *cloudFakes) {
			f.allow = func(*authorizationv1.ResourceAttributes) (bool, string) { return false, canaryErr }
		}, []string{"may create"}, installflow.CheckUnknown},
		{"registry unresolved", func(f *cloudFakes) {
			f.httpErr["charts.jetstack.io"] = &net.DNSError{Err: canaryErr, IsNotFound: true}
		}, []string{"cert-manager chart repository"}, installflow.CheckWarn},
		{"registry other", func(f *cloudFakes) { f.httpErr["registry.opentofu.org"] = errors.New(canaryErr) },
			[]string{"OpenTofu provider registry"}, installflow.CheckUnknown},
		{"registry 503", func(f *cloudFakes) { f.status["ghcr.io"] = 503 },
			[]string{"CloudNativePG chart registry", "image registry"}, installflow.CheckWarn},
		{"image registry down", func(f *cloudFakes) {
			f.httpErr["ghcr.io"] = &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
		}, []string{"image registry"}, installflow.CheckWarn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newCloudFakes(t)
			c.setup(f)
			rows, _ := runCloud(t, f, testServer)
			for _, prefix := range c.rows {
				matched := false
				for _, o := range rows {
					if !strings.HasPrefix(o.Text.String(), prefix) {
						continue
					}
					matched = true
					switch o.Verdict {
					case installflow.CheckWarn, installflow.CheckUnknown, installflow.CheckNotChecked:
					default:
						t.Errorf("%q reads %s after %s", o.Text, o.Verdict, c.name)
					}
					if c.verdit != "" && o.Verdict != c.verdit {
						t.Errorf("%q reads %s, want %s", o.Text, o.Verdict, c.verdit)
					}
				}
				if !matched {
					t.Errorf("no row starting %q", prefix)
				}
			}
		})
	}
}

// The context's TLS posture: an unverified or plain-HTTP answer is a warning, never a pass.
func TestCloudDiagnosticsUnverifiedTLSWarns(t *testing.T) {
	for _, tc := range []struct {
		server   string
		insecure bool
		want     string
	}{
		{"http://api.prod.example:8080", false, "without TLS"},
		{testServer, true, "does not verify"},
	} {
		f := newCloudFakes(t)
		probes := func(ctx context.Context, c cloudTarget) (cloudProbes, error) {
			p, err := f.probes(tc.server)(ctx, c)
			p.insecure = tc.insecure
			return p, err
		}
		var rows []installflow.PreflightObservation
		_, _ = cloudDiagnosticsRunner(prodTarget, probes).RunPreflight(context.Background(),
			func(o installflow.PreflightObservation) { rows = append(rows, o) })
		if r := row(t, rows, "API server api.prod.example answered"); r.Verdict != installflow.CheckWarn ||
			!strings.Contains(r.Text.String(), tc.want) {
			t.Errorf("%s insecure=%v: %s %q", tc.server, tc.insecure, r.Verdict, r.Text)
		}
	}
}

// An IP address needs no name lookup, and a proxied API server is resolved by the proxy:
// neither is a pass.
func TestCloudDiagnosticsNameRowsThatAreNotChecks(t *testing.T) {
	f := newCloudFakes(t)
	rows, _ := runCloud(t, f, "https://10.0.0.1:6443")
	if r := row(t, rows, "API server name"); r.Verdict != installflow.CheckNotNeeded {
		t.Errorf("an IP server reads %s", r.Verdict)
	}
	probes := func(ctx context.Context, c cloudTarget) (cloudProbes, error) {
		p, err := f.probes(testServer)(ctx, c)
		p.proxied = true
		return p, err
	}
	var got []installflow.PreflightObservation
	_, _ = cloudDiagnosticsRunner(prodTarget, probes).RunPreflight(context.Background(),
		func(o installflow.PreflightObservation) { got = append(got, o) })
	if r := row(t, got, "API server name"); r.Verdict != installflow.CheckNotChecked {
		t.Errorf("a proxied server reads %s", r.Verdict)
	}
}

// A context that cannot be loaded is one UNKNOWN row, not a failure and not an error.
func TestCloudDiagnosticsUnloadableContextIsUnknown(t *testing.T) {
	var rows []installflow.PreflightObservation
	res, err := cloudDiagnosticsRunner(prodTarget, func(context.Context, cloudTarget) (cloudProbes, error) {
		return cloudProbes{}, errors.New(canaryErr)
	}).RunPreflight(context.Background(), func(o installflow.PreflightObservation) {
		if o.Observation == installflow.ObserveCheck {
			rows = append(rows, o)
		}
	})
	if err != nil || res.Fails != 0 || len(rows) != 1 || rows[0].Verdict != installflow.CheckUnknown ||
		strings.Contains(rows[0].Text.String(), "s3cr3t") {
		t.Fatalf("err %v, result %+v, rows %+v", err, res, rows)
	}
}

// 🔴 THE NAMED MUTANT: an unbounded timeout. Every probe hangs, ignoring its context; the
// run still ends within its bounds, and each hung probe reads UNKNOWN or NOT CHECKED. An
// unbounded probe would wait for the release and then read PASS.
func TestCloudDiagnosticsHungProbesAreBounded(t *testing.T) {
	oldConnect, oldAPI := cloudConnectTimeout, cloudAPITimeout
	t.Cleanup(func() { cloudConnectTimeout, cloudAPITimeout = oldConnect, oldAPI })
	cloudConnectTimeout, cloudAPITimeout = 30*time.Millisecond, 60*time.Millisecond
	oldJoin := probeJoinDelay
	t.Cleanup(func() { probeJoinDelay = oldJoin })
	probeJoinDelay = 10 * time.Millisecond // the fakes ignore their cancel

	f := newCloudFakes(t)
	f.hang = true
	rows, _, took := runCloudTimed(t, f, testServer)
	if took > 2*time.Second {
		t.Errorf("hung probes took %v", took)
	}
	for _, o := range rows {
		if o.Verdict == installflow.CheckPass || o.Verdict == installflow.CheckInfo {
			t.Errorf("a hung probe reads %s: %q", o.Verdict, o.Text)
		}
	}
	if r := row(t, rows, "API server api.prod.example"); r.Verdict != installflow.CheckUnknown {
		t.Errorf("a hung API server reads %s", r.Verdict)
	}
}

// The access reviews share one budget: a server that stalls on every review costs one
// bound, not one per review.
func TestCloudDiagnosticsReviewsShareOneBudget(t *testing.T) {
	oldAPI := cloudAPITimeout
	t.Cleanup(func() { cloudAPITimeout = oldAPI })
	cloudAPITimeout = 100 * time.Millisecond
	oldJoin := probeJoinDelay
	t.Cleanup(func() { probeJoinDelay = oldJoin })
	probeJoinDelay = 10 * time.Millisecond // the fake ignores its cancel
	f := newCloudFakes(t)
	f.kube.PrependReactor("create", "selfsubjectreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		f.block()
		if f.ssrErr != nil {
			return true, nil, f.ssrErr
		}
		r := a.(ktesting.CreateAction).GetObject().(*authenticationv1.SelfSubjectReview).DeepCopy()
		r.Status.UserInfo.Username, r.Status.UserInfo.Groups = f.ssrUser, f.ssrGroups
		if r.Status.UserInfo.Username == "" && !f.ssrEmpty {
			r.Status.UserInfo.Username = "alice"
		}
		return true, r, nil
	})
	f.kube.PrependReactor("create", "selfsubjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		select {
		case <-f.release:
		case <-time.After(3 * time.Second):
		}
		return true, nil, errors.New("late")
	})
	rows, _, took := runCloudTimed(t, f, "https://10.0.0.1:6443")
	if took > time.Second {
		t.Errorf("stalled reviews took %v", took)
	}
	if r := row(t, rows, "may create secrets"); r.Verdict != installflow.CheckUnknown {
		t.Errorf("a stalled review reads %s", r.Verdict)
	}
}

// The bounds are the spec's: 5 s to connect, 15 s for an answer, carried by the REST
// config and the registry client the diagnostics really use.
func TestCloudDiagnosticsBounds(t *testing.T) {
	if cloudConnectTimeout != 5*time.Second || cloudAPITimeout != 15*time.Second {
		t.Fatalf("bounds %v / %v", cloudConnectTimeout, cloudAPITimeout)
	}
	c, server, err := cloudKubeConfig(context.Background(), &rest.Config{Host: testServer, BearerToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	kt, ok := c.Transport.(*http.Transport)
	if c.Timeout != cloudAPITimeout || c.WrapTransport == nil || server.Host != "api.prod.example:6443" || !ok ||
		kt.DialContext == nil || kt.TLSHandshakeTimeout != cloudConnectTimeout || kt.ResponseHeaderTimeout != cloudAPITimeout ||
		!kt.DisableKeepAlives || kt.Proxy != nil {
		t.Errorf("rest config timeout %v, wrapped %v, server %v, transport %+v", c.Timeout, c.WrapTransport != nil, server, c.Transport)
	}
	h := cloudHTTPClient(nil)
	if h.Timeout != cloudAPITimeout {
		t.Errorf("registry client timeout %v", h.Timeout)
	}
	tr, ok := h.Transport.(readOnlyTransport).next.(*http.Transport)
	if !ok || tr.TLSHandshakeTimeout != cloudConnectTimeout || tr.ResponseHeaderTimeout != cloudAPITimeout || tr.Proxy != nil ||
		!tr.DisableKeepAlives || h.CheckRedirect == nil {
		t.Errorf("registry transport %+v", h.Transport)
	}
}

// 🔴 THE NAMED MUTANT: a mutating verb allowed. The registry guard lets out reads only
// (the cluster guard is TestKubeRequestPermitted).
func TestReadOnlyRequest(t *testing.T) {
	for m, ok := range map[string]bool{"GET": true, "HEAD": true, "POST": false, "PUT": false, "PATCH": false,
		"DELETE": false, "OPTIONS": false, "CONNECT": false} {
		if got := readOnlyRequest(m); got != ok {
			t.Errorf("%s: allowed %v, want %v", m, got, ok)
		}
	}
}

// The guard is on the REST config the diagnostics build: a create of a Pod or a Secret
// through it never reaches the API server, while an access review does.
func TestTheDiagnosticsRestConfigCannotCreate(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"kind":"SelfSubjectAccessReview","apiVersion":"authorization.k8s.io/v1","status":{"allowed":true}}`)
	}))
	defer srv.Close()
	kube, err := cloudKubeClient(context.Background(), &rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, perr := kube.CoreV1().Pods("default").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}}, metav1.CreateOptions{})
	_, serr := kube.CoreV1().Secrets("default").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s"}}, metav1.CreateOptions{})
	derr := kube.CoreV1().Namespaces().Delete(ctx, "default", metav1.DeleteOptions{})
	if !errors.Is(perr, errMutationRefused) || !errors.Is(serr, errMutationRefused) || !errors.Is(derr, errMutationRefused) {
		t.Errorf("writes got %v / %v / %v", perr, serr, derr)
	}
	if _, err := kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{}, metav1.CreateOptions{}); err != nil {
		t.Errorf("an access review was refused: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "POST "+ssarPath {
		t.Errorf("the server saw %v", seen)
	}
}

// The registry client refuses anything but a read, before it leaves.
func TestTheRegistryClientCannotWrite(t *testing.T) {
	f := newCloudFakes(t)
	c := cloudHTTPClient(f)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, _ := http.NewRequest(m, "https://ghcr.io/v2/x/blobs/uploads/", nil)
		if _, err := c.Do(req); !errors.Is(err, errMutationRefused) {
			t.Errorf("%s: %v", m, err)
		}
	}
	if len(f.methods) != 0 {
		t.Errorf("a write reached the network: %v", f.methods)
	}
}

// The wizard asks an existing cluster the advisory questions, and nothing else does: a
// kind target and --skip-preflight have none, and the run's own preflight is the doctor.
func TestOnlyTheWizardOfAnExistingClusterHasAdvisoryChecks(t *testing.T) {
	saveChoiceFlags(t)
	provider, err := bootstrap.Get("local")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"local", "--kube-context", "prod", "--version", "v1.12.0"}, true},
		{[]string{"local", "--kube-context", "prod", "--skip-preflight", "--version", "v1.12.0"}, false},
		{[]string{"local", "--version", "v1.12.0"}, false},
	} {
		saveChoiceFlags(t)
		c := parseChoices(t, tc.args)
		prepared, err := c.Prepare(Version, nil)
		if err != nil {
			t.Fatal(err)
		}
		if w := installWizardFor(provider, c, prepared); (w.Advisory != nil) != tc.want {
			t.Errorf("%v: advisory %v", tc.args, w.Advisory != nil)
		}
	}
}
