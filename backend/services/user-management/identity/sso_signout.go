// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"net/url"

	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/rs/zerolog/log"
)

// SIGNING OUT AT THE PROVIDER (OpenID Connect RP-Initiated Logout 1.0).
//
// Signing out here always ends THIS platform's sign-in first (endSignIn). Only then, and
// only when the sign-in came through a connection whose administrator switched
// ProviderSignOut on, is the person also offered the provider's own sign-out: the answer
// carries the address of the APPROVED snapshot's end_session_endpoint, and the app sends
// the browser there. Nothing here dials the provider, so a provider that is slow, down or
// gone cannot hold the platform sign-out open; every failure past endSignIn is "no
// provider address", never an error.
//
// The request carries client_id and post_logout_redirect_uri and nothing else:
//
//   - no id_token_hint: the provider's ID token is verified at the callback and never kept.
//     Keeping it would mean storing, for every sign-in, a provider-signed statement of the
//     person's identity claims, to use once. The specification allows client_id in its
//     place; the provider may then ask the person to confirm.
//   - no state: the return lands on the plain sign-in page, which acts on no parameter, so
//     there is nothing a forged return could trigger and nothing to correlate.
//
// post_logout_redirect_uri is never taken from the caller. The caller names an APP, and
// the address is built here from the issuer's own origin and a fixed path for that app,
// so no input can make the provider send the browser anywhere else.

// SignOutReturn names the app a provider sign-out returns the browser to.
type SignOutReturn string

const (
	// SignOutReturnConsole returns to the console's sign-in page.
	SignOutReturnConsole SignOutReturn = "CONSOLE"
	// SignOutReturnDashboard returns to the dashboard app.
	SignOutReturnDashboard SignOutReturn = "DASHBOARD"
)

// signOutReturnPaths are the only places a provider sign-out returns to, on the issuer's
// own origin, in the order they are listed for registration.
var signOutReturnPaths = []struct {
	app  SignOutReturn
	path string
}{
	{SignOutReturnConsole, "/login"},
	{SignOutReturnDashboard, "/dash/"},
}

// signOutReturnURL is the post-logout address for app, or "" when app is not one of ours
// or the instance has no public issuer.
func (m *Manager) signOutReturnURL(app SignOutReturn) string {
	issuer, err := url.Parse(m.issuerUrl)
	if err != nil || issuer.Scheme == "" || issuer.Host == "" {
		return ""
	}
	for _, p := range signOutReturnPaths {
		if p.app == app {
			return (&url.URL{Scheme: issuer.Scheme, Host: issuer.Host, Path: p.path}).String()
		}
	}
	return ""
}

// signOutReturnURLs lists every post-logout address, for the administrator to register at
// the provider. Empty when the instance has no public issuer.
func (m *Manager) signOutReturnURLs() []string {
	out := make([]string, 0, len(signOutReturnPaths))
	for _, p := range signOutReturnPaths {
		if u := m.signOutReturnURL(p.app); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// SignOut ends the sign-in token belongs to, exactly as Logout does, and then -- only when
// app is named -- returns the provider's sign-out address to send the browser to, or ""
// when there is none to offer. The error is Logout's: once the sign-in has ended, nothing
// about the provider can make this fail.
func (m *Manager) SignOut(ctx context.Context, token string, app SignOutReturn) (string, error) {
	rec, err := m.endSignIn(ctx, token)
	if err != nil {
		return "", err
	}
	if app == "" || rec == nil {
		return "", nil
	}
	return m.providerSignOutURL(ctx, rec, app), nil
}

// providerSignOutURL is the RP-initiated logout request for an ended sign-in, or "".
func (m *Manager) providerSignOutURL(ctx context.Context, rec *sessionRecord, app SignOutReturn) string {
	if rec.Method != SessionMethodSso || rec.ConnectionID == 0 {
		return ""
	}
	returnTo := m.signOutReturnURL(app)
	if returnTo == "" {
		return ""
	}
	conn, err := m.iam.IdpConnectionByID(ctx, rec.ConnectionID)
	if err != nil {
		// Deleted since the sign-in, or unreadable: the platform sign-out stands either way.
		log.Debug().Err(err).Msg("Sign-out: no connection to sign out at; signed out of the platform only.")
		return ""
	}
	return providerSignOutRequest(conn, returnTo)
}

// providerSignOutRequest builds the request from the connection's APPROVED snapshot, or ""
// when the connection does not offer one: switched off, the connection disabled, its
// snapshot unapproved, or the provider publishing no end_session_endpoint.
func providerSignOutRequest(conn *iam.IdpConnection, returnTo string) string {
	if !conn.ProviderSignOut || !usableConnection(conn) || conn.DiscoverySnapshot.EndSessionEndpoint == "" {
		return ""
	}
	u, err := url.Parse(conn.DiscoverySnapshot.EndSessionEndpoint)
	if err != nil || checkScheme(u) != nil || u.Host == "" {
		return ""
	}
	// The endpoint's own query parameters are kept; the specification allows them.
	q := u.Query()
	q.Set("client_id", conn.ClientID)
	q.Set("post_logout_redirect_uri", returnTo)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String()
}
