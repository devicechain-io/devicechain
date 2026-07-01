---
sidebar_position: 3
title: Managing Device Assignments
---

# Managing Device Assignments

An **assignment** relates a device to a **customer**, **area**, or **asset** so its telemetry carries organizational context. In DeviceChain an assignment is just a **tracked relationship** on the uniform entity graph (ADR-013) — there is no separate assignment record.

:::note Status
Available. Assignments are managed from the device detail page's **Assignment** tab in the console, or over the device-management GraphQL API.
:::

## Assignment organizes; it does not gate

A device authenticates with a **credential**; assignment only **organizes** its data. The two are independent:

- A device that is registered and credentialed **reports telemetry immediately**, even with no assignment. Its events resolve with a **null anchor** — they still persist and still update the device's live state; they simply aren't attributed to a customer/area/asset yet.
- **Assigning** the device later gives its subsequent events an **anchor**, so queries like "every reading for Building 7" find them.

Unassigned devices are therefore never silently dropped — a change from earlier behavior (see ADR-013, addendum 2026-07-01).

## The primary anchor

A device may hold **several** assignments at once — a customer *and* an area *and* an asset. All of them live in the relationship graph. When the device reports an event, exactly **one** of them — the **primary**, defined as the device's **first** (lowest-id) assignment — is denormalized onto the event as its `(anchor_type, anchor_id)` anchor. This keeps anchor-filtered event queries join-free for the common case. Secondary assignments are recorded in the graph but are not denormalized onto events.

The console marks the primary assignment with a **`primary`** badge.

## Assign a device (console)

1. Open the device's detail page and select the **Assignment** tab.
2. Choose a **target type** (Customer / Area / Asset) and pick the **target** entity.
3. Click **Assign**. Repeat to add more assignments.

To unassign, click **Unassign** on a row. Removing the primary promotes the next-lowest assignment to primary for the device's *future* events.

## Assign a device (GraphQL)

An assignment is a relationship edge of the reserved **`assigned`** type (a built-in *tracked* type, auto-provisioned per tenant on first use). Create one with the bulk mutation, addressing source and target by `(type, token)`:

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

Remove one with `removeEntityRelationships(tokens: ["<edge token>"])`. All three operations require the `device:write` authority (list requires `device:read`).

## Relationship vs. assignment

Assignment is one use of the general relationship graph. The same `createEntityRelationships` / `removeEntityRelationships` mutations back **group membership** (the reserved untracked `member` type) and any custom relationship type you define. What makes a relationship an *assignment that anchors events* is simply that its type is **tracked**. See the [Domain Model](../concepts/domain-model.md#relationships).
