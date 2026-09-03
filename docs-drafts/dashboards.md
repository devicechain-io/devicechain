---
title: Dashboards — definitions, slots, and the live runtime
status: draft
audience: engineering reference — see "Publishing this" at the end
adrs: [ADR-039, ADR-008, ADR-016, ADR-023, ADR-038, ADR-042, ADR-044, ADR-045, ADR-077]
---

# Dashboards — definitions, slots, and the live runtime

A dashboard is the one entity in DeviceChain the **backend deliberately does not understand**. The
service stores an opaque JSON document; everything that gives it meaning — the widget vocabulary,
the layout, the entity bindings, the data plumbing — lives in the frontend packages. That single
decision explains most of what follows, including several of the gaps.

The property worth holding onto before the detail: **a dashboard names entity ROLES, not entities.**
A board authored against "the pump" and "the area it sits in" can be handed to a different customer
with a different binding manifest and render their pump. That is the whole point of the arc, and it
works — inside this repo.

Every claim below names the file it came from.

## The shape, end to end

```
console authoring                              dashboard-management
  canvas editor ──── createDashboard ─────────▶  dashboards      (the mutable DRAFT)
  slot binding  ──── updateDashboard  ─────────▶      │
  publish       ──── publishDashboard ────────▶  dashboard_versions (immutable snapshots)
  export .json ──── (client-side only) ───┐          │
                                          │      rollback = COPY a snapshot back over the draft
                                          ▼
                                    a JSON file
                                          │
                                   paste into /dash
                                          │
        parseDashboardDefinition → effectiveBindings(manifest) → resolveContextBindings
                                          │
                                    DashboardHub
                    ┌─────────────────────┼─────────────────────┐
              measurement              alarm                 control
              (subscription,      (query + trigger,        (poll only,
               multiplexed)        30s poll backstop)        every 4s)
                                          │
                                  DashboardRenderer → ConnectedWidget → a pure widget
```

Two arrows are not what they look like. **Publishing changes what nobody reads** — every reader gets
the draft — and **rollback is destructive**, copying a snapshot over the draft rather than moving a
pointer.

## 1. What the server understands: three things

`backend/services/dashboard-management/model/api.go:52-65` is the *entire* server-side comprehension
of a dashboard definition: it is at most 1 MiB, it is valid JSON, and its first non-whitespace byte
is `{`. It does not parse the document, does not read any key, and stores the caller's bytes
verbatim.

🔴 **The model comment claims more than that.** `model/model.go:17-19` says the definition is
"validated only as well-formed JSON **carrying a schemaVersion**". Nothing reads `schemaVersion` —
not on the server, and not on the client either, where the parser defaults it when absent
(`frontend/packages/dashboards/src/definition.ts:71`). The claim is false at both ends.

What is *not* validated is the interesting list, because each item is a class of thing that fails
later and further away:

- **Widget types and options.** The only such check is client-side, inside the console's publish
  wrapper (`frontend/apps/console/src/lib/api/dashboards.ts:250-257`) — and its own comment concedes
  the mutation sends no definition, so the server freezes whatever is already in the draft column.
  Any non-console client publishes an unvalidated board.
- **Selectors, slots and bindings.** Nothing resolves or validates them. This is tracked, not
  accidental: the original decision promised selector validation and the amendment records losing it,
  with the consequence stated plainly — a malformed selector now fails **at render, not at save**.
- **Anything at publish or rollback time.** Publish copies the draft's bytes; rollback copies a
  stored snapshot straight back with no re-validation, so a definition frozen under an older format
  is restored unconditionally.

## 2. Versioning, and the asymmetry with device profiles

Device profiles are the established versioning pattern in the platform, and
`backend/services/device-management/model/model_profile_versions.go:22` says outright that it mirrors
this one. The two have since diverged in three ways that matter:

| | Device profile | Dashboard |
|---|---|---|
| What consumers read | the **active published version** — a device never resolves the draft | **the draft**. There is no active-version pointer; every reader gets it |
| Rollback | a non-destructive **pointer flip** | a **destructive copy** back into the draft; unpublished work is lost |
| Publish validation | fail-closed compile gate on the exact bytes being frozen | **none** |
| Publish atomicity | version insert + pointer flip in one transaction | two unwrapped statements |

