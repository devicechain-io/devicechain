---
title: Two-factor authentication
---

# Two-factor authentication

Two-factor authentication adds a second step to signing in with a password: a six-digit code from an authenticator app on the person's phone. It is built into DeviceChain, so it needs no identity provider and no extra service.

This guide has three parts: [for people](#enrol) who want to turn it on for their own account, [for tenant administrators](#require) who want to require it, and [for operators](#operators) who reset it, recover from a lost device, and watch for attacks on it. For where it sits among the platform's other protections, read [the security model](../concepts/security-model.md#two-factor) first or afterwards.

## What it covers {#scope}

- **One factor per account, in every tenant.** An account (an identity) is global: it belongs to the instance, and its tenant memberships are separate. Two-factor authentication therefore belongs to the account, and applies in every tenant the person can enter.
- **Password sign-ins only.** A sign-in through [single sign-on](../concepts/single-sign-on.md) does not use it, because the identity provider has already done the work of proving who the person is. Service accounts and devices never use it either. See [single sign-on and two-factor](#sso).
- **An authenticator app plus recovery codes.** The app produces a six-digit code that changes every 30 seconds (the time-based one-time password standard, RFC 6238, with SHA-1 and a window of one step either side of now). Ten single-use recovery codes cover a lost phone.
- **The nodes' clocks must be right.** A code is only valid for the 30-second step it was made in, plus one step either side, so a service node whose clock has drifted by more than about 30 seconds refuses correct codes. Run NTP (or the cloud's time service) on every node.

## Turn it on for your account {#enrol}

Open the user menu and choose **Account security**.

1. Choose **Set up two-factor authentication** and enter your password. The console asks again because a sign-in token can travel, and enrolling needs more than a token.
2. Scan the QR code with an authenticator app (any app that supports time-based codes will do), or type in the key shown under **Can't scan? Enter this key**. The QR code is drawn in your browser from a secret that is shown only now; DeviceChain keeps the secret sealed and never returns it. The app labels the account with the instance's display name, or with the instance's host name when no display name is set.
3. Enter the six-digit code the app shows and choose **Turn on**. Until you do, nothing about how you sign in has changed. An unconfirmed setup expires after ten minutes, and five wrong codes cancel it.
4. The console shows ten **recovery codes**, once. Copy or download them and keep them somewhere other than the phone. Each one signs you in once, in place of an app code. They look like `ABCD-EFGH-JKMN-PQRS`.

Turning it on **ends every other session your account has**, anywhere, so a session started by someone who had only your password does not outlast it. The console swaps your own session for a fresh one, so you carry on without signing in again.

The same page shows whether it is on, when it was set up, and how many recovery codes are left. It also lets you:

- **Generate new recovery codes.** This replaces all ten, used or not: the old ones stop working. It needs your password and a code (an app code or an unused recovery code). When only a couple are left the page says so.
- **Turn off two-factor.** This needs your password and a code, removes the factor and the codes, and **ends every session your account has, including this one**. You sign in again with the password alone.

## Signing in {#sign-in}

Sign in as usual. When the password is right and the account has a second factor, the console asks for a code instead of letting you in:

- Enter the six-digit code from the app. A code works **once**. If you have just used one, wait for the app to show the next.
- Or choose **Use a recovery code instead** and enter one. Type it any way you like: capitals or not, with or without the hyphens or spaces. The look-alikes are forgiven: a letter O is read as zero, and a letter I or L as one. Each recovery code works once.

The code step times out after five minutes and accepts five attempts. After that, sign in again. The same step appears wherever a password sign-in does: the console, the dashboard app (`/dash`), accepting an invitation, and the page an AI agent's [MCP](../concepts/mcp.md) connection sends you to. The dashboard app and the MCP page can ask for the code, but cannot set it up: they tell you to set it up under **Account security** in the console.

A sign-in that used a recovery code is recorded as such. **Account security** warns you when you are running low on codes.

## If you lose your phone {#lost-device}

- **You still have a recovery code.** Sign in with it, then open **Account security**, turn two-factor off, and set it up again on the new phone. Or just generate a new set of recovery codes if you only want to be safe.
- **You have no recovery code.** Ask an operator to reset your second factor ([below](#reset)). A tenant administrator cannot do this for you, because the account is shared by every tenant. After the reset you sign in with your password alone and can set the factor up again.
- **You are the only operator and have no recovery code.** See [break-glass](#break-glass).

## Requiring it for a tenant {#require}

A tenant administrator can require a second factor of everyone who signs in to the tenant with a password. Open **Settings → Security** and choose **Require**. This needs the permission to manage members, for the whole tenant: a member limited to some customers cannot change it. An operator can set the same flag from **Admin → Tenants**, on the tenant's page.

What requiring it does:

- A member whose password sign-in did not use a second factor is refused entry to the tenant. If they have a factor, the console asks them to sign in again and enter the code. If they have none, the console sends them to **Account security** to set one up first.
- **Nobody is stranded.** A member can always sign in and reach **Account security** without a second factor, and enrolling needs nothing but their sign-in and their password. So turning the requirement on does not lock anyone out, and there is no grace period to configure.
- **Sessions already inside the tenant end at their next refresh.** Anyone using the console at that moment is asked to sign in again, and sees why.
- **Service accounts are not affected**, because they never sign in with a password.
- **Superusers are not held to it.** A superuser with a password-only session can enter the tenant, and each entry is recorded as `mfa_enforcement_bypassed`. Operators can still turn their own second factor on, and should.
- **It does not bind the admin console or the admin API.** The tenant's requirement decides who may enter that tenant. Reaching the operators' admin plane is a different question, and nothing a tenant sets affects it.

### The tier floor {#tier-floor}

An operator can make two-factor authentication a floor for every tenant at a tier: open the tier (**Tenants → Tiers**), and on its **Settings** tab tick **Require two-factor authentication**. A tenant at that tier is then required to use it, whatever its own setting, and its administrator cannot turn it off: the tenant's **Security** page says the tier requires it. Leave the key off to let each tenant decide.

### Single sign-on and two-factor {#sso}

The tenant requirement binds **password** sign-ins only. A person who signs in through single sign-on is governed by their connection's own requirements: for a stronger guarantee, require an authentication method on the connection (`requiredAmr`, for example `["mfa"]`) so the provider must have performed its own second factor. See [Configure single sign-on](./configuring-sso.md). A tenant that turns on **Require SSO** has no password sign-ins left for the requirement to apply to.

## Command line and API clients {#clients}

### dcctl {#dcctl}

`dcctl dead-letters`, `dcctl presence` and `dcctl sim` sign in as an account on your behalf, so they take `--otp <code>` for an account that has a second factor. A recovery code is accepted too. The code is used for the first sign-in only, and **a code works once**:

- If the command has to sign in again (its token expired during a long run), it asks for a code on the terminal, without echoing it.
- With no terminal, it stops and says to run it again with a fresh `--otp`. It never offers the same code twice, because the server would refuse it as a replay and a retry loop on top of that is how an account locks.

For automation, either keep one superuser **without** a second factor and use it only for scripts, or accept the prompts. The first is the pragmatic choice for a pipeline, and its price is that the account is protected by its password alone, so give it a long generated password. `dcctl sim` reads the seed superuser's password from the instance's Kubernetes Secret, so an enrolled seed superuser needs `--otp` there too.

### GraphQL {#graphql-clients}

`login` no longer returns a token for an account with a second factor. It fails with the error code `MFA_CHALLENGE`, which carries a challenge, and the client finishes with `verifyMfa`. Every client that signs in with `login` must handle it. The exact shape is in [the GraphQL API reference](../reference/graphql-api.md#two-factor-sign-in).

### C# SDK {#csharp}

`LoginAsync` throws `MfaRequiredException` for such an account. Its `Challenge` goes to `VerifyMfaAsync` with the code:

```csharp
try
{
    await client.LoginAsync(email, password);
}
catch (MfaRequiredException mfa)
{
    // Ask the person for the code from their authenticator app, or a recovery code.
    await client.VerifyMfaAsync(mfa.Challenge, code);
}
```

`MfaRequiredException` is a `GraphQlRequestException`, so code that already catches that type still catches it. Do not call `LoginAsync` again to retry: a second call makes a second challenge. A wrong code is a `GraphQlRequestException` and the challenge stays usable for a few more tries; a challenge that expired is one with the code `MFA_CHALLENGE_EXPIRED`, and the person signs in again.

## For operators {#operators}

### Reset a lost second factor {#reset}

Open **Admin → Identities**, open the person, and in the **Two-factor authentication** section choose **Reset two-factor**. The authenticator, the recovery codes and the stored secret are removed, and every session the person has ends. Their next sign-in asks for the password only, and they can set up two-factor authentication again. The list shows a **Two-factor** column so you can see who has it.

- It needs the permission to manage users. Resetting an identity that holds a **system role** needs a superuser.
- **If the account has had wrong codes since its last successful one, the reset asks for a new password and sets it in the same step.** Wrong codes mean someone who holds the password has been guessing the code, so removing the second factor alone would hand the account to them. The console shows the extra field when the server requires it; the API answers `MFA_RESET_NEEDS_PASSWORD` and changes nothing until you send `newPassword`.
- The reset is recorded as `mfa_reset`, naming who did it.
- The API call is `resetIdentityMfa(email, newPassword)` on the admin API.

Clearing an identity's password (`clearPassword` on the admin API, which converts it to single sign-on only) also removes its second factor, the recovery codes and the stored secret, in the same step. The factor protects password sign-ins only, so with no password it would guard nothing and would be left orphaned.

### Break-glass: the operator who cannot sign in {#break-glass}

If the only superuser has a second factor and has lost both the phone and the recovery codes, nobody can reset them through the admin API. `dcctl instances break-glass mfa-reset` does it from inside the cluster:

```bash
dcctl instances break-glass mfa-reset --instance <instance> --email superuser@devicechain.local
```

- **It needs cluster credentials and nothing else.** It runs the service's own binary inside a ready `user-management` pod (so it needs permission to `exec` into pods in the instance's namespace) and signs in to nothing.
- **It always replaces the password.** There is no caller to vouch for, so removing the factor alone would hand the account to whoever holds the old password. dcctl generates a password and prints it **once**. For scripts, or to choose it yourself, add `--password-stdin` and the first line of standard input is used (nothing is printed; it also needs `--yes`).
- **It asks you to type the instance name** to confirm, or takes `--yes`.
- **It ends every session** the account has and removes the factor, the codes and the secret.
- **It refuses an identity with no system role.** Anyone else is reset through the admin API, where the log names the operator who did it. The break-glass reset is recorded as `mfa_reset` by `break-glass:pod-exec`.
- **For the instance's seed superuser** (`superuser@devicechain.local`), dcctl also updates the `password` key of the instance's `dci-<instance>-superuser` Secret, which `dcctl sim`, the probes and a bootstrap re-run read. If it cannot, it says so loudly, because the reset has already happened and the Secret now holds the old password: pass the new one to `dcctl sim` with `--admin-password` until you fix the Secret.

### Lockouts, and why the remedy is the password {#lockouts}

Every guess at a code is made by someone who already has the password, so a pattern of wrong codes is a sign that the password is known to someone else.

- **Each wrong code is counted before it is checked.** After **5** wrong codes from one source address, that address is locked out of the account's authenticator; after **20** from all addresses together, the authenticator is locked for everyone. A lock lasts one minute and doubles with each further failure, up to **15 minutes**.
- **A locked attempt is answered exactly like a wrong code** ("invalid code"), so the answer tells a guesser nothing, and no code is evaluated while the lock holds.
- **Recovery codes still work during a lock.** They carry 80 random bits and are not guessable, so they are exempt from the authenticator's lock. This is what keeps a person with their recovery codes from being shut out.
- **A password holder can renew a lock indefinitely**, one wrong code each time it expires. There is no permanent lock, by design: a permanent one would let anyone who knew an address lock its owner out forever. This is the same trade as [the sign-in backoff](../reference/graphql-api.md#sign-in-backoff).
- **The remedy is to reset the password** (`setPassword` on the admin API, or **Set password** on the identity's page), **not the second factor.** Resetting the password rotates the account's session epoch, which also voids every outstanding sign-in challenge. Resetting only the factor while the password is known would give the account to whoever is guessing. The reset refuses to run without a new password for this reason.
- Source addresses are in the ingress logs. A sender who controls the forwarded-address header can get a fresh per-address allowance, so the account-wide limit of 20 is the real bound.

The residual risk is stated in [the security model](../concepts/security-model.md#two-factor): for someone who already holds the password and keeps guessing at the cap, about 10% a year.

### Alerts and metrics {#alerts}

Two alerts ship with the chart and fire only when metrics alerting is on:

| Alert | Severity | Meaning | What to do |
| --- | --- | --- | --- |
| `MfaIdentityLocked` | critical | An account's identity-wide lock armed: twenty wrong authenticator codes were checked, each after a **correct password**. Someone has this account's password and is guessing its code. It fires on any single occurrence. | Find the account in the audit journal (`mfa_locked`, preceded by `mfa_failed` and `mfa_challenged` rows) and **reset its password**. Do not reset the second factor first. Find the source addresses in the ingress logs. |
| `MfaVerificationFailuresSustained` | warning | For fifteen minutes, wrong, replayed or locked-out codes have been refused at more than one every ten seconds across the instance. Either people are mistyping in large numbers (a clock problem on the nodes, or apps out of sync) or someone is using passwords they obtained. | Check that the nodes' clocks are synchronised, then look for `mfa_failed` rows grouped by account and at the ingress logs. An account that locks raises `MfaIdentityLocked` on its own. |

The counters behind them are `devicechain_usermanagement_mfa_verifications_total{method,result}` (`method` is `totp` or `recovery`; `result` is `ok`, `invalid`, `replay`, `locked`, `expired`, `budget`, `rate_limited` or `unavailable`), `mfa_challenges_total`, `mfa_enrollments_total{result}` (`begun`, `confirmed`, `disabled`, `reset`) and `mfa_identity_locks_total`. See [the metrics reference](../deployment/metrics-reference.md#user-management).

### What the audit journal records {#audit}

The sign-in service records `mfa_challenged` when a password was right and a code is needed, `mfa_failed` for a code that was checked and refused, `mfa_locked` for the attempt that armed the account-wide lock, `mfa_enrolled`, `mfa_disabled`, `mfa_reset` (with who did it), `mfa_recovery_code_used`, `mfa_recovery_codes_regenerated`, `mfa_enforcement_bypassed` and `tenant_mfa_required_changed`. An attempt refused by a lock is counted and not written, so someone holding a password cannot drive writes to the journal. No row ever holds a code, a secret or a challenge.

**The `login` row changed meaning.** It is now written when a sign-in **completes**, not when the password is accepted, and it says how: `pwd`, `pwd+otp` or `pwd+recovery`. For an account with a second factor, the password step is the `mfa_challenged` row, and a `login` row appears only after the code is accepted.

## Troubleshooting {#troubleshooting}

| Symptom | Likely cause |
| --- | --- |
| Correct codes are refused for everyone | The clocks on the service nodes have drifted more than about 30 seconds. Fix time synchronisation. |
| One person's code is refused, others work | The phone's clock is off (turn on automatic time), or the code was already used: a code works once, so wait for the next. |
| "invalid code" even with a fresh code | The account may be locked after repeated wrong codes. A locked attempt looks exactly like a wrong one. Use a recovery code, wait up to 15 minutes, and ask an operator if the lock keeps returning. |
| "This sign-in timed out" | The challenge lasts five minutes and five attempts. Sign in again. A password change also voids it. |
| A member is sent to **Account security** | Their tenant requires two-factor authentication and they have not set it up. |
| Every session ended after setting it up | Intended: turning it on ends the account's other sessions. |
| **Account security** says "Managing two-factor authentication needs a recent password sign-in. Sign in again" | The page needs a recent password sign-in, because the identity token it uses lasts 15 minutes. Sign out, sign in again with your password and open **Account security** again. |
