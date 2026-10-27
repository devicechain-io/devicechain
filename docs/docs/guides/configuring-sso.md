---
title: Configure single sign-on
---

# Configure single sign-on

This guide connects an OpenID Connect identity provider so that a tenant's people sign in with their organization account. For how the feature behaves and where its limits are, read [Single sign-on](../concepts/single-sign-on.md) first.

:::caution Provider recipes are not yet live-tested
The recipes for Microsoft Entra ID, Okta and Google Workspace below are written from each provider's documentation and from the claims DeviceChain checks. They have **not yet been run against live tenants** of those providers. The sign-in flow itself is tested end to end against a stand-in OpenID Connect provider. Expect to adjust a console label or a claim name, and tell us what you find.
:::

## Before you start {#before}

- **The instance needs a public address.** Single sign-on needs `auth.issuerUrl` ([below](#deployment)). If it is empty, creating a connection fails with "sso requires a public issuer URL".
- **You need a superuser sign-in** to the console (**Admin → Identity providers**) or the admin API. An identity whose system role holds `user:write` can use the admin API too, but not the console's admin pages, which open for a superuser only.
- **At the provider** you need to register DeviceChain as an application (a "web" or "confidential" client using the authorization code flow) and note its client id and client secret.
- **Decide the tenant and the domain.** A tenant connection serves tenants you name, and speaks for the email domains you assert.

## Create a connection {#create}

1. In the console, open **Admin → Identity providers** and choose **New connection**. Give it a token (letters, digits and hyphens), a name, the kind (`tenant`), the provider's **issuer**, and the client id and secret. The issuer and kind cannot be changed later; a different issuer is a different connection. If the provider's certificate was signed by a private certificate authority, also supply its certificate here ([details](#private-ca)).
2. DeviceChain fetches the provider's discovery document and holds it for approval. The connection starts disabled. The page shows its callback URL, labelled **Callback address**: `<issuerUrl>/sso/callback/<token>`. Register exactly that as the redirect URI at the provider.
3. Review the endpoints and choose **Approve endpoints**.
4. Add the email domains the provider speaks for (for example `example.com`).
5. Choose the tenants it serves.
6. If people should be created on first sign-in, turn on provisioning and pick the default roles (`setIdpJit`). Add group-to-role mappings if the provider sends groups (`setIdpRoleMappings`). The console has no controls for these yet, so use the admin API.
7. Choose **Enable**.
8. Optionally set the session ceiling (12 hours by default; `setIdpSessionMaxAge` in the admin API, as the console has no control for it yet) and, once everyone has a way in, turn on **Require SSO** for the tenant (**Admin → Tenants**). The console asks you to confirm first, because it ends the tenant's password sessions.

The same steps are available in the admin GraphQL API: `createIdpConnection`, `approveIdpDiscovery`, `addIdpDomain`, `setIdpConnectionTenants`, `setIdpJit`, `setIdpRoleMappings`, `updateIdpConnection` (`enabled: true`), `setIdpSessionMaxAge` and `setTenantSsoEnforced`. A superuser can also use `setIdpRequiredClaims` to change the claims a connection requires.

An instance connection (for your own operators) is created the same way with kind `instance`, and additionally needs `requiredAmr` (for example `["mfa"]`) or `mfaEnforcedByIdp: true`. Link each operator explicitly with `linkIdentity`; nothing links them automatically.

## Two-factor authentication {#two-factor}

A tenant's [two-factor requirement](./two-factor-authentication.md#require) binds **password** sign-ins only. A person who signs in through single sign-on does not meet it, and the tenant's setting does not apply to them. What governs an SSO sign-in is the connection: require an authentication method on it (`requiredAmr`, for example `["mfa"]`) so that the identity provider must have performed its own second factor, or confirm the provider enforces one with `mfaEnforcedByIdp`. A tenant that turns on **Require SSO** has no password sign-ins left for the requirement to apply to.

## Sign out at the provider {#provider-sign-out}

By default, signing out of the console or the dashboard app ends the DeviceChain session only. The person stays signed in at the identity provider, so their next **Sign in with SSO** may go straight through without asking for a password. A connection can also send people to the provider's own sign-out page when they sign out (OpenID Connect RP-initiated logout). It is **off by default**, because signing out at the provider usually ends the person's session there for every application the provider serves.

1. Check that the approved discovery document shows a **Sign-out endpoint**. A provider that publishes none cannot be signed out of this way: the setting then has no effect, and signing out still ends the DeviceChain session.
2. On the connection's **Settings** tab, under **Sign out at the provider**, copy the two **Post-logout redirect addresses** and register them at the provider as post-logout (sign-out) redirect URIs. They are `<origin>/login` for the console and `<origin>/dash/` for the dashboard app, where `<origin>` is the scheme and host of the instance's [public address](#issuer-url).
3. Tick **Also sign people out at the provider**. In the admin API this is `updateIdpConnection` with `providerSignOut: true`.

What happens when someone signs out:

- **The DeviceChain session always ends first**, on the server, whatever the provider does afterwards. Only then is the browser sent to the provider, which sends it back to the sign-in page.
- The request carries the connection's client id and the return address, and nothing else. DeviceChain does not keep the provider's ID token, so it sends no `id_token_hint`, and the provider may ask the person to confirm. A provider that refuses a sign-out request without `id_token_hint` cannot be used with this setting; leave it off.
- The return address is built by the server from the instance's public address and cannot be supplied by a caller. If it is not registered at the provider, the provider shows its own error instead of returning the person; the DeviceChain session has ended regardless.
- It applies only to sign-ins through that connection while it is enabled and its discovery document is approved. A password sign-in signs out of DeviceChain only.

## Provider recipes {#recipes}

In each recipe, replace `<callback URL>` with the connection's callback URL.

### Microsoft Entra ID {#entra}

1. In the Entra admin center, **App registrations → New registration**. Account type: **Accounts in this organizational directory only**. Redirect URI: platform **Web**, value `<callback URL>`.
2. **Certificates & secrets → New client secret.** Copy the value.
3. **Token configuration → Add optional claim → ID token**, and add `email` and `xms_edov`. Microsoft sends no `email_verified`; DeviceChain trusts an address only when `xms_edov` is true, so without this claim every sign-in that is not already linked to an identity is refused (`refused_unverified_email`), and no link request is left either.
4. For group mapping: **Token configuration → Add groups claim**, choose **Groups assigned to the application**. The `groups` claim then carries group object ids, so map those ids, not group names.
5. Create the connection with:
   - issuer `https://login.microsoftonline.com/<tenant GUID>/v2.0`, with your directory's tenant id. `common`, `organizations` and `consumers` are refused.
   - required claim `tid` = the same tenant GUID. It is mandatory for a Microsoft issuer.
   - scopes `openid`, `email`, `profile`.
6. **Groups overage.** A token can list at most about 200 groups; beyond that Entra sends a pointer instead of the list (`hasgroups` or `_claim_names`). DeviceChain **refuses the sign-in** when a connection maps groups (`refused_groups_overage`; the person is told to ask their administrator) instead of treating the person as having no groups. Avoid it by assigning only the needed groups to the application. Sovereign-cloud issuer hosts are treated like `login.microsoftonline.com`.
7. For an instance connection, Entra reports `mfa` in `amr` when multi-factor authentication was performed; require it.

### Okta {#okta}

1. In the Okta admin console, **Applications → Create App Integration → OIDC - OpenID Connect → Web Application**. Sign-in redirect URI: `<callback URL>`. Assign the people or groups who may use it.
2. Copy the client id and client secret.
3. Issuer: your Okta org URL for the org authorization server (`https://<org>.okta.com`), or a custom authorization server (`https://<org>.okta.com/oauth2/<id>`). It must equal the `issuer` in that server's discovery document exactly.
4. Okta sends `email` and `email_verified`. If your Okta users' emails are not verified, sign-ins are refused.
5. For group mapping: add the `groups` scope to the connection's scopes, and add a **groups claim** (Sign On tab, or the authorization server's Claims) with a filter such as "Starts with" or "Matches regex" so only the groups you want are sent. Map the group names. Okta's `amr` includes `mfa` when multi-factor was used.
6. Okta has no tenant-pinning claim like `tid`; each organization has its own issuer, which is what pins it.

### Google Workspace {#google}

1. In the Google Cloud console, **APIs & Services → OAuth consent screen**: user type **Internal** (limits sign-in to your Workspace). **Credentials → Create credentials → OAuth client ID → Web application**, authorized redirect URI `<callback URL>`.
2. Issuer `https://accounts.google.com`. Required claim `hd` = your Workspace primary domain (for example `example.com`). It is mandatory: Google signs for every Google account, personal ones included, and only Workspace accounts carry `hd`. The token's `hd` must also equal the domain of the address.
3. Add the same domain as the connection's verified domain.
4. Google ID tokens carry **no groups** and no `amr`. Use the connection's default roles, not group mapping, and do not use Google for an instance connection unless you set `mfaEnforcedByIdp` and enforce 2-step verification in Workspace.

### Other providers {#other}

Any OpenID Connect provider that serves a discovery document over https, signs ID tokens with RS256, ES256 or PS256, and sends `email` with `email_verified` works. `none` and HMAC signatures are refused. Set the email claim and groups claim names on the connection if the provider uses different ones.

## Deployment and configuration {#deployment}

### The public address {#issuer-url}

user-management's `auth.issuerUrl` must be the externally reachable address of its API, for example `https://dc.example.com/api/user-management`. The chart derives it from the ingress (`ingress.host`, with `ingress.tls.enabled`), so a normal install needs nothing. Set it yourself under `functionalAreas.user-management.config.auth.issuerUrl` to override it, or to `""` to keep single sign-on and the OAuth authorization server off. An http address on a real hostname is refused at startup; serve the instance over https. Your ingress must route `/api/user-management/sso/` to user-management (the chart's ingress does).

### Reaching the provider {#egress}

Every request to a provider goes through the egress guard, which refuses private, loopback and link-local destinations. A public provider needs nothing. For an on-premises provider (ADFS, Keycloak, Dex on your network), allow its address range under `instance.config.infrastructure.egress.allowedDestinations` as narrow CIDRs. The allow list is instance-wide and also applies to webhooks and connectors, so prefer a /32. The provider's certificate must chain to the system roots in user-management's image, or to a [private CA bundle](#private-ca) you give that connection.

### A provider with a private CA certificate {#private-ca}

An on-premises provider often serves a TLS certificate signed by your organization's own certificate authority, which the system roots do not include. Without help, creating the connection fails with "certificate signed by unknown authority". Give the connection that CA's certificate instead of weakening anything instance-wide:

1. Get the CA certificate (and any intermediate certificates) as PEM text: one or more `-----BEGIN CERTIFICATE-----` blocks, up to 64 KiB. Never a private key; a private key in the text is refused and is not echoed back.
2. When you create the connection, paste it into **Private CA certificates** or choose the file. On an existing connection, use **Settings → Private CA**, then **Verify and save bundle**. In the admin API this is `caBundle` on `createIdpConnection`, or `setIdpCaBundle`.
3. Check the listed certificates: each shows its subject, expiry and SHA-256 fingerprint. Compare the fingerprint with the one your CA publishes. A certificate that does not say it is a certificate authority is trusted as exactly that certificate and is flagged **Not a CA**.

What it does and does not do:

- The bundle applies to **that connection only**: discovery, the key set and the token exchange all trust it, and no other connection or feature does. It is trusted **in addition to** the system roots, so a provider on a public certificate keeps working with a bundle set.
- It does not change the egress guard. The provider's address must still be reachable under [the allow list](#egress); a certificate never makes a forbidden address reachable.
- The bundle is pasted or uploaded, never fetched from an address, because a fetched trust root is chosen by whoever answers the fetch.
- Saving a new bundle first reaches the provider's discovery address with it, and refuses (saving nothing) if the certificate does not chain to it. If the provider then names different endpoints than the ones you approved, the approval is withdrawn and you review them again; if it names the same ones, the approval stands, so rotating the CA does not lock anyone out.
- The provider's certificate must still carry its address (a DNS name or IP address matching the issuer) in its subject alternative names, and be within its validity period. Replace the bundle before a CA certificate expires. **Remove bundle** (`setIdpCaBundle` with an empty string) returns the connection to the system roots alone.
- The bundle is a public certificate, so it is stored with the connection rather than as a secret, and it is included in a database backup.

### Behind a load balancer {#load-balancer}

Preserve the client address (`externalTrafficPolicy: Local` or proxy protocol), or all your users share one rate-limit allowance. See [the concept page](../concepts/single-sign-on.md#load-balancer).

### Alerts {#alerts}

| Alert | Severity | Meaning | What to do |
| --- | --- | --- | --- |
| `SsoIdpUnreachable` | warning | For ten minutes user-management has failed to reach a provider's token endpoint or signing keys, so people who sign in through it are refused. Password sign-in is unaffected. | Check the provider is up and resolves from the cluster, that the egress allow list permits an on-premises range, and, if the provider moved an endpoint, refresh and approve the discovery snapshot. |
| `SsoUnverifiedIdentityRefused` | warning | A sign-in was refused although the provider vouched for it: `refused_unverified_email`, `refused_unverified_domain`, `refused_required_claim` or `refused_system_role_identity` (the `outcome` label). Nobody was signed in. | Usually a misconfigured connection or someone probing one. Read the audit journal for the asserted address and the provider's own sign-in log. |
| `SsoStateStoreFull` | critical | The store holding begun sign-ins is full, so no single sign-on can begin through any provider until begun sign-ins expire, ten minutes after the flood of them stops. Password sign-in is unaffected. | Someone is likely calling the public sign-in start in a loop: find them in the ingress logs and block upstream. If the volume is legitimate, raise `instance.config.infrastructure.nats.kvStateMaxBytes`. |

The metrics behind them are `devicechain_usermanagement_sso_logins_total{connection,outcome}` and `devicechain_usermanagement_sso_idp_fetch_failures_total{connection}`. Other outcomes you may see in the first one: `state_invalid` (an expired, replayed or mismatched sign-in), `idp_error`, `rate_limited`, `refused_link_required`, `refused_groups_overage`, `refused_token_invalid`, `refused_issuer_mismatch`.

## Troubleshooting {#troubleshooting}

| Symptom | Likely cause |
| --- | --- |
| The console asks for a password for an address that should use single sign-on, or **Sign in with SSO** says "Single sign-on is not set up for that email address" | No usable connection speaks for the domain: it is not asserted on any connection, or its connection is disabled or its discovery is not approved. A sign-in by address answers all of these the same way (the connection shows up under the `refused_connection_unavailable` outcome), and the console also asks for a password when the instance has no single sign-on, so check **Admin → Identity providers**. |
| "Single sign-on is not available right now" | The instance is not set up for single sign-on (see [Deployment](#deployment)), or too many sign-ins were started from the same address: wait a moment and try again. |
| "Your account exists but is not linked" | The address already has an identity: approve its link request. |
| Every sign-in refused as unverified | The provider does not send a verified email (for Entra, add `xms_edov`). |
| Refused after a provider change | The provider moved an endpoint: **Refresh from provider** and approve again. |
| "no enabled identity provider connection serves this tenant" | Connect and enable a provider before turning on **Require SSO**. (error code `SSO_ENFORCE_NO_CONNECTION`.) |
| "this is the last enabled identity provider connection" | Turn **Require SSO** off for the tenant, or enable another connection that serves it, first; then disable or delete the connection. (error code `SSO_ENFORCED_TENANT_STRANDED`; the console explains it.) |
| Linking an account: "Single sign-on is not set up for your email domain" | No enabled, approved tenant connection has the person's email domain. Add the domain to the connection, or enable or approve it. (`SSO_LINK_NO_CONNECTION`.) |
| Linking an account: "sign in again" | The password sign-in is older than 15 minutes, has ended, or came through single sign-on. Sign in with the password and link again. (`SSO_LINK_SESSION_REQUIRED`, `SSO_LINK_PASSWORD_REQUIRED`, or `link_session_ended` on return.) |
| Linking an account: "a different email address" | The person signed in at the provider as someone else, or the provider reports another address. (`link_email_mismatch` on return.) |
| Linking an account: "already linked" | The identity is already linked on that connection, or the provider account is linked to another identity: unlink it first. (`SSO_LINK_ALREADY_LINKED`, or `link_already_linked` on return.) |
| Linking an account: "Ask a superuser to link it" | The identity holds a system role; link it with `linkIdentity`. (`SSO_LINK_NOT_ALLOWED`.) |
