// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"html/template"
	"net/http"
	"net/url"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
)

// maxAuthorizeBodyBytes caps the authorize POST body (a small set of form fields).
const maxAuthorizeBodyBytes = 1 << 16

// AuthorizeService is the set of Manager operations the authorize HTTP handler
// drives (ADR-047). Modeling it as an interface keeps the handler's control flow —
// especially the resolve-before-anything ordering that protects the redirect_uri —
// unit-testable with a fake.
type AuthorizeService interface {
	// ResolveAuthorizeClient verifies the client + redirect_uri (the checks that must
	// pass before any error may be redirected). Returns ErrAuthorizeClientUnknown /
	// ErrAuthorizeRedirectUnregistered for the render-error-page cases.
	ResolveAuthorizeClient(ctx context.Context, clientID, redirectURI string) (*iam.OAuthClient, error)
	// Login authenticates an email/password and returns the identity token +
	// memberships (no tenant chosen yet).
	Login(ctx context.Context, email, password string) (*IdentityAuth, error)
	// IdentityEmail validates the identity token carried across steps → subject email.
	IdentityEmail(identityToken string) (string, error)
	// IssueAuthorizationCode mints the one-time code for the consenting user.
	IssueAuthorizationCode(ctx context.Context, client *iam.OAuthClient, p AuthorizeParams, email, tenant string) (string, error)
}

// AuthorizeHandler builds the OAuth 2.1 authorization endpoint (ADR-047 / RFC 6749
// §4.1.1). It is a browser-facing, server-rendered flow (no JS, no cookies): GET
// shows the login form; the login POST authenticates and shows the tenant-select +
// consent form; the consent POST issues a code and 302-redirects back to the
// client. State is carried statelessly in hidden form fields — including a
// short-lived identity token as the proof of authentication across the two POSTs —
// so no server-side session is needed. Every request re-resolves the client and
// re-validates the parameters, so tampering a hidden field cannot bypass a check.
func AuthorizeHandler(svc AuthorizeService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			renderAuthorizeError(w, http.StatusMethodNotAllowed, "Method not allowed", "Use GET or POST.")
			return
		}

		values, err := authorizeValues(w, r)
		if err != nil {
			renderAuthorizeError(w, http.StatusBadRequest, "Invalid request", "The request could not be parsed.")
			return
		}
		p := ParseAuthorizeParams(values)

		// 1. Resolve the client + redirect_uri FIRST. Until this succeeds the
		// redirect_uri is untrusted, so any failure renders an error page and never
		// redirects (RFC 6749 §4.1.2.1). Treat every non-nil error this way (a bare DB
		// error is not a redirectable condition either).
		client, err := svc.ResolveAuthorizeClient(r.Context(), p.ClientID, p.RedirectURI)
		if err != nil {
			renderAuthorizeError(w, http.StatusBadRequest, "Invalid request",
				"The client or redirect URI is not recognized. Do not proceed.")
			return
		}

		// 2. Validate the request parameters. Now that the redirect_uri is trusted,
		// these errors are reported by redirecting back with an error code.
		if verr := ValidateAuthorizeRequest(client, p); verr != nil {
			if re, ok := verr.(*authorizeRedirectError); ok {
				redirectAuthorizeError(w, r, p.RedirectURI, re.Code, re.Desc, p.State)
				return
			}
			renderAuthorizeError(w, http.StatusBadRequest, "Invalid request", "The request is invalid.")
			return
		}

		// 3. Drive the step. GET (or a POST with no step) shows the login form.
		switch values.Get("step") {
		case "login":
			handleAuthorizeLogin(w, r, svc, client, p)
		case "consent":
			handleAuthorizeConsent(w, r, svc, client, p)
		default:
			renderAuthorizeLogin(w, p, "")
		}
	}
}

// handleAuthorizeLogin authenticates the login POST and renders the consent form on
// success, or re-renders the login form with a generic error on failure.
func handleAuthorizeLogin(w http.ResponseWriter, r *http.Request, svc AuthorizeService, client *iam.OAuthClient, p AuthorizeParams) {
	ident, err := svc.Login(r.Context(), r.PostFormValue("email"), r.PostFormValue("password"))
	if err != nil {
		renderAuthorizeLogin(w, p, "Invalid email or password.")
		return
	}
	renderAuthorizeConsent(w, p, client, ident)
}

