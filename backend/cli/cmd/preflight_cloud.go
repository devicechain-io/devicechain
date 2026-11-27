// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// The advisory cloud diagnostics: read-only questions about an EXISTING cluster that the
// doctor's checks do not ask (does the API server's name resolve, does it answer over a
// certificate this context trusts, are these credentials accepted, may they create what
// the install creates, and can this machine reach the registries the install downloads
// from). They are the doctor's checks, recorded through the doctor, but they are a
// separate runner that only the terminal UI's preflight screen runs:
//
//   - 🔴 THEY ARE NEVER A FAILURE. They have no d.fail; a problem they find is WARN, and
//     anything they cannot decide is UNKNOWN or NOT CHECKED, never PASS. The plain
//     report, `dcctl preflight` and the engine's own preflight stage never run them, so
//     no verdict on the legacy path moves.
//   - 🔴 THEY CHANGE NOTHING. Every request leaves through readOnlyTransport, which lets
//     out GET and HEAD, and POST only to the SelfSubjectAccessReview endpoint, which
//     asks the API server a question and stores nothing. They can never create a Pod or
//     a Secret.
//   - 🔴 THEY ARE BOUNDED: a connection gets cloudConnectTimeout, an API or registry
//     answer cloudAPITimeout, and each probe runs under within(), so a hung server or a
//     credential plugin that never returns costs a bounded wait and reads UNKNOWN.
//   - 🔴 THEY NEVER QUOTE AN ERROR. A client-go or net/http error can carry a URL with
//     userinfo or a token; a probe's row is dcctl's own sentence for the CLASS of
//     error (classifyProbeErr), and the hosts it names are the kubeconfig's server
//     without userinfo and dcctl's own registry list.

// The bounds the spec sets for the advisory diagnostics. Variables only so a test can
// make a hung probe give up in milliseconds; production never reassigns them.
var (
	cloudConnectTimeout = 5 * time.Second
	cloudAPITimeout     = 15 * time.Second
)

// cloudTarget is what the advisory diagnostics look at: the existing cluster the install
// targets and the registries it downloads from.
type cloudTarget struct {
	KubeContext string
	// ImageRegistry is the image registry the run uses (ghcr.io/devicechain-io, or the
	// --build push registry); Build says this machine pushes to it.
	ImageRegistry string
	Build         bool
}

// cloudProbes are the diagnostics' only windows onto the network and the cluster, so a
// test can present any server with fakes.
type cloudProbes struct {
	// server is the API server's URL, without userinfo; insecure is the context's
	// insecure-skip-tls-verify; proxied says a proxy stands between this machine and the
	// API server, which then resolves the name itself.
	server   string
	insecure bool
	proxied  bool

	lookupHost func(ctx context.Context, host string) ([]string, error)
	kube       kubernetes.Interface
	http       *http.Client
	// tofuRegistry is the provider registry the IaC binary on PATH downloads from.
	tofuRegistry string
}

// cloudDiagnosticsRunner is the advisory diagnostics as a preflight runner, built for
// one target. probes is called once per run, so a Recheck reads the kubeconfig again.
// Its result never carries a failure.
func cloudDiagnosticsRunner(t cloudTarget, probes func(cloudTarget) (cloudProbes, error)) installflow.PreflightRunner {
	return installflow.PreflightFunc(func(ctx context.Context, observe func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		d := &doctor{observe: observe}
		d.section(installplain.SectionCloud)
		p, err := probes(t)
		if err != nil {
			// The doctor's kube-context check has already said why, in dcctl's words.
			d.unknown("the advisory cluster checks could not load --kube-context "+t.KubeContext,
				"see the Kubernetes contexts check above, fix the kubeconfig, then Recheck")
			return installflow.PreflightResult{Warns: d.warns}, nil
		}
		d.checkCloud(ctx, t, p)
		// 🔴 Never a failure, whatever a probe recorded.
		return installflow.PreflightResult{Warns: d.warns}, nil
	})
}

func (d *doctor) unknown(msg, fix string)    { d.check(installflow.CheckUnknown, msg, fix) }
func (d *doctor) notChecked(msg, fix string) { d.check(installflow.CheckNotChecked, msg, fix) }

