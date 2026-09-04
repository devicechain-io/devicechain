---
title: The asset property contract, and versioning it
status: draft
audience: engineering reference — see "Publishing this" at the end
adrs: [ADR-072, ADR-043, ADR-016, ADR-045, ADR-039, ADR-062, ADR-012]
---

# The asset property contract, and versioning it

An asset type can now say **what an asset of that type carries**, and that statement is a
governed, versioned tenant resource: authored as a draft, frozen by publishing, reverted by
rolling back. An asset's properties are checked against the version its type currently
publishes — refused when a property is undeclared, wrong-typed, out of range, or required and
missing.

Every claim below names the file it came from.

## What the gap actually was, and what it was not

The framing that motivated this work was right about the destination and wrong about the route.
It said "add draft/publish/rollback over the asset model." The previous slice's author had
already found the problem with that as a standalone task and it was upheld on review: **there
was nothing to freeze.** An asset type carried a name, a description and an appearance. Publish
what?

So the keystone was never the versioning. It was the **property schema** — the contract that
makes an asset of type "pump" a described thing rather than a labelled one. Versioning follows
from it and is nearly mechanical once it exists.

Two premises about the surrounding code turned out to be worth checking, and both moved the
design:

| Premise | What was actually there |
| --- | --- |
| "The pattern exists three times over; reuse it." | **Four** times — dashboards, connectors, device profiles, entity groups — and there is **no shared helper**. `backend/core` has nothing version-related; the four are copy-and-adapt descendants and each one's comments say so. This is the fifth. |
| "Nothing declares a typed field contract; facet keys are the closest thing." | Something does, and completely: `CommandDefinition.ParameterSchema` (ADR-043) declares typed fields and `command_validation.go` validates values against them, in two passes, strictly. That validator is now shared rather than re-spelled. |

## The descriptor is one vocabulary, not two

`backend/services/device-management/model/model_parameter_spec.go` holds `ParameterSpec` — a
name, a datatype from the `MetricDataType` vocabulary, and the constraints an author can put on
values that fill it (`required`, `default`, `unit`, `minValue`, `maxValue`, `enum`, and nested
`OBJECT` fields).

It was called `CommandParameter` while it had one user. Renaming it was the whole of what made
reuse honest: giving the asset side that name would have made every asset-side signature read
as if it were about commands, and writing a structurally identical `AssetPropertyDefinition`
beside it would have let the two contracts diverge one constraint at a time. `MetricDataType`
had already crossed the same line — it is the metric vocabulary *and* the command-parameter one
— so this follows a precedent rather than setting one.

The validator moved with it, into
`backend/services/device-management/model/spec_validation.go`, as two passes:

```
DECLARATION  is this contract coherent at all?
             unique names · a known datatype · bounds that can be satisfied
             · an enum whose members parse · a default inside its own enum
INSTANCE     does this document satisfy it?
             no undeclared keys · required present · type · bounds · enum
```

**The only thing that varies between the two users is the noun in the error message**, carried
as a `specChecker` value rather than as a second copy of the code. That is not cosmetic: those
strings are relayed verbatim to the tenant API client as a command rejection reason, and one of
them is pinned character for character by command-delivery's `enqueue_validator_test`.

## Strict, and why that is a decision rather than an inheritance

This area already had two value gates sitting on **opposite sides** of the strict/lenient line,
each with its reason written down:

- **measurements are lenient** — an undeclared metric key passes. A *device* wrote the value, and
  a profile misconfiguration must not reject a fleet's data.
- **commands are strict** — an undeclared payload key is refused. A command is an actuation, and
  a mis-keyed argument must never be silently delivered.

An asset property is written by an **operator through the API**. A misspelled property quietly
accepted produces an asset that reads as correctly described and is not. So: strict, on the
command side of the line.

Strictness needed one deliberate departure from the shared validator, in
`api_asset_properties.go`. An empty schema means *free-form, accept anything* on the command
path — the right reading for a definition that predates schemas, and exactly the wrong one for a
contract an author just published. `[]` published on an asset type means "assets of this type
carry **nothing**", and it refuses every property.

## The four cases, and each one is a decision

`validateAssetProperties` is total over these:

| The type… | …and the document | Result |
| --- | --- | --- |
| has no published version | absent | fine — the asset carries none |
| has no published version | present | **refused** — there is nothing to check it against |
| published an **empty** contract | non-empty | **refused** — the author said "nothing" |
| published a contract | absent | validated as `{}`, so a **required** property refuses it |
| published a contract | present | the strict document check |

The fourth row has a consequence worth meeting in prose rather than in production: **publishing
a contract with a required property makes every existing asset of that type non-conformant**,
and the next write to any of them — including one that only renames it — is refused until the
property is supplied. That is what "required" means. The escape hatch is a rollback, which is
instant and non-destructive.

### A retype re-validates what the caller never mentioned

`UpdateAsset` validates the **resulting** (type, document) pair, not the field the caller sent.
Either half can move: a retype re-points the contract while leaving the document alone, and a
document edit fills a contract that did not move. Checking only what was mentioned would let a
retype strand an asset carrying properties its new type never declared — conformant when
written, silently not afterwards.

## The versioning is the fifth instance, and it is the pointer variant

`api_asset_type_versions.go`, structurally alongside `api_profile_versions.go` and
`api_group_versions.go`: `AssetType.PropertySchema` is the mutable **draft**;
`PublishAssetType` re-validates it and freezes it into the next monotonic
`AssetTypeVersion`; `AssetType.ActiveVersion` points at the live one; `RollbackAssetType` flips
that pointer without touching the draft or deleting a version.