// handleAuthorizeConsent handles the consent POST: a denial redirects back with
// access_denied; an approval validates the carried identity token, issues the code,
// and redirects back with it.
func handleAuthorizeConsent(w http.ResponseWriter, r *http.Request, svc AuthorizeService, client *iam.OAuthClient, p AuthorizeParams) {
	if r.PostFormValue("action") != "allow" {
		redirectAuthorizeError(w, r, p.RedirectURI, "access_denied", "the user denied the request", p.State)
		return
	}
	email, err := svc.IdentityEmail(r.PostFormValue("identity_token"))
	if err != nil {
		renderAuthorizeError(w, http.StatusBadRequest, "Session expired",
			"Your sign-in session expired. Start the authorization again.")
		return
	}
	code, err := svc.IssueAuthorizationCode(r.Context(), client, p, email, r.PostFormValue("tenant"))
	if err != nil {
		// Most commonly the subject can't act in the selected tenant; report it as a
		// denial on the (trusted) redirect_uri rather than leaking the reason.
		redirectAuthorizeError(w, r, p.RedirectURI, "access_denied", "the grant could not be issued", p.State)
		return
	}
	redirectAuthorizeCode(w, r, p.RedirectURI, code, p.State)
}

// authorizeValues returns the request parameters: the query string for GET, the
// parsed (size-capped) form body for POST.
func authorizeValues(w http.ResponseWriter, r *http.Request) (url.Values, error) {
	if r.Method == http.MethodGet {
		return r.URL.Query(), nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthorizeBodyBytes)
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	return r.PostForm, nil
}

// redirectAuthorizeCode 302-redirects to the (already validated) redirect_uri with
// the authorization code + state (RFC 6749 §4.1.2).
func redirectAuthorizeCode(w http.ResponseWriter, r *http.Request, redirectURI, code, state string) {
	http.Redirect(w, r, buildRedirect(redirectURI, map[string]string{"code": code, "state": state}), http.StatusFound)
}

// redirectAuthorizeError 302-redirects to the (already validated) redirect_uri with
// an error code + state (RFC 6749 §4.1.2.1).
func redirectAuthorizeError(w http.ResponseWriter, r *http.Request, redirectURI, code, desc, state string) {
	http.Redirect(w, r, buildRedirect(redirectURI, map[string]string{
		"error": code, "error_description": desc, "state": state,
	}), http.StatusFound)
}

// buildRedirect appends the given (non-empty) params to a redirect URI, preserving
// any query already registered on it.
func buildRedirect(redirectURI string, params map[string]string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		// redirectURI was validated upstream; on the impossible parse failure fall
		// back to the raw string so we never redirect somewhere unexpected.
		return redirectURI
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// --- server-rendered pages (html/template auto-escapes every reflected value) ---

type authorizeFormData struct {
	Params AuthorizeParams
	Error  string
}

type authorizeConsentData struct {
	Params        AuthorizeParams
	ClientName    string
	IdentityToken string
	Memberships   []MembershipInfo
	Superuser     bool
	Scopes        []string
}

// authorizePageHeaders sets the security headers every authorize page needs. The
// consent page embeds a live identity-tier token (and the flow reflects an
// authorization code), so the pages MUST NOT be cached anywhere (no-store) — the
// same rule the token endpoint follows. Framing is denied (clickjacking on a login
// page is the textbook UI-redress target) and a tight CSP limits the blast radius
// of any future injection (the pages are fully self-contained: inline style, form
// posts to self, no scripts or external assets).
func authorizePageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Pragma", "no-cache")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
}

func renderAuthorizeLogin(w http.ResponseWriter, p AuthorizeParams, errMsg string) {
	authorizePageHeaders(w)
	_ = authorizeLoginTmpl.Execute(w, authorizeFormData{Params: p, Error: errMsg})
}

func renderAuthorizeConsent(w http.ResponseWriter, p AuthorizeParams, client *iam.OAuthClient, ident *IdentityAuth) {
	name := client.ClientId
	if client.Name.Valid && client.Name.String != "" {
		name = client.Name.String
	}
	authorizePageHeaders(w)
	_ = authorizeConsentTmpl.Execute(w, authorizeConsentData{
		Params:        p,
		ClientName:    name,
		IdentityToken: ident.IdentityToken,
		Memberships:   ident.Memberships,
		Superuser:     ident.Superuser,
		Scopes:        auth.ParseScope(p.Scope),
	})
}

