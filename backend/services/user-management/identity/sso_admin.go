// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/devicechain-io/dc-user-management/iam"
	"gorm.io/gorm"
)

// The superuser's control surface over identity provider connections: create, change and
// delete them, assert the domains they speak for, approve what their discovery document
// says, and link external accounts to identities.
//
// Every mutation here is reachable only through the admin API (system authority) and every
// one is enforced somewhere in the sign-in flow in the same release: a setting that nothing
// reads would be a promise this service does not keep.

var (
	// ErrIdpNotFound is returned for a connection token that names nothing.
	ErrIdpNotFound = rdb.NewNotFound("identity provider connection not found")
	// ErrIdpInvalid carries the reason an administrator's input was refused.
	ErrIdpInvalid = errors.New("invalid identity provider connection")
	// ErrIdpNotApproved refuses enabling a connection whose discovery snapshot has not been
	// approved.
	ErrIdpNotApproved = errors.New("approve the connection's discovery snapshot before enabling it")
	// ErrIdpFingerprint refuses an approval of a snapshot other than the one the administrator
	// was shown.
	ErrIdpFingerprint = errors.New("the discovery snapshot changed since you reviewed it; review it again")
	// ErrLinkRequestNotFound is returned for a link request that does not exist (or no longer).
	ErrLinkRequestNotFound = rdb.NewNotFound("link request not found")
	// ErrDispositionRequired refuses approving a link for an identity that already holds
	// memberships in the connection's tenants without saying what becomes of them.
	ErrDispositionRequired = errors.New("this identity already has manual memberships in the connection's tenants; choose 'convert' or 'keep_manual'")
)

// Membership dispositions at link approval.
const (
	// DispositionConvert marks the identity's existing memberships in the connection's tenants
	// as managed by it: its roles there will follow the connection.
	DispositionConvert = "convert"
	// DispositionKeepManual leaves them as they are: an administrator's, not the connection's.
	DispositionKeepManual = "keep_manual"
)

const maxIdpClaims = 16

// DefaultGroupsClaim is the claim a connection reads group values from unless told otherwise.
const DefaultGroupsClaim = "groups"

// actor is the administrator making the call, for the records that name one.
func actor(ctx context.Context) string {
	if c, ok := auth.ClaimsFromContext(ctx); ok && c.Username != "" {
		return c.Username
	}
	return "system"
}

// IdpConnectionInput is the data to create a connection.
type IdpConnectionInput struct {
	Token    string
	Name     string
	Kind     iam.IdpKind
	Issuer   string
	ClientID string
	// ClientSecret is optional (a public client has none). It is sealed in the secret store
	// and never read back.
	ClientSecret string
	Scopes       []string
	EmailClaim   string
	// GroupsClaim names the claim carrying the person's group values; empty means "groups".
	// Only a tenant connection reads it.
	GroupsClaim    string
	RequiredClaims map[string]string
	RequiredAmr    []string
	// MfaEnforcedByIdp is an instance connection's alternative to RequiredAmr: the
	// administrator's own statement that the provider enforces multi-factor sign-in. It is
	// audited at create and not stored (nothing would read it).
	MfaEnforcedByIdp bool
	// CaBundle is optional PEM: private CA certificates to trust for this provider's TLS
	// certificate, beside the system roots (see x509trust.ParseCaBundle).
	CaBundle string
}

// IdpConnectionUpdateRequest is the partial update of a connection. The issuer, kind and
// token are fixed at creation: a different issuer is a different connection, and the rules
// that pin an issuer to one organisation ran against this one. Provisioning and group
// mappings have their own calls (SetIdpJit, SetIdpRoleMappings), and so does the session
// ceiling (SetIdpSessionMaxAge).
type IdpConnectionUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Enabled     dcgraphql.OptionalBool
	ClientID    dcgraphql.OptionalString
	EmailClaim  dcgraphql.OptionalString
	GroupsClaim dcgraphql.OptionalString
	Scopes      dcgraphql.OptionalStringList
	RequiredAmr dcgraphql.OptionalStringList
	// ProviderSignOut switches signing out at the provider too on or off.
	ProviderSignOut dcgraphql.OptionalBool
}