It takes the **pointer** variant (device profiles, entity groups) rather than the re-draft
variant (dashboards, connectors) because it has a runtime consumer: every asset write resolves a
specific version, so rollback has to be instant and non-destructive.

Three details are worth naming because each closes something that fails quietly:

- **`Omit("ActiveVersion")` on the type update.** This is the third instance of an identical
  latent bug — `DeviceProfile` and `EntityGroup` each carry the same line with the same comment.
  Without it, an edit racing a publish writes the pointer back from its stale load and silently
  reverts the contract every asset is checked against.
- **A dangling pointer fails CLOSED.** The profile equivalent logs and resolves empty, because a
  device with no declared capabilities is inert. Here an empty resolve is the branch that
  *accepts anything*, so the same leniency would turn a broken pointer into an open door.
- **A published version's frozen document IS exposed**, unlike a profile snapshot's. A rollback
  list showing only version numbers asks somebody to choose between contracts they cannot see.

And one that is stated rather than fixed: **a rollback does not re-validate stored properties.**
Conformance is enforced at write, against the version active at that moment. Rolling back to a
narrower contract can leave documents that no longer satisfy it, and nothing sweeps for them.

## Storage

One appended migration,
`backend/services/device-management/schema/migration_asset_property_schema.go`: three nullable
columns (`asset_types.property_schema`, `asset_types.active_version`, `assets.properties`) and
one new table, `asset_type_versions`.

The columns are **raw DDL rather than a snapshot struct**, which is the same decision
`NewListOrderIndexesSchema` records: gorm derives a table name from the type name, so a second Go
shape mapping to `asset_types` needs a `TableName()`, and a `TableName()` bypasses this area's
`TablePrefix` — the statement would be issued against an unqualified `asset_types` resolved
through `search_path`. Writing the column, its type and its table out literally *is* the snapshot.

No backfill, and the claim is the narrow one: NULL is the correct reading of every pre-existing
row, not a placeholder for a value that should have been computed. Defaulting `property_schema`
to `[]` instead would be a **stronger** claim than the data supports — an empty contract refuses
properties, where no contract merely has none.

`asset_type_versions` carries `tenant_id`, so the catalog-driven tenant purge classifies it with
no exemption to register, and `DeleteAssetType` cascades it by hand — there is no database
cascade, and without it a *published* asset type would be permanently undeletable behind a raw
constraint error. `assetPropertyTestApi` runs sqlite with `PRAGMA foreign_keys = ON` so the
harness can see that; the fixtures that leave it off cannot.

## Properties are not attributes, and the distinction is the point

An asset can already carry `EntityAttribute` rows — free-form, entity-agnostic current-state
key/values that facet keys classify (ADR-012). Properties are a different thing and live in a
different column:

| | Entity attribute | Asset property |
| --- | --- | --- |
| Declared by | nobody; any key, any type | the asset's **type**, per type |
| Shape | one row per (entity, scope, key) | one JSON document on the asset |
| Refuses a key nothing declared | no | yes |
| Required / default / bounds | no | yes |
| Versioned | no | yes — the contract is |

Storing properties as attributes would have meant either abandoning that refusal or imposing it
on the free-form surface facet authoring already writes through.

## The console

- **Asset type → Properties** authors the draft contract as JSON, which is the same surface a
  command definition's parameter schema is authored through and the same descriptor. Validation
  is the server's; a client-side re-implementation would be a second opinion that is wrong the
  moment the server's changes.
- **Asset type → Versions** publishes and rolls back. The panel is now
  `frontend/apps/console/src/components/VersionsPanel.tsx`, **shared** with device profiles — the
  mechanics are identical everywhere and only the sentences differ, so the sentences arrive as
  props and the generic strings moved to a `versions` i18n namespace.
- **Asset → Properties** renders a typed form from the type's **active published** contract (not
  its draft — the draft is not what the save is validated against) reusing the same
  parse/validate/serialize helpers the dashboard command-button widget uses.

## What is NOT built

Recorded here rather than left to be discovered:

- **No conformance sweep.** Nothing reports which assets no longer satisfy their type's current
  contract after a publish or a rollback. The information is derivable; nothing derives it.
- **No structured-property authoring in the console.** The contract supports nested `OBJECT`
  fields and the API validates them; the asset form refuses to open as a form when it meets one
  and shows the stored document instead. That is deliberate — the shared form helpers *drop*
  object fields on serialize, so a form that looked complete would delete data the author could
  not see it holding.
- **A declared `BOOLEAN` property is always written**, as true or false. Its checkbox has no
  third state, so "absent" is not a value the console form can express for one.
- **No property-based search or filtering.** `assets(criteria:)` still filters by type only; the
  document is stored as JSON and nothing queries inside it.
- **Defaults are never materialized.** A `default` is an authoring hint: it does not satisfy a
  required property and is not written into the stored document. A materialized default would
  freeze one version's declaration into data a later publish could no longer reach.
- **No navigable asset tree browser, no paging on the asset-devices or children lists, and the
  asset-devices view is still read-only** — the three items the previous slice named, all still
  open.

## Publishing this

Nothing here is user-facing prose. When a public page is written, the audience-facing half is
"describe what an asset type's assets carry, and change that description safely"; it belongs
under the concepts/guides split, in **both** locales, and **may not cite an ADR** — the docs
site's readers cannot follow those references.
