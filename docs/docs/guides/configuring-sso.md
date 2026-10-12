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
- **You need a superuser sign-in** to the console (**Admin → Identity providers**) or the admin API.
- **At the provider** you need to register DeviceChain as an application (a "web" or "confidential" client using the authorization code flow) and note its client id and client secret.
- **Decide the tenant and the domain.** A tenant connection serves tenants you name, and speaks for the email domains you assert.

## Create a connection {#create}

1. In the console, open **Admin → Identity providers** and choose **New connection**. Give it a token (letters, digits and hyphens), a name, the kind (`tenant`), the provider's **issuer**, and the client id and secret. The issuer and kind cannot be changed later; a different issuer is a different connection.
2. DeviceChain fetches the provider's discovery document and holds it for approval. The connection starts disabled. The page shows its callback URL, labelled **Callback address**: `<issuerUrl>/sso/callback/<token>`. Register exactly that as the redirect URI at the provider.
3. Review the endpoints and choose **Approve endpoints**.
4. Add the email domains the provider speaks for (for example `example.com`).
5. Choose the tenants it serves.
6. If people should be created on first sign-in, turn on provisioning and pick the default roles (`setIdpJit`). Add group-to-role mappings if the provider sends groups (`setIdpRoleMappings`). The console has no controls for these yet, so use the admin API.
7. Choose **Enable**.
8. Optionally set the session ceiling (12 hours by default; `setIdpSessionMaxAge` in the admin API, as the console has no control for it yet) and, once everyone has a way in, turn on **Require SSO** for the tenant (**Admin → Tenants**). The console asks you to confirm first, because it ends the tenant's password sessions.

The same steps are available in the admin GraphQL API: `createIdpConnection`, `approveIdpDiscovery`, `addIdpDomain`, `setIdpConnectionTenants`, `setIdpJit`, `setIdpRoleMappings`, `updateIdpConnection` (`enabled: true`), `setIdpSessionMaxAge` and `setTenantSsoEnforced`. A superuser can also use `setIdpRequiredClaims` to change the claims a connection requires.

An instance connection (for your own operators) is created the same way with kind `instance`, and additionally needs `requiredAmr` (for example `["mfa"]`) or `mfaEnforcedByIdp: true`. Link each operator explicitly with `linkIdentity`; nothing links them automatically.

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

Every request to a provider goes through the egress guard, which refuses private, loopback and link-local destinations. A public provider needs nothing. For an on-premises provider (ADFS, Keycloak, Dex on your network), allow its address range under `instance.config.infrastructure.egress.allowedDestinations` as narrow CIDRs. The allow list is instance-wide and also applies to webhooks and connectors, so prefer a /32. The provider's certificate must chain to the system roots in user-management's image; there is no per-connection CA setting in this release.

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
| The console asks for a password for an address that should use single sign-on, or **Sign in with SSO** says "Single sign-on is not set up for that email address" | The domain is not asserted on any connection. The console answers the same way for a domain with no connection, a disabled connection and an instance without single sign-on, so check **Admin → Identity providers**. |
| "Single sign-on is not available right now" | The connection for that domain is disabled, or its discovery is not approved. |
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