// IdpConnectionDetail is a connection with everything an administrator reviews beside it.
type IdpConnectionDetail struct {
	Connection  iam.IdpConnection
	Domains     []iam.IdpConnectionDomain
	Tenants     []string
	HasSecret   bool
	CallbackURL string
	// SignOutReturnURLs are the addresses a provider sends the browser back to after
	// signing the person out, one per app, to register at the provider.
	SignOutReturnURLs []string
	// RoleMappings are the connection's group-to-role mappings, across its tenants.
	RoleMappings []IdpRoleMappingDetail
	// CaCertificates are the certificates of the connection's private CA bundle, parsed for
	// display; empty when it has none.
	CaCertificates []x509trust.CaCertificate
	// Fingerprint identifies the discovery snapshot, so an approval is of THE snapshot the
	// administrator saw.
	Fingerprint string
}

func snapshotFingerprint(s *iam.DiscoverySnapshot) string {
	if s == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		s.Issuer, s.AuthorizationEndpoint, s.TokenEndpoint, s.JwksURI, s.EndSessionEndpoint}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (m *Manager) loadConnection(ctx context.Context, token string) (*iam.IdpConnection, error) {
	conn, err := m.iam.IdpConnectionByToken(ctx, token)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrIdpNotFound
		}
		return nil, err
	}
	return conn, nil
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrIdpInvalid, fmt.Sprintf(format, a...))
}

func validateClaimRequirements(claims map[string]string, amr []string) error {
	if len(claims) > maxIdpClaims || len(amr) > maxIdpClaims {
		return invalid("at most %d required claims and %d authentication methods", maxIdpClaims, maxIdpClaims)
	}
	for k, v := range claims {
		if k == "" || len(k) > 64 || v == "" || len(v) > 256 {
			return invalid("a required claim needs a name of up to 64 characters and a value of up to 256")
		}
	}
	for _, a := range amr {
		if a == "" || len(a) > 64 {
			return invalid("an authentication method needs a name of up to 64 characters")
		}
	}
	return nil
}

// CreateIdpConnection creates a connection DISABLED, with its discovery snapshot fetched
// and waiting for approval. Nothing can sign in through it until a person approves the
// snapshot and enables it.
func (m *Manager) CreateIdpConnection(ctx context.Context, in IdpConnectionInput) (*IdpConnectionDetail, error) {
	if m.issuerUrl == "" {
		return nil, ErrSsoNoPublicIssuer
	}
	if err := core.ValidateToken(in.Token); err != nil {
		return nil, invalid("the token %q is not a valid token", in.Token)
	}
	if !in.Kind.Valid() {
		return nil, invalid("kind must be %q or %q", iam.IdpKindTenant, iam.IdpKindInstance)
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 256 || in.ClientID == "" || len(in.ClientID) > 256 ||
		len(in.ClientSecret) > 1024 {
		return nil, invalid("a name and a client id are required (name up to 256 characters, client id up to 256)")
	}
	if err := validateClaimRequirements(in.RequiredClaims, in.RequiredAmr); err != nil {
		return nil, err
	}
	if err := ValidateConnectionIssuer(in.Issuer, in.RequiredClaims); err != nil {
		return nil, err
	}
	// An instance connection is a credential for the whole instance. It must report a
	// multi-factor method, or the administrator must say on the record that the provider
	// enforces one.
	if in.Kind == iam.IdpKindInstance && len(in.RequiredAmr) == 0 && !in.MfaEnforcedByIdp {
		return nil, invalid("an instance connection needs required authentication methods (for example mfa) " +
			"or an acknowledgement that the provider enforces multi-factor sign-in")
	}
	groupsClaim := in.GroupsClaim
	if groupsClaim == "" {
		groupsClaim = DefaultGroupsClaim
	}
	if len(groupsClaim) > 64 {
		return nil, invalid("the groups claim name is up to 64 characters")
	}
	caBundle, _, err := parseCaBundle(in.CaBundle)
	if err != nil {
		return nil, err
	}
	snap, err := m.FetchIdpDiscovery(ctx, in.Issuer, caBundle)
	if err != nil {
		return nil, err
	}
	scopes := in.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "email", "profile"}
	}
	conn := &iam.IdpConnection{
		Token: in.Token, Name: strings.TrimSpace(in.Name), Kind: in.Kind, Issuer: in.Issuer,
		ClientID: in.ClientID, Scopes: scopes, EmailClaim: in.EmailClaim, GroupsClaim: groupsClaim,
		RequiredClaims: in.RequiredClaims, RequiredAmr: in.RequiredAmr,
		DiscoverySnapshot: snap, CaBundle: caBundle,
	}
	if err := m.iam.CreateIdpConnection(ctx, conn); err != nil {
		return nil, err
	}
	if in.ClientSecret != "" {
		if err := m.putConnectionSecret(ctx, conn, in.ClientSecret); err != nil {
			// The row and the secret are one fact; do not leave the half.
			if derr := m.iam.DeleteIdpConnection(ctx, m.secrets, conn); derr != nil {
				return nil, errors.Join(err, derr)
			}
			return nil, err
		}
	}
	if caBundle != "" {
		m.recordAuthLabeled(ctx, rdb.AuditOpIdpCaBundleChanged, actor(ctx), "", caAuditLabel(conn.Token, "", caBundle))
	}
	if in.Kind == iam.IdpKindInstance && in.MfaEnforcedByIdp {
		m.recordAuthLabeled(ctx, rdb.AuditOpIdpMfaAcknowledged, actor(ctx), "", conn.Token)
	}
	return m.IdpConnection(ctx, conn.Token)
}

