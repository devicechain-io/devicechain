---
title: Members, roles and customer users
sidebar_position: 19
---

# Members, roles and customer users

This chapter is for **tenant administrators**. It covers the people who belong to your tenant,
the roles they hold, and the logins you give your own customers so they can watch their
equipment without seeing anyone else's.

Two pages under **Settings** in the sidebar do this work: **Members** and **Roles**. Each needs
its own permission. **Members** lists people with `member:read` and changes them with
`member:write`; **Roles** lists roles with `role:read` and changes them with `role:write`. The
built-in tenant administrator role holds all four. Without a permission, its page is not in your
sidebar.

## Members {#members}

**Members** lists everyone who belongs to the tenant, ordered by email address. For each person
it shows:

- their name, when they have set one, and their email address,
- the **Roles** they hold,
- what they **Can see**: **Whole tenant**, or the customers they are limited to,
- their status: **Enabled**, **Disabled**, or **Invited** while an invitation waits to be
  accepted. A membership that comes from your tenant's identity provider is also marked
  **Identity provider**.

{/* TODO(screenshot): members-list, the Members page with staff and customer users. */}

The menu at the end of a row holds the actions for that person. Your own row has none: nobody
changes their own membership here, so another administrator has to. A membership marked
**Identity provider** has none either, because it is managed by the identity provider and changes
there.

### Inviting a member {#inviting-a-member}

Choose **Invite member** and fill in:

- **Email**, the address the person signs in with.
- **Roles**, what they may do. You can give only roles whose authorities you hold yourself.
- **Can see**: **Whole tenant**, or **Specific customers** and then up to 16 customers. A member
  limited to customers is a *customer user*, described [below](#customer-users).

Choose **Invite member** again to create the invitation. The console then shows an **Invitation
link**. Copy it and send it to the person yourself: DeviceChain does not email it.

{/* TODO(screenshot): member-invite, the invitation drawer showing the one-time link. */}

The link is shown **only once**. The server keeps no copy it could show you again, so if the link
is lost, reissue the invitation (below), which stops the old link working. A link is valid for 7
days. Until the person accepts, their membership stays disabled and is marked **Invited**.

The answer is the same whether or not the person already has a DeviceChain account, so inviting
someone does not tell you whether they have one.

### Accepting an invitation {#accepting-an-invitation}

The invitation link opens a **Join a tenant** page. The person either:

- chooses **New account** and sets a password, or
- chooses **I have an account** and signs in with the account the invitation was sent to, with
  its password or with single sign-on.

If your tenant requires single sign-on, the password choice is refused, and the page asks the
person to sign in with SSO instead. Once accepted, the person is taken into the tenant. If they
belong to more than one tenant, they choose which to open.

A link that cannot be used (it expired, was revoked, was already used, or was sent to a different
account) gets one answer, "This invitation cannot be used", whichever reason it is. Ask for a new
one.

### Changing a member's access {#changing-access}

From a row's menu:

- **Edit access** changes the member's **Roles** and what they **Can see**.
- **Disable** keeps the member out of this tenant without removing them, and **Enable** lets them
  back in.
- **Remove** takes the member out of the tenant. Their account is kept, and they can be invited
  again.
- For a member still marked **Invited**, **Reissue invitation** shows a new link and stops the old
  one, and **Revoke invitation** stops the link without issuing another.

A change takes effect the next time the member's session refreshes: within 15 minutes, or 5
minutes for a customer user. That applies to taking access away too. The member does not have to
sign out, and their sessions in other tenants are not affected.

Some changes are refused, with a message that says why:

- You cannot give an authority you do not hold yourself, or change a member who holds one you do
  not.
- You cannot change your own membership.
- You cannot leave the tenant without an enabled member who sees the whole tenant and can manage
  members.
- A membership managed by the identity provider cannot be changed here.

### Invitations {#invitations}

Under the members list, **Invitations** lists every invitation issued in the tenant, newest first,
with its state (**Waiting**, **Accepted**, **Expired** or **Revoked**), when it was issued and when
it expires.

## Roles {#roles}

**Roles** lists the roles you can give your members. **Defined by** says where each one comes
from:

- **Platform** roles are defined by your instance operator, such as the built-in viewer and
  tenant administrator. You can assign them, and open one to see its authorities, but only the
  operator can change them.
- **This tenant** roles are your own. Only your tenant has them.

{/* TODO(screenshot): roles-list, the Roles page with platform and tenant roles. */}

Choose **New role** to define one. Give it a token (it cannot change later), a name and a
description, then choose its **Authorities** from the checklist. The checklist groups authorities by
the area they act on, so `device:read` and `device:write` sit together under `device`. `*` grants
every tenant authority, including ones added in later releases.

{/* TODO(screenshot): role-detail, a tenant role with the grouped authority checklist. */}

Authorities you do not hold yourself are greyed out: nobody can grant more than they have. A role
can only carry tenant authorities, so instance-level permissions are never offered.

When you change a role, everyone who holds it gets the change at their next session refresh, as
described in [Changing a member's access](#changing-access). **Delete role** is refused while any
member still holds the role; take it away from them first.

## Customer users {#customer-users}

A customer user is a member whose access is limited to one or more of your customers. Use it to
give a customer of yours its own login: they see their own equipment, and nothing of anyone
else's.

### What a customer user sees {#what-a-customer-user-sees}

A customer user's sidebar holds only **Dashboard**, **Dashboards**, **Alarms** and **Devices**,
and in those:

- **Devices** lists the devices assigned to their customers now. A device moved to another customer
  leaves their list at once, without anyone editing the user.
- A device's page shows its live state, latest readings, events, commands and, where your
  installation runs it, its updates. Events and alarms are those recorded while the device belonged
  to one of their customers: a customer that receives a device does not see its history from before,
  and a customer that hands one on keeps the history from while it owned it.
- **Alarms** lists the alarms raised on those devices while their customers owned them. A
  customer user can acknowledge and clear them if their role allows.
- **Dashboards** lists the published dashboards shared with their customers, and opens their
  published version. They never see a draft.

Everything else, from device profiles and rules to connectors, audit and settings, is not in
their sidebar, and opening such a page by its address takes them back to the home page. The
server refuses those operations to them whatever their role says, so a customer user cannot
change your configuration even with a role that would allow it for staff. They also cannot
see a device's assignment, facets or stored configuration.

The tenant chip at the top right shows what they are limited to and their role, for example
**Acme · viewer** (the customer's name, then the token of the role).

To let a customer user send commands, give them a role that includes `command:write`. They can
then send a command to their own customers' devices and are refused for any other.

### From a customer's page {#from-a-customers-page}

A customer's page in the console has two tabs for this:

- **Devices** lists the devices assigned to the customer: the ones its users see. Assign a device
  from the device's own **Assignment** tab.
- **Users** lists the members limited to this customer. **Invite user** opens the invitation
  with **Can see** already set to this customer. The tab is shown only to administrators who can
  read members.

{/* TODO(screenshot): customer-users, a customer's Users tab. */}

### Sharing dashboards with customers {#sharing-dashboards}

A customer user sees a dashboard only once it is shared with one of their customers and published.
On the **Dashboards** list, choose **Share** on a dashboard and pick the customers. See
[Sharing with customers](/user-guide/dashboards#sharing-with-customers).

A shared dashboard shows each customer user only their own data. Bind its widgets to devices: a
widget bound to a customer, an asset or an area reads the relationship graph to find its devices,
which customer users are not given, so it shows them nothing.

### What is not covered yet {#not-covered-yet}

- Limiting a member to a group of devices, rather than to customers, is not available yet.
- Assets and areas assigned to a customer are not shown to its users.
- A customer cannot manage its own users. Your administrators invite and change them.

## Related

- [Assets, customers and areas](/user-guide/assets-customers-areas)
- [Dashboards](/user-guide/dashboards#sharing-with-customers)
- [Viewing dashboards in /dash](/user-guide/viewing-dashboards-in-dash)
- [GraphQL API: when your access is limited to some customers](/reference/graphql-api#limited-access)
- [Managing assignments](/guides/managing-assignments): attaching devices to customers.
