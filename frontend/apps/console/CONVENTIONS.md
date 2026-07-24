# Console UI conventions

The patterns the management console follows, and the reasoning behind them. This is a
living doc — when we make a UI decision worth not re-litigating, record it here with a
pointer to the code that embodies it. It is descriptive of what the console already does,
not aspirational; if a rule here and the code disagree, one of them is a bug.

Scope: `apps/console`. The embeddable dashboard/widgets packages are deliberately
Tailwind-free and framework-neutral (they run inside a host), so these console-specific
rules do not apply to them.

---

## Detail pages

The tenant, tier, and AI-provider detail pages share one shape. Copy it for the next one.

- **Header.** Title is the human name, falling back to the token when unnamed
  (`entity.name || entity.token`). When the entity has a human name *distinct from its
  token*, that **token rides on the same line as the title** via `titleAdornment`, as a
  click-to-copy [`CopyToken`](src/components/ui/copy-token.tsx) chip — a token is the id
  everything references, so it belongs beside the name and must be trivially copyable. Gate
  the chip on the name existing (`name ? <CopyToken value={token} /> : undefined`): with no
  name the token *is* the title, so a chip would just repeat it. Entities whose identity
  simply is the token (e.g. a role) title by the token and add no chip. Other identifying
  metadata (tier, enabled state, counts) sits *below* as badges/pills in the `description`
  slot, one row. The **`action`** slot holds only the entity's OWN actions (Enable/Disable,
  Delete) — never the back link (see Back navigation). See
  [`TierDetailPage.tsx`](src/routes/admin/tiers/TierDetailPage.tsx) and
  [`TenantDetailPage.tsx`](src/routes/admin/tenants/TenantDetailPage.tsx).

- **Back navigation lives in the layout top bar, not the page header.** Both
  [`AppLayout`](src/routes/AppLayout.tsx) and [`AdminLayout`](src/routes/admin/AdminLayout.tsx)
  render a route-derived back-link on the RIGHT of the top bar — beside the context
  indicator (the tenant chip; in admin, the "Admin" badge) — and set the top-bar title to
  `<Singular> Detail` on a detail page. A detail page therefore passes **only its own
  actions** to `PageShell`, never a `BackLink`: navigation ("leave this entity") stays
  visually separate from acting on the entity, and every detail page gets a back-link for
  free without wiring one.

- **Tabs.** The detail page owns the tabs; each tab wraps its content in its own
  [`SectionPanel`](src/components/ui/section-panel.tsx). The editing tabs share **one form
  state and one submit** (see the full-replace rule below); read-only or
  independently-mutating tabs (Effective settings, AI models, the provider smoke test) are
  separate panels the page passes in as props, not part of the entity save.

- **First-load gating.** Gate the spinner and error on the **first** load only —
  `if (loading && !data)` / `if (error && !data)`. Our
  [`useQuery`](src/lib/hooks/use-query.ts) re-enters `loading` on every refetch while
  keeping the prior `data`, so a bare `if (loading)` unmounts the whole form after every
  save and dumps the operator from whatever tab they were on back to the default. With
  `&& !data` the form stays mounted and the refetch repaints in place.

- **Width.** Detail forms span the page. Don't clamp the tabs container with `max-w-*`;
  individual inputs size themselves.

## Forms & inputs

- **Closed enums use [`Combobox`](src/components/ui/combobox.tsx), never a native
  `<select>`.** A native select renders an OS-drawn arrow that doesn't match the theme (a
  visible tell). Set `allowClear={false}` when the field is required. This applies even to
  a one-option enum (e.g. provider `kind`) for visual consistency. Multi-select uses
  [`multi-select.tsx`](src/components/ui/multi-select.tsx). There is no shadcn `select`
  primitive in this repo — don't add one.

- **Full-replace update APIs → one state, one submit.** Several backend updates
  (`updateTenant`, `updateTenantTier`, `updateAiProvider`) are **full replacements**: every
  field is written unconditionally, so a `null`/omitted field clears the stored value. A
  form split across tabs must therefore keep **one shared state object and one `save()`**,
  with the Save button rendered on each editing tab — all of them persist the whole entity.
  A per-tab split-submit would let one tab silently blank a field another tab owns. This is
  why the tenant/tier/provider detail tabs are views over a single editor, not independent
  forms. See [`TenantForm.tsx`](src/routes/admin/tenants/TenantForm.tsx) and
  [`AiProviderDetailPage.tsx`](src/routes/admin/ai-providers/AiProviderDetailPage.tsx).

- **Write-only secrets (ADR-059) are never displayed.** The read side exposes only
  `hasSecret`. Show "configured" with Replace / Clear affordances; the input is only ever
  for a *new* value, and "no change" must send `null` (preserve), not `""` (clear). See
  [`ProviderApiKeyControl`](src/routes/admin/ai-providers/AiProviderForm.tsx).

## Navigation (admin sidebar)

- **Groups** are collapsible via `SidebarMenuSub`. Nest a screen under a parent when it is
  configuration *of* that parent's domain, and give the parent an explicit **List** child
  for its own list page. Established instances: Tiers under Tenants (a tier is packaging of
  tenants, ADR-065) and Roles under Identities (roles are the RBAC vocabulary identities are
  assigned). See [`AdminSidebar.tsx`](src/routes/admin/AdminSidebar.tsx) and the richer
  [`AppSidebar.tsx`](src/routes/AppSidebar.tsx).