func (m *Manager) putConnectionSecret(ctx context.Context, conn *iam.IdpConnection, secret string) error {
	if m.secrets == nil {
		return errors.New("no secret store is configured")
	}
	ref := iam.IdpConnectionSecretRef(conn.ID)
	if err := m.secrets.Put(ctx, ref, []byte(secret)); err != nil {
		return err
	}
	conn.ClientSecretRef = ref.Name
	return m.iam.UpdateIdpConnection(ctx, conn)
}

// SetIdpConnectionSecret replaces a connection's client secret. WRITE-ONLY: no call returns
// it, and a connection reports only whether it has one.
func (m *Manager) SetIdpConnectionSecret(ctx context.Context, token, secret string) error {
	if secret == "" || len(secret) > 1024 {
		return invalid("a client secret is required (up to 1024 characters)")
	}
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return err
	}
	return m.putConnectionSecret(ctx, conn, secret)
}

// IdpConnection returns one connection with its review detail.
func (m *Manager) IdpConnection(ctx context.Context, token string) (*IdpConnectionDetail, error) {
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return nil, err
	}
	return m.connectionDetail(ctx, conn)
}

func (m *Manager) connectionDetail(ctx context.Context, conn *iam.IdpConnection) (*IdpConnectionDetail, error) {
	domains, err := m.iam.ListIdpDomains(ctx, conn.ID)
	if err != nil {
		return nil, err
	}
	tenants, err := m.iam.ConnectionTenants(ctx, conn.ID)
	if err != nil {
		return nil, err
	}
	has := false
	if m.secrets != nil {
		if has, err = m.secrets.Exists(ctx, iam.IdpConnectionSecretRef(conn.ID)); err != nil {
			return nil, err
		}
	}
	mappings, err := m.roleMappingDetails(ctx, conn.ID)
	if err != nil {
		return nil, err
	}
	// The stored bundle was canonicalised on write; a row that no longer parses (edited in
	// the database) shows no certificates and, because the client builder refuses it too,
	// reaches no provider: it fails closed rather than quietly trusting the system roots.
	_, caCerts, _ := parseCaBundle(conn.CaBundle)
	return &IdpConnectionDetail{
		CaCertificates: caCerts,
		Connection:     *conn, Domains: domains, Tenants: tenants, HasSecret: has, RoleMappings: mappings,
		CallbackURL: m.SsoCallbackURL(conn.Token), Fingerprint: snapshotFingerprint(conn.DiscoverySnapshot),
		SignOutReturnURLs: m.signOutReturnURLs(),
	}, nil
}