So a published dashboard version is an **archival snapshot for rollback and export, not a serving
artifact**. That is the design — the decision record frames export as targeting a chosen version —
but it is stated nowhere in the service's own comments, and it is the first thing that surprises
someone who arrives from the profile code.

Two concurrency details worth knowing. `UpdateDashboard` does a genuine guarded write —
`UPDATE … WHERE id = ? AND updated_at = ?` with a zero-rows check
(`model/api.go:140-153`). `PublishDashboard` has only the pre-read string compare, so its
precondition is racy in a way update's is not. And **`RollbackDashboard` has no precondition at
all** (`model/api.go:217`): it silently clobbers a concurrent editor's saved draft.

🔴 **There is no way to read a published version.** A `DashboardVersion`'s definition field is
deliberately absent from the schema (`graphql/schema.graphql:53-66`), so the only retrieval path is
`rollbackDashboard` — a `dashboard:write` mutation that overwrites the draft. A user with
`dashboard:read` can see that version 7 exists, its label, and who published it, and cannot see what
is in it.

And versions are **hard-deleted with their parent** in one transaction (`model/api.go:320-325`), so
history is not durable past the dashboard.

## 3. Slots, bindings, manifests — the heart

Four concepts, deliberately separated, and the separation is what makes the model work.

**A slot is a named entity ROLE** the dashboard declares —
`{type: 'device'|'anchor', label?, defaultBinding?, scope?}`
(`frontend/packages/dashboards/src/types.ts:209-220`). A widget references it by name only.

🔑 **The measurements stay on the widget; the entity stays on the slot**
(`types.ts:177-181`). That is why one slot can feed a chart and a gauge different metrics off the
same device.

**A binding is the entity, and nothing else** (`types.ts:182-184`). An anchor binding is a real graph
edge — relationship plus target type plus token — explicitly *not* an opaque alias, because the
platform already resolves events against that edge.

**A manifest is the host's mount-time override map.** `effectiveBindings`
(`frontend/packages/dashboards/src/bindings.ts:24-36`) lays the manifest over the slots' own
defaults, and **a slot with neither is omitted** so it renders as an empty placeholder rather than
guessing.

🔑 **The sharpest edge in the whole API is a silent drop, and the code says so**
(`bindings.ts:50-56`): a typo'd binding does not fail — it vanishes, the slot stays unbound, and the
widgets render as empty frames with nothing saying why. `parseBindingManifest` therefore returns
`{bindings, dropped}` rather than just bindings, and a prototype-polluting key is refused *and
reported* rather than silently ignored.

**Authoring converts concrete to slot.** `migrateToSlots`
(`frontend/packages/dashboards/src/slots.ts:81-101`) rewrites every concrete selector into a slot
default-bound to that entity, **deduping identical bindings into one shared slot**, and returns the
same reference when nothing changed. The console runs it on every load
(`frontend/apps/console/src/routes/dashboards/DashboardDetailPage.tsx:52`) — a decisive pre-GA
rewrite rather than a compatibility shim.

Two carve-outs, both reasoned: an anchor carrying an aggregation stays concrete because the slot
model has no aggregation field, and slot reuse considers only *unscoped* slots so a plain widget
cannot silently inherit cascade behaviour.

**Template export** is `stripDefaultBindings` (`bindings.ts:85-93`), and its docstring is honest
about two leaks: a slot's `label` is often the author's own entity token and is not stripped, and an
aggregation-carrying anchor was never slotted so it is not rebindable.

### The cascade

A slot may declare `scope = {parent, strategy}`, resolving relative to a parent anchor slot's current
binding. **One parent only**, so the slots form a forest with no diamonds and cascade order is
well-defined (`types.ts:194-197`).

`resolveContextBindings` (`frontend/packages/dashboards/src/context.ts:100-160`) walks parent-first
and is fail-safe at every branch: an unbound or non-anchor parent leaves the child unbound, a
membership error leaves it unbound, a type-incompatible selection is ignored rather than corrupting
the context, and a selection naming an undeclared slot is dropped so a mis-authored drill target
cannot churn a rebuild. It never throws.

🔑 The interim state strips a scoped slot's default at first paint — **"unbound, never stale"**
(`context.ts:45-49`) — because a default computed for one context is wrong in another.

The same care shows up in the frame subtitle: a scoped slot reads **only** the resolved bindings and
never its default, because falling back would name an entity that is not being shown — a lying
subtitle (`frontend/packages/widgets/src/dashboard-renderer.tsx:189-195`).

