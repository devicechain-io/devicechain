---
title: Members, roles and customer users
sidebar_position: 19
---

# Members, roles and customer users

This chapter is for **tenant administrators**. It covers the people who belong to your tenant,
the roles they hold, and the logins you give your own customers so they can watch their
equipment without seeing anyone else's, whether you limit them to customers or to groups of
devices.

Two pages under **Access** in the sidebar do this work: **Members** and **Roles**. Each needs
its own permission. **Members** lists people with `member:read` and changes them with
`member:write`; **Roles** lists roles with `role:read` and changes them with `role:write`. The
built-in tenant administrator role holds all four. Without a permission, its page is not in your
sidebar.

## Members {#members}

**Members** lists everyone who belongs to the tenant, ordered by email address. For each person
it shows:

- their name, when they have set one, and their email address,
- the **Roles** they hold,
- what they **Can see**: **Whole tenant**, or the customers and groups of devices they are limited
  to,
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
- **Can see**: **Whole tenant**, or **Specific customers or groups**. Then pick **Customers**,
  **Groups of devices**, or both: up to 16 in all. A member limited to customers or groups is a
  *customer user*, described [below](#customer-users). Only groups used as an access scope are
  offered; if a group is missing, enable it on the group's page, as described in
  [Limiting a member to a group of devices](#group-scopes).

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

### Two-factor authentication {#member-two-factor}

The **Two-factor** column of the Members list shows whether each person has turned on two-factor authentication. The tenant can require it of everyone who signs in with a password; see [Sign-in Security](#sign-in-security). Resetting a person who has lost their phone is an operator's job, because the account is shared by every tenant. See [Two-factor authentication](/guides/two-factor-authentication#require).

### Sign-in Security {#sign-in-security}

**Access → Sign-in Security** holds the sign-in requirements for your tenant's people. Its **Two-factor
authentication** panel has one setting, **Require two-factor authentication**: members who sign in with
a password must use a code from an authenticator app, and anyone without one is asked to set it up the
next time they enter the tenant.

Choose **Require**. The console asks you to confirm first: members who sign in with a password and no
second factor will be asked to set one up before they can enter, and password sessions already inside
end at their next refresh. To stop requiring it, use the same control.

- Changing it needs the permission to manage members, for the whole tenant. Without it the page tells
  you so.
- If the tenant's tier requires two-factor authentication, the page says so and the requirement cannot
  be turned off here.
- Single sign-on is governed by its connection's requirements and is not affected by this setting.
- Each person sets up their own second factor under **Account security** in the user menu. The guide
  to [Two-factor authentication](/guides/two-factor-authentication#require) covers what the requirement
  does and does not bind.

### Changing a member's access {#changing-access}

From a row's menu:

- **Edit access** changes the member's **Roles** and what they **Can see**. When you change both
  in one save, they are saved together: if either is refused, neither changes.
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

A customer user is a member whose access is limited to one or more of your customers, to one or
more groups of devices, or to both. Use it to give a customer of yours its own login: they see
their own equipment, and nothing of anyone else's. Limit them to a customer when the devices are
assigned to it, or to a group when the devices they should see are the ones whose attributes match
a filter, such as every device in one region.

### What a customer user sees {#what-a-customer-user-sees}

A customer user's sidebar holds only **Home**, **Dashboards**, **Alarms** and **Devices**,
and in those:

- **Devices** lists the devices assigned to their customers now, and the devices in their groups
  now. A device moved to another customer, or out of the group, leaves their list at once, without
  anyone editing the user.
- A device's page shows its live state, latest readings, events, commands and, where your
  installation runs it, its updates. Events and alarms are those recorded while the device belonged
  to one of their customers, or was in one of their groups: a customer that receives a device does
  not see its history from before, and a customer that hands one on keeps the history from while it
  owned it.
- **Alarms** lists the alarms raised on those devices while their customers owned them, or while
  they were in their groups. A customer user can acknowledge and clear them if their role allows.
- **Dashboards** lists the published dashboards shared with their customers or groups, and opens
  their published version. They never see a draft.

Everything else, from device profiles and rules to connectors, audit and settings, is not in
their sidebar, and opening such a page by its address takes them back to the home page. The
server refuses those operations to them whatever their role says, so a customer user cannot
change your configuration even with a role that would allow it for staff. They also cannot
see a device's assignment, facets or stored configuration.

The tenant chip at the top right shows what they are limited to and their role, for example
**Acme · viewer** or **Acme, North wing · viewer** (the names of their customers and groups, then
the token of the role).

To let a customer user send commands, give them a role that includes `command:write`. They can
then send a command to the devices they can see and are refused for any other.

### From a customer's page {#from-a-customers-page}

A customer's page in the console has two tabs for this:

- **Devices** lists the devices assigned to the customer: the ones its users see. Assign a device
  from the device's own **Assignment** tab.
- **Users** lists the members limited to this customer. **Invite user** opens the invitation
  with **Can see** already set to this customer. The tab is shown only to administrators who can
  read members.

{/* TODO(screenshot): customer-users, a customer's Users tab. */}

### Limiting a member to a group of devices {#group-scopes}

A member can be limited to a group of devices instead of, or as well as, to customers. The group
has to be an **access scope** first, which you set on the group itself:

1. The group must be a **dynamic** group of devices: one whose members are chosen by a filter on
   their attributes. Save one from the **Browse** screen, as described in
   [Groups, facets and Browse](/user-guide/groups-facets-browse).
2. Open the group under **Device Groups** and choose its **Access scope** tab.
3. A dynamic group that has never been published shows **Publish filter**. Choose it to publish
   the group's filter as its first version. An access scope follows the group's published version,
   so the filter has to be published before the group can be one. If someone else publishes the
   group while you have the tab open, **Publish filter** does not publish a second version on top
   of theirs: the tab says the group changed and shows its current state.
4. Tick **Use as access scope**. This needs `device:write`.

{/* TODO(screenshot): group-access-scope, a device group's Access scope tab with the box ticked. */}

The tab says why a group cannot be an access scope when it cannot: only a group of devices can, and
only a dynamic one. A tenant can have at most 32 access-scope groups; ticking the box for one more
is refused, and the tab says so.

A member limited to the group then sees the devices in it, and only while they are in it. Membership
follows the devices' attributes, so a device whose attributes move it into the group appears for
them without anyone editing the member, and a device moved out disappears. What they see recorded
about a device (its events, alarms, commands and updates) is what was recorded while it was in the
group, the same rule that applies to a customer.

Clearing **Use as access scope** asks you to confirm. Members limited to the group then see none of
its devices, and the group is no longer offered on the **Members** page or for sharing a dashboard.
Their memberships, and the dashboards shared with the group, are kept: such a member can still open
those dashboards, but they show none of the group's devices. Ticking the box again gives them back
what they saw.

### Sharing dashboards with customers and groups {#sharing-dashboards}

A customer user sees a dashboard only once it is shared with one of their customers or groups and
published. On the **Dashboards** list, choose **Share** on a dashboard and pick the customers and
groups. See [Sharing with customers and groups](/user-guide/dashboards#sharing-with-customers).

A shared dashboard shows each customer user only their own data. Bind its widgets to devices, or to
the customer with the `assigned` relationship: such a widget shows the customer's users the devices
assigned to it. A widget bound to an asset, an area or a group reads the relationship graph to find
its devices, which customer users are not given, so it shows them **Not available for customer
users**.

### What customer users can read of areas and assets {#areas-assets-visibility}

A customer user can read the areas and assets that one of their devices is placed in, and the area
types and asset types of the tenant. They see the **name** and **description** of an area or asset,
and the **name** and **description** of a type, but not its free-form metadata. Keep text that
identifies a customer, or any other confidential detail, out of those names and descriptions, and
out of the name of an area that devices of several customers share: a customer user can read it.

### What is not covered yet {#not-covered-yet}

- A dashboard widget cannot yet show the devices of a member's group as a whole. Bind widgets on a
  dashboard shared with a group to its devices.
- After a group's first version, publishing a new version, which changes what its members see, is
  done through the API (`publishEntityGroup`), not from the console.
- Assets and areas assigned to a customer are not shown to its users.
- A customer cannot manage its own users. Your administrators invite and change them.

## Related

- [Assets, customers and areas](/user-guide/assets-customers-areas)
- [Dashboards](/user-guide/dashboards#sharing-with-customers)
- [Viewing dashboards in /dash](/user-guide/viewing-dashboards-in-dash)
- [GraphQL API: when your access is limited to some customers](/reference/graphql-api#limited-access)
- [Managing assignments](/guides/managing-assignments): attaching devices to customers.
- [Service accounts](/user-guide/service-accounts): non-human accounts for integrations.