// ListIdpConnections returns every connection with its review detail.
func (m *Manager) ListIdpConnections(ctx context.Context) ([]IdpConnectionDetail, error) {
	conns, err := m.iam.ListIdpConnections(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]IdpConnectionDetail, 0, len(conns))
	for i := range conns {
		d, err := m.connectionDetail(ctx, &conns[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, nil
}

// UpdateIdpConnection applies a partial update. Enabling needs an approved snapshot.
func (m *Manager) UpdateIdpConnection(ctx context.Context, token string, req *IdpConnectionUpdateRequest) (*IdpConnectionDetail, error) {
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return nil, err
	}
	if conn.Name, err = req.Name.ApplyToRequired("name", conn.Name); err != nil {
		return nil, err
	}
	if conn.ClientID, err = req.ClientID.ApplyToRequired("clientId", conn.ClientID); err != nil {
		return nil, err
	}
	if conn.EmailClaim, err = req.EmailClaim.ApplyToRequired("emailClaim", conn.EmailClaim); err != nil {
		return nil, err
	}
	if conn.GroupsClaim, err = req.GroupsClaim.ApplyToRequired("groupsClaim", conn.GroupsClaim); err != nil {
		return nil, err
	}
	conn.Scopes = req.Scopes.ApplyTo(conn.Scopes)
	conn.RequiredAmr = req.RequiredAmr.ApplyTo(conn.RequiredAmr)
	enabled, err := req.Enabled.ApplyToRequired("enabled", conn.Enabled)
	if err != nil {
		return nil, err
	}
	if conn.ProviderSignOut, err = req.ProviderSignOut.ApplyToRequired("providerSignOut", conn.ProviderSignOut); err != nil {
		return nil, err
	}
	if strings.TrimSpace(conn.Name) == "" || len(conn.Name) > 256 || len(conn.ClientID) > 256 ||
		len(conn.EmailClaim) > 64 || len(conn.GroupsClaim) > 64 {
		return nil, invalid("name, client id, email claim or groups claim is out of bounds")
	}
	if err := validateClaimRequirements(conn.RequiredClaims, conn.RequiredAmr); err != nil {
		return nil, err
	}
	// An instance connection keeps the multi-factor rule it was created under: clearing
	// required_amr would silently drop it (the acknowledgement was not stored).
	if conn.Kind == iam.IdpKindInstance && len(conn.RequiredAmr) == 0 && req.RequiredAmr.Set {
		return nil, invalid("an instance connection must keep its required authentication methods")
	}
	if enabled && (conn.DiscoverySnapshot == nil || conn.DiscoveryApprovedAt == nil) {
		return nil, ErrIdpNotApproved
	}
	if !enabled {
		if err := m.refuseStrandingEnforcedTenants(ctx, conn, nil); err != nil {
			return nil, err
		}
	}
	conn.Enabled = enabled
	if err := m.iam.UpdateIdpConnection(ctx, conn); err != nil {
		return nil, err
	}
	return m.IdpConnection(ctx, token)
}

// SetIdpCaBundle replaces a connection's private CA bundle; an empty bundle clears it,
// returning the connection to the system roots alone.
//
// A new bundle must WORK: the provider's discovery document is fetched under it before
// anything is saved, so a bundle that does not verify the provider's certificate is refused
// with the reason rather than saved and found at the first sign-in. The document so fetched
// is held to the same rule as RefreshIdpDiscovery: if it names the endpoints a person
// approved, the approval stands (rotating a private CA must not lock people out); if it
// names others, it replaces the snapshot and withdraws the approval, so an address that a
// different trust root vouched for never redirects a sign-in unreviewed. Clearing fetches
// nothing (the provider may be unreachable by the time the bundle is retired); the next
// refresh shows whether the provider still answers under the system roots.
func (m *Manager) SetIdpCaBundle(ctx context.Context, token, bundle string) (*IdpConnectionDetail, error) {
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return nil, err
	}
	canonical, _, err := parseCaBundle(bundle)
	if err != nil {
		return nil, err
	}
	oldBundle := conn.CaBundle
	snapChanged := false
	if canonical != "" {
		// A failure here is the administrator trying a bundle, not the provider failing
		// people, so it is not counted as a provider fetch failure.
		snap, err := m.FetchIdpDiscovery(ctx, conn.Issuer, canonical)
		if err != nil {
			return nil, err
		}
		if conn.DiscoverySnapshot == nil || !conn.DiscoverySnapshot.SameEndpoints(*snap) {
			conn.DiscoverySnapshot = snap
			conn.DiscoveryApprovedAt = nil
			conn.DiscoveryApprovedBy = ""
			snapChanged = true
		}
	}
	bundleChanged := canonical != oldBundle
	if !bundleChanged && !snapChanged {
		return m.connectionDetail(ctx, conn)
	}
	conn.CaBundle = canonical
	// Only the columns this call changes: a concurrent edit to any other (disabling the
	// connection during an incident) must not be reverted by this write.
	fields := []string{"CaBundle"}
	if snapChanged {
		fields = append(fields, "DiscoverySnapshot", "DiscoveryApprovedAt", "DiscoveryApprovedBy")
	}
	if err := m.iam.UpdateIdpConnectionFields(ctx, conn, fields...); err != nil {
		return nil, err
	}
	if bundleChanged {
		m.recordAuthLabeled(ctx, rdb.AuditOpIdpCaBundleChanged, actor(ctx), "", caAuditLabel(conn.Token, oldBundle, canonical))
	}
	return m.IdpConnection(ctx, token)
}

