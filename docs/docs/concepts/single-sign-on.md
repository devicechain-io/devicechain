---
title: Single sign-on
---

# Single sign-on

People can sign in to DeviceChain through their organization's own identity provider (Microsoft Entra ID, Okta, Google Workspace, or any OpenID Connect provider) instead of a DeviceChain password. An administrator connects the provider once; the people it speaks for then sign in with the account they already have, and the provider, not DeviceChain, decides who they are and whether their account is still active.

This page explains how the feature behaves and where its limits are. To set it up, see [Configure single sign-on](../guides/configuring-sso.md).

:::note Status
Single sign-on speaks **OpenID Connect**. SAML is not supported. The recipes for Microsoft Entra ID, Okta and Google Workspace follow each provider's documented behavior but have **not yet been tested against live tenants of those providers**; the flow is tested end to end against a stand-in OpenID Connect provider.
:::

## Connections {#connections}

A **connection** is one identity provider registered with the instance. Only a superuser creates, changes or deletes connections, through **Admin → Identity providers** in the console or the admin API. There are two kinds:

| | Tenant connection | Instance connection |
| --- | --- | --- |
| Whose provider | A customer's or organization's own people | The operator's own workforce |
| Serves | One or more tenants you choose | No tenant: it signs operators in to the instance |
| Creates identities on first sign-in | Yes, if you turn on provisioning | Never |
| Reaches an identity | By email match after a link is approved, or by provisioning | Only by an explicit link a superuser makes |
| Can reach an identity that holds a system role | **Never** | Yes: that is its purpose |
| Enters a tenant | Only the tenants it serves | Only as a superuser, which is recorded in the audit journal |

A tenant connection is structurally unable to sign anyone in as an operator. Whatever its provider claims, the session it produces carries no system roles and no administration authority, and a sign-in that resolves to an identity holding a system role is refused. An instance connection must require a multi-factor method (`requiredAmr`, for example `mfa`) or carry your recorded statement (`mfaEnforcedByIdp`) that the provider enforces one. The seeded superuser password remains as break-glass.

A connection is created **disabled**. It becomes usable when a person has approved its discovery snapshot and enabled it.

## Verified domains {#domains}

A connection speaks for a set of email domains, each asserted by a superuser and recorded with their name. When someone types their email at the console sign-in page, the exact domain selects the connection. A domain belongs to one connection, and DeviceChain refuses public suffixes (`com`, `co.uk`) and well-known consumer mail domains (`gmail.com`, `outlook.com` and the like), because an assertion for those would route strangers to your provider.

A first sign-in that is not already linked to an identity is accepted only if the provider vouches for the address (`email_verified`; for Microsoft Entra ID, the `xms_edov` claim) **and** the address is in one of the connection's domains. There is no setting that trusts an address the provider did not verify.

Shared multi-tenant issuers are refused unless the connection is pinned to your organization:

- A Microsoft issuer must name one tenant id, and the connection must require the claim `tid` equal to it. `common`, `organizations` and `consumers` are refused outright.
- A Google connection must require the claim `hd` (your Workspace domain), and the token's `hd` must match the domain of the address. Without it, any Google account, including personal ones, could sign in.

## Approving the discovery document {#discovery}

When a connection is created, DeviceChain fetches the provider's discovery document and keeps a **snapshot** of its endpoints (authorization, token, signing keys, sign-out). A superuser reviews the snapshot and approves it (**Approve endpoints**) by its fingerprint. Sign-ins use only the approved endpoints and never fetch discovery live. If the provider later moves an endpoint, **Refresh from provider** replaces the snapshot, withdraws the approval, and the connection refuses sign-ins until a person approves the new one. Only the *contents* of the signing-key set are fetched at sign-in, because key rotation is routine.

Every request to a provider goes through the platform's egress guard, so a provider at a private, loopback or link-local address is refused unless the operator allowed its range (see [Configure single sign-on](../guides/configuring-sso.md#deployment)).

## Signing in {#sign-in}

The console opens on the email address alone. If an enabled connection speaks for the address's domain, it offers **Continue with single sign-on**, with **Use a password instead** beside it, so an account that has only a password (an operator's, say) can still sign in. Any other address is asked for its password. **Sign in with SSO** stays on the screen for anyone who wants to go straight to their provider, and the dashboard app keeps its single form with that button beside the password.

The check is about the **domain, never the person**. It answers the same way whether or not an identity exists for the address, so it cannot be used to find accounts. A domain nobody has set up and a domain whose people use passwords get exactly the same screen, and so does an instance with single sign-on switched off, a throttled address or an unreachable server: the only difference a person can see is whether single sign-on is offered. The console is not told whether a tenant requires single sign-on, which would reveal which domains belong to such a tenant, so it never hides the password field on that basis; the server judges that when a tenant is chosen (see below).

The flow is OpenID Connect authorization code with PKCE:

1. The person enters their email and chooses **Continue with single sign-on** (or **Sign in with SSO**). The browser keeps a random secret and sends its hash.
2. DeviceChain records the sign-in (valid ten minutes, usable once) and sends the browser to the provider.
3. The provider authenticates the person and returns to `<issuerUrl>/sso/callback/<connection>`.
4. DeviceChain checks the answer: the state and the connection match, the token's signature, issuer, audience, expiry and nonce, the connection's required claims and methods, and the identity rules below.
5. It hands the browser a one-minute, single-use code. The browser redeems it with the secret from step 1, so only the browser that began the sign-in can finish it, and receives the same kind of identity token a password sign-in gives. The person then chooses a tenant as usual.

The page that receives the code (`/login/sso/complete` for the console, `/dash/sso/complete` for the dashboard app) takes it out of the address bar at once and is served with `Referrer-Policy: no-referrer`, so nothing it loads can carry the address. Once the sign-in is finished, or the person goes back to the sign-in form, the app is loaded afresh rather than continuing on that page, so the rest of the session sends a normal `Referer`; the default OpenStreetMap basemap requires one. If your own proxy sits in front of the console, scope any Referer-stripping header to those two exact paths.

When a sign-in is refused, the person sees a coarse message ("Single sign-on did not allow this account in", "your account is not linked yet") and never which check failed. (Linking one's own account is the exception: the person is already signed in and every check is about their own account, so a link refusal says which check failed; see [linking](#linking).) The precise outcome is counted in the `devicechain_usermanagement_sso_logins_total` metric and refusals are written to the audit journal with the address the provider asserted.

## Identities, linking and provisioning {#linking}

DeviceChain never links an external account to an existing identity because the email addresses look alike. A link is made in one of four ways, and every one is audited:

- **Provisioning (just-in-time).** On a tenant connection with provisioning on, a sign-in whose address has **no identity at all** creates one (with no password) and links it. This is the only automatic write.
- **A superuser approves a link request.** A sign-in that is not linked and is not provisioned (its address already has an identity, or the connection does not provision) is refused, and a **link request** appears under **Identity providers → Link requests**. The superuser approves or rejects it. If the identity already holds memberships in the connection's tenants that no connection manages, approval requires a choice: `convert` makes the connection manage them from then on, `keep_manual` leaves them alone. A request flagged *subject changed* means the identity is already linked on that connection under a different account, which is what a recycled email address looks like; treat it with suspicion.
- **A superuser links directly** (`linkIdentity`, `unlinkIdentity` in the admin API). An instance connection is only ever linked by a superuser: this way, or by approving a link request.
- **The person links their own account.** Signed in with a password, they can start `beginSsoLink`. Nobody names a connection: DeviceChain uses the tenant connection whose verified domain is the identity's own email domain (the same lookup a sign-in by email uses), and it must be enabled and approved. The provider must report the identity's own email address. In the console, choose **Link single sign-on account** in the user menu and continue to the provider; the same action is available in the API. The password sign-in it needs lasts 15 minutes, so after that the console asks the person to sign in again first. Each refusal says which check failed: before the provider, `beginSsoLink` fails with an `extensions.code` (`SSO_LINK_NO_CONNECTION`, `SSO_LINK_ALREADY_LINKED`, `SSO_LINK_SESSION_REQUIRED`, `SSO_LINK_PASSWORD_REQUIRED`, `SSO_LINK_NOT_ALLOWED`); after it, the return address carries `error=link_email_mismatch`, `link_unverified_email`, `link_session_ended`, `link_already_linked` or `link_not_allowed`.

A tenant connection refuses an identity that holds any system role, even if the link was made before the role was granted. An identity that has been linked to a connection cannot take a second account on the same connection.

The email address on an identity is never updated from a provider's claims.

## Group-to-role mapping {#role-mapping}

For a tenant connection, each sign-in recomputes the person's **managed** memberships in the connection's tenants: the connection's default roles, plus the role of every mapping whose group appears in the token's groups claim. No roles at all disables the managed membership; a missing one is created. Memberships an administrator made by hand are never touched, and neither are memberships managed by a different connection.

The limits are deliberate:

- **Tenant roles only.** A mapping can never name a system role. A mapping, or a default role, that carries a tenant's full authority needs an explicit acknowledgement and is audited.
- **Applied at sign-in.** A change at the provider reaches DeviceChain at the person's next sign-in, or when their session next hits its ceiling (below).
- **Groups must arrive in the token.** If the connection maps groups and the provider reports that a person has too many groups to list (Microsoft Entra ID's "groups overage"), the sign-in is **refused** rather than treated as "no groups", which would disable the person's access. Fix it at the provider by sending only the groups the application needs. A groups claim that is neither a string nor a list of strings, or that lists more than 1,000 groups, is also refused.
- **Deleting a connection disables, never deletes,** the memberships it managed, so a role the provider granted never silently becomes a manual grant.

## Requiring single sign-on for a tenant {#require-sso}

A superuser can set **Require SSO** on a tenant (**Admin → Tenants → the tenant**, or `setTenantSsoEnforced`). Then:

- A password session cannot enter that tenant. The console explains why and sends the person to **Sign in with SSO**; passwords on the MCP authorization page are refused the same way.
- Password sessions already inside the tenant end at their next refresh.
- **Superusers are exempt**, and each of their entries is audited, so an identity-provider outage never locks the operator out.
- **The stranded-tenant refusal.** Turning it on needs at least one *enabled* connection serving the tenant. And the reverse: you cannot disable or delete the last enabled connection that serves a tenant requiring single sign-on, nor remove that tenant from it. DeviceChain refuses instead of silently re-admitting passwords. Turn the requirement off, or enable another connection that serves the tenant, first; then make the change.

Requiring single sign-on does not remove anyone's password. To make an individual SSO-only, an administrator uses `clearPassword`, which is refused unless the identity is linked to an enabled connection it can sign in through.

## Session ceiling, sign-out and revoking {#sessions}

Each connection has a **session ceiling**: how long a sign-in through it may last, counted from the moment DeviceChain accepted it (not the provider's own authentication time) and never extended by a refresh. The default is 12 hours; the range is 15 minutes to 7 days, and the upper bound is lower if the deployment's refresh-token lifetime is shorter. When the ceiling passes, the session ends at its next exchange and the person signs in again. It applies to sessions already open.

- **Sign-out** from the console or dashboard ends the DeviceChain session on the server. It does not sign the person out of the identity provider.
- **Revoking** (`revokeSessions` in the admin API; the console has no button for it yet) ends every sign-in an identity has on every device, password and SSO, including MCP grants.
- **Disabling or deleting a connection**, or unlinking an identity, ends the sessions that depended on it at their next exchange.

### Residual windows {#residual-windows}

Ending a session stops *renewal*; it cannot recall a token already issued. After a sign-out, a revoke, an unlink or a connection disable, the **access token and the identity token already issued keep working until they expire: 15 minutes by default**, on the admin API as well. Deprovisioning someone at the provider without any DeviceChain action reaches DeviceChain only at their next sign-in or when the session ceiling ends the session, so the bound is the ceiling (12 hours by default) plus up to 15 minutes. There is no SCIM provisioning and no back-channel logout in this release. If that window is too long for a population, shorten the connection's ceiling, and use `revokeSessions` for an urgent departure.

## MCP and AI agents {#mcp}

The OAuth authorization page that MCP clients use offers **Sign in with SSO** next to the password form. The person signs in at their provider and returns to the same authorization request, which DeviceChain kept, and sees the **consent screen: consent is never skipped**, even when the provider signed them in silently. The grant belongs to that sign-in and ends with it (its ceiling, or a revoke). A tenant that requires single sign-on refuses the page's password form, so an SSO-only organization still connects MCP clients. See [Access for AI (MCP)](./mcp.md).

## The public address (`issuerUrl`) {#issuer-url}

Every part of the sign-in flow returns to this instance's public address, which is user-management's `auth.issuerUrl`. The chart **derives** it from the ingress as `<scheme>://<host>/api/user-management` when the instance is served over https or on a loopback host, and an explicit value always wins. With no issuer there is no single sign-on and no OAuth authorization server: creating a connection fails with "sso requires a public issuer URL". The redirect URI you register at the provider is `<issuerUrl>/sso/callback/<connection token>`, shown on the connection's page as its **Callback address**.

### Behind a load balancer {#load-balancer}

The public sign-in start is rate limited **per source address**, and that address is the last entry of `X-Forwarded-For`, or the connection's peer when there is none. If your load balancer or ingress replaces every client's address with its own (a cloud load balancer in its default mode source-NATs traffic), all your users share one allowance of five starts and then one every two seconds, and legitimate users are refused during a rush. Preserve the client address: `externalTrafficPolicy: Local` on the ingress controller's Service, or the load balancer's proxy protocol with the ingress configured to read it. A global limit and a bound on the sign-in state store hold regardless of the source, and when the store fills, starting a sign-in fails closed (see the `SsoStateStoreFull` alert in [Configure single sign-on](../guides/configuring-sso.md#alerts)) while password sign-in carries on.

## What is audited {#audit}

The audit journal records connection create, update and delete, secret changes, domain add and remove, discovery approval, link and unlink, link approvals, each time a superuser bypasses a tenant's single-sign-on requirement, an instance connection's tenant entries, and each refusal with the address the provider asserted. Raw tokens and codes are never logged.

## See also

- [Configure single sign-on](../guides/configuring-sso.md): setup, provider recipes, configuration and alerts.
- [Multi-tenancy](./multi-tenancy.md): tenants, memberships and roles.
- [Access for AI (MCP)](./mcp.md): how MCP clients authorize.
