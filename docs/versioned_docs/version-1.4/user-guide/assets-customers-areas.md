---
title: Assets, customers and areas
sidebar_position: 9
---

import GuideShot from '@site/src/components/GuideShot';

# Assets, customers and areas

Devices measure things. The registry is where you describe those things and how they are
organized: the **assets** a device monitors, the **customers** you serve, and the **areas** your
fleet is spread over. This chapter is for tenant users and administrators who set up and maintain
that structure.

The three families work the same way, so learn one and you know them all. Each family has three
screens under **Organization** in the sidebar:

| Family | Instances | Templates | Collections |
| --- | --- | --- | --- |
| Assets | Assets | Asset Types | Asset Groups |
| Customers | Customers | Customer Types | Customer Groups |
| Areas | Areas | Area Types | Area Groups |

A **type** classifies instances, for example "HVAC Unit" for assets or "Building" for areas. A
**group** is a named collection of instances of one family. The Areas menu also holds Geofences,
which have their own chapter, [Geofences](/user-guide/geofences).

For how these entities relate to devices and to each other, see the
[domain model](/concepts/domain-model).

## Lists

Each list shows the family's instances in pages, with **Prev** and **Next** below the table. For
assets, customers and areas, the columns are Token, Name, Type and Created. Click a row to open it.

<GuideShot id="assets-list" alt="The Assets list: five assets, three HVAC units and two of their components, each with a token, a name, the HVAC Unit type and a creation date. A New asset button is at the top right." />

The **token** is the instance's identifier. Other screens, rules and the API refer to it by token,
so it never changes after you create the instance. The **name** is the label people read, and you
can change it at any time.

<GuideShot id="customers-list" alt="The Customers list with one customer, Acme Properties, whose type is Facility Owner." />

<GuideShot id="areas-list" alt="The Areas list with three buildings, each of the Building area type. The expanded Areas menu shows Areas, Area Types, Area Groups and Geofences." />

## Creating an instance

Click **New asset** (or **New customer**, **New area**). A drawer opens with the same four fields for
every family:

- **Asset type** (or customer type, area type): the template this instance belongs to. If none
  exists yet, the picker tells you to create a type first.
- **Token**: the identifier. The button beside it fills in a token from the token pattern your
  operator has set for this kind of entity, shown below the field. You can also type your own. A token may contain letters, digits,
  hyphens and underscores, and must start with a letter or digit.
- **Name** and **Description**: free text.

<GuideShot id="assets-new" alt="The New asset drawer, with an Asset type picker, a Token field showing the pattern {slug} and a button to generate a token, Name and Description fields, and a Create asset button." />

Click **Create asset** to save. The button stays disabled until you have chosen a type and entered a
token.

## The detail page

Open an instance to see its detail page. The header shows the name, the token (with a copy
button) and the type. **Delete** is at the top right; it asks for confirmation, and a deletion
cannot be undone. If something still depends on the instance, the console shows the server's
refusal instead.

Deleting a customer also retires its token: you cannot create another customer with the same token
in this tenant. Alarms, commands, update assignments, shared dashboards and event history name the
customer by its token and stay after it is deleted, so a new customer at that token would inherit
them. Choose a different token for the new customer.

The **Basic** tab holds the same form you used to create the instance. You can change the type,
name and description, then click **Save changes**. The token is shown but cannot be edited.

<GuideShot id="asset-detail-basic" alt="The detail page of HVAC Unit 1, on the Basic tab: Asset type HVAC Unit, a read-only Token with the note that the asset id cannot change, Name, Description and a Save changes button. Tabs for Facets, Hierarchy, Properties and Devices sit beside Basic." />

