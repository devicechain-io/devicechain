---
title: Service accounts
sidebar_position: 20
---

# Service accounts

This chapter is for **tenant administrators** connecting an integration — a script, an ETL job, a
backend service — to DeviceChain. A **service account** is a non-human account of your tenant. The
integration presents the account's **client ID** and **client secret** to the OAuth token endpoint
and receives a short-lived access token, which it sends to the APIs like any other. No person's
sign-in is involved, so the integration keeps working when someone leaves, resets a password or
changes roles.

**Service accounts** is under **Access** in the sidebar, next to **Members**. It uses the same
permissions: `member:read` to list accounts and `member:write` to create or change them. A member
limited to part of the tenant (a customer user) cannot see the page.

## What an account can do {#what-an-account-can-do}

An account acts with **exactly the authorities of its roles**. Unlike a member, it does not also
receive the read-only viewer baseline: if the integration needs to read devices, give it a role
that grants `device:read` (the built-in **Viewer** role does).

Like a member, an account can be limited to **specific customers or groups**. Its tokens then see
only those customers' and groups' devices and their data, exactly as a
[customer user](./members-roles-and-customer-users.md#customer-users) does.

The rules that protect delegated administration apply here too:

- You can give an account only roles whose authorities you hold yourself.
- You can change, rotate, disable or delete an account only if you hold every authority it holds.
  Otherwise the console refuses the change and says so.
- An account cannot administer the tenant: it cannot manage members, roles or other service
  accounts, whatever its roles say.

A tenant can hold up to 200 service accounts.

## Creating an account {#creating-an-account}

Choose **New service account** and fill in:

- **Token**, a unique identifier within the tenant (letters, digits, hyphens and underscores). It
  names the account in the audit log and cannot be changed later.
- **Name** and **Description**, optional.
- **Roles**, what the account may do.
- **Can see**: **Whole tenant**, or **Specific customers or groups**.

Choose **Create service account**. The console then shows the **Client ID**, the **Client secret**
and the **Token endpoint**.

{/* TODO(screenshot): service-account-secret, the one-time client credentials panel. */}

The secret is shown **only once**. DeviceChain stores a keyed digest of it, never the secret
itself, so nothing can show it to you again. Copy it into your integration's secret store now. If
it is lost, rotate the secret (below).

Every secret expires. A new secret is valid for 365 days unless the API is told otherwise (1 to
730 days). The **Secrets** column shows how many secrets are working, their last four characters,
and when the next one expires.

## Obtaining a token {#obtaining-a-token}

The integration requests a token with the OAuth `client_credentials` grant at the token endpoint
the console shows (the instance's issuer URL followed by `/oauth/token`). It authenticates with
HTTP Basic:

```bash
curl -s -u "$CLIENT_ID:$CLIENT_SECRET" \
  -d grant_type=client_credentials \
  https://dc.example.com/api/user-management/oauth/token
```

or, for a client that cannot set an `Authorization` header, with the credentials as form fields:

```bash
curl -s \
  -d grant_type=client_credentials \
  --data-urlencode "client_id=$CLIENT_ID" \
  --data-urlencode "client_secret=$CLIENT_SECRET" \
  https://dc.example.com/api/user-management/oauth/token
```

The answer is a JSON object with an `access_token` and its `expires_in`. Send the token as a
Bearer token to any tenant API:

```bash
curl -s -X POST https://dc.example.com/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"query($c:DeviceSearchCriteria!){devices(criteria:$c){results{token}}}",
       "variables":{"c":{"pageNumber":1,"pageSize":10}}}'
```

What to know about the token:

- **It lasts 5 minutes, and there is no refresh token.** Request a new token when it expires; each
  request re-reads the account, so a change you make reaches the integration at its next token.
- **It names the account's tenant** and no other. A request cannot ask for another tenant.
- Two optional form fields narrow it. `scope` caps the token to an OAuth scope's ceiling (for
  example `read-only`). `resource` binds it to one resource server by its address (an absolute
  `https` URI): the MCP server, for instance, accepts only a token bound to its own address.
- The token endpoint is rate limited. Each account can obtain about one token every 30 seconds,
  after a burst of 10, and each source address can make about one attempt every 3 seconds, after a
  burst of 20. A client that asks too often is answered `429` (`slow_down`) with a `Retry-After`
  header in seconds. Reuse a token until it is about to expire rather than asking for one per call.

Every failed attempt — an unknown client ID, a wrong or expired secret, a disabled account —
gets the same answer, `401 invalid_client`, so a caller cannot tell which it was. An account of a
tenant that is disabled or being deleted is answered `400 invalid_grant`.

The tenant's own single sign-on requirement applies to people, not to service accounts.

## Rotating a secret {#rotating-a-secret}

Choose **Rotate secret** in the row's menu. Set **Overlap (hours)**, how long the current secret
keeps working (0 to 168, 24 by default), and confirm. The console shows the new secret once.

During the overlap both secrets work, so you can deploy the new one without downtime. When the
overlap ends, the old secret stops working. An account never has more than two working secrets:
rotating again during an overlap stops the oldest one at once. An overlap of 0 stops the current
secret immediately, which is what to do when a secret has leaked.

## Disabling and deleting {#disabling-and-deleting}

**Disable** stops the account obtaining new tokens, after you confirm; **Enable** lets it again. **Delete** removes
the account and all its secrets. Its token can be reused for a new account, which gets a new client
ID, so an old secret never works for it.

Neither recalls a token already issued: it stays valid until it expires, within 5 minutes. The
same is true of a change to the account's roles or scope.

A role held by a service account cannot be deleted until no account (and no member) holds it.

## The audit log {#the-audit-log}

What an account does is recorded under its own name, `service-account:<token>`, so the
[audit log](./audit.md) never attributes an integration's changes to a person. Each token it
obtains is recorded as a `service_account_token` event. Creating, changing and deleting accounts
is recorded under the administrator who did it. Failed attempts are not recorded.

## The API {#the-api}

The page is a view over the tenant's user-management API. For example, to create an account:

```graphql
mutation {
  createServiceAccount(request: { token: "ci-bot", name: "CI", roleTokens: ["viewer"] }) {
    clientId
    clientSecret
    tokenEndpoint
  }
}
```

`serviceAccounts`, `serviceAccount`, `setServiceAccountAccess`, `setServiceAccountEnabled`,
`rotateServiceAccountSecret` and `deleteServiceAccount` do the rest; the
[GraphQL API reference](../reference/graphql-api.md) describes each one.