// checkCloud runs every advisory probe, in the order the rows are read.
func (d *doctor) checkCloud(ctx context.Context, t cloudTarget, p cloudProbes) {
	host := serverHost(p.server)
	d.checkAPIName(ctx, p, host)
	answered := d.checkAPIAnswers(ctx, p, host, t.KubeContext)
	d.checkAccess(ctx, p, answered)
	d.checkRegistries(ctx, t, p)
}

// ---- API server name ----------------------------------------------------------

func (d *doctor) checkAPIName(ctx context.Context, p cloudProbes, host string) {
	switch {
	case host == "":
		d.unknown("the API server address of this context could not be read", "check the context's cluster server entry")
		return
	case net.ParseIP(host) != nil:
		d.check(installflow.CheckNotNeeded, "API server name: the address "+host+" is an IP address", "")
		return
	case p.proxied:
		d.notChecked("API server name "+host+": a proxy resolves it, not this machine", "")
		return
	}
	_, err := within(ctx, cloudConnectTimeout, func(ctx context.Context) ([]string, error) {
		return p.lookupHost(ctx, host)
	})
	if err == nil {
		d.pass("API server name " + host + " resolves")
		return
	}
	switch classifyProbeErr(err) {
	case probeNoSuchHost:
		d.warn("API server name "+host+" does not resolve from this machine",
			"check the context's server address, your DNS, or the VPN the cluster's API is reached through")
	case probeTimeout:
		d.unknown("API server name "+host+" did not resolve within "+cloudConnectTimeout.String(),
			"check your DNS, then Recheck")
	default:
		d.unknown("API server name "+host+" could not be resolved", "check your DNS, then Recheck")
	}
}

// ---- API server answers, over TLS -----------------------------------------------

// apiAnswer is how the API server answered the version request.
type apiAnswer uint8

const (
	apiSilent       apiAnswer = iota // no answer: nothing further can be asked
	apiAnswered                      // answered, so the access reviews can be asked
	apiUnauthorized                  // refused the credentials
)

func (d *doctor) checkAPIAnswers(ctx context.Context, p cloudProbes, host, kubeContext string) apiAnswer {
	_, err := within(ctx, cloudAPITimeout, func(context.Context) (struct{}, error) {
		_, err := p.kube.Discovery().ServerVersion()
		return struct{}{}, err
	})
	what := "API server " + host
	// A refusal of the credentials is still an answer, over the same TLS: the
	// authentication row says what it means.
	unauthorized := err != nil && classifyProbeErr(err) == probeUnauthorized
	if err == nil || unauthorized {
		switch {
		case strings.HasPrefix(p.server, "http://"):
			d.warn(what+" answered, without TLS", "use an https:// server address for this context")
		case p.insecure:
			d.warn(what+" answered, but this context does not verify its TLS certificate",
				"remove insecure-skip-tls-verify from the context and give it the cluster's certificate-authority")
		default:
			d.pass(what + " answered over verified TLS")
		}
		if unauthorized {
			return apiUnauthorized
		}
		return apiAnswered
	}
	switch classifyProbeErr(err) {
	case probeTLS:
		d.warn(what+" presented a TLS certificate this context does not trust",
			"refresh the context's certificate-authority, for example with your provider's get-credentials command")
	case probeNoSuchHost:
		d.warn(what+" could not be reached: its name does not resolve", "check the context's server address and your DNS")
	case probeRefused:
		d.warn(what+" refused the connection", "check the context's server address, and that the API is reachable from this network")
	case probeTimeout:
		d.unknown(what+" did not answer within "+cloudAPITimeout.String(),
			"check the network path to the API server (VPN, firewall, authorised networks), then Recheck")
	default:
		d.unknown(what+" could not be reached", "run kubectl --context "+kubeContext+" version to see why, then Recheck")
	}
	return apiSilent
}

// ---- authentication and RBAC ------------------------------------------------------

// cloudAccess is one permission the install needs, asked of the API server through a
// SelfSubjectAccessReview: a question, not the action.
type cloudAccess struct {
	verb, group, resource string
}

func (a cloudAccess) String() string {
	r := a.resource
	if a.group != "" {
		r += "." + a.group
	}
	return a.verb + " " + r
}

