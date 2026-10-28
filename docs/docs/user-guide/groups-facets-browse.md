---
title: Groups, facets and Browse
sidebar_position: 11
---

import GuideShot from '@site/src/components/GuideShot';

# Groups, facets and Browse

As a fleet grows, you need ways to pick out the devices (or assets, areas, customers) you care
about: the units on the roof, the sensors on floor 3. DeviceChain gives you three tools for this,
and this chapter covers all of them:

- **Groups**: named collections of one family, either a list you maintain by hand or a saved filter.
- **Facets**: classification keys, such as `zone` or `floor`, that you declare once and then set on
  each entity.
- **Browse**: a screen that filters a family by its facets, shows the matches live, and saves the
  filter as a **dynamic group** whose members stay up to date on their own.

It is for tenant users and administrators who organize a fleet. For the model behind it, see
[facets and dynamic groups](/concepts/domain-model#facets-and-dynamic-groups).

## Groups

Each family has its own group screen: **Devices → Device Groups**, **Organization → Assets → Asset Groups**,
**Organization → Customers → Customer Groups** and **Organization → Areas → Area Groups**. A group holds members of one family
only. The list shows each group's token, name, description and creation date.

<GuideShot id="device-groups-list" alt="The Device Groups list under the Devices menu, with one group, rooftop-units, named Rooftop units and described as every thermostat on the roof. A New device group button is at the top right." />

There are two kinds of group:

- A **static** group holds an explicit list of members. Create it with **New device group** (or the
  matching button for the family), then open it and use its **Members** tab to add and remove
  members.
- A **dynamic** group holds a saved filter instead of a list. Its members are worked out each time
  the group is read, so they always reflect the entities' current facet values. You create one on
  the [Browse](#browse) screen. Dynamic groups appear in the family's group list too; the
  `rooftop-units` group above is one. To see a dynamic group's current members, use its **Members**
  button under **Saved dynamic groups** on the Browse screen, which works them out from the filter.
  The **Members** tab on the group's own page manages a hand-kept list, so it does not show the
  members the filter matches.

## Facets

A **facet** is an attribute key you declare as a classification axis for one family, such as `zone`
for devices. Declaring a facet does two things: it makes the key available as a filter on the
Browse screen, and it gives the key a value field on each entity's **Facets** tab.

The values themselves live on the entities, not on this screen. A facet that is declared but has
no value on any entity matches nothing.

Open **Organization → Facets** in the sidebar. The chips at the top filter the list by member family: **All**,
**Device**, **Asset**, **Area** or **Customer**. Each row shows the member family, the key, the value
type, the suggested values and the label.

<GuideShot id="facets-list" alt="The Facets screen, filtered to All. Two device facets: zone, a STRING facet with the values lobby, rooftop, offices, amenities, technical and circulation, labeled Zone; and floor, a LONG facet with no value list, labeled Floor. Each row has a Delete action." />

### Declaring a facet

Declaring or deleting a facet, and saving a dynamic group, need the `device:write` permission;
without it those buttons are hidden.

Click **New facet**. The drawer asks for:

- **Member family**: Device, Asset, Area or Customer.
- **Attribute key**: the key the values are stored under, for example `climate`.
- **Value type**: `STRING`, `LONG`, `DOUBLE`, `BOOLEAN` or `JSON`. Values entered for the facet are
  checked against it.
- **Values**: optional. A comma-separated list of suggested values, offered as you type. Leave it
  blank for free-form values.
- **Label**: optional. The name shown for the facet; it defaults to the key.

<GuideShot id="facets-new" alt="The New facet drawer with the fields Member family set to Device, Attribute key with the placeholder climate, Value type set to STRING, Values with the placeholder arid, temperate, tropical, and Label, followed by a Save facet button." />

Click **Save facet**. There is no separate edit screen: to change a facet's type, values or label,
declare it again with the same member family and key, and the new declaration replaces the old one.

**Delete** removes only the declaration. The values stored on entities are left alone, and any saved
dynamic group that uses the facet keeps working.

### Setting facet values

To give an entity a facet value, open the entity and go to its **Facets** tab. Each facet declared
for that family is listed with its current value, or **Not set**. Enter a value and click **Save**, or
click **Clear** to remove it. If dynamic groups depend on the facet, the tab tells you which groups a
change would move the entity into or out of. The tab is described in
[Assets, customers and areas](/user-guide/assets-customers-areas#facets); devices have the same tab.

## Browse

**Browse** turns facets into a filter. Pick a member family with the chips at the top (**Device**,
**Asset**, **Area** or **Customer**). The **Facet axes** panel lists every facet declared for that
family, each with an operator:

- **— (ignore)**: the facet plays no part in the filter. This is the default.
- **is** and **is not**: match a value. For a text facet with suggested values, **is** lets you pick
  several values, and the entity matches any of them.
- **has any value**: the entity has some value for the facet. This is the only operator for a
  `JSON` facet.
- For `LONG` and `DOUBLE` facets, the comparisons `<`, `≤`, `>` and `≥` are also offered.

As you set conditions, the **Matches** panel shows the **Selector** the console has composed, the
number of members that match, and a sample of them. The count updates when you pause typing.

<GuideShot id="browse-filter" alt="The Browse screen with the Device family selected. Under Facet axes, Zone is set to is rooftop and Floor is ignored. The Matches panel shows the selector attr zone equals rooftop, 2 members match, and lists bp-therm-002 and bp-therm-003, with a Save as group button. Below, Saved dynamic groups lists rooftop-units with a dynamic badge, its selector and a Members button." />

The selector is shown exactly as it will be stored. It is written in CEL, the same expression
language detection rules use.

### Saving a dynamic group

When the filter matches what you want, click **Save as group**. The drawer shows the selector again
and asks for a **Token** (the button beside it generates one) and an optional **Name**. Click
**Save group**.

<GuideShot id="browse-save-group" alt="The Save as dynamic group drawer, showing the selector attr zone equals offices, a Token field with a generate button, an optional Name field, and a Save group button." />

The platform checks the selector again when you save, and refuses one it cannot run efficiently.
Once saved, the group appears under **Saved dynamic groups** for that family, with its token, name and
selector. Click **Members** to see who belongs to it right now; the list is worked out from the
selector each time you open it.

## Related

- [Facets and dynamic groups](/concepts/domain-model#facets-and-dynamic-groups)
- [Assets, customers and areas](/user-guide/assets-customers-areas)
- [Commanding a fleet](/guides/commanding-a-fleet): sending a command to every device in a group.