// SetIdpRequiredClaims replaces a connection's required claims, re-running the issuer rules
// (a Google connection cannot drop its hd, an Entra one its tid).
func (m *Manager) SetIdpRequiredClaims(ctx context.Context, token string, claims map[string]string) (*IdpConnectionDetail, error) {
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := validateClaimRequirements(claims, nil); err != nil {
		return nil, err
	}
	if err := ValidateConnectionIssuer(conn.Issuer, claims); err != nil {
		return nil, err
	}
	conn.RequiredClaims = claims
	if err := m.iam.UpdateIdpConnection(ctx, conn); err != nil {
		return nil, err
	}
	return m.IdpConnection(ctx, token)
}

// DeleteIdpConnection removes a connection and everything attached to it. Idempotent.
func (m *Manager) DeleteIdpConnection(ctx context.Context, token string) (bool, error) {
	conn, err := m.loadConnection(ctx, token)
	if errors.Is(err, ErrIdpNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := m.refuseStrandingEnforcedTenants(ctx, conn, nil); err != nil {
		return false, err
	}
	if err := m.iam.DeleteIdpConnection(ctx, m.secrets, conn); err != nil {
		return false, err
	}
	return true, nil
}

// SetIdpConnectionTenants makes a tenant connection serve exactly the given tenants. A
// session through it can reach those tenants and no others.
func (m *Manager) SetIdpConnectionTenants(ctx context.Context, token string, tenants []string) (*IdpConnectionDetail, error) {
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return nil, err
	}
	if len(tenants) > 256 {
		return nil, invalid("at most 256 tenants")
	}
	for _, t := range tenants {
		if err := core.ValidateToken(t); err != nil {
			return nil, invalid("%q is not a valid tenant token", t)
		}
	}
	if err := m.refuseStrandingEnforcedTenants(ctx, conn, tenants); err != nil {
		return nil, err
	}
	if err := m.iam.ReplaceConnectionTenants(ctx, conn, tenants); err != nil {
		return nil, err
	}
	return m.IdpConnection(ctx, token)
}

// AddIdpDomain asserts that a connection speaks for an email domain. The superuser's word
// is the verification (recorded with their name). Freemail and public-suffix domains are
// refused, and one domain belongs to one connection.
func (m *Manager) AddIdpDomain(ctx context.Context, token, domain string) (*IdpConnectionDetail, error) {
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return nil, err
	}
	normal, err := ValidateDomain(domain)
	if err != nil {
		return nil, err
	}
	if _, err := m.iam.AddIdpDomain(ctx, conn.ID, normal, actor(ctx)); err != nil {
		return nil, err
	}
	return m.IdpConnection(ctx, token)
}

// RemoveIdpDomain removes a connection's domain.
func (m *Manager) RemoveIdpDomain(ctx context.Context, token, domain string) (*IdpConnectionDetail, error) {
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return nil, err
	}
	normal, err := NormalizeDomain(domain)
	if err != nil {
		return nil, err
	}
	if _, err := m.iam.RemoveIdpDomain(ctx, conn.ID, normal); err != nil {
		return nil, err
	}
	return m.IdpConnection(ctx, token)
}