## 4. Rendering

CSS Grid, not absolute pixels. A widget is placed by span, and `gridItemStyle`
(`dashboard-renderer.tsx:131-144`) **clamps columns** so an overrunning box cannot spill into
implicit tracks, while leaving rows unbounded on purpose.

The widget contract is pure — `{widget, data, actions?}`
(`frontend/packages/widgets/src/widget.ts:21-28`) — so a widget renders identically from a live
stream, a replayed window, or a fixture. **No widget imports the client SDK.**

**There is no dynamic widget registration.** Registration is a set of constant maps keyed by a
derived type subset (`frontend/packages/widgets/src/registry.ts:32-55`), where `WIDGET_CHANNEL` is
`as const satisfies Record<WidgetType, WidgetChannel>` — so a new widget type does not compile until
it declares a channel, and the per-channel registries are *derived* from that map rather than
hand-maintained. Registry drift is impossible by construction.

An unregistered widget kind never reaches a registry: the parser throws and the **whole board fails
to load** (`frontend/packages/dashboards/src/definition.ts:283-285`). Everything else degrades
silently — a malformed datasource drops to undefined, a bad grid falls back to defaults.

## 5. Data — three channels, and only one is a subscription

This is the most commonly mis-stated part of the arc.

**Measurement is a real subscription, and it is multiplexed.** The hub attaches per **device token**
to a shared upstream that subscribes *unfiltered*, with each subscriber filtering by name locally
(`frontend/packages/dashboards/src/hub.ts:685-741`). So a crowded board opens one stream per distinct
device, not one per widget. Ref-counted teardown is guarded against deleting a *replacement* stream
after an eviction, and an upstream error **evicts** the stream so the next subscriber reopens rather
than attaching to a corpse.

**Alarm is a query reconciled by a trigger, not a subscription.** The live stream is used only as a
signal to re-query; the authoritative rows come from a query, debounced at 800 ms, with a **30-second
poll backstop** and a re-query on reconnect (`hub.ts:313-386`). It is **not** multiplexed — every
alarm widget opens its own trigger and its own poll.

**Control is poll-only** at 4 seconds, because command-delivery exposes no subscription at all
(`hub.ts:449-498`).

A datasource that resolves to **zero devices yields empty, never tenant-wide** — on both the alarm
and control channels. That is the fail-safe direction: an unbound slot shows nothing rather than
silently widening to everything.

Error behaviour differs per channel on purpose:

- measurement: any error replaces the widget with an error frame;
- alarm: the error pane shows **only when there is nothing else to display**, so a transient error
  keeps last-good rows on screen — the channel self-heals in 30 seconds;
- control: the command button is **never** replaced by an error frame, because its primary control is
  the send form and a failed history poll must not tear the form down.

The availability probe **fails open** — an errored check renders the widget available — and
re-checks every 60 seconds while unavailable so a device deleted and recreated under the same token
recovers.

## 6. Actions and authorization

`WidgetActions` (`hub.ts:172-188`) is the only write path a widget may touch, and the reason is
stated: a widget that acts never reaches the SDK, so preview stays offline and the "a widget never
touches the backend" invariant holds. `sendCommand` is declared optional so an external host
predating it still satisfies the type.

Selection is deliberately a **different** seam from actions
(`frontend/packages/widgets/src/frame.tsx:34-38`): selection changes the view's binding overlay and
never the backend.

**The client-side `can()` is a UI affordance and says so in three places.** It reads authorities from
an **unverified** client-side JWT decode (`frontend/packages/client/src/jwt.ts:5-8`, `:46`).

**Enforcement is server-side, per resolver** — middleware verifies the token and attaches claims, and
each mutation calls `auth.Authorize` explicitly (`backend/services/device-management/graphql/mutations_alarm_state.go:19`,
`:41`; `backend/services/command-delivery/graphql/mutations.go:17`). The acknowledging identity is
taken from the verified claims and stamped under a `WHERE acknowledged = false` predicate, so a
second acknowledger cannot overwrite the winner.

🔴 **Those per-resolver calls are the sole line of defence.** There is no schema directive and no
middleware-level authorization — a resolver added without the line is world-readable within the
tenant. And **no test in dashboard-management reaches an authorization gate**: deleting every
`Authorize` call from its queries and mutations turns no test red.

## 7. Known gaps

