---
title: Assets, the hierarchy, and device replacement
status: draft
audience: engineering reference — see "Publishing this" at the end
adrs: [ADR-072, ADR-074, ADR-013, ADR-014, ADR-042, ADR-049, ADR-061, ADR-019]
---

# Assets, the hierarchy, and device replacement

Two lifecycle surfaces that both rest on the same thing: **an identity that does not move.** An
asset is the durable business thing being measured; a device is the hardware measuring it; and
DeviceChain's model keeps both stable while everything around them rotates. This draft is the
traversal across the components that no single source file holds.

Every claim below names the file it came from.

## What was already there, and what the gap actually was

The framing that motivated this work said DeviceChain had "no business-facing app on top" of the
graph — that a non-engineer could not model an asset type and drop devices under it. **That is
about half wrong**, and the half that was wrong is worth recording, because it is the half people
keep re-proposing.

Already shipped before this arc:

| | Where |
| --- | --- |
| `AssetType` / `Asset` as first-class tenant entities, with tables, tokens, CRUD and search | `backend/services/device-management/model/model_assets.go`, `api_assets.go` |
| GraphQL for both, gated on `device:read` / `device:write` | `backend/services/device-management/graphql/queries_assets.go`, `mutations_assets.go` |
| Console list / detail / create / edit / delete for assets and asset types | `frontend/apps/console/src/routes/assets/resource.tsx`, `routes/asset-types/resource.tsx` |
| Asset groups — static membership and dynamic CEL selectors | `frontend/apps/console/src/routes/asset-groups/resource.tsx`, `backend/services/device-management/model/model_groups.go` |
| Facet-value authoring on an asset | `frontend/apps/console/src/components/EntityAttributesPanel.tsx` |
| Device → asset assignment, as a **tracked** edge, so the asset becomes an anchor on the device's events | `frontend/apps/console/src/routes/devices/DeviceAssignmentPanel.tsx`, `backend/services/device-management/model/api_membership.go` |

So "add an asset model" was already done. The real gap was narrower and different:

1. **No hierarchy of any kind.** `Asset` had no parent, no relationship type expressed containment,
   and nothing in the console offered a tree. Every asset was a peer of every other.
2. **No reverse view.** Assignment could only be seen from the DEVICE side. Standing on an asset,
   there was no way to ask "what is measuring this?" — you had to enumerate devices and look at each
   one's assignments.

Both of those are what this slice closes. Neither needed a new storage primitive.

## The hierarchy is an edge, not a column

`backend/services/device-management/model/api_asset_hierarchy.go` adds a third reserved relationship
type alongside `member` and `assigned`:

```
ContainmentRelationshipType = "contains"    // untracked; source = PARENT, target = CHILD
```

Untracked is deliberate. Tracked-ness decides whether a relationship is denormalized onto a
**device's** events as an anchor; a containment edge has an asset on both ends, so it can never be a
device's anchor. Assigning a device to an asset is what gives telemetry its asset context; this type
organizes the assets themselves.

```
plant ──contains──▶ line-a ──contains──▶ cell-1
                                    └──▶ cell-2
                                            ▲
                            dozer-01 ──assigned──┘   (tracked: cell-2 becomes an event anchor)
```

Reads: `AssetParent`, `AssetAncestors` (nearest-first, for a breadcrumb), `AssetChildren` (which
answers the ROOT level when its parent argument is nil — a tree browser asks the same question at
every level). Writes: `SetAssetParent`, `ClearAssetParent`.

### The three invariants, and the one place they are enforced

A tree is a tree only because something keeps it one:

1. both ends are assets,
2. at most one parent,
3. no cycle (plus a depth bound, `MaxAssetHierarchyDepth`).

All three live in `admitContainmentEdge`, and **it is called from the generic edge API as well as
from `SetAssetParent`** — `CreateEntityRelationship` in `api_common.go` and
`CreateEntityRelationships` in `api_membership.go`. That placement is the whole point:
`createEntityRelationship` is a public mutation any `device:write` holder can call with
`relationshipType: "contains"`, so an invariant checked only in the convenience door is an invariant
with a public bypass.

Two details that are easy to get wrong and were:

- **The checks read on the handle the caller is writing on.** `admitContainmentEdge` takes a
  `*gorm.DB`. A re-parent deletes the old edge inside its transaction and then asks "does this asset
  already have a parent?"; a read issued on a fresh session would still see the deleted edge and
  refuse every move. Same shape on the bulk path, where two edges in one batch can close a loop that
  neither closes alone.
