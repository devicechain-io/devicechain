---
sidebar_position: 4
title: Managing Device Assignments
---

# Managing Device Assignments

An **assignment** relates a device to a customer, area or asset, so its telemetry carries organizational context. In DeviceChain an assignment is a **tracked relationship** on the uniform entity graph. There is no separate assignment record.

:::note Status
Available. Manage assignments from the **Assignment** tab on the device detail page in the console, or over the device-management GraphQL API.
:::

## Assignment organizes; it does not gate {#assignment-organizes-it-does-not-gate}

A device authenticates with a credential. Assignment only organizes its data, and the two are independent:

- A registered, credentialed device reports telemetry immediately, even with no assignment. Its events resolve with an empty anchor set: they still persist and still update the device's live state, but they are not yet attributed to a customer, area or asset.
- Assigning the device later gives its subsequent events an anchor, so queries such as "every reading for Building 7" find them.

Unassigned devices are therefore never silently dropped. This is a change from earlier behavior.

## Every assignment is an anchor {#every-assignment-is-an-anchor}

A device may hold several assignments at once: a customer, an area and an asset. When the device reports an event, each assignment is recorded as an **anchor** on that event. The same reading is then queryable by every dimension; it shows up under the customer and under the area. No assignment is primary — they are equal.

Each event's anchors live in a sibling `event_anchors` set, one row per assignment. An anchor-filtered query ("events for area Y") matches events whose set contains that anchor.

Anchors are captured at write time, so history is stable. A device that later moves areas keeps, on each old event, the area it was in when that event happened.

## Assign a device (console) {#assign-a-device-console}

1. Open the device's detail page and select the **Assignment** tab.
2. Choose a target type (Customer / Area / Asset) and pick the target entity.
3. Click **Assign**.
4. Repeat to add more assignments. The device can be assigned to several targets at once.

To unassign, click **Unassign** on a row. The device's future events stop being anchored to that target; events already recorded keep their anchors.

## Assign a device (GraphQL) {#assign-a-device-graphql}

An assignment is a relationship edge of the reserved **`assigned`** type. This is a built-in tracked type, provisioned automatically per tenant on first use. Create one with the bulk mutation, addressing source and target by `(type, token)`:

```graphql
mutation {
  createEntityRelationships(requests: [{
    token: "3f1c…",            # a fresh unique edge token (e.g. a UUID)
    sourceType: "device",
    source: "sensor-001",       # device token
    targetType: "customer",     # customer | area | asset
    target: "lucidworks",       # target entity token
    relationshipType: "assigned"
  }]) { id token }
}
```

List a device's assignments by querying its tracked edges of the `assigned` type:

```graphql
query {
  entityRelationships(criteria: {
    sourceType: "device", source: "sensor-001",
    relationshipType: "assigned", pageNumber: 1, pageSize: 100
  }) {
    results { id token targetType target { token } }
  }
}
```

Remove one with `removeEntityRelationships(tokens: ["<edge token>"])`.

Creating and removing assignments requires the `device:write` authority. Listing them requires `device:read`.

## Relationship vs. assignment {#relationship-vs-assignment}

Assignment is one use of the general relationship graph. The same `createEntityRelationships` and `removeEntityRelationships` mutations back group membership (the reserved untracked `member` type) and any custom relationship type you define. What makes a relationship an assignment that anchors events is that its type is **tracked**. See the [Domain Model](../concepts/domain-model.md#relationships).