// cloudAccessNeeds is what installing the shared prerequisites creates, cluster-wide:
// namespaces, the operators' CRDs and cluster roles, their deployments and secrets.
var cloudAccessNeeds = []cloudAccess{
	{"create", "", "namespaces"},
	{"create", "apiextensions.k8s.io", "customresourcedefinitions"},
	{"create", "rbac.authorization.k8s.io", "clusterroles"},
	{"create", "rbac.authorization.k8s.io", "clusterrolebindings"},
	{"create", "apps", "deployments"},
	{"create", "", "secrets"},
}

const accessFix = "installing the shared prerequisites needs cluster-admin on this cluster; " +
	"use a context that has it, or ask the cluster's administrator"

type accessResult struct {
	allowed, denied bool
	evalErr         bool
	err             error
}

func (d *doctor) checkAccess(ctx context.Context, p cloudProbes, answered apiAnswer) {
	if answered == apiSilent {
		d.notChecked("authentication: not asked, the API server did not answer", "")
		for _, a := range cloudAccessNeeds {
			d.notChecked("may "+a.String()+": not asked, the API server did not answer", "")
		}
		return
	}
	// One budget for every review: they are cheap, and a server that stalls on the first
	// would otherwise cost the budget once per review.
	budget, cancel := context.WithTimeout(ctx, cloudAPITimeout)
	defer cancel()
	results := make([]accessResult, len(cloudAccessNeeds))
	for i, a := range cloudAccessNeeds {
		review := &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb: a.verb, Group: a.group, Resource: a.resource,
			}},
		}
		r, err := within(budget, cloudAPITimeout, func(ctx context.Context) (*authorizationv1.SelfSubjectAccessReview, error) {
			return p.kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
		})
		switch {
		case err != nil:
			results[i] = accessResult{err: err}
		case r == nil:
			results[i] = accessResult{err: errors.New("empty review")}
		default:
			results[i] = accessResult{allowed: r.Status.Allowed, denied: r.Status.Denied, evalErr: r.Status.EvaluationError != ""}
		}
	}

	unauthorized := answered == apiUnauthorized
	accepted := false
	for _, r := range results {
		switch {
		case r.err == nil, classifyProbeErr(r.err) == probeForbidden:
			// Answered, or refused to THIS user: either way the credentials were accepted.
			accepted = true
		case classifyProbeErr(r.err) == probeUnauthorized:
			unauthorized = true
		}
	}
	switch {
	case unauthorized:
		d.warn("authentication: the API server refused this context's credentials",
			"refresh the context's login (for example your provider's get-credentials command), then Recheck")
		for _, a := range cloudAccessNeeds {
			d.notChecked("may "+a.String()+": not asked, the credentials were refused", "")
		}
		return
	case accepted:
		d.pass("authentication: this context's credentials are accepted")
	default:
		d.unknown("authentication: the API server did not say whether it accepts these credentials",
			"run kubectl auth can-i --list against this context to see why, then Recheck")
	}
	for i, a := range cloudAccessNeeds {
		r := results[i]
		what := "may " + a.String()
		switch {
		case r.err != nil && classifyProbeErr(r.err) == probeForbidden:
			d.unknown(what+": this context may not ask (SelfSubjectAccessReview refused)", accessFix)
		case r.err != nil && classifyProbeErr(r.err) == probeTimeout:
			d.unknown(what+": not answered within "+cloudAPITimeout.String(), "Recheck")
		case r.err != nil:
			d.unknown(what+": the access review could not be asked", "Recheck")
		case r.allowed && !r.denied:
			d.pass(what)
		case r.evalErr:
			d.unknown(what+": the API server could not decide", accessFix)
		default:
			d.warn(what+": not permitted", accessFix)
		}
	}
}

// ---- registries ---------------------------------------------------------------------

// cloudRegistry is one place the install downloads from.
type cloudRegistry struct {
	what string // in words: what the install fetches there
	url  string // what is asked, with HEAD
	// fromNodes: the cluster's nodes, not this machine, fetch from it, so reaching it
	// from here is a fact, not a verdict.
	fromNodes bool
}

// cloudChartRegistries are the Helm chart repositories the cluster root installs from,
// with the components that use them.
var cloudChartRegistries = []cloudRegistry{
	{what: "cert-manager chart repository", url: "https://charts.jetstack.io/index.yaml"},
	{what: "CloudNativePG chart registry", url: "https://ghcr.io/v2/"},
	{what: "ingress-nginx chart repository", url: "https://kubernetes.github.io/ingress-nginx/index.yaml"},
	{what: "monitoring chart repository", url: "https://prometheus-community.github.io/helm-charts/index.yaml"},
}