Every asset, customer and area also has a **Facets** tab, described [below](#facets). Assets have
three more tabs: Hierarchy, Properties and Devices. Customers have two: Devices and Users.

### Hierarchy (assets)

Assets form a tree. A pump can sit under the plant room it belongs to, and a fan motor under its
HVAC unit. The **Hierarchy** tab shows where this asset sits and what is directly below it.

- **Location in the hierarchy** shows the path above the asset, or says that it is at the top.
- To move the asset, choose a **Parent asset** and click **Place under parent**. This replaces its
  current parent, and its own children move with it.
- **Detach** takes the asset out from under its parent, so it becomes a top-level asset.
- **Directly below** lists the asset's children. Click one to open it.

<GuideShot id="asset-detail-hierarchy" alt="The Hierarchy tab of HVAC Unit 1. It says the asset is at the top of the hierarchy, offers a Parent asset picker with Place under parent and Detach buttons, and lists two assets directly below it: the supply fan and the compressor." />

### Properties (assets)

An asset type can publish a **property contract**: a list of named, typed values that its assets
must carry, such as a manufacturer or a rated capacity. The asset's **Properties** tab is where you
fill those values in. If the asset's type has not published a contract, the tab says so, and the
asset carries no properties. You set up the contract on the asset type, as described in
[Asset types](#asset-types).

### Devices (assets)

The **Devices** tab lists the devices assigned to this asset, which is the reverse view of a device's
assignment. Click a token to open the device. To assign a device to an asset, see
[managing assignments](/guides/managing-assignments).

<GuideShot id="asset-detail-devices" alt="The Devices tab of HVAC Unit 1, with a single device, bp-therm-001." />

### Devices and Users (customers) {#customer-devices-and-users}

A customer's **Devices** tab lists the devices assigned to it, in the same way as an asset's. These
are the devices the customer's own users see.

The **Users** tab lists the members whose access is limited to this customer: the customer's own
logins. **Invite user** invites someone with their access already limited to this customer. The tab
is shown only if you can read the tenant's members. Customer users are described in
[Members, roles and customer users](/user-guide/members-roles-and-customer-users#customer-users).

{/* TODO(screenshot): customer-detail-users, a customer's Users tab. */}

### Facets {#facets}

The **Facets** tab holds this instance's **facet values**: the classification values you browse and
filter by on the Browse screen, such as the climate zone a building is in. A facet value lives on the instance
itself. The Facets screen only declares which keys are facets, so a declared facet with no value
here matches nothing.

If no facet is declared for the family yet, the tab says so and points you to the Facets screen.
Below, **Other attributes** lists everything else the instance carries. Values reported by a device
(CLIENT) or set by the platform (SERVER) are shown for reference only, and a facet filter does not
read them. **Add attribute** stores a new key; to browse by it, declare it on the Facets screen.

<GuideShot id="customer-detail-facets" alt="The Facets tab of the customer Acme Properties. Under Facet values, a note says no facets are declared for this family yet. Under Other attributes, an Add attribute button and the note No other attributes." />

<GuideShot id="area-detail-facets" alt="The Facets tab of the area Building 1, with the same layout: no facets declared for the area family, and no other attributes." />

Editing attribute values requires the `device:write` permission, for every family. Facets,
Browse and dynamic groups are covered in [Groups, facets and Browse](/user-guide/groups-facets-browse).

## Types

Types are templates that classify instances. Each type list shows an Appearance column (the badge
the type is drawn with), the token, a description and the creation date.

<GuideShot id="area-types-list" alt="The Area Types list with one type, Building, token bp-building. A New area type button is at the top right." />

A type's detail page has a **Basic** tab for its name and description and an **Appearance** tab for
the icon and colors of its badge. The badge appears wherever an instance of that type is shown.

### Asset types: property contracts and versions {#asset-types}

Asset types have two more tabs, because an asset type can declare what its assets carry.

The **Properties** tab holds the draft **Property contract**: a JSON array of property descriptors.
Each has a `name` and a `dataType` of `DOUBLE`, `INT`, `BOOLEAN` or `STRING`, plus optional
`description`, `unit`, `required`, `default`, `minValue`, `maxValue` and `enum`. Assets of the type are
then checked against it: an undeclared property, a value of the wrong type, or a missing required
one is refused.

Two things to know:

- Leaving the field empty **withdraws** the contract. That is not the same as an empty list `[]`,
  which declares that assets of this type carry nothing.
- Saving changes only the draft. Assets are checked against the **published** version until you
  publish again.

<GuideShot id="asset-type-properties" alt="The Properties tab of the HVAC Unit asset type. A note says assets of this type are checked against published version 1 and that edits take effect only when published again. The Property contract field holds a JSON array declaring manufacturer, ratedCapacityKw, installedYear and serviceTier." />

The **Versions** tab shows the active version and every published version, with its label, when it
was published and by whom.

- **Publish** freezes the current draft into a new version and makes it the one assets are checked
  against. You can give the version a label and a description.
- **Roll back** makes an earlier version the active one. The draft is not changed. Properties already
  stored are neither rechecked nor rewritten, so an asset that no longer fits stays as it is until
  someone writes its properties again.

Publishing, rolling back and editing a property contract need `device:write` too.

<GuideShot id="asset-type-versions" alt="The Versions tab of the HVAC Unit asset type. Active version: 1. One row: version 1, marked active, labeled Nameplate properties, with its publish time and publisher, and a Roll back action. A Publish button is at the top right." />

## Groups

A group collects instances of one family: Asset Groups hold assets, Customer Groups hold
customers, and Area Groups hold areas. A group's list and detail pages work like the others. Its
detail page has a **Members** tab where you add instances to the group and remove them.

Groups can also be **dynamic**: instead of a list you maintain by hand, the group is a saved filter
whose members are worked out each time it is read. You create those on the Browse screen. See
[Groups, facets and Browse](/user-guide/groups-facets-browse).

Deleting a group retires its token, as deleting a customer does: you cannot create another group
with the same token in this tenant. A member's access can be limited to a group, and the records
written while a device was in it name the group by its token, so a new group at that token would
inherit them. Choose a different token for the new group.

## Related

- [Domain model](/concepts/domain-model): assets, the asset hierarchy, groups and attributes.
- [Managing assignments](/guides/managing-assignments): attaching devices to assets, areas and
  customers.
- [Groups, facets and Browse](/user-guide/groups-facets-browse)
- [Geofences](/user-guide/geofences)
- [Members, roles and customer users](/user-guide/members-roles-and-customer-users)
