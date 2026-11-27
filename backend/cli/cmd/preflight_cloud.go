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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
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
//     answer cloudAPITimeout, a credential plugin execPluginTimeout, and each probe runs
//     under within(), so a hung server or a credential plugin that never returns costs a
//     bounded wait and reads UNKNOWN (and the plugin is killed with all it started).
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
	// The cluster root's optional components THIS plan installs, so the registries asked
	// are the ones it downloads from (cloudTargetFor): a component the plan leaves out is
	// not asked, and cannot WARN.
	CertManager, CNPG, Monitoring bool
	// RemoveLegacyIngress is --remove-legacy-ingress: the plan removes ingress-nginx.
	RemoveLegacyIngress bool
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
	// creds is what kind of credentials the context carries.
	creds credKind
	// authenticate, for a context whose credentials come from an exec plugin (credExec),
	// runs the plugin and returns a client carrying what it gave (execAuthenticator).
	// nil: there is no plugin to run, and authentication reads NOT CHECKED.
	authenticate func(context.Context) (kubernetes.Interface, execResult)

	lookupHost func(ctx context.Context, host string) ([]string, error)
	kube       kubernetes.Interface
	http       *http.Client
	// tofuRegistry is the provider registry the IaC binary on PATH downloads from.
	tofuRegistry string
}