// RefreshIdpDiscovery fetches the provider's discovery document again. A document that names
// the same endpoints changes nothing. One that does not REPLACES the snapshot and withdraws
// its approval, so the connection refuses sign-ins until a person approves the new snapshot:
// an endpoint that moved never redirects a sign-in unreviewed.
func (m *Manager) RefreshIdpDiscovery(ctx context.Context, token string) (*IdpConnectionDetail, error) {
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return nil, err
	}
	snap, err := m.FetchIdpDiscovery(ctx, conn.Issuer, conn.CaBundle)
	if err != nil {
		m.countIdpFailure(conn.Token)
		return nil, err
	}
	if conn.DiscoverySnapshot == nil || !conn.DiscoverySnapshot.SameEndpoints(*snap) {
		conn.DiscoverySnapshot = snap
		conn.DiscoveryApprovedAt = nil
		conn.DiscoveryApprovedBy = ""
		if err := m.iam.UpdateIdpConnectionFields(ctx, conn, "DiscoverySnapshot", "DiscoveryApprovedAt", "DiscoveryApprovedBy"); err != nil {
			return nil, err
		}
	}
	return m.IdpConnection(ctx, token)
}

// ApproveIdpDiscovery approves the connection's current snapshot, identified by the
// fingerprint the administrator was shown.
func (m *Manager) ApproveIdpDiscovery(ctx context.Context, token, fingerprint string) (*IdpConnectionDetail, error) {
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return nil, err
	}
	if conn.DiscoverySnapshot == nil || fingerprint == "" || fingerprint != snapshotFingerprint(conn.DiscoverySnapshot) {
		return nil, ErrIdpFingerprint
	}
	now := time.Now().UTC()
	conn.DiscoveryApprovedAt, conn.DiscoveryApprovedBy = &now, actor(ctx)
	if err := m.iam.UpdateIdpConnection(ctx, conn); err != nil {
		return nil, err
	}
	return m.IdpConnection(ctx, token)
}

// LinkIdentity binds an external subject on a connection to the identity with email. This
// is the only way an INSTANCE connection reaches an identity (besides approving a request).
// A tenant connection refuses an identity that holds a system role.
func (m *Manager) LinkIdentity(ctx context.Context, email, connectionToken, subject string) error {
	conn, err := m.loadConnection(ctx, connectionToken)
	if err != nil {
		return err
	}
	if subject == "" || len(subject) > 512 {
		return invalid("a subject of up to 512 characters is required")
	}
	email, err = iam.CanonicalEmail(email)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrIdpInvalid, err)
	}
	id, err := m.iam.IdentityByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: no identity with that email", ErrIdpInvalid)
		}
		return err
	}
	return m.createLink(ctx, conn, id, subject)
}

func (m *Manager) createLink(ctx context.Context, conn *iam.IdpConnection, id *iam.Identity, subject string) error {
	if conn.Kind == iam.IdpKindTenant && len(id.SystemRoles) > 0 {
		return invalid("a tenant connection cannot be linked to an identity that holds a system role")
	}
	if err := m.iam.CreateIdentityLink(ctx, &iam.IdentityLink{
		IdentityID: id.ID, ConnectionID: conn.ID, Subject: subject, CreatedBy: actor(ctx),
	}); err != nil {
		return err
	}
	m.recordAuth(ctx, rdb.AuditOpIdentityLinked, actor(ctx), "")
	return nil
}

// UnlinkIdentity removes an identity's link on a connection. Its single sign-on sessions end
// at their next exchange (admitTenant).
func (m *Manager) UnlinkIdentity(ctx context.Context, email, connectionToken string) (bool, error) {
	conn, err := m.loadConnection(ctx, connectionToken)
	if err != nil {
		return false, err
	}
	// An administrator action: IdentityForAdmin also finds an identity whose stored email is
	// not canonical, so its link can be removed and the subject linked to its replacement.
	id, err := m.iam.IdentityForAdmin(ctx, email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		if errors.Is(err, iam.ErrEmailInvalid) {
			return false, fmt.Errorf("%w: %v", ErrIdpInvalid, err)
		}
		return false, err
	}
	removed, err := m.iam.DeleteIdentityLink(ctx, id.ID, conn.ID)
	if err != nil {
		return false, err
	}
	if removed {
		m.recordAuth(ctx, rdb.AuditOpIdentityUnlinked, actor(ctx), "")
	}
	return removed, nil
}

