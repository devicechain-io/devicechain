// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"sigs.k8s.io/yaml"
)

// The chart renders its routing two ways, selected by ingress.controller: Ingress objects
// for ingress-nginx, and a Gateway with HTTPRoutes for a Gateway API controller. The two
// are written by hand in two templates, so nothing makes them agree except this test: for
// every profile dcctl installs, it renders both, resolves a set of request paths through
// each one's own matching rules, and requires the same answer (backend Service and port,
// and the path the Service is then asked for) from both.
//
// The request paths are DERIVED from the rendered objects, not listed by hand: every path
// either front end declares is instantiated into a concrete request, plus its neighbours
// (the bare prefix, a trailing slash, a sub-path, a path that merely shares the prefix
// text). A route added to one template and forgotten in the other is therefore probed by
// the side that has it, and answered "nothing" by the side that does not.
//
// The two matching models are deliberately small:
//
//   - Ingress, as ingress-nginx 1.15 renders it once any Ingress for the host rewrites
//     (the API Ingress does, which turns on its enforceRegex): updateServerLocations
//     expands a Prefix /p into /p/ plus an Exact /p, and buildLocation then emits EVERY
//     location as ~* "^<path>", a case-insensitive CHARACTER prefix, tried longest first.
//     rewrite-target /$N expands capture N. Prefix /p therefore also matches /pX and /P.
//   - HTTPRoute: PathPrefix is case-sensitive and matches whole path elements, the longest
//     prefix wins, and URLRewrite ReplacePrefixMatch swaps the matched prefix.
//
// Those two leniencies of ingress-nginx (case, and a prefix ending mid-element) are the only
// places the front ends are ACCEPTED to differ: the Gateway's behaviour is the stricter and
// the more correct one, since /api/device-managementX is not the device-management API and
// paths are case-sensitive. The comparison is also run against a strict model of the Ingress
// (case-sensitive, whole elements). A difference is accepted only when that strict model
// agrees with the Gateway on the request, or on its lower-cased form, which pins the
// difference to those two leniencies; anything else fails. Each kind must also be observed
// in every render, so an acceptance cannot outlive the probe that justifies it.

type routingObject struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec json.RawMessage `json:"spec"`
}

// To keep the decoders honest each kind has its own spec type.

type ingressSpec struct {
	TLS []struct {
		Hosts      []string `json:"hosts"`
		SecretName string   `json:"secretName"`
	} `json:"tls"`
	Rules []struct {
		Host string `json:"host"`
		HTTP struct {
			Paths []struct {
				Path     string `json:"path"`
				PathType string `json:"pathType"`
				Backend  struct {
					Service struct {
						Name string `json:"name"`
						Port struct {
							Name   string `json:"name"`
							Number int    `json:"number"`
						} `json:"port"`
					} `json:"service"`
				} `json:"backend"`
			} `json:"paths"`
		} `json:"http"`
	} `json:"rules"`
}