func renderAuthorizeError(w http.ResponseWriter, status int, title, msg string) {
	authorizePageHeaders(w)
	w.WriteHeader(status)
	_ = authorizeErrorTmpl.Execute(w, map[string]string{"Title": title, "Message": msg})
}

// hiddenParams re-emits the authorize parameters as hidden form fields so the flow
// carries them across POSTs without a server session. Defined as a template so
// html/template escapes each value in attribute context.
const hiddenParamsPartial = `
{{define "hidden"}}
<input type="hidden" name="response_type" value="{{.ResponseType}}">
<input type="hidden" name="client_id" value="{{.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
<input type="hidden" name="scope" value="{{.Scope}}">
<input type="hidden" name="state" value="{{.State}}">
<input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
<input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}">
<input type="hidden" name="resource" value="{{.Resource}}">
{{end}}`

const pageStyle = `<style>
body{font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;background:#0f172a;color:#e2e8f0;display:flex;min-height:100vh;margin:0;align-items:center;justify-content:center}
.card{background:#1e293b;padding:2rem;border-radius:12px;box-shadow:0 10px 30px rgba(0,0,0,.4);width:100%;max-width:380px}
h1{font-size:1.25rem;margin:0 0 1rem}
label{display:block;font-size:.8rem;margin:.75rem 0 .25rem;color:#94a3b8}
input[type=email],input[type=password],select{width:100%;box-sizing:border-box;padding:.6rem;border-radius:8px;border:1px solid #334155;background:#0f172a;color:#e2e8f0}
button{margin-top:1.25rem;padding:.6rem 1rem;border:0;border-radius:8px;background:#6366f1;color:#fff;font-weight:600;cursor:pointer}
button.secondary{background:#334155}
.err{background:#7f1d1d;color:#fecaca;padding:.5rem .75rem;border-radius:8px;font-size:.85rem;margin-bottom:.5rem}
.scopes{background:#0f172a;border:1px solid #334155;border-radius:8px;padding:.75rem;margin:.75rem 0}
.scopes li{font-size:.85rem}
.muted{color:#94a3b8;font-size:.8rem}
.row{display:flex;gap:.5rem}
</style>`

var authorizeLoginTmpl = template.Must(template.New("login").Parse(hiddenParamsPartial + `
<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sign in — DeviceChain</title>` + pageStyle + `</head>
<body><form class="card" method="post" action="">
<h1>Sign in to DeviceChain</h1>
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
{{template "hidden" .Params}}
<input type="hidden" name="step" value="login">
<label>Email</label><input type="email" name="email" autocomplete="username" required autofocus>
<label>Password</label><input type="password" name="password" autocomplete="current-password" required>
<button type="submit">Continue</button>
</form></body></html>`))

var authorizeConsentTmpl = template.Must(template.New("consent").Parse(hiddenParamsPartial + `
<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorize — DeviceChain</title>` + pageStyle + `</head>
<body><form class="card" method="post" action="">
<h1>Authorize {{.ClientName}}</h1>
<p class="muted">{{.ClientName}} is requesting access to your DeviceChain account.</p>
{{template "hidden" .Params}}
<input type="hidden" name="step" value="consent">
<input type="hidden" name="identity_token" value="{{.IdentityToken}}">
<div class="scopes"><strong>This will grant:</strong><ul>{{range .Scopes}}<li>{{.}}</li>{{end}}</ul></div>
<label>Tenant</label>
{{if .Memberships}}
<select name="tenant" required>{{range .Memberships}}<option value="{{.Tenant}}">{{.Tenant}}</option>{{end}}</select>
{{else}}
<input type="text" name="tenant" placeholder="tenant" required>
{{end}}
<div class="row">
<button type="submit" name="action" value="allow">Allow</button>
<button type="submit" name="action" value="deny" class="secondary">Deny</button>
</div>
</form></body></html>`))

var authorizeErrorTmpl = template.Must(template.New("error").Parse(`
<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}} — DeviceChain</title>` + pageStyle + `</head>
<body><div class="card"><h1>{{.Title}}</h1><p class="muted">{{.Message}}</p></div></body></html>`))