func (t cloudTarget) registries(p cloudProbes) []cloudRegistry {
	regs := []cloudRegistry{{what: "OpenTofu provider registry", url: "https://" + p.tofuRegistry + "/.well-known/terraform.json"}}
	regs = append(regs, cloudChartRegistries...)
	if host := registryHost(t.ImageRegistry); host != "" {
		regs = append(regs, cloudRegistry{what: "image registry", url: "https://" + host + "/v2/", fromNodes: !t.Build})
	}
	return regs
}

// registryHost is the host part of an image registry reference ("ghcr.io/org" is ghcr.io).
func registryHost(reg string) string {
	reg = strings.TrimPrefix(strings.TrimPrefix(reg, "https://"), "http://")
	if i := strings.IndexByte(reg, '/'); i >= 0 {
		reg = reg[:i]
	}
	return reg
}

func (d *doctor) checkRegistries(ctx context.Context, t cloudTarget, p cloudProbes) {
	regs := t.registries(p)
	codes := make([]int, len(regs))
	errs := make([]error, len(regs))
	var wg sync.WaitGroup
	for i, r := range regs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], errs[i] = within(ctx, cloudAPITimeout, func(ctx context.Context) (int, error) {
				req, err := http.NewRequestWithContext(ctx, http.MethodHead, r.url, nil)
				if err != nil {
					return 0, err
				}
				resp, err := p.http.Do(req)
				if err != nil {
					return 0, err
				}
				_ = resp.Body.Close()
				return resp.StatusCode, nil
			})
		}()
	}
	wg.Wait()
	for i, r := range regs {
		host := serverHost(r.url)
		what := r.what + " " + host
		fix := "the install downloads from " + host + " on this machine; allow HTTPS to it, or set HTTPS_PROXY, then Recheck"
		if r.fromNodes {
			fix = "the cluster's nodes pull images from " + host + "; check that they can reach it"
		}
		err := errs[i]
		switch {
		case err == nil && codes[i] >= 500:
			d.warn(fmt.Sprintf("%s answered HTTP %d", what, codes[i]), fix)
		case err == nil && r.fromNodes:
			// Reaching it from here does not say the nodes can: a fact, not a pass.
			d.info(what + " answers from this machine (the cluster's nodes pull from it themselves)")
		case err == nil:
			d.pass(what + " reachable")
		default:
			switch classifyProbeErr(err) {
			case probeNoSuchHost:
				d.warn(what+": the name does not resolve from this machine", fix)
			case probeRefused:
				d.warn(what+": the connection was refused", fix)
			case probeTLS:
				d.warn(what+": its TLS certificate is not trusted by this machine", fix)
			case probeTimeout:
				d.unknown(what+": no answer within "+cloudAPITimeout.String(), fix)
			default:
				d.unknown(what+": could not be reached", fix)
			}
		}
	}
}

// ---- bounds, errors and the read-only guard --------------------------------------------

// within runs f under a deadline of d and returns when f does or the deadline passes,
// whichever is first, so a probe that ignores its context still costs no more than d.
// Its answer comes back over a channel, so a probe still running after its deadline
// writes nothing the caller reads.
func within[T any](ctx context.Context, d time.Duration, f func(context.Context) (T, error)) (T, error) {
	type answer struct {
		v   T
		err error
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	if err := ctx.Err(); err != nil { // a spent budget asks nothing
		var zero T
		return zero, err
	}
	done := make(chan answer, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- answer{err: errors.New("probe stopped unexpectedly")}
			}
		}()
		v, err := f(ctx)
		done <- answer{v, err}
	}()
	select {
	case a := <-done:
		return a.v, a.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// probeClass is the kind of error a probe met, which is all a row ever says of it.
type probeClass uint8

const (
	probeOther probeClass = iota
	probeTimeout
	probeNoSuchHost
	probeRefused
	probeTLS
	probeUnauthorized
	probeForbidden
)

func classifyProbeErr(err error) probeClass {
	var dnsErr *net.DNSError
	var netErr net.Error
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verify *tls.CertificateVerificationError
	switch {
	case err == nil:
		return probeOther
	case apierrors.IsUnauthorized(err):
		return probeUnauthorized
	case apierrors.IsForbidden(err):
		return probeForbidden
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return probeNoSuchHost
	case errors.Is(err, context.DeadlineExceeded), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err),
		errors.As(err, &netErr) && netErr.Timeout():
		return probeTimeout
	case errors.Is(err, syscall.ECONNREFUSED):
		return probeRefused
	case errors.As(err, &unknownAuthority), errors.As(err, &hostname), errors.As(err, &invalid), errors.As(err, &verify):
		return probeTLS
	}
	return probeOther
}