type gatewaySpec struct {
	GatewayClassName string `json:"gatewayClassName"`
	Listeners        []struct {
		Name     string `json:"name"`
		Protocol string `json:"protocol"`
		Port     int    `json:"port"`
		Hostname string `json:"hostname"`
		TLS      *struct {
			Mode            string `json:"mode"`
			CertificateRefs []struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"certificateRefs"`
		} `json:"tls"`
		AllowedRoutes struct {
			Namespaces struct {
				From string `json:"from"`
			} `json:"namespaces"`
		} `json:"allowedRoutes"`
	} `json:"listeners"`
}

type httpRouteSpec struct {
	ParentRefs []struct {
		Name        string `json:"name"`
		SectionName string `json:"sectionName"`
	} `json:"parentRefs"`
	Hostnames []string `json:"hostnames"`
	Rules     []struct {
		Matches []struct {
			Path struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"path"`
		} `json:"matches"`
		Filters []struct {
			Type       string `json:"type"`
			URLRewrite *struct {
				Path struct {
					Type               string `json:"type"`
					ReplacePrefixMatch string `json:"replacePrefixMatch"`
				} `json:"path"`
			} `json:"urlRewrite"`
			ResponseHeaderModifier *struct {
				Set []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"set"`
			} `json:"responseHeaderModifier"`
			RequestRedirect *struct {
				Scheme     string `json:"scheme"`
				StatusCode int    `json:"statusCode"`
			} `json:"requestRedirect"`
		} `json:"filters"`
		BackendRefs []struct {
			Name string `json:"name"`
			Port int    `json:"port"`
		} `json:"backendRefs"`
	} `json:"rules"`
}

// renderedRouting is every routing-relevant object of one render.
type renderedRouting struct {
	ingresses []routingObject
	gateways  []routingObject
	routes    []routingObject
	// servicePorts maps "<service>/<port name>" to the Service's port number, which is how
	// an Ingress's named backend port is compared with an HTTPRoute's numeric one.
	servicePorts map[string]int
}

func collectRouting(t *testing.T, manifest string) renderedRouting {
	t.Helper()
	r := renderedRouting{servicePorts: map[string]int{}}
	for _, doc := range splitManifests(manifest) {
		var o routingObject
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("decoding a rendered document: %v\n%s", err, doc)
		}
		switch o.Kind {
		case "Ingress":
			r.ingresses = append(r.ingresses, o)
		case "Gateway":
			r.gateways = append(r.gateways, o)
		case "HTTPRoute":
			r.routes = append(r.routes, o)
		case "Service":
			var s struct {
				Spec struct {
					Ports []struct {
						Name string `json:"name"`
						Port int    `json:"port"`
					} `json:"ports"`
				} `json:"spec"`
			}
			if err := yaml.Unmarshal([]byte(doc), &s); err != nil {
				t.Fatalf("decoding a Service: %v", err)
			}
			for _, p := range s.Spec.Ports {
				r.servicePorts[o.Metadata.Name+"/"+p.Name] = p.Port
			}
		}
	}
	return r
}

// answer is what a front end does with one request: the backend that gets it and the path
// that backend is asked for. "" means no route matches (the controller's own 404).
type answer string

const noRoute answer = ""

type ingressEntry struct {
	path, pathType, rewrite, backend string
	re                               *regexp.Regexp
}

// resolveIngress answers host+path through the Ingress objects.
//
// strict switches the model from ingress-nginx's matching (case-insensitive character
// prefixes) to the Gateway API's (case-sensitive, whole path elements).
func (r renderedRouting) resolveIngress(host, path string, strict bool) (answer, error) {
	var entries []ingressEntry
	for _, ing := range r.ingresses {
		var spec ingressSpec
		if err := yaml.Unmarshal(ing.Spec, &spec); err != nil {
			return "", fmt.Errorf("decoding Ingress/%s: %w", ing.Metadata.Name, err)
		}
		for _, rule := range spec.Rules {
			if rule.Host != host {
				continue
			}
			for _, p := range rule.HTTP.Paths {
				port := p.Backend.Service.Port.Number
				if n := p.Backend.Service.Port.Name; n != "" {
					var ok bool
					if port, ok = r.servicePorts[p.Backend.Service.Name+"/"+n]; !ok {
						return "", fmt.Errorf("Ingress/%s names port %q of Service %q, which does not render it", ing.Metadata.Name, n, p.Backend.Service.Name)
					}
				}
				e := ingressEntry{
					path: p.Path, pathType: p.PathType,
					rewrite: ing.Metadata.Annotations["nginx.ingress.kubernetes.io/rewrite-target"],
					backend: fmt.Sprintf("%s:%d", p.Backend.Service.Name, port),
				}
				if p.PathType == "ImplementationSpecific" {
					flags := "(?i)"
					if strict {
						flags = ""
					}
					re, err := regexp.Compile(flags + "^" + p.Path)
					if err != nil {
						return "", fmt.Errorf("Ingress/%s path %q is not a regular expression: %w", ing.Metadata.Name, p.Path, err)
					}
					e.re = re
				} else if p.PathType != "Prefix" && p.PathType != "Exact" {
					return "", fmt.Errorf("Ingress/%s path %q has pathType %q, which this model does not know", ing.Metadata.Name, p.Path, p.PathType)
				}
				entries = append(entries, e)
				if p.PathType == "Prefix" && !strict && p.Path != "/" && !strings.HasSuffix(p.Path, "/") {
					// updateServerLocations adds the trailing-slash form beside the exact one.
					e2 := e
					e2.path = p.Path + "/"
					entries = append(entries, e2)
				}
			}
		}
	}
	// Longest first, as ingress-nginx orders its locations.
	sort.SliceStable(entries, func(i, j int) bool { return len(entries[i].path) > len(entries[j].path) })
	for _, e := range entries {
		var groups []string
		switch e.pathType {
		case "ImplementationSpecific":
			groups = e.re.FindStringSubmatch(path)
		case "Exact":
			if path == e.path {
				groups = []string{path}
			}
		default:
			if strict && prefixMatches(e.path, path) {
				groups = []string{path}
			} else if !strict && strings.HasPrefix(strings.ToLower(path), strings.ToLower(e.path)) {
				groups = []string{path}
			}
		}
		if groups == nil {
			continue
		}
		upstream := path
		if e.rewrite != "" {
			upstream = e.rewrite
			for i := len(groups) - 1; i >= 1; i-- {
				upstream = strings.ReplaceAll(upstream, "$"+strconv.Itoa(i), groups[i])
			}
			if strings.Contains(upstream, "$") {
				return "", fmt.Errorf("rewrite %q for %q references a capture group the path does not have", e.rewrite, e.path)
			}
		}
		return answer(e.backend + " " + upstream), nil
	}
	return noRoute, nil
}

// prefixMatches is the Gateway API and Ingress Prefix rule: whole path elements.
func prefixMatches(prefix, path string) bool {
	if prefix == "/" {
		return true
	}
	p := strings.TrimSuffix(prefix, "/")
	return path == p || strings.HasPrefix(path, p+"/")
}

// resolveGateway answers host+path through the HTTPRoutes attached to the Gateway.
func (r renderedRouting) resolveGateway(host, path string) (answer, error) {
	type cand struct {
		prefix, replace, backend string
		rewrites                 bool
	}
	var best *cand
	for _, rt := range r.routes {
		var spec httpRouteSpec
		if err := yaml.Unmarshal(rt.Spec, &spec); err != nil {
			return "", fmt.Errorf("decoding HTTPRoute/%s: %w", rt.Metadata.Name, err)
		}
		if !slices.Contains(spec.Hostnames, host) {
			continue
		}
		for _, rule := range spec.Rules {
			if len(rule.BackendRefs) == 0 {
				continue // a redirect: it answers with no backend
			}
			if len(rule.BackendRefs) != 1 {
				return "", fmt.Errorf("HTTPRoute/%s has a rule with %d backends; the model reads one", rt.Metadata.Name, len(rule.BackendRefs))
			}
			c := cand{backend: fmt.Sprintf("%s:%d", rule.BackendRefs[0].Name, rule.BackendRefs[0].Port)}
			for _, f := range rule.Filters {
				switch f.Type {
				case "URLRewrite":
					if f.URLRewrite == nil || f.URLRewrite.Path.Type != "ReplacePrefixMatch" {
						return "", fmt.Errorf("HTTPRoute/%s has a URLRewrite this model does not read", rt.Metadata.Name)
					}
					c.rewrites, c.replace = true, f.URLRewrite.Path.ReplacePrefixMatch
				case "ResponseHeaderModifier":
					// Does not change where the request goes; TestGatewayCarriesTheIngressTLS pins it.
				default:
					return "", fmt.Errorf("HTTPRoute/%s has a %s filter on a backend rule, which this model does not read", rt.Metadata.Name, f.Type)
				}
			}
			for _, m := range rule.Matches {
				if m.Path.Type != "PathPrefix" {
					return "", fmt.Errorf("HTTPRoute/%s matches with %q, which this model does not read", rt.Metadata.Name, m.Path.Type)
				}
				if !prefixMatches(m.Path.Value, path) {
					continue
				}
				if best != nil && len(strings.TrimSuffix(best.prefix, "/")) == len(strings.TrimSuffix(m.Path.Value, "/")) && best.backend != c.backend {
					return "", fmt.Errorf("two routes match %q with the same prefix %q and different backends", path, m.Path.Value)
				}
				if best == nil || len(strings.TrimSuffix(m.Path.Value, "/")) > len(strings.TrimSuffix(best.prefix, "/")) {
					cc := c
					cc.prefix = m.Path.Value
					best = &cc
				}
			}
		}
	}
	if best == nil {
		return noRoute, nil
	}
	upstream := path
	if best.rewrites {
		rest := strings.TrimPrefix(path, strings.TrimSuffix(best.prefix, "/"))
		upstream = strings.TrimSuffix(best.replace, "/") + "/" + strings.TrimPrefix(rest, "/")
	}
	return answer(best.backend + " " + upstream), nil
}

var (
	reTokenClass = regexp.MustCompile(`\[\^/\]\+`)
	reRegexTail  = regexp.MustCompile(`\(/\|\$\)|\(/\)|\(\.\*\)`)
)

// probesFor turns one declared path (a literal prefix, or an nginx regex) into concrete
// requests: the path itself instantiated, and its neighbours.
func probesFor(declared string) []string {
	inst := reTokenClass.ReplaceAllString(declared, "tok")
	inst = reRegexTail.ReplaceAllStringFunc(inst, func(m string) string {
		if m == "(.*)" {
			return "x"
		}
		return "/"
	})
	inst = strings.NewReplacer("(", "", ")", "", "$", "").Replace(inst)
	base := declared
	if i := strings.Index(base, "("); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSuffix(base, "/")
	// base+"X" ends the prefix mid-element, and the upper-cased forms change only the case:
	// the two places ingress-nginx's matching is more lenient than a Gateway's.
	out := []string{inst, inst + "/more", base, base + "/", base + "/sub/path", base + "X", base + "/graphql",
		strings.ToUpper(inst), strings.ToUpper(base) + "/X"}
	return out
}

// routingProbes is every request the two front ends are asked, derived from the objects.
func routingProbes(r renderedRouting) ([]string, error) {
	set := map[string]bool{"/": true, "/nope": true, "/api": true, "/api/": true, "/api/nope/x": true, "/.well-known/": true}
	add := func(p string) {
		if p != "" {
			set[p] = true
		}
	}
	for _, ing := range r.ingresses {
		var spec ingressSpec
		if err := yaml.Unmarshal(ing.Spec, &spec); err != nil {
			return nil, err
		}
		for _, rule := range spec.Rules {
			for _, p := range rule.HTTP.Paths {
				for _, pr := range probesFor(p.Path) {
					add(pr)
				}
			}
		}
	}
	for _, rt := range r.routes {
		var spec httpRouteSpec
		if err := yaml.Unmarshal(rt.Spec, &spec); err != nil {
			return nil, err
		}
		for _, rule := range spec.Rules {
			for _, m := range rule.Matches {
				for _, pr := range probesFor(m.Path.Value) {
					add(pr)
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// ingressHosts returns every host an Ingress rule serves.
func (r renderedRouting) ingressHosts() []string {
	seen := map[string]bool{}
	for _, ing := range r.ingresses {
		var spec ingressSpec
		_ = yaml.Unmarshal(ing.Spec, &spec)
		for _, rule := range spec.Rules {
			seen[rule.Host] = true
		}
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// routingDifferences compares the two front ends over every probe, and returns one line
// per disagreement. nginx is the Ingress render, gw the Gateway render.
//
// A difference is returned in unexplained unless it is one of ingress-nginx's two accepted
// leniencies (see the header), which are counted in accepted under their kind: "case" and
// "character-prefix".
func routingDifferences(nginx, gw renderedRouting) (unexplained []string, accepted map[string]int, err error) {
	hosts := nginx.ingressHosts()
	if len(hosts) != 1 {
		return nil, nil, fmt.Errorf("the Ingress render serves %d hosts, want exactly 1", len(hosts))
	}
	// Probes from BOTH renders, so a path only one side declares is still asked of both.
	union := renderedRouting{ingresses: nginx.ingresses, routes: gw.routes}
	probes, err := routingProbes(union)
	if err != nil {
		return nil, nil, err
	}
	accepted = map[string]int{}
	served := 0
	backend := func(a answer) string { return strings.SplitN(string(a), " ", 2)[0] }
	for _, p := range probes {
		a, err := nginx.resolveIngress(hosts[0], p, false)
		if err != nil {
			return nil, nil, err
		}
		b, err := gw.resolveGateway(hosts[0], p)
		if err != nil {
			return nil, nil, err
		}
		if a != noRoute {
			served++
		}
		if a == b {
			continue
		}
		strict, err := nginx.resolveIngress(hosts[0], p, true)
		if err != nil {
			return nil, nil, err
		}
		if strict != b {
			unexplained = append(unexplained, fmt.Sprintf("%s%s: Ingress -> %q, Gateway -> %q", hosts[0], p, string(a), string(b)))
			continue
		}
		lower, err := nginx.resolveIngress(hosts[0], strings.ToLower(p), true)
		if err != nil {
			return nil, nil, err
		}
		if strings.ToLower(p) != p && backend(lower) == backend(a) {
			accepted["case"]++
		} else {
			accepted["character-prefix"]++
		}
	}
	if served == 0 {
		return nil, nil, fmt.Errorf("no probe reached any Ingress route: the comparison would pass over nothing")
	}
	return unexplained, accepted, nil
}

// routingVariantValues is the install values of profile p with the given front end and TLS shape.
func routingVariantValues(ctx context.Context, ch *chart.Chart, p renderProfile, instance, controller string, tlsOn bool, clusterIssuer string) (map[string]interface{}, error) {
	vals, err := profileValuesE(ctx, ch, p, instance)
	if err != nil {
		return nil, err
	}
	ing, _ := vals["ingress"].(map[string]interface{})
	if ing == nil {
		return nil, fmt.Errorf("%s: the install values carry no ingress block: %v", p.name, vals)
	}
	ing["controller"] = controller
	// The parity is between the two PURE front ends; the cutover window renders both at once
	// and is held by frontend_test.go.
	delete(ing, "legacy")
	tls, _ := ing["tls"].(map[string]interface{})
	if tls == nil {
		tls = map[string]interface{}{}
		ing["tls"] = tls
	}
	tls["enabled"] = tlsOn
	if clusterIssuer != "" {
		tls["selfSigned"] = false
		tls["clusterIssuer"] = clusterIssuer
	}
	return vals, nil
}

// gatewayVariant renders vals with the given controller and the TLS shape of tlsOn.
func renderRoutingVariant(t *testing.T, p renderProfile, instance, controller string, tlsOn bool, clusterIssuer string) (renderedRouting, string) {
	t.Helper()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading the embedded chart: %v", err)
	}
	vals, err := routingVariantValues(t.Context(), ch, p, instance, controller, tlsOn, clusterIssuer)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := renderChartClientSide(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("%s (%s): rendering: %v", p.name, controller, err)
	}
	return collectRouting(t, manifest), manifest
}

// TestGatewayRoutesServeWhatTheIngressesServe is the parity check described above.
func TestGatewayRoutesServeWhatTheIngressesServe(t *testing.T) {
	t.Parallel()
	for i, p := range renderProfiles {
		for _, tlsOn := range []bool{true, false} {
			name := fmt.Sprintf("%s/tls=%v", p.name, tlsOn)
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				instance := profileInstance(i)
				nginx, nginxManifest := renderRoutingVariant(t, p, instance, "nginx", tlsOn, "")
				gw, gwManifest := renderRoutingVariant(t, p, instance, "gateway", tlsOn, "")

				// Exactly one front end per render.
				if len(nginx.gateways)+len(nginx.routes) != 0 {
					t.Fatalf("controller nginx rendered Gateway API objects")
				}
				if len(gw.ingresses) != 0 {
					t.Fatalf("controller gateway rendered Ingress objects")
				}
				if len(nginx.ingresses) == 0 || len(gw.routes) == 0 || len(gw.gateways) != 1 {
					t.Fatalf("a front end rendered nothing: %d Ingresses, %d HTTPRoutes, %d Gateways",
						len(nginx.ingresses), len(gw.routes), len(gw.gateways))
				}
				// Nothing else in the release depends on the front end: the Issuer is shared.
				if strings.Count(nginxManifest, "kind: Issuer") != strings.Count(gwManifest, "kind: Issuer") {
					t.Fatalf("the two renders differ in their cert-manager Issuers")
				}

				diffs, accepted, err := routingDifferences(nginx, gw)
				if err != nil {
					t.Fatal(err)
				}
				// The accepted leniencies of ingress-nginx must still be observed, or the
				// acceptance has outlived the probe that justifies it.
				for _, kind := range []string{"case", "character-prefix"} {
					if accepted[kind] == 0 {
						t.Fatalf("no probe exercised the accepted %s divergence: %v", kind, accepted)
					}
				}
				if len(diffs) != 0 {
					t.Fatalf("the Gateway routes do not serve what the Ingresses serve:\n  %s", strings.Join(diffs, "\n  "))
				}

				// Self-registration (G14) is one of the paths served, and says so by name.
				const reg = "/api/device-management/provisioning/acme/register"
				host := nginx.ingressHosts()[0]
				want := answer("device-management:8080 /provisioning/acme/register")
				for label, resolve := range map[string]func(string, string) (answer, error){
					"Ingress": func(h, p string) (answer, error) { return nginx.resolveIngress(h, p, false) },
					"Gateway": gw.resolveGateway,
				} {
					got, err := resolve(host, reg)
					if err != nil || got != want {
						t.Fatalf("%s: POST %s -> %q (%v), want %q", label, reg, got, err, want)
					}
				}

				// The inbound integration webhook (G17): the public path carries no instance id and
				// reaches the event-sources ingest listener at the instance-less route, with the
				// tenant, the integration and the optional message type intact.
				const ingest = "event-sources:8081"
				resolvers := map[string]func(string, string) (answer, error){
					"Ingress": func(h, p string) (answer, error) { return nginx.resolveIngress(h, p, false) },
					"Gateway": gw.resolveGateway,
				}
				for in, out := range map[string]string{
					"/api/integrations/acme/cs":           "/integrations/acme/cs",
					"/api/integrations/acme/cs/up":        "/integrations/acme/cs/up",
					"/api/integrations/acme/cs/join-acpt": "/integrations/acme/cs/join-acpt",
				} {
					for label, resolve := range resolvers {
						got, err := resolve(host, in)
						if want := answer(ingest + " " + out); err != nil || got != want {
							t.Fatalf("%s: POST %s -> %q (%v), want %q", label, in, got, err, want)
						}
					}
				}

				// NEGATIVE CONTROL. Of everything either front end is asked, the only requests that
				// reach the ingest listener are /api/integrations/..., and they arrive at
				// /integrations/...: never at /events, and never at an instance-qualified path.
				probes, err := routingProbes(renderedRouting{ingresses: nginx.ingresses, routes: gw.routes})
				if err != nil {
					t.Fatal(err)
				}
				probes = append(probes, "/events", "/acme/events", instance+"/acme/events", "/"+instance+"/acme/events",
					"/api/integrations/acme/events", "/api/integrationsX/acme/cs",
					"/api/event-sources/events", "/api/event-sources/integrations/acme/cs", "/integrations/acme/cs",
					"/api/integrations", "/api/integrations/")
				reached := 0
				for _, pth := range probes {
					for label, resolve := range resolvers {
						got, err := resolve(host, pth)
						if err != nil {
							t.Fatal(err)
						}
						backend, upstream, _ := strings.Cut(string(got), " ")
						if backend != ingest {
							continue
						}
						reached++
						if !(strings.EqualFold(pth, "/api/integrations") || strings.HasPrefix(strings.ToLower(pth), "/api/integrations/")) || !strings.HasPrefix(upstream, "/integrations/") {
							t.Fatalf("%s: %s reaches the ingest listener at %q; only /api/integrations/... may, and only at /integrations/...", label, pth, upstream)
						}
					}
				}
				if reached == 0 {
					t.Fatalf("no probe reached the ingest listener: the negative control looked at nothing")
				}
				// The listener's general device-ingest route is not published by either front end.
				for label, resolve := range resolvers {
					for _, pth := range []string{"/" + instance + "/acme/events", "/events", "/api/integrations/acme/events"} {
						got, _ := resolve(host, pth)
						if strings.HasSuffix(string(got), "/"+instance+"/acme/events") && strings.HasPrefix(string(got), ingest) {
							t.Fatalf("%s: %s reaches the device-ingest route: %q", label, pth, got)
						}
					}
				}
			})
		}
	}
}

// TestGatewayCarriesTheIngressTLS pins what is not a path: the host, the certificate
// secret, the issuer annotation, and the plain-HTTP redirect ingress-nginx performs for a
// host with a TLS section.
func TestGatewayCarriesTheIngressTLS(t *testing.T) {
	t.Parallel()
	p := renderProfiles[0]
	cases := []struct {
		name          string
		tlsOn         bool
		clusterIssuer string
		wantIssuerKey string
		wantIssuer    string
	}{
		{"self-signed", true, "", "cert-manager.io/issuer", "devicechain-selfsigned"},
		{"cluster issuer", true, "letsencrypt", "cert-manager.io/cluster-issuer", "letsencrypt"},
		{"no TLS", false, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			nginx, _ := renderRoutingVariant(t, p, profileInstance(0), "nginx", c.tlsOn, c.clusterIssuer)
			gw, _ := renderRoutingVariant(t, p, profileInstance(0), "gateway", c.tlsOn, c.clusterIssuer)

			var api *routingObject
			for i := range nginx.ingresses {
				if nginx.ingresses[i].Metadata.Name == "devicechain-api" {
					api = &nginx.ingresses[i]
				}
			}
			if api == nil {
				t.Fatal("no devicechain-api Ingress")
			}
			var ispec ingressSpec
			if err := yaml.Unmarshal(api.Spec, &ispec); err != nil {
				t.Fatal(err)
			}
			var gspec gatewaySpec
			if err := yaml.Unmarshal(gw.gateways[0].Spec, &gspec); err != nil {
				t.Fatal(err)
			}
			host := ispec.Rules[0].Host
			if gw.gateways[0].Metadata.Namespace != api.Metadata.Namespace {
				t.Errorf("Gateway namespace %q, Ingress namespace %q", gw.gateways[0].Metadata.Namespace, api.Metadata.Namespace)
			}

			// The issuer annotation, key and value.
			for _, k := range []string{"cert-manager.io/issuer", "cert-manager.io/cluster-issuer"} {
				if ig, gg := api.Metadata.Annotations[k], gw.gateways[0].Metadata.Annotations[k]; ig != gg {
					t.Errorf("annotation %s: Ingress %q, Gateway %q", k, ig, gg)
				}
			}
			if c.wantIssuerKey != "" && gw.gateways[0].Metadata.Annotations[c.wantIssuerKey] != c.wantIssuer {
				t.Errorf("Gateway %s = %q, want %q", c.wantIssuerKey, gw.gateways[0].Metadata.Annotations[c.wantIssuerKey], c.wantIssuer)
			}

			// Listeners: http always; https exactly when the Ingress has a tls section.
			var https, httpL = -1, -1
			for i, l := range gspec.Listeners {
				if l.Hostname != host {
					t.Errorf("listener %s serves %q, the Ingress serves %q", l.Name, l.Hostname, host)
				}
				if l.AllowedRoutes.Namespaces.From != "Same" {
					t.Errorf("listener %s admits routes from %q, want Same", l.Name, l.AllowedRoutes.Namespaces.From)
				}
				switch l.Name {
				case "http":
					httpL = i
				case "https":
					https = i
				}
			}
			if httpL < 0 {
				t.Error("no http listener")
			}
			if (len(ispec.TLS) > 0) != (https >= 0) {
				t.Fatalf("Ingress has TLS=%v, Gateway has an https listener=%v", len(ispec.TLS) > 0, https >= 0)
			}
			if https >= 0 {
				l := gspec.Listeners[https]
				if l.Port != 443 || l.Protocol != "HTTPS" || l.TLS == nil || l.TLS.Mode != "Terminate" ||
					len(l.TLS.CertificateRefs) != 1 || l.TLS.CertificateRefs[0].Name != ispec.TLS[0].SecretName ||
					l.TLS.CertificateRefs[0].Kind != "Secret" {
					t.Errorf("https listener %+v does not terminate with the Ingress's secret %q", l, ispec.TLS[0].SecretName)
				}
				if !slices.Equal(ispec.TLS[0].Hosts, []string{host}) {
					t.Errorf("Ingress TLS hosts %v", ispec.TLS[0].Hosts)
				}
			}

			// Redirect route iff TLS; every backend route attaches to the right listener.
			sawRedirect := false
			for _, rt := range gw.routes {
				var spec httpRouteSpec
				if err := yaml.Unmarshal(rt.Spec, &spec); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(spec.Hostnames, []string{host}) {
					t.Errorf("HTTPRoute/%s hostnames %v, want [%s]", rt.Metadata.Name, spec.Hostnames, host)
				}
				if len(spec.ParentRefs) != 1 || spec.ParentRefs[0].Name != "devicechain" {
					t.Errorf("HTTPRoute/%s parentRefs %+v", rt.Metadata.Name, spec.ParentRefs)
					continue
				}
				section := spec.ParentRefs[0].SectionName
				redirect := len(spec.Rules) == 1 && len(spec.Rules[0].BackendRefs) == 0
				if !redirect {
					// ingress-nginx sends HSTS by default for a TLS host, and nothing else in the
					// release does, so every backend rule on the https listener must set it.
					const hsts = "max-age=31536000; includeSubDomains"
					for i, rule := range spec.Rules {
						got := ""
						for _, f := range rule.Filters {
							if f.ResponseHeaderModifier != nil {
								for _, h := range f.ResponseHeaderModifier.Set {
									if h.Name == "Strict-Transport-Security" {
										got = h.Value
									}
								}
							}
						}
						want := ""
						if https >= 0 {
							want = hsts
						}
						if got != want {
							t.Errorf("HTTPRoute/%s rule %d sets Strict-Transport-Security %q, want %q", rt.Metadata.Name, i, got, want)
						}
					}
				}
				switch {
				case redirect:
					sawRedirect = true
					f := spec.Rules[0].Filters
					if section != "http" || len(f) != 1 || f[0].RequestRedirect == nil ||
						f[0].RequestRedirect.Scheme != "https" || f[0].RequestRedirect.StatusCode != 308 {
						t.Errorf("redirect route %s is not a 308 to https on the http listener: %+v", rt.Metadata.Name, spec)
					}
				case https >= 0 && section != "https":
					t.Errorf("HTTPRoute/%s serves a backend on listener %q although TLS is on", rt.Metadata.Name, section)
				case https < 0 && section != "http":
					t.Errorf("HTTPRoute/%s attaches to listener %q", rt.Metadata.Name, section)
				}
			}
			if sawRedirect != (https >= 0) {
				t.Errorf("redirect route present=%v with TLS=%v", sawRedirect, https >= 0)
			}
		})
	}
}

// TestRoutingParityCheckFailsWhenARouteIsMissing is the negative control: the comparison
// is only worth something if dropping a route from one side makes it fail. Each case
// removes one thing from the Gateway render of a profile that serves every route kind,
// and the comparison must name it.
func TestRoutingParityCheckFailsWhenARouteIsMissing(t *testing.T) {
	t.Parallel()
	var full renderProfile
	var fullInstance string
	for i, p := range renderProfiles {
		if p.name == "--profile full" {
			full, fullInstance = p, profileInstance(i)
		}
	}
	if full.name == "" {
		t.Fatal("no --profile full row in renderProfiles")
	}
	nginx, _ := renderRoutingVariant(t, full, fullInstance, "nginx", true, "")
	gw, _ := renderRoutingVariant(t, full, fullInstance, "gateway", true, "")

	if diffs, _, err := routingDifferences(nginx, gw); err != nil || len(diffs) != 0 {
		t.Fatalf("the unmodified renders must agree before the control means anything: %v %v", diffs, err)
	}

	dropRoute := func(name string) renderedRouting {
		out := gw
		out.routes = nil
		for _, rt := range gw.routes {
			if rt.Metadata.Name != name {
				out.routes = append(out.routes, rt)
			}
		}
		if len(out.routes) != len(gw.routes)-1 {
			t.Fatalf("control: no HTTPRoute %q to drop", name)
		}
		return out
	}
	// dropRule removes the rule whose first match is `prefix` from every route.
	dropRule := func(prefix string) renderedRouting {
		out := gw
		out.routes = nil
		dropped := false
		for _, rt := range gw.routes {
			var spec map[string]interface{}
			if err := yaml.Unmarshal(rt.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			var keep []interface{}
			rules, _ := spec["rules"].([]interface{})
			for _, r := range rules {
				m := r.(map[string]interface{})["matches"].([]interface{})[0].(map[string]interface{})["path"].(map[string]interface{})["value"]
				if m == prefix {
					dropped = true
					continue
				}
				keep = append(keep, r)
			}
			spec["rules"] = keep
			raw, _ := yaml.Marshal(spec)
			rt.Spec = raw
			out.routes = append(out.routes, rt)
		}
		if !dropped {
			t.Fatalf("control: no rule matching %q to drop", prefix)
		}
		return out
	}
	// breakRewrite points a rule at the wrong upstream path: the prefix is no longer stripped.
	breakRewrite := func() renderedRouting {
		out := gw
		out.routes = nil
		for _, rt := range gw.routes {
			rt.Spec = []byte(strings.ReplaceAll(string(rt.Spec), `"replacePrefixMatch":"/"`, `"replacePrefixMatch":"/x/"`))
			out.routes = append(out.routes, rt)
		}
		return out
	}

	cases := []struct {
		name   string
		mutant renderedRouting
		want   string
	}{
		{"the console route is dropped", dropRoute("devicechain-web"), "frontend:80"},
		{"the well-known route is dropped", dropRoute("devicechain-wellknown"), "/.well-known/oauth-"},
		{"the update-management rule is dropped (upload and download go with it)", dropRule("/api/update-management"), "/updates/artifacts/tok/content"},
		{"one area's rule is dropped", dropRule("/api/device-state"), "device-state:8080"},
		{"the prefix rewrite is wrong", breakRewrite(), "/x/"},
	}
	for _, c := range cases {
		diffs, _, err := routingDifferences(nginx, c.mutant)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(diffs) == 0 {
			t.Errorf("%s: the comparison found no difference, so it cannot be trusted to find one", c.name)
			continue
		}
		if !strings.Contains(strings.Join(diffs, "\n"), c.want) {
			t.Errorf("%s: the differences do not name %q:\n  %s", c.name, c.want, strings.Join(diffs, "\n  "))
		}
	}
}

// TestAnOverfullAPIRouteFailsTheRender is the negative control for the 16-rule guard in
// gateway.yaml. No real area set reaches 16 public areas, so the guard's threshold is
// lowered in an in-memory copy of the chart: the render must then refuse, naming the limit,
// rather than emit an HTTPRoute a cluster would reject.
func TestAnOverfullAPIRouteFailsTheRender(t *testing.T) {
	t.Parallel()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	p := renderProfiles[0]
	vals := profileValues(t, ch, p, profileInstance(0))
	vals["ingress"].(map[string]interface{})["controller"] = "gateway"

	// Unmodified, the default profile renders.
	if _, err := renderChartClientSide(t.Context(), ch, vals); err != nil {
		t.Fatalf("the unmodified chart must render before the control means anything: %v", err)
	}

	patched := false
	for _, f := range ch.Templates {
		if strings.HasSuffix(f.Name, "templates/gateway.yaml") {
			const guard = "{{- if gt $rules 16 }}"
			if !strings.Contains(string(f.Data), guard) {
				t.Fatalf("gateway.yaml no longer carries the rule-count guard %q: update this control", guard)
			}
			f.Data = []byte(strings.Replace(string(f.Data), guard, "{{- if gt $rules 2 }}", 1))
			patched = true
		}
	}
	if !patched {
		t.Fatal("no gateway.yaml template to patch")
	}
	_, err = renderChartClientSide(t.Context(), ch, vals)
	if err == nil || !strings.Contains(err.Error(), "at most 16") {
		t.Fatalf("a route over the rule limit rendered (err = %v), want a refusal naming the limit of 16", err)
	}
}

// An instance named "integrations" would make the ingest listener's own first path segment the
// segment the public integrations route is published under, so the chart refuses it on both
// front ends rather than render a route that could reach the device-ingest path.
func TestAnInstanceNamedIntegrationsIsRefusedByBothFrontEnds(t *testing.T) {
	t.Parallel()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading the embedded chart: %v", err)
	}
	for _, controller := range []string{"nginx", "gateway"} {
		for _, name := range []string{"integrations", "integrations-x"} {
			// Composing the install values renders the chart, so the refusal can surface from either step.
			vals, err := routingVariantValues(t.Context(), ch, renderProfiles[0], name, controller, true, "")
			if err == nil {
				_, err = renderChartClientSide(t.Context(), ch, vals)
			}
			if name == "integrations" {
				if err == nil || !strings.Contains(err.Error(), "reserved") {
					t.Fatalf("%s: instance %q rendered (err %v), want a refusal naming the reservation", controller, name, err)
				}
			} else if err != nil {
				// The paired positive: a neighbouring name is fine, so the refusal is the name's.
				t.Fatalf("%s: instance %q was refused: %v", controller, name, err)
			}
		}
	}
	for _, name := range []string{"integrations", "Integrations"} {
		if err := ValidateInstanceName(name); err == nil {
			t.Errorf("dcctl accepts the instance name %q", name)
		}
	}
	if err := ValidateInstanceName("integrations-x"); err != nil {
		t.Errorf("dcctl refuses the neighbouring name: %v", err)
	}
}