// cloudDiagnosticsRunner is the advisory diagnostics as a preflight runner, built for
// one target. probes is called once per run, so a Recheck reads the kubeconfig again.
// Its result never carries a failure.
func cloudDiagnosticsRunner(t cloudTarget, probes func(context.Context, cloudTarget) (cloudProbes, error)) installflow.PreflightRunner {
	return installflow.PreflightFunc(func(ctx context.Context, observe func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		// Bound to the screen's context, like the doctor the screen runs.
		d := &doctor{observe: observe, ctx: ctx}
		d.section(installplain.SectionCloud)
		// 🔴 life is the run's: the connections the probes open are tied to it, and it
		// ends when the run returns, which cancels any dial or TLS handshake still in
		// progress. Go dials on a context detached from the request, so without this a
		// stalled handshake would outlive the run. It ends at the run's END, not at the
		// screen's cancel: a dial is cancelled only once its request has given up, and
		// on an Exit the requests give up a moment AFTER the cancel, inside the probes'
		// join. An Exit ends the run promptly, so it ends life promptly too.
		life, end := context.WithCancel(context.WithoutCancel(ctx))
		defer end()
		p, err := probes(life, t)
		var irregular notRegularFileError
		switch {
		case errors.As(err, &irregular):
			d.notChecked("the advisory cluster checks did not read "+filepath.Base(irregular.path)+": it is not a regular file",
				"point the context at regular files, then Recheck")
			return installflow.PreflightResult{Warns: d.warns}, nil
		case err != nil && errors.Is(err, context.DeadlineExceeded):
			d.unknown("loading --kube-context "+t.KubeContext+" did not finish within "+cloudAPITimeout.String(),
				"check that the kubeconfig and the files it names can be read, then Recheck")
			return installflow.PreflightResult{Warns: d.warns}, nil
		case err != nil:
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
	kube := d.checkAccess(ctx, p, answered)
	d.checkRegistries(ctx, t, p, frontEndOf(ctx, kube, t))
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
		d.notChecked("API server name "+host+": "+proxyNotUsed, "")
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
	apiProxied                       // not asked: a proxy is configured
)

// proxyNotUsed is every row a configured proxy leaves unasked. 🔴 THE CHECKS NEVER GO
// THROUGH A PROXY: Go makes an https proxy's own TLS handshake with the cluster's TLS
// settings, so a proxy could ask for, and get, the context's client certificate.
const proxyNotUsed = "not checked, a proxy is configured; the advisory checks do not go through proxies"

func (d *doctor) checkAPIAnswers(ctx context.Context, p cloudProbes, host, kubeContext string) apiAnswer {
	if p.proxied {
		d.notChecked("API server "+host+": "+proxyNotUsed, "")
		return apiProxied
	}
	_, err := within(ctx, cloudAPITimeout, func(ctx context.Context) (struct{}, error) {
		// Under the probe's context, so an Exit or the deadline cancels the request
		// itself; ServerVersion() would run it on context.Background().
		_, err := p.kube.Discovery().ServerVersionWithContext(ctx)
		return struct{}{}, err
	})
	what := "API server " + host
	// A refusal (401, or a 403 to whoever the server took the caller to be) is still an
	// answer, over the same TLS: the authentication row says what it means.
	class := classifyProbeErr(err)
	unauthorized := err != nil && class == probeUnauthorized
	if err == nil || unauthorized || class == probeForbidden {
		switch {
		case strings.HasPrefix(p.server, "http://"):
			d.warn(what+" answered, without TLS", "use an https:// server address for this context")
		case p.insecure:
			d.warn(what+" answered, but this context does not verify its TLS certificate",
				"remove insecure-skip-tls-verify from the context and give it the cluster's certificate-authority")
		default:
			d.pass(what + " answered over verified TLS")
		}
		// (With a credential plugin the request went without credentials: the plugin
		// runs only once the API server has answered, and checkAccess reads nothing
		// into a refusal of a request that carried none.)
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
//
// checkAccess returns the client the checks went on to ask with (the plugin's, for an
// exec plugin), or nil when nothing could be asked.

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

// anonymous reports whether a SelfSubjectReview says the API server took the caller for
// nobody.
func anonymous(r *authenticationv1.SelfSubjectReview) bool {
	if r.Status.UserInfo.Username == "system:anonymous" {
		return true
	}
	for _, g := range r.Status.UserInfo.Groups {
		if g == "system:unauthenticated" {
			return true
		}
	}
	return false
}

const pluginNotRun = "not checked: this context gets its credentials from an auth-provider plugin, which the advisory checks never run"

// whoamiFix is the fix of an authentication the checks could not judge.
const whoamiFix = "check them yourself with kubectl auth whoami and kubectl auth can-i --list"

func (d *doctor) checkAccess(ctx context.Context, p cloudProbes, answered apiAnswer) kubernetes.Interface {
	plugin := false // the credentials are the ones an exec plugin just gave
	notAsked := func(why string) {
		for _, a := range cloudAccessNeeds {
			d.notChecked("may "+a.String()+": "+why, "")
		}
	}
	switch {
	case answered == apiSilent:
		d.notChecked("authentication: not asked, the API server did not answer", "")
		notAsked("not asked, the API server did not answer")
		return nil
	case answered == apiProxied:
		d.notChecked("authentication: "+proxyNotUsed, "")
		notAsked(proxyNotUsed)
		return nil
	case p.creds == credPlugin:
		// 🔴 An auth-provider is never run (cloudKubeConfig), so there are no credentials
		// to judge, and a review asked without them would answer for nobody.
		d.notChecked("authentication: "+pluginNotRun, whoamiFix)
		notAsked(pluginNotRun)
		return nil
	case p.creds == credExec:
		// 🔴 The exec plugin is run now, bounded and killed with all it starts
		// (execAuthenticator), and only its credential is judged from here on. Whatever
		// stops it reads NOT CHECKED, WARN or UNKNOWN, and the reviews are not asked:
		// asked without credentials they would answer for nobody.
		res := execResult{outcome: execNotRun, why: "this context's credential plugin is not run"}
		var kube kubernetes.Interface
		if p.authenticate != nil {
			kube, res = p.authenticate(ctx)
		}
		const noCredential = "not asked, the credential plugin gave no credentials"
		switch res.outcome {
		case execOK:
			p.kube, plugin = kube, true
			// The version request went without the plugin's credential; a refusal of it
			// says nothing about the credential.
			answered = apiAnswered
		case execNotRun:
			d.notChecked("authentication: not checked: "+res.why, whoamiFix)
			notAsked(noCredential)
			return nil
		case execMissing:
			d.warn("authentication: "+res.why,
				"install it (the context's installHint, or your provider's documentation, says how), then Recheck")
			notAsked(noCredential)
			return nil
		default:
			// 🔴 dcctl's sentence only: never anything the plugin wrote.
			d.unknown("authentication: "+res.why, "check with kubectl auth whoami against this context, then Recheck")
			notAsked(noCredential)
			return nil
		}
	}
	// One budget for every review: they are cheap, and a server that stalls on the first
	// would otherwise cost the budget once per review.
	budget, cancel := context.WithTimeout(ctx, cloudAPITimeout)
	defer cancel()
	who, whoErr := within(budget, cloudAPITimeout, func(ctx context.Context) (*authenticationv1.SelfSubjectReview, error) {
		return p.kube.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	})
	if whoErr == nil && who == nil {
		whoErr = errors.New("empty review")
	}
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

	unauthorized := answered == apiUnauthorized || classifyProbeErr(whoErr) == probeUnauthorized
	for _, r := range results {
		unauthorized = unauthorized || (r.err != nil && classifyProbeErr(r.err) == probeUnauthorized)
	}
	// 🔴 Authentication passes ONLY on the API server's own word that it knows the
	// caller: a SelfSubjectReview naming someone other than the anonymous user, for a
	// context that holds credentials. A 403 on a review, or a review answered, proves
	// nothing: an anonymous caller can be refused, or allowed, exactly the same way.
	switch {
	case p.creds == credNone:
		d.warn("authentication: this context holds no credentials, so the API server treats it as anonymous",
			"give the context credentials for this cluster, for example with your provider's get-credentials command")
		if unauthorized {
			notAsked("not asked, the API server refused anonymous requests")
			return p.kube
		}
	case unauthorized:
		d.warn("authentication: the API server refused this context's credentials",
			"refresh the context's login (for example your provider's get-credentials command), then Recheck")
		notAsked("not asked, the credentials were refused")
		return p.kube
	case whoErr == nil && who.Status.UserInfo.Username == "":
		// A review that names nobody says nothing about the credentials.
		d.unknown("authentication: the API server's review named no user",
			"check with kubectl auth whoami against this context, then Recheck")
	case whoErr == nil && anonymous(who):
		d.warn("authentication: the API server treats this context as anonymous",
			"its credentials are not being accepted; refresh the context's login, then Recheck")
	case whoErr == nil && plugin:
		d.pass("authentication: the API server accepts the credentials this context's plugin gave")
	case whoErr == nil:
		d.pass("authentication: the API server accepts this context's credentials")
	default:
		d.unknown("authentication: the API server did not say who it takes this context to be",
			"check with kubectl auth whoami against this context, then Recheck")
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
	return p.kube
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

// The Helm chart repositories the cluster root installs from, each with the component
// that uses it.
var (
	certManagerCharts = cloudRegistry{what: "cert-manager chart repository", url: "https://charts.jetstack.io/index.yaml"}
	cnpgCharts        = cloudRegistry{what: "CloudNativePG chart registry", url: "https://ghcr.io/v2/"}
	traefikCharts     = cloudRegistry{what: "Traefik chart repository", url: "https://traefik.github.io/charts/index.yaml"}
	monitoringCharts  = cloudRegistry{what: "monitoring chart repository", url: "https://prometheus-community.github.io/helm-charts/index.yaml"}
	nginxCharts       = cloudRegistry{what: "ingress-nginx chart repository", url: "https://kubernetes.github.io/ingress-nginx/index.yaml"}
)

// frontEnd is which ingress controllers the install's plan for this cluster keeps:
// Traefik always, ingress-nginx while the cluster runs it (bootstrap.ClusterChartControllers).
// known is false when the cluster's IngressClasses could not be read.
type frontEnd struct{ known, traefik, nginx bool }

// frontEndOf reads the cluster's IngressClasses through kube (the checks' guarded client,
// nil when nothing could be asked) and resolves the install's front-end plan from them.
func frontEndOf(ctx context.Context, kube kubernetes.Interface, t cloudTarget) frontEnd {
	if kube == nil {
		return frontEnd{}
	}
	list, err := within(ctx, cloudAPITimeout, func(ctx context.Context) (*networkingv1.IngressClassList, error) {
		return kube.NetworkingV1().IngressClasses().List(ctx, metav1.ListOptions{})
	})
	if err != nil || list == nil {
		return frontEnd{}
	}
	traefik, nginx, err := bootstrap.ClusterChartControllers(list.Items, t.RemoveLegacyIngress)
	if err != nil {
		return frontEnd{}
	}
	return frontEnd{known: true, traefik: traefik, nginx: nginx}
}

// registries are the places THIS plan downloads from: the provider registry, the chart
// repositories of the components it installs, and the image registry. Traefik is planned
// on every cluster, so it is asked even when the front end could not be read.
func (t cloudTarget) registries(p cloudProbes, fe frontEnd) []cloudRegistry {
	regs := []cloudRegistry{{what: "OpenTofu provider registry", url: "https://" + p.tofuRegistry + "/.well-known/terraform.json"}}
	if t.CertManager {
		regs = append(regs, certManagerCharts)
	}
	if t.CNPG {
		regs = append(regs, cnpgCharts)
	}
	if fe.traefik || !fe.known {
		regs = append(regs, traefikCharts)
	}
	if fe.nginx {
		regs = append(regs, nginxCharts)
	}
	if t.Monitoring {
		regs = append(regs, monitoringCharts)
	}
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

func (d *doctor) checkRegistries(ctx context.Context, t cloudTarget, p cloudProbes, fe frontEnd) {
	regs := t.registries(p, fe)
	codes := make([]int, len(regs))
	errs := make([]error, len(regs))
	var wg sync.WaitGroup
	proxied := make([]bool, len(regs))
	for i, r := range regs {
		if u, err := url.Parse(r.url); err == nil {
			if pu, _ := envProxy(u); pu != nil {
				proxied[i] = true
				continue
			}
		}
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
		case proxied[i]:
			d.notChecked(what+": "+proxyNotUsed, "")
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
	if !fe.known && !t.RemoveLegacyIngress {
		d.notChecked("ingress-nginx chart repository: not checked, the cluster's IngressClasses could not be read, "+
			"so whether the install keeps ingress-nginx is not known", "")
	}
}

// ---- bounds, errors and the read-only guard --------------------------------------------

// within runs f under a deadline of d and returns when f does or the deadline passes,
// whichever is first, so a probe that ignores its context still costs no more than d.
// Its answer comes back over a channel, so a probe still running after its deadline
// writes nothing the caller reads.
//
// 🔴 A PROBE IS JOINED, BOUNDEDLY, as the screen's commands are: past the deadline its
// context is cancelled and within waits up to probeJoinDelay for it to stop, so a probe
// that honours its context (every real one does) never outlives the run it belongs
// to, and one that does not costs at most the deadline plus that delay.
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
		err := ctx.Err()
		cancel()
		select {
		case <-done:
		case <-time.After(probeJoinDelay):
		}
		var zero T
		return zero, err
	}
}

// probeJoinDelay is how long within waits for a probe it gave up on to stop: the same
// budget the screen gives a command it killed. A variable only so a test can make a
// probe that ignores its context give up in milliseconds.
var probeJoinDelay = commandWaitDelay

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
	case refused(err):
		return probeRefused
	case errors.As(err, &unknownAuthority), errors.As(err, &hostname), errors.As(err, &invalid), errors.As(err, &verify):
		return probeTLS
	}
	return probeOther
}

// refused reports whether err is a refused connection, in any form this OS reports one
// (refusedConnection, per OS).
func refused(err error) bool {
	for _, r := range refusedConnection {
		if errors.Is(err, r) {
			return true
		}
	}
	return false
}

// errMutationRefused is what the guards answer a request the diagnostics do not send.
// Nothing in the diagnostics sends one; the guards are what make that a property rather
// than a promise.
var errMutationRefused = errors.New("the advisory diagnostics refused a request they do not send")

// ssarPath is one of the two POSTs the diagnostics may send: a SelfSubjectAccessReview
// asks the API server a question and stores nothing.
const ssarPath = "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews"

// ssrPath is the other: a SelfSubjectReview says who the API server takes the caller
// to be, and stores nothing.
const ssrPath = "/apis/authentication.k8s.io/v1/selfsubjectreviews"

// ingressClassesPath is the diagnostics' one read of cluster state: the IngressClasses,
// which say whether the install keeps ingress-nginx (and so downloads its chart).
const ingressClassesPath = "/apis/networking.k8s.io/v1/ingressclasses"

// kubeRequestPermitted reports whether a request to the cluster may leave. 🔴 IT IS AN
// ALLOWLIST OF THE DIAGNOSTICS' OWN FOUR REQUESTS, each matched EXACTLY under the
// configured server (its scheme, host and port, and any path prefix it has): GET
// /version and the IngressClass list (the one read of cluster state, which nothing
// secret is in), and POST to the two review endpoints. A suffix would let a proxy subresource
// (.../services/x/proxy/apis/...) through to a service that can write; so nothing is
// matched by suffix, and a path that is not already clean, or that was sent escaped,
// is refused rather than interpreted.
func kubeRequestPermitted(server *url.URL, method string, u *url.URL) bool {
	if u.Scheme != server.Scheme || u.Host != server.Host || u.Opaque != "" || u.User != nil {
		return false
	}
	// An escaped path is refused, never decoded into one of ours. An unclean one (a dot
	// segment, a doubled slash) cannot equal any of the clean paths below, so the exact
	// match refuses it too.
	if u.RawPath != "" && u.RawPath != u.Path {
		return false
	}
	base := strings.TrimSuffix(server.Path, "/")
	switch u.Path {
	case base + "/version", base + ingressClassesPath:
		return method == http.MethodGet
	case base + ssarPath, base + ssrPath:
		return method == http.MethodPost
	}
	return false
}

// kubeGuard lets out only what kubeRequestPermitted allows. It sits under client-go's
// auth wrappers, so it sees every request, with its credentials, before it leaves.
//
// 🔴 A CREDENTIAL IS NEVER SENT PAST ITS EXPIRY: with notAfter set (a plugin's credential
// says when it expires), a request from then on is refused before it leaves.
type kubeGuard struct {
	server   *url.URL
	next     http.RoundTripper
	notAfter time.Time
}

func (g kubeGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	if !kubeRequestPermitted(g.server, r.Method, r.URL) {
		return refuse(r)
	}
	if !g.notAfter.IsZero() && !time.Now().Before(g.notAfter) {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, errCredentialExpired
	}
	return g.next.RoundTrip(r)
}

// errCredentialExpired is the guard's answer to a request whose credential has expired.
var errCredentialExpired = errors.New("the credential has expired")

// readOnlyRequest reports whether a registry probe may leave: GET and HEAD only.
func readOnlyRequest(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// readOnlyTransport lets out only what readOnlyRequest allows.
type readOnlyTransport struct{ next http.RoundTripper }

func (t readOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !readOnlyRequest(r.Method) {
		return refuse(r)
	}
	return t.next.RoundTrip(r)
}

func refuse(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_ = r.Body.Close()
	}
	return nil, errMutationRefused
}

// noRedirect is every diagnostics client's CheckRedirect. 🔴 A REDIRECT IS NEVER
// FOLLOWED: client-go's auth wrappers put the kubeconfig's credentials on every hop, so
// following one would hand them to wherever it points. The 3xx comes back as the
// answer, which a probe reads as advice.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// boundedTransport is the diagnostics' one transport shape: a bounded connection and
// answer, 🔴 NO PROXY (proxyNotUsed), and 🔴 NO KEEP-ALIVE, so a run leaves no idle
// connection (and no goroutine holding one) behind for the next Recheck to pile on.
// When life ends, every dial and TLS handshake still in progress is cancelled
// (CloseIdleConnections cancels the dials whose requests have gone), so connection
// setup never outlives the run.
func boundedTransport(life context.Context, tlsCfg *tls.Config) *http.Transport {
	tr := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       tlsCfg,
		DialContext:           (&net.Dialer{Timeout: cloudConnectTimeout}).DialContext,
		TLSHandshakeTimeout:   cloudConnectTimeout,
		ResponseHeaderTimeout: cloudAPITimeout,
		DisableKeepAlives:     true,
	}
	context.AfterFunc(life, tr.CloseIdleConnections)
	return tr
}

// ---- production probes -------------------------------------------------------------------

// cloudKubeConfig is the context's REST config as the diagnostics use it, and the
// server it addresses:
//
//   - 🔴 no credential plugin. client-go runs an exec plugin with no way to cancel it, so
//     a hung one would outlive every bound, and Exit, and each Recheck would queue
//     another behind it. The plugin is dropped here; an exec plugin is run by the checks
//     themselves (execAuthenticator), and an auth-provider never (credPlugin).
//   - every request through kubeGuard, on dcctl's own bounded, keep-alive-free
//     transport carrying the context's TLS settings.
func cloudKubeConfig(life context.Context, cfg *rest.Config) (*rest.Config, *url.URL, error) {
	return cloudKubeConfigUntil(life, cfg, time.Time{})
}

// cloudKubeConfigUntil is cloudKubeConfig for credentials that expire at notAfter (zero:
// never): the guard refuses to send them from then on.
func cloudKubeConfigUntil(life context.Context, cfg *rest.Config, notAfter time.Time) (*rest.Config, *url.URL, error) {
	c := rest.CopyConfig(cfg)
	c.ExecProvider, c.AuthProvider = nil, nil
	server, _, err := rest.DefaultServerUrlFor(c)
	if err != nil {
		return nil, nil, err
	}
	tlsCfg, err := rest.TLSConfigFor(c)
	if err != nil {
		return nil, nil, err
	}
	c.Transport = boundedTransport(life, tlsCfg)
	// The TLS settings now live in the transport; client-go refuses both at once.
	c.TLSClientConfig, c.Proxy, c.Dial = rest.TLSClientConfig{}, nil, nil
	c.Timeout = cloudAPITimeout
	c.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return kubeGuard{server: server, next: rt, notAfter: notAfter}
	})
	return c, server, nil
}

// cloudKubeClient is the cluster client the diagnostics ask through: cloudKubeConfig's,
// following no redirect.
func cloudKubeClient(life context.Context, cfg *rest.Config) (kubernetes.Interface, error) {
	return cloudKubeClientUntil(life, cfg, time.Time{})
}

// cloudKubeClientUntil is cloudKubeClient for credentials that expire at notAfter.
func cloudKubeClientUntil(life context.Context, cfg *rest.Config, notAfter time.Time) (kubernetes.Interface, error) {
	c, _, err := cloudKubeConfigUntil(life, cfg, notAfter)
	if err != nil {
		return nil, err
	}
	hc, err := rest.HTTPClientFor(c)
	if err != nil {
		return nil, err
	}
	own := *hc // never change a client shared with anything else
	own.CheckRedirect = noRedirect
	return kubernetes.NewForConfigAndClient(c, &own)
}

// cloudHTTPClient is the registry probes' client: bounded, read-only, proxy-aware,
// keep-alive-free, and following no redirect (any answer at all says the registry is
// reachable).
func cloudHTTPClient(next http.RoundTripper) *http.Client {
	return cloudHTTPClientFor(context.Background(), next)
}

// cloudHTTPClientFor is cloudHTTPClient with its connections tied to life.
func cloudHTTPClientFor(life context.Context, next http.RoundTripper) *http.Client {
	if next == nil {
		next = boundedTransport(life, nil)
	}
	return &http.Client{Timeout: cloudAPITimeout, Transport: readOnlyTransport{next: next}, CheckRedirect: noRedirect}
}

// credKind is what kind of credentials a context carries.
type credKind uint8

const (
	// credStatic: a token, a client certificate or a password, held in the kubeconfig.
	credStatic credKind = iota
	// credNone: nothing; the API server sees the context as anonymous.
	credNone
	// credPlugin: an auth-provider plugin (the legacy gcp and azure ones), never run.
	credPlugin
	// credExec: an exec plugin, which the checks run themselves (execAuthenticator).
	credExec
)

// credentialsOf classifies a context's credentials as client-go uses them: static
// credentials take precedence over a plugin beside them (the plugin is then never
// consulted), so they are looked for first.
func credentialsOf(cfg *rest.Config) credKind {
	switch {
	case cfg.BearerToken != "" || cfg.BearerTokenFile != "" || cfg.Username != "" || cfg.Password != "" ||
		cfg.CertData != nil || cfg.CertFile != "" || cfg.KeyData != nil || cfg.KeyFile != "":
		return credStatic
	case cfg.ExecProvider != nil && cfg.AuthProvider == nil:
		return credExec
	case cfg.ExecProvider != nil || cfg.AuthProvider != nil:
		// client-go refuses an exec plugin beside an auth-provider; so do the checks.
		return credPlugin
	}
	return credNone
}

// liveCloudProbes are the real windows: the context's API server, this machine's DNS and
// network.
//
// 🔴 LOADING IS BOUNDED AND READS ONLY REGULAR FILES. The kubeconfig, and the token,
// certificate and key files it names, are read with plain file reads nothing can
// cancel: a FIFO nobody writes to, or a stalled network file, would hold the run, and
// the screen's Exit, for ever. So every file is checked to be a regular file before
// anything opens it (one that is not reads NOT CHECKED), and the whole load runs under
// within(): past cloudAPITimeout it reads UNKNOWN and the read is given up on. A token
// file is read once, here; client-go's refreshing reader, which would read it again on
// a request, is never used.
func liveCloudProbes(life context.Context, t cloudTarget) (cloudProbes, error) {
	// Taken before the bound: a load given up on keeps running, and must not read
	// anything a later run could change.
	load := loadRestConfig
	return within(life, cloudAPITimeout, func(context.Context) (cloudProbes, error) {
		if f := irregularKubeFile(t.KubeContext); f != "" {
			return cloudProbes{}, notRegularFileError{path: f}
		}
		cfg, err := load(t.KubeContext)
		if err != nil {
			return cloudProbes{}, err
		}
		if err := pinToken(cfg); err != nil {
			return cloudProbes{}, err
		}
		// The connections belong to the run (life), not to this load's deadline.
		return cloudProbesFrom(life, cfg)
	})
}

// notRegularFileError is a file the checks will not read: not a regular file.
type notRegularFileError struct{ path string }

func (e notRegularFileError) Error() string { return e.path + " is not a regular file" }

// irregularKubeFile is the first file the context's kubeconfig depends on that exists
// and is not a regular file, or "". It reads only regular files to find out.
func irregularKubeFile(kubeContext string) string {
	irregular := func(path string) bool {
		if path == "" {
			return false
		}
		fi, err := os.Stat(path)
		return err == nil && !fi.Mode().IsRegular()
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	for _, path := range rules.GetLoadingPrecedence() {
		if irregular(path) {
			return path
		}
	}
	raw, err := rules.Load()
	if err != nil {
		return "" // the loader reports it, in its own words
	}
	name := kubeContext
	if name == "" {
		name = raw.CurrentContext
	}
	c, ok := raw.Contexts[name]
	if !ok || c == nil {
		return ""
	}
	var files []string
	if a := raw.AuthInfos[c.AuthInfo]; a != nil {
		files = append(files, a.TokenFile, a.ClientCertificate, a.ClientKey)
	}
	if cl := raw.Clusters[c.Cluster]; cl != nil {
		files = append(files, cl.CertificateAuthority)
	}
	for _, f := range files {
		if irregular(f) {
			return f
		}
	}
	return ""
}

// pinToken makes a token file a token read once, now: client-go would otherwise read
// the file again, unboundedly, on a request.
func pinToken(cfg *rest.Config) error {
	if cfg.BearerTokenFile == "" {
		return nil
	}
	if cfg.BearerToken == "" {
		fi, err := os.Stat(cfg.BearerTokenFile)
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return notRegularFileError{path: cfg.BearerTokenFile}
		}
		b, err := os.ReadFile(cfg.BearerTokenFile)
		if err != nil {
			return err
		}
		cfg.BearerToken = strings.TrimSpace(string(b))
	}
	cfg.BearerTokenFile = ""
	return nil
}

// cloudProbesFrom are the windows onto the cluster one REST config describes.
//
// 🔴 A credential plugin in cfg is never run by client-go: cloudKubeClient drops it. An
// exec plugin is run by the checks themselves, later, under the probe's context
// (authenticate); an auth-provider never is, and creds says so.
func cloudProbesFrom(life context.Context, cfg *rest.Config) (cloudProbes, error) {
	kube, err := cloudKubeClient(life, cfg)
	if err != nil {
		return cloudProbes{}, err
	}
	server := withoutUserinfoURL(cfg.Host)
	p := cloudProbes{server: server, insecure: cfg.Insecure, creds: credentialsOf(cfg), lookupHost: net.DefaultResolver.LookupHost,
		kube: kube, http: cloudHTTPClientFor(life, nil), tofuRegistry: "registry.opentofu.org"}
	if p.creds == credExec {
		// Every file the plugin's client could need is read now, inside the load's bound,
		// so authenticating later reads none.
		base := rest.CopyConfig(cfg)
		if err := rest.LoadTLSFiles(base); err != nil {
			return cloudProbes{}, err
		}
		p.authenticate = execAuthenticator(life, base)
	}
	if _, err := lookPath("tofu"); err != nil {
		if _, err := lookPath("terraform"); err == nil {
			p.tofuRegistry = "registry.terraform.io"
		}
	}
	if u, err := url.Parse(server); err == nil {
		if cfg.Proxy != nil {
			pu, _ := cfg.Proxy(&http.Request{URL: u})
			p.proxied = pu != nil
		} else if pu, _ := envProxy(u); pu != nil {
			p.proxied = true
		}
	}
	return p, nil
}

// loadRestConfig reads the context's kubeconfig into a REST config, and envProxy is the
// proxy the environment (HTTPS_PROXY and the rest) names for a URL. Variables so a test
// can present a kubeconfig that never finishes loading, or a proxy, without the
// process's own environment; production never reassigns them.
var (
	loadRestConfig = bootstrap.RestConfig
	envProxy       = func(u *url.URL) (*url.URL, error) { return http.ProxyFromEnvironment(&http.Request{URL: u}) }
)

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