- **Accordion:** at most one group open. Auto-expand the group that owns the active route,
  computed from the pathname so deep links and refreshes land expanded.
- **Don't yank the user off an active child.** Re-expanding a group navigates to its first
  child only when opening from *outside* the group; if the current route is already a child
  (a tier detail page, say), expanding just toggles the disclosure.
- **Active state is a prefix match** (`pathname.startsWith(href)`), so a detail page keeps
  its section highlighted. Keep sibling hrefs on **disjoint prefixes**
  (`/admin/tenants` vs `/admin/tiers`) so exactly one matches.

## Layout primitives

- **Every page uses [`PageShell`](src/components/ui/page-shell.tsx).** Don't hand-roll a
  page header. Its slots: `title`, `titleAdornment` (inline with the title — the token
  chip), `description` (string or node, sits below the title — header badges), `action`
  (right-aligned buttons), and an optional decorative `banner`. Pages own their vertical
  rhythm with `space-y-*`.
- **Pass bare buttons to `action`; the shell lays them out.** It right-aligns them, puts a
  gap between them, and keeps them `shrink-0` so a long `description` can never crowd or
  overlap them. Do **not** wrap your actions in your own `flex`/`gap` row — that was how a
  page ended up with actions jammed against the edge. One button or several, just pass them
  (a fragment is fine).
- [`SectionPanel`](src/components/ui/section-panel.tsx) — the titled card. One per tab; it
  supplies its own border/heading, so don't wrap already-bordered content in it (that
  double-borders).

## Client logic

- **Split pure rules out of components.** Fold/derive/validate logic lives in a plain,
  unit-tested `.ts` beside the component, not inline in JSX — e.g.
  [`aiPackaging.ts`](src/routes/admin/ai-packaging/aiPackaging.ts),
  [`tenantAiModels.ts`](src/routes/admin/tenants/tenantAiModels.ts). The component stays a
  thin renderer; the rules get tests.
- **A client-side mirror of server logic is a hint, and says so.** When the console
  re-states a server rule it cannot query (e.g. what a tenant's AI function resolves to), it
  carries an explicit "mirrors the server, decides nothing" caveat and stays a re-statement
  of the server's cases — never a second source of truth. The server always re-checks. See
  the doc comment in [`tenantAiModels.ts`](src/routes/admin/tenants/tenantAiModels.ts).

## Internationalization (i18n)

The console is internationalized with **react-i18next** (ADR-066). The framework is
wired ([`src/i18n/config.ts`](src/i18n/config.ts)); [`Login.tsx`](src/routes/Login.tsx) is
the reference-converted screen — copy its shape. The string-externalization *sweep* over
the rest of the console (and the CI lint that holds the line, and the Spanish catalog) are
follow-on sub-workstreams; until the sweep reaches a screen, that screen's strings are still
hardcoded — but **new** copy must be written i18n-aware from now on.

- **No bare user-facing text.** Every string the user reads goes through the catalog:
  `t('key')` or `<Trans i18nKey="key">`. This includes `aria-label`, `title`, `placeholder`,
  and toast/error copy the console composes. It does **not** include tokens, ids, log
  messages, or user/tenant data (a device's name is the customer's data), and it does **not**
  include backend-originated error text — that stays English at GA (ADR-066 scope boundary);
  the console localizes only the copy it owns.
- **A sentence is one key with interpolation slots — never assembled from fragments.**
  Write `t('devices.count', { count })` (catalog: `"{{count}} devices"`), not
  `` `${count} ` + t('devices') ``. Fragment concatenation hard-codes English word order and
  is untranslatable. For counts/plurals use ICU/i18next plural keys (`_one`/`_other`), not an
  `n === 1 ? … : …` branch in the component. For a sentence with embedded markup (a link, a
  `<strong>`), use `<Trans>` so the whole sentence stays one translatable unit.
- **One namespace per screen; `common` for cross-screen copy.** `useTranslation('devices')`
  binds `t` to the screen's namespace; pull shared strings explicitly with the namespace
  prefix (`t('common:back')`). Namespaces are registered in
  [`config.ts`](src/i18n/config.ts) (`NAMESPACES`) and catalogs live under
  `src/i18n/locales/<code>/<namespace>.json`. Keys are **flat semantic identifiers**
  (`signInSubtitle`), never the English text and never dotted into a tree.
- **Keep copy in the render, in whole sentences**, not pre-built in a helper — so the
  extraction sweep can find it and so a translator sees a full sentence.
- **Locale precedence** (ADR-066): explicit user choice → tenant default → browser → `en`.
  The switcher persists the user
  choice; the tenant-default rung is a documented seam (`applyTenantDefaultLocale`) not yet
  wired. Prefer CSS logical properties (`ms-*`/`me-*`, `start`/`end`) over `left`/`right`
  where it's free, so a future RTL locale is cheaper — RTL is out of GA scope.

## Gotchas

- **Radix Tabs `forceMount` does not reliably hide inactive tab content in this Tailwind
  setup** — it renders the panel on *every* tab. Don't reach for it to preserve a tab's
  ephemeral state across switches; lift that state into the parent instead (or accept the
  reset). We hit this on the provider Test tab.
- **`useQuery` keeps prior `data` through a refetch and even a refetch error.** Gate
  loading/error UI on `&& !data` (see Detail pages) so background refetches don't blank a
  populated screen.