1. ✅ **FIXED — `dashboard:read` is now in the default tenant-member baseline.** It was omitted
   from `viewerAuthorities` (`backend/services/user-management/identity/manager.go`), so an ordinary
   member and every OAuth `read-only` token were refused by all three dashboard queries. The OAuth
   read-only ceiling moved with it, since the two are kept exactly equal.
   Still absent, and still deliberate as far as anything records: `notification:read`,
   `connector:read` and `audit:read`. Note the list's comment used to claim "**all** domain
   objects", which was already untrue of those and became untrue again when device credentials
   moved behind `device:write`; the wording was corrected rather than the list widened.
   An ordinary member with no explicitly assigned role cannot list or open a dashboard, and neither
   can an OAuth read-only-scoped token.
2. 🔴 **The runtime packages cannot be consumed outside this repo.** All four are version `0.0.1`,
   none is published, three have **no build step at all**, and their entry points are
   `./src/index.ts`. They are consumed through the npm-workspace symlink and transpiled as
   first-party source by each app's bundler. The published docs claim a dashboard can be embedded in
   "any React app"; inside the repo that is true and `/dash` is the working proof, outside it there
   is nothing to install.
3. 🔴 **No tenant-lifecycle gate on dashboard-management.** Every other write path in the platform
   carries one. A write that re-lands rows after a purge sweep is reported by the sweeper as residual
   rows — a retryable error that **blocks purge completion**. The exposure is bounded, because
   deleting a tenant requires its memberships to be gone first so no new token can be minted; what
   remains is an already-issued access token living out its TTL. Same shape as the emitter gaps, on a
   control-plane write path.
4. **No caps anywhere that matter.** No limit on dashboards per tenant, versions per dashboard, or
   the `dashboardVersions` response, which takes no pagination and returns every version. Combined
   with unbounded publishing, that query on a heavily-published board is an unbounded response.
5. **Rollback has no concurrency precondition**, unlike save and publish.
6. **A published version cannot be read** — §2.
7. **The 1 MiB cap is bytes on the server and UTF-16 code units in the viewer**
   (`model/api.go:55` vs `frontend/apps/dashboard/src/load.ts:48`), under a comment claiming they
   match. For any non-ASCII definition the client's ceiling is the looser one.
8. **Audit rows are written and unreadable.** Dashboards are journalled by construction, and the
   service exposes no query over the journal.
9. **Anyone who can save can publish, roll back, and hard-delete** a dashboard with its whole
   history. There is no separate right for any of those.
10. **Per-breakpoint layout is supported by the format and the renderer and authored by nothing** —
    the editor edits the base breakpoint only. The published page states it as an available feature
    *and* lists it as planned, in the same file.
11. **The console cannot import a definition.** Export is console-only and entirely client-side;
    import is `/dash`-only and by paste. A board exported from one instance cannot be loaded into
    another instance's console.

## 8. What the tests do and do not reach

The strongest test is `frontend/packages/widgets/src/widgetlab-render.test.tsx`, and what makes it
strong is that it documents its own former weakness and fixes it: the input data is a **fixed**
metric vocabulary deliberately **not derived from the widgets' own datasources**, because building
the data from what each widget asks for "fabricates exactly the sample needed to make it look
healthy" — a card whose configured measurement is a typo would be handed a sample under the typo and
render a value it never could live. It also cut its placeholder list from seven strings to two after
finding five unreachable, on the reasoning that absence of a known failure string is a weak claim
where presence of the expected output is not.

The slot and cascade model is genuinely well covered, including the embed contract itself — a
template plus a matching host manifest resolving a slot.

What nothing reaches: **`DashboardRenderer` is never mounted**, and neither is `ConnectedWidget` —
so the channel dispatch, the error frames, the "keep last-good rows on a later error" rule, the
history seeding and the breakpoint resize listener are all unasserted. There is also no test for the
history seed, the device resolver and its positive-only existence cache, the theme store, or the
client's subscription transport.

## Publishing this

`docs/docs/concepts/dashboards.md` is the existing user-facing page. This arc's corrections land
there rather than in a new deployment page: dashboards have almost no operator surface — no tunables,
no scaling decision, no failure mode an operator acts on — so a "running dashboards" page would have
nothing true to say. What the page needed was to stop overstating.

The body carries no decision-record references so that it and anything derived from it are
publishable as-is; the frontmatter holds the pointers.
