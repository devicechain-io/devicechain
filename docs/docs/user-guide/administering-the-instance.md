---
title: Administering the instance
sidebar_position: 15
---

import GuideShot from '@site/src/components/GuideShot';

# Administering the instance

This chapter is for **instance operators**: the superusers who run a DeviceChain installation for
everyone on it. If you work inside one tenant, you will not see these screens, and you can skip this
chapter.

The admin console is where you create and delete tenants, decide what each tenant is packaged at,
manage the people who sign in and the roles they hold, connect outside identity providers, review
what was changed across the instance, and set instance-wide defaults.

## Opening the admin console

A superuser who signs in lands in the admin console straight away, without choosing a tenant. From
the tenant console, the user menu also offers **Admin console**
([the user menu](/user-guide/signing-in#the-user-menu)). The admin console opens for a superuser
only; anyone else is sent back to the sign-in page.

The sidebar reads **Admin Console** under the logo, and an **Admin** badge sits at the right of the
page header. Its entries, under **Platform**, are:

- **Tenants**, with **List**, **Tiers** and **Deletions**.
- **Identities**, with **List**, **Roles** and **Identity providers**.
- **AI Providers**, **Audit** and **Settings**.

## Tenants

**Tenants → List** is the instance's tenant registry. A tenant is a record the platform keeps, not
something it provisions: creating one takes effect at once, with no new servers or services.

<GuideShot id="admin-tenants" alt="The Tenants list with three rows. harbor-view-pilot, named Harbor View pilot, is on the pilot tier and shows a Disabled badge and a red Deleting since 10/14/2026 badge. sim-acme and sim-widgets are on the silver tier and Enabled. The Config column is empty for all three. A New tenant button is at the top right." />

Each row shows the tenant's token and name, its tier as a coloured pill, its status and any freeform
configuration. A tenant that is being deleted keeps its row, marked **Deleting** with the date the
deletion started, until the deletion finishes.

### Creating a tenant

Choose **New tenant**. The form asks for:

- **Token**: the tenant's id across the platform. It is generated from the name using the
  instance's [token mask](/concepts/system-settings#token-masks), shown under the field, and the
  button beside it generates another. It cannot be changed later.
- **Name**: a display name.
- **Tier**: the packaging the tenant is held to (see [Tiers](#tiers)). Every tenant has one.
- **Config (JSON)**: an optional freeform JSON object.
- The tenant's own limits, described under [Settings](#a-tenants-settings) below. Leave them blank
  to inherit.

Then choose **Create tenant**.

<GuideShot id="admin-tenant-new" alt="The New tenant form. The Token field shows the mask {slug} with a regenerate button and the hint Pattern: {slug}. Below are Name (placeholder Acme Corp), a Tier picker reading Select a tier…, Config (JSON), Ingest rate and Ingest burst, Outbound rate and Outbound burst, all reading default, and the External AI routing checkbox, unticked." />

### A tenant's page

Open a tenant to edit it. Its page shows the name with the token beside it, the tier and status
badges, and two actions: **Disable** (or **Enable**) and **Delete**. A disabled tenant admits nobody
until you enable it again.

Above the tabs, the **Single sign-on** panel says whether the tenant's people must sign in through
single sign-on. **Require SSO** asks you to confirm first, because the tenant's password sessions end
at their next refresh. It is refused unless at least one enabled identity provider connection serves
the tenant. Superusers are never held to it, so an identity provider outage cannot lock you out.
See [Requiring single sign-on](/concepts/single-sign-on#require-sso).

The **Basic** tab edits the name, the tier and the config. The token is shown but cannot change.
Changing the tier applies within a minute.

<GuideShot id="admin-tenant-basic" alt="The page of the tenant acme (sim), token sim-acme, with silver and Enabled badges and Disable and Delete buttons. The Single sign-on panel explains the setting and shows Not required, with a Require SSO button. Below are the tabs Basic, Settings, Effective settings and AI models, with Basic open: Token sim-acme (greyed out), Name acme (sim), Tier Silver and an empty Config (JSON) field." />

The tenant's page also has a **Two-factor authentication** panel with **Require two-factor** (or **Stop requiring**). It requires a second factor of the tenant's password sign-ins, and the button is unavailable when the tenant's tier already requires it. Superusers are exempt, and single sign-on follows its connection's own requirements. See [Two-factor authentication](/guides/two-factor-authentication#require).

### A tenant's settings {#a-tenants-settings}

The **Settings** tab holds the limits this one tenant is held to, when they should differ from its
tier. A field left blank inherits: first the tier's value, then the platform default. The fields are the ingest rate and burst, the outbound rate
and burst for outbound connectors, **External AI routing** (the tenant's consent to send rule
descriptions to an external AI model, off by default), the AI drafting rate and burst, the **Held
command ceiling**, the **Shed priority**, and three geofence limits: **Geofence count**, **Points per
geofence** and **Geofence point budget**. Each field carries a short explanation in the console.
**Save changes** saves the Basic and Settings tabs together.

<GuideShot id="admin-tenant-settings" alt="The Settings tab of acme (sim): Ingest rate (readings/sec), Ingest burst, Outbound rate (calls/sec) and Outbound burst, all reading default; the External AI routing checkbox Allow external AI routing for NL→rule authoring, unticked; AI drafting rate (requests/min) and AI drafting burst; and the start of Held command ceiling." />

The **Effective settings** tab is read-only. It shows the result for the ingest, outbound and AI
drafting rates and bursts: the tier's value, this tenant's override, and the value in force, with a
badge naming where it came from. The held command ceiling, shed priority and geofence limits resolve
the same way, but this tab does not list them. A setting
falls back from its own override, to the tier, to the platform default. The platform default is a
real limit, never "unlimited". When the value comes from a service's own default, the console shows
**Set by the service**. The tier's name in the explanation links to the tier.

<GuideShot id="admin-tenant-effective" alt="The Effective settings tab of acme (sim). The explanation names the Silver tier as a link. A table lists Ingest rate, Ingest burst, Outbound rate, Outbound burst, AI drafting rate and AI drafting burst; the Tier and Override columns show a dash, and each Effective cell reads Set by the service with a Platform default badge." />

The **AI models** tab chooses which AI model the tenant uses for each AI function. It needs the
optional AI service; on an instance without it, the tab says the area is not deployed. For how
limits and entitlement work, see [Tenant tiers](/concepts/tenant-tiers) and
[Governance and quotas](/concepts/governance).

### Deleting a tenant

**Delete** opens a confirmation. Read it: deletion cannot be undone. Sign-in to the tenant is revoked
at once, and its devices, dashboards and history are erased in the background afterwards, which
takes at least 12 hours. The tenant's token stays reserved until that finishes.

<GuideShot id="admin-tenant-delete-confirm" alt="The Delete tenant confirmation over the acme (sim) page. It reads: Delete sim-acme? Sign-in is revoked immediately and this cannot be undone. The tenant's devices, dashboards and history are erased in the background afterwards, which takes at least 12 hours. The token sim-acme stays reserved until that finishes, and becomes available again once it does. Cancel and Delete buttons." />

While a deletion runs, the tenant's page loses its **Disable** and **Delete** buttons and the
single sign-on panel, and gains a **Deletion** tab. The tab starts with a one-line status that says
what the deletion is waiting for. In order:

1. **Waiting on** one or more storage systems that still hold the tenant's data.
2. All storage systems report clean, and the deletion waits a short while longer to confirm nothing
   further arrives.
3. Data reclamation is complete, and the token is held before it becomes available again: about 12
   hours by default.
4. All checks have passed, and the deletion completes on the next sweep.

Below the status, a table has one row per storage system the tenant had data in, with its state:

- **Clean**: nothing of the tenant's is left there.
- **Retaining**: data is still held, and will stay until someone changes something. Where the
  console knows the fix, it says what to do under the row.
- **Retrying**: an attempt failed, and the next sweep tries again on its own.

Each row also shows how many records were erased and when it was last checked. The **Detail**
column carries the storage system's own notes. Some are long internal explanations of what a
system deliberately leaves alone, such as short-lived sign-in records; a note on a **Clean** row is a
footnote, not a problem to act on. The tab refreshes itself while the deletion is in progress, and
if it is empty just after you delete, the first check has not run yet; it starts within a minute.

<GuideShot id="admin-tenant-deletion" alt="The page of Harbor View pilot, token harbor-view-pilot, with pilot, Disabled and Deleting since 10/14/2026 badges and no action buttons. The Deletion tab is open. Its status line reads: Data reclamation is complete. The token harbor-view-pilot becomes available again in about 12 hours. A table lists the storage systems, starting with blob, broker, detect and kv, each Clean with 0 erased and a last-checked time. The kv row carries a long technical note in its Detail column." />

### The deletions list

**Tenants → Deletions** lists every tenant deletion on the instance, newest first. Use the **All**,
**In progress** and **Completed** buttons to filter it. Each row shows the token, when the deletion
was requested, its status with the same one-line summary as the tenant's Deletion tab, the number of
records erased, and how many storage systems are clean or still holding data.

<GuideShot id="admin-deletions" alt="The Tenant deletions page with All, In progress and Completed filter buttons. One row: harbor-view-pilot, requested 10/14/2026 4:19:03 PM, status In progress with the note that data reclamation is complete and the token becomes available again in about 12 hours, 0 erased, and 6 systems, all clean." />

This list is the lasting record. When a deletion completes, the tenant's row and page disappear, so
a finished deletion appears only here. Open it when someone asks you to show that a customer's data
was erased. For the full timeline, see [Tenant deletion](/deployment/tenant-deletion).

## Tiers {#tiers}

A tier is the packaging a tenant is sold: the default limits every tenant at that tier is held to,
and which AI models those tenants may use. **Tenants → Tiers** lists them, each with its coloured
pill, name, description and how many tenants are on it.

<GuideShot id="admin-tiers" alt="The Tiers list with four rows, each with a drag handle: bronze (Bronze, Entry packaging: the first to shed under contention, a reduced set of AI models, and more, 0 tenants), gold (Gold, Premium packaging: the last to shed under contention, the broadest set of AI models, and more, 0 tenants), pilot (Pilot, Time-boxed evaluations: a small ingest ceiling and the first to be shed under load, 0 tenants) and silver (Silver, Standard packaging: the platform's default ceilings and entitlements, 2 tenants). Each tier pill has its own colour. A New tier button is at the top right." />

Drag a row by its handle to change the order tiers are listed in. The order is presentation only: it
is not a ranking, and nothing is computed from it. **New tier** creates one. Defining a tier
changes nothing on its own; it takes effect once tenants are created at it or moved to it.

A tier's page has three tabs. **Basic** holds the token, which cannot change, the name, an
operator-facing description, and the **Color** of its pill. The colour is presentation only.

<GuideShot id="admin-tier-basic" alt="The page of the Pilot tier, token pilot, with a pilot pill, 0 tenants and a Delete button. The Basic tab shows Token pilot (greyed out), Name Pilot, the Description Time-boxed evaluations: a small ingest ceiling and the first to be shed under load, a row of colour swatches with a teal one selected beside a preview pill, and a Save changes button." />

**Settings** holds the tier's **Ceilings**: the ingest, outbound and AI drafting rates and bursts.
Leave a field blank for the platform default. A tenant can still be given its own override on its
Settings tab. When tenants are on the tier, saving re-prices them within a minute, without a restart.

A tier can also carry a held command ceiling, a shed priority and the three geofence limits, and
tenants at the tier inherit them the same way. The console has no fields for these on a tier: they
are set through the admin API, and saving this tab leaves them as they are. A tenant's own value on
its Settings tab still takes precedence over the tier's.

<GuideShot id="admin-tier-settings" alt="The Settings tab of the Pilot tier, under the heading Ceilings: Ingest rate 50, Ingest burst 100, Outbound rate 5, Outbound burst 10, and AI drafting rate and AI drafting burst both reading platform default. A Save changes button." />

**AI models** chooses which AI models the tier grants, and needs the optional AI service. A tier
cannot be deleted while any tenant is on it. See [Tenant tiers](/concepts/tenant-tiers).

A tier's **Settings** tab also has **Require two-factor authentication**. Ticked, every tenant at the tier must use a second factor for password sign-ins, and a tenant cannot turn it off. See [Two-factor authentication](/guides/two-factor-authentication#tier-floor).

## Identities

**Identities → List** is the instance's directory of people. A person is one identity, keyed by
email address, that can be a member of many tenants. The list shows each identity's email, name,
status, any system roles, and how many tenants it belongs to.

<GuideShot id="admin-identities" alt="The Identities list: dana.okafor, jonas.weber and maria.chen at acme-properties.example, all Enabled; lee.park at acme-properties.example, Disabled; superuser@devicechain.local with no name, Enabled, holding the superuser system role and 0 tenants; and sam.rivera@widgetlab.example, Enabled. Each of the people belongs to 1 tenant. A New identity button is at the top right." />

**New identity** asks for an email, a password, a first and last name, and any **System roles**.
System roles gate the admin console and its API; leave them empty for an ordinary person. Tenant
memberships are added after the identity exists.

<GuideShot id="admin-identity-new" alt="The New identity form, described as an email-keyed global principal, with memberships added after creating it. Fields: Email, Password, First name, Last name and a System roles picker, with Create identity (disabled until filled in) and Cancel buttons." />

An identity's page has **Disable** and **Delete** at the top. A disabled identity cannot sign in, and
its sessions end at their next refresh. Deleting removes the identity and all its memberships.

- **Access** sets the identity's **System roles** (then **Save**) and replaces its password with
  **Set password**.
- **Memberships** lists the tenants it belongs to, each with its status and its tenant roles. Change
  the roles and choose **Save roles**, or **Disable** or **Remove** the membership. A disabled
  membership keeps the person out of that one tenant. To add one, pick a tenant and roles under
  **Add to tenant** and choose **Add membership**.

<GuideShot id="admin-identity-detail" alt="The page of Maria Chen, maria.chen@acme-properties.example, Enabled, with Disable and Delete buttons. The Access panel has a System roles picker with Save, and a Set password field with Set. The Memberships panel shows sim-acme, Enabled, with the role Facilities viewer and Save roles, Disable and Remove buttons. Below, Add to tenant and Roles pickers and an Add membership button." />

The list has a **Two-factor** column. On an identity's page, the **Two-factor authentication** section shows whether it is on and has **Reset two-factor** for a person who lost their phone: it removes the authenticator and recovery codes and ends every session. If the identity has had wrong codes recently, its password may be known to someone else, so the reset also asks for a new password. See [Two-factor authentication](/guides/two-factor-authentication#reset) and, for an operator who cannot sign in, [break-glass](/guides/two-factor-authentication#break-glass). The admin API's `clearPassword` also removes the person's second factor, because with no password there is nothing left for it to protect.

## Roles

**Identities → Roles** is the role catalogue for the whole instance. A **system** role gates the
admin console and its API; a **tenant** role gates what a member can do inside a tenant. Each role
lists its authorities, the permissions it grants. `*` grants everything within the role's scope.

<GuideShot id="admin-roles" alt="The Roles list. superuser is a system role with authority *. facilities-viewer, Facilities viewer, is a tenant role with device:read, state:read, event:read, alarm:read, alarm:write, dashboard:read and location:read. tenant-admin, Tenant Administrator, is a tenant role with *. viewer, Viewer, is a tenant role with device:read, event:read, state:read, command:read, alarm:read and dashboard:read. A New role button is at the top right." />

Open a role to change its name, description and **Authorities**, then choose **Save changes**. The
scope and token are fixed once the role exists. **New role** creates one; **Delete** removes a role
from everyone who holds it.

A tenant's own administrators assign these tenant roles to their members, and can also define roles
of their own, which exist only in that tenant and carry only tenant authorities. They manage their
members, invitations and roles themselves, without the admin console; see
[Members, roles and customer users](/user-guide/members-roles-and-customer-users).

<GuideShot id="admin-role-detail" alt="The facilities-viewer tenant role. Scope shows tenant selected and system unavailable. Token facilities-viewer (greyed out), Name Facilities viewer, Description Reads devices, live state, telemetry and alarms, and acknowledges alarms. Changes nothing else. Authorities lists device:read, state:read, event:read, alarm:read, alarm:write, dashboard:read and location:read. A Save changes button and a Delete button." />

## Identity providers

**Identities → Identity providers** connects outside OpenID Connect providers, such as Google
Workspace or Microsoft Entra ID, that people sign in through. The list shows each connection's
name, kind, issuer, the email domains it speaks for, whether its endpoints are approved, and its
status. A connection starts disabled, and cannot be enabled until someone has approved the
provider's endpoints.

<GuideShot id="admin-identity-providers" alt="The Identity providers page with one connection: Acme Google Workspace, kind Tenant, issuer https://accounts.google.com, domain acme-properties.example, endpoints Approved, status Disabled. Below, the Link requests panel explains refused sign-ins and reads No sign-ins are waiting for a link decision. A New connection button is at the top right." />

Below the list, **Link requests** collects sign-ins that were refused because they matched an
existing identity that has no link to that provider. A request grants nothing until you **Approve**
it; **Reject** deletes it.

**New connection** opens a form. The **Kind** decides who the connection is for:

- **Tenant**: a provider for one or more tenants' own people. Its sign-ins can enter only the
  tenants it serves, and never reach an identity that holds a system role.
- **Instance**: your operators' own provider. It is linked to identities only by explicit action,
  and must either require an authentication method, such as `mfa`, or be confirmed as a provider
  that enforces multi-factor sign-in. It serves no tenants, and enters a tenant only as a
  superuser.

The form also asks for a token, a name, the provider's **Issuer**, the **Client ID** and **Client
secret** it gave you, and optionally the scopes, the email claim and the authentication methods and
claims every sign-in must carry. The token and issuer cannot be changed later. The client secret is
write-only: it is never shown again. If the provider runs inside your network and its TLS
certificate was signed by your own certificate authority, paste that CA certificate (PEM) under
**Private CA certificates**, or choose the file.

<GuideShot id="admin-identity-provider-new" alt="The New identity provider drawer. Kind has Tenant selected, with Instance as the alternative, and explains that a tenant provider's sign-ins can enter only its tenants. Fields: Token, Name, Issuer (placeholder https://login.example.com), Client ID, Client secret (write-only), Scopes (placeholder openid email profile) and Email claim (placeholder email), each with a short explanation." />

A connection's page has four tabs, with **Enable** (or **Disable**) and **Delete** at the top.

**Endpoints** shows the discovery snapshot: the provider's authorization, token, signing-key and
sign-out endpoints as last fetched, with a fingerprint and the approval. Sign-ins use only the
endpoints approved here. Until they are approved, the tab offers **Approve endpoints**, and **Enable**
stays unavailable. **Refresh from provider** fetches them again; if they changed, the approval is
withdrawn, sign-ins through the connection are refused, and you review and approve the new endpoints
to resume.

<GuideShot id="admin-identity-provider-discovery" alt="The Acme Google Workspace connection, token acme-google, Disabled, kind Tenant, with Enable and Delete buttons. The Endpoints tab shows the Discovery snapshot: Google's authorization, token and signing-key endpoints, Sign-out endpoint None, the Fetched time, a fingerprint, and Approval: Approved by superuser@devicechain.local with a timestamp. A Refresh from provider button." />

**Settings** edits the connection. Its **General** section shows the fixed issuer and the
**Callback address**, which you register at the provider as the redirect URI, then the name, client
ID, scopes, email claim and required authentication methods. Further sections handle the client
secret, the required claims, the private CA, and whether signing out also signs people out at the
provider.

The **Private CA** section is for a provider inside your network whose TLS certificate was signed
by your own certificate authority. The certificates you add are trusted for this provider only, in
addition to the system's trusted roots, and only certificates are accepted, never a private key.
**Verify and save bundle** first reaches the provider with the new bundle, and saves nothing if its
certificate does not chain to it. If the provider then names different endpoints, the approval is
withdrawn and you review them again. **Remove bundle** goes back to the system's roots alone.

<GuideShot id="admin-identity-provider-settings" alt="The Settings tab of Acme Google Workspace. Under General: Issuer https://accounts.google.com, fixed when the connection was created; the Callback address http://localhost/api/user-management/sso/callback/acme-google with a copy button; Name Acme Google Workspace; a Client ID ending in apps.googleusercontent.com; Scopes openid email profile; Email claim email; and the start of Required authentication methods." />

**Tenants** names exactly which tenants sign-ins through this connection can enter, and no others.
Choose **Save tenants** after changing the list.

<GuideShot id="admin-identity-provider-tenants" alt="The Tenants tab of Acme Google Workspace. Under Served tenants, the Tenants served picker holds acme (sim). The Save tenants button is greyed out because nothing has changed." />

**Domains** lists the email domains this connection speaks for, who asserted each one and when.
Add one under **Add a domain**, or **Remove** it. Freemail providers and public suffixes are
refused, and a domain belongs to one connection only.

<GuideShot id="admin-identity-provider-domains" alt="The Domains tab of Acme Google Workspace. Under Verified domains, one row: acme-properties.example, asserted by superuser@devicechain.local with a timestamp, and a Remove button. Below, an Add a domain field (placeholder example.com) and an Add domain button." />

The connection in these screenshots is approved but still disabled, so nobody can sign in through it
yet. For the whole setup, step by step, see [Configure single sign-on](/guides/configuring-sso), and
for how it behaves, [Single sign-on](/concepts/single-sign-on).

## AI providers

**AI Providers** registers the AI models the instance can use for drafting detection rules in plain
language. Each provider has a kind, a model, an API key and, where needed, an endpoint. The key is write-only: it is
never shown again. A provider's page has **Basic**, **Connection** and **Test** tabs, and **Test
provider** runs a prompt through it to check the connection. Registering a provider offers it to
nobody: you grant it to a tier, under **Packaging**, to put it on those tenants' menu.

The AI providers screens need the optional AI service. The instance in these screenshots does not run
it, so the page reports the area as not deployed:

<GuideShot id="console-area-unavailable" alt="The AI Providers page of the admin console showing an empty state titled 'Not deployed'. The text explains that the instance was deployed without the ai-inference service, so the feature is unavailable, that nothing is wrong with the session, and that an administrator chooses which services an instance runs." />

See [AI providers](/concepts/ai-authoring#ai-providers-are-operator-configuration).

## Instance audit

**Audit** is the instance-wide journal. It records sign-ins, failed sign-ins and session refreshes
(the **Auth** category), and changes to identities, roles, tenants and identity providers (the
**Mutation** category), across every tenant. Each row shows the time, category, actor, operation,
tenant and target.

Filter with **All tenants** (a tenant whose deletion has completed is listed too, with the date, so
its journal stays reachable), **All categories** and **Filter by actor…**. **Export CSV** downloads everything that matches, the same way
as the tenant [Audit](/user-guide/audit#exporting) export.

<GuideShot id="admin-audit" alt="The Audit page filtered to the Mutation category and the actor superuser@devicechain.local. Every row is a Mutation by superuser@devicechain.local: an Update to iam_identities lee.park@acme-properties.example, Creates on iam_memberships and iam_membership_tenant_roles, a Create and an Update on iam_tenants harbor-view-pilot, and Creates and an Update on the acme-google identity provider connection and its tenants and domains. The Tenant column shows a dash. An Export CSV button is at the top right." />

The screenshot is filtered to the operator's own changes. Unfiltered, the journal is mostly
sign-ins.

## Settings

**Settings** holds the instance-wide settings, each on its own tab. Defaults live in the platform's code, and
only your overrides are stored, so each tab shows a **Default** or **Overridden** badge. **Save
override** stores your value; **Reset to default** removes it.

**Branding** sets the default app title, colours, logo height and logo every tenant gets unless it
sets its own, with a live preview. The logo here is an https address, saved with the rest by **Save
override**. See [White-labeling](/concepts/white-labeling).

<GuideShot id="admin-settings" alt="The System settings page on the Branding tab, with tabs Token masks, Branding, Map and Language. The setting branding.default carries a Default badge. Fields: App title DeviceChain; Primary, Accent, Sidebar background and Sidebar foreground colours, each reading inherit; Logo max height 28; and a Logo field holding an https:// placeholder, whose help text describes an upload or an https address. A Preview card shows DeviceChain, a Primary button and sample text. Save override and Reset to default buttons." />

**Token masks** sets how the console generates tokens for new records. A mask is literal text plus
placeholders: `{slug}` (from the record's name), `{uuid}`, `{alphanumeric-N}` and `{numeric-N}`.
The `default` entry applies to any type without a mask of its own, and each row previews a sample
token. **Add a mask** adds a row for one entity type. See
[Token masks](/concepts/system-settings#token-masks).

<GuideShot id="admin-settings-token-masks" alt="The Token masks tab. The setting entity.token_masks carries a Default badge. An explanation of the placeholders, then one row: entity type default, mask {slug}, sample sample-name, with a delete icon. Add a mask, Save override and Reset to default buttons." />

**Map** sets the basemap every tenant starts with ([Basemaps](/concepts/basemaps)), and
**Language** sets the language the console opens in when a tenant has set none
([Default language](/concepts/system-settings#locale-default)). Tenant administrators can override
the branding, map and language for their own tenant; see [Tenant settings](/user-guide/tenant-settings).

## Related

- [Multi-tenancy](/concepts/multi-tenancy): how tenants are kept apart on one shared installation.
- [Tenant tiers](/concepts/tenant-tiers) and [Governance and quotas](/concepts/governance): what a
  tier packages and how limits resolve.
- [Tenant deletion](/deployment/tenant-deletion): what a deletion erases, keeps, and how long it
  takes.
- [Single sign-on](/concepts/single-sign-on) and [Configure single sign-on](/guides/configuring-sso).
- [System settings](/concepts/system-settings): the instance-wide defaults.