// errMutationRefused is what readOnlyTransport answers a request that could change
// something. Nothing in the diagnostics sends one; the guard is what makes that a
// property rather than a promise.
var errMutationRefused = errors.New("the advisory diagnostics refused a request that could change the cluster")

// ssarPath is the one POST the diagnostics may send: a SelfSubjectAccessReview asks the
// API server a question and stores nothing.
const ssarPath = "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews"

// readOnlyRequest reports whether a request may leave: GET and HEAD, and a POST to the
// access-review endpoint (under any path prefix a proxying API front-end adds).
func readOnlyRequest(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodHead:
		return true
	case http.MethodPost:
		return strings.HasSuffix(path, ssarPath)
	}
	return false
}

// readOnlyTransport lets out only what readOnlyRequest allows.
type readOnlyTransport struct{ next http.RoundTripper }

func (t readOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !readOnlyRequest(r.Method, r.URL.Path) {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, errMutationRefused
	}
	return t.next.RoundTrip(r)
}

// ---- production probes -------------------------------------------------------------------

// cloudRestConfig is the context's REST config as the diagnostics use it: every request
// through the read-only guard, a connection bounded by cloudConnectTimeout and every
// answer by cloudAPITimeout.
func cloudRestConfig(cfg *rest.Config) *rest.Config {
	c := rest.CopyConfig(cfg)
	c.Timeout = cloudAPITimeout
	c.Dial = (&net.Dialer{Timeout: cloudConnectTimeout}).DialContext
	c.Wrap(func(rt http.RoundTripper) http.RoundTripper { return readOnlyTransport{next: rt} })
	return c
}

// cloudHTTPClient is the registry probes' client: bounded, read-only, proxy-aware, and
// never following a redirect (any answer at all says the registry is reachable).
func cloudHTTPClient(next http.RoundTripper) *http.Client {
	if next == nil {
		next = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: cloudConnectTimeout}).DialContext,
			TLSHandshakeTimeout:   cloudConnectTimeout,
			ResponseHeaderTimeout: cloudAPITimeout,
		}
	}
	return &http.Client{
		Timeout:       cloudAPITimeout,
		Transport:     readOnlyTransport{next: next},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// liveCloudProbes are the real windows: the context's API server, this machine's DNS and
// network.
func liveCloudProbes(t cloudTarget) (cloudProbes, error) {
	cfg, err := bootstrap.RestConfig(t.KubeContext)
	if err != nil {
		return cloudProbes{}, err
	}
	c := cloudRestConfig(cfg)
	kube, err := kubernetes.NewForConfig(c)
	if err != nil {
		return cloudProbes{}, err
	}
	server := withoutUserinfoURL(cfg.Host)
	p := cloudProbes{server: server, insecure: cfg.Insecure, lookupHost: net.DefaultResolver.LookupHost,
		kube: kube, http: cloudHTTPClient(nil), tofuRegistry: "registry.opentofu.org"}
	if _, err := lookPath("tofu"); err != nil {
		if _, err := lookPath("terraform"); err == nil {
			p.tofuRegistry = "registry.terraform.io"
		}
	}
	if u, err := url.Parse(server); err == nil {
		if cfg.Proxy != nil {
			pu, _ := cfg.Proxy(&http.Request{URL: u})
			p.proxied = pu != nil
		} else if pu, _ := http.ProxyFromEnvironment(&http.Request{URL: u}); pu != nil {
			p.proxied = true
		}
	}
	return p, nil
}

// withoutUserinfoURL is a server address with any userinfo removed.
func withoutUserinfoURL(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	return raw
}

// serverHost is the host name of a URL, without port or userinfo; "" when it has none.
func serverHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