// SsoLinkRequestView is a pending request with what the superuser needs to decide it.
type SsoLinkRequestView struct {
	Request    iam.SsoLinkRequest
	Connection string
	// IdentityExists is whether an identity with the request's email exists now.
	IdentityExists bool
	// SubjectChanged flags a request whose identity is already linked on the connection under
	// a DIFFERENT subject: what a recycled address looks like.
	SubjectChanged bool
	// ManualMemberships are the tenants, among those the connection serves, where the
	// identity already holds a membership no connection manages.
	ManualMemberships []string
}

// ListSsoLinkRequests returns the pending requests, oldest first.
func (m *Manager) ListSsoLinkRequests(ctx context.Context) ([]SsoLinkRequestView, error) {
	reqs, err := m.iam.ListSsoLinkRequests(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SsoLinkRequestView, 0, len(reqs))
	for _, r := range reqs {
		v, err := m.linkRequestView(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

func (m *Manager) linkRequestView(ctx context.Context, r iam.SsoLinkRequest) (*SsoLinkRequestView, error) {
	v := &SsoLinkRequestView{Request: r}
	conn, err := m.iam.IdpConnectionByID(ctx, r.ConnectionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return v, nil
		}
		return nil, err
	}
	v.Connection = conn.Token
	// The identity is read NOW, not when the request was made: a request keeps only the
	// email, and the identity behind it may have been deleted or created since.
	id, err := m.iam.IdentityByEmail(ctx, r.Email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return v, nil
		}
		return nil, err
	}
	v.IdentityExists = true
	links, err := m.iam.LinksForIdentity(ctx, id.ID)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.ConnectionID == conn.ID && l.Subject != r.Subject {
			v.SubjectChanged = true
		}
	}
	tenants, err := m.iam.ConnectionTenants(ctx, conn.ID)
	if err != nil {
		return nil, err
	}
	v.ManualMemberships = manualMembershipsIn(id, tenants)
	return v, nil
}

// manualMembershipsIn lists the tenants among tenants where id holds a membership that no
// connection manages.
func manualMembershipsIn(id *iam.Identity, tenants []string) []string {
	in := map[string]struct{}{}
	for _, t := range tenants {
		in[t] = struct{}{}
	}
	var out []string
	for _, mem := range id.Memberships {
		if _, ok := in[mem.TenantId]; ok && mem.ManagedBy == nil {
			out = append(out, mem.TenantId)
		}
	}
	sort.Strings(out)
	return out
}

// ApproveSsoLinkRequest links the request's external account to the identity that has its
// email NOW. The identity is resolved again here, never taken from when the request was
// made. If the identity already holds manual memberships in the connection's tenants a
// disposition for them is required.
func (m *Manager) ApproveSsoLinkRequest(ctx context.Context, requestID uint, disposition string) error {
	req, err := m.iam.SsoLinkRequestByID(ctx, requestID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrLinkRequestNotFound
		}
		return err
	}
	conn, err := m.iam.IdpConnectionByID(ctx, req.ConnectionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrLinkRequestNotFound
		}
		return err
	}
	id, err := m.iam.IdentityByEmail(ctx, req.Email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: no identity has the email %s now; create one, or reject the request", ErrIdpInvalid, req.Email)
		}
		return err
	}
	manual := []string(nil)
	if conn.Kind == iam.IdpKindTenant {
		tenants, err := m.iam.ConnectionTenants(ctx, conn.ID)
		if err != nil {
			return err
		}
		manual = manualMembershipsIn(id, tenants)
	}
	if len(manual) > 0 && disposition != DispositionConvert && disposition != DispositionKeepManual {
		return ErrDispositionRequired
	}
	if err := m.createLink(ctx, conn, id, req.Subject); err != nil {
		return err
	}
	if len(manual) > 0 && disposition == DispositionConvert {
		if err := m.iam.MarkMembershipsManagedBy(ctx, id.ID, manual, conn.ID); err != nil {
			return err
		}
	}
	_, err = m.iam.DeleteSsoLinkRequest(ctx, req.ID)
	return err
}

// RejectSsoLinkRequest deletes a request without linking anything.
func (m *Manager) RejectSsoLinkRequest(ctx context.Context, requestID uint) (bool, error) {
	return m.iam.DeleteSsoLinkRequest(ctx, requestID)
}