- **The depth bound checks the moved subtree's height, not only the parent's ancestor chain.**
  Otherwise every edge is legal when created and the tree they form is not — a bound that reads as
  enforced and is not.

The ancestor walk is bounded independently, and returns `ErrAssetHierarchyTooDeep` rather than a
truncated path. That bound is dead code against every write this API performs, because the cycle
check keeps the graph acyclic; it exists for the case where that premise stops holding (a hand-run
`UPDATE`, a restored backup). `TestAssetAncestorWalkRefusesACyclePlantedOutsideTheApi` plants a
cycle with a direct `Create` — the one door that skips the gate — to cover exactly that input class.

## Device replacement rotates credentials, not identity

`backend/services/device-management/model/api_device_replacement.go`. `ReplaceDevice` does three
things in one transaction:

```
  retire every ENABLED credential the outgoing unit holds   (disabled, not deleted)
  mint one credential for the incoming unit
  write one DeviceReplacement row                            (append-only)
```

Identity, `externalId`, device type and profile binding are untouched — so events, alarms,
relationship edges and group memberships all carry forward, because every one of them keys on the
device row that did not move. "History carries forward" is not something this function does; it is
something it is careful not to break.

**The retirement is the security-critical half.** A failed unit is commonly still powered and still
in someone's hands; until its credential is disabled it authenticates exactly as it always did,
under the identity now also held by its replacement. Two units answering as one device produce
telemetry no reader can attribute. Disabled is sufficient, because
`DeviceCredentialByCredentialId` — the resolve every transport authenticates through — matches
`enabled = true` only.

`DeviceReplaceRequest` carries **no device identity fields at all**: no token, no `externalId`, no
device type, no name. "Rotate credentials, not identity" is unrepresentable to violate rather than
merely refused, which is the same line `DeviceTypeUpdateRequest` drew when it dropped `Token`.

### What the journal stores, and what it deliberately does not

`DeviceReplacement` (`model_device_replacement.go`) records the device, the instant, the actor, an
optional reason, the incoming unit's own serial, the credentials retired and the credential minted.
Credentials are named by their **entity tokens**, never by `CredentialId` — which for an
`ACCESS_TOKEN` *is* the bearer. That is what lets `deviceReplacements` read at `device:read` while
the three credential queries next door need `device:write`.

It is append-only by construction, not by convention: it has no update or delete entry point
anywhere in the API or the schema, and it carries no token, so there is nothing to address a
re-point at. The one writer is `ReplaceDevice`.

The incoming unit's material is therefore readable **exactly once**, in the mutation result. The
console's `DeviceReplacementPanel` shows it in a banner that survives the history reload and stays
up until dismissed; nothing can re-fetch it afterwards.

### Storage

One appended migration, `backend/services/device-management/schema/migration_device_replacements.go`,
with its own snapshot struct. No backfill: a replacement row records an operation, and no such
operation has ever run, so there is no historical state to derive one from. The table carries
`tenant_id`, so the catalog-driven tenant purge classifies it with no exemption to register.

## What is NOT built

Recorded here rather than left to be discovered:

- **No asset-type property schema.** An asset type still carries only a name, description and
  appearance. There is no analogue of `DeviceProfile`'s metric/command vocabulary for assets, and
  therefore nothing to version.
- **No draft/publish/rollback for an asset model.** The asset-modelling decision asks for it; the pattern exists three
  times over (`api_profile_versions.go`, `api_group_versions.go`, dashboard-management), and the
  asset surface uses none of it. There is currently no versionable payload for it to freeze, which
  is why this waits on the item above rather than being independent of it.
- **No tree browser.** `AssetChildren` answers the root level and each child level, which is the
  whole API a tree needs, but the console renders one asset's parent path and its direct children —
  not a navigable tree from the asset list.
- **The asset-devices view is read-only.** Assign/unassign stays on the device's own panel, which is
  where the operator already chooses among customer/area/asset targets. A second authoring surface
  for one edge would be two places to keep in agreement.
- **No paging on the asset-devices or children lists.** Both read one bounded page and report the
  total, so a long list reads as "showing N of M" rather than as a complete short list.

## Publishing this

Nothing here is user-facing prose. When a public page is written, the audience-facing halves are
"how to build an asset hierarchy" and "how to replace a failed device without losing its history";
both belong under the concepts/guides split, in **both** locales, and **neither may cite an ADR** —
the docs site's readers cannot follow those references.
