// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the dashboard-management service (ADR-039).
import { gql, GraphQLRequestError } from '@devicechain/client';
import {
  blockingLayoutFindings,
  lintLayout,
  parseDashboardDefinition,
  serializeDefinition,
  spanLimitsOfLayout,
  UNKNOWN_WIDGET_TYPE,
  unresolvedWidgets,
  type DashboardDefinition,
  type WidgetCatalog,
} from '@devicechain/dashboards';
import { hostWidgetCatalog, hostWidgetRegistry, type WidgetRegistry } from '@devicechain/host-widgets';
import { validateDefinitionOptions } from '@devicechain/widgets';
import { graphql } from '@/gql/dashboard-management';
import type {
  DashboardsQuery,
  DashboardQuery,
  DashboardVersionsQuery,
  PublishedDashboardQuery,
  DashboardUpdateRequest,
  ScopeAnchorKind,
} from '@/gql/dashboard-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type DashboardListItem = DashboardsQuery['dashboards']['results'][number];
export type Dashboard = NonNullable<DashboardQuery['dashboard']>;
export type Pagination = DashboardsQuery['dashboards']['pagination'];
export type DashboardSearchResults = DashboardsQuery['dashboards'];
export type DashboardVersion = DashboardVersionsQuery['dashboardVersions'][number];
export type PublishedDashboard = NonNullable<PublishedDashboardQuery['publishedDashboard']>;

// ── Dashboards ──────────────────────────────────────────────────────────

// The list omits the heavy `definition` JSON — the table only needs metadata.
const DASHBOARDS = graphql(`
  query Dashboards($criteria: DashboardSearchCriteria!) {
    dashboards(criteria: $criteria) {
      results {
        token
        name
        description
        createdAt
        updatedAt
        publishedVersion
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listDashboards(opts: {
  pageNumber: number;
  pageSize: number;
  name?: string;
}): Promise<DashboardSearchResults> {
  const data = await gql('dashboard-management', DASHBOARDS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
      name: opts.name ?? null,
    },
  });
  return data.dashboards;
}

const DASHBOARD_BY_TOKEN = graphql(`
  query Dashboard($token: String!) {
    dashboard(token: $token) {
      token
      name
      description
      definition
      updatedAt
      publishedVersion
    }
  }
`);

// The DRAFT, which is author-only: a caller without dashboard:write is refused. Members
// who may not write read the published snapshot with getPublishedDashboard instead.
export async function getDashboard(token: string): Promise<Dashboard | null> {
  const data = await gql('dashboard-management', DASHBOARD_BY_TOKEN, { token });
  return data.dashboard ?? null;
}

const DASHBOARD_PUBLISHED_VERSION = graphql(`
  query DashboardPublishedVersion($token: String!) {
    dashboard(token: $token) {
      publishedVersion
    }
  }
`);

// Which version viewers are served (null when never published). Metadata only -- it never
// selects the draft definition, so it needs just dashboard:read.
export async function getPublishedVersion(token: string): Promise<number | null> {
  const data = await gql('dashboard-management', DASHBOARD_PUBLISHED_VERSION, { token });
  return data.dashboard?.publishedVersion ?? null;
}

const PUBLISHED_DASHBOARD = graphql(`
  query PublishedDashboard($token: String!) {
    publishedDashboard(token: $token) {
      token
      name
      description
      version
      publishedAt
      definition
    }
  }
`);

// NOT_PUBLISHED_CODE is the extensions code the server answers for a dashboard that
// exists but has never been published.
export const NOT_PUBLISHED_CODE = 'NOT_PUBLISHED';

export function isNotPublishedError(err: unknown): boolean {
  return (
    err instanceof GraphQLRequestError &&
    (err.errors ?? []).some((e) => e.extensions?.code === NOT_PUBLISHED_CODE)
  );
}

// The snapshot viewers are served: null when the dashboard does not exist, and a thrown
// error for which isNotPublishedError is true when it exists but was never published.
export async function getPublishedDashboard(token: string): Promise<PublishedDashboard | null> {
  const data = await gql('dashboard-management', PUBLISHED_DASHBOARD, { token });
  return data.publishedDashboard ?? null;
}

const CREATE_DASHBOARD = graphql(`
  mutation CreateDashboard($request: DashboardCreateRequest!) {
    createDashboard(request: $request) {
      token
    }
  }
`);

// `definition` is the serialized board to start from (an import, a clone or a template);
// omitted, the dashboard is seeded with a canonical empty one. Either way it is created as a
// DRAFT -- publishing is a separate, explicit step.
export async function createDashboard(opts: {
  token: string;
  name?: string;
  description?: string;
  definition?: string;
}): Promise<{ token: string }> {
  // Seed the dashboard with a canonical empty definition so the stored JSON is
  // always a valid, round-trippable DashboardDefinition (ADR-039).
  const definition =
    opts.definition ??
    serializeDefinition(parseDashboardDefinition({ title: opts.name ?? '', widgets: [] }));
  const data = await gql('dashboard-management', CREATE_DASHBOARD, {
    request: {
      token: opts.token,
      name: opts.name ?? null,
      description: opts.description ?? null,
      definition,
    },
  });
  return data.createDashboard;
}

// updateDashboard is a PARTIAL update (ADR-039): a field this request omits is left
// exactly as it is stored, a field it sends is written, and an explicit null clears it.
// The `token` argument is the only thing that names the dashboard — DashboardUpdateRequest
// carries no token, so a request cannot name a second one.
//
// 🔴 THE THREE STATES ARE SPELLED BY `undefined` / `null` / A VALUE, AND THE DIFFERENCE
// MATTERS. Leaving a key off (or passing undefined) means "leave it alone"; passing null
// means "clear it". The previous shape collapsed the two — it defaulted every missing
// field to null, so a caller that did not mention `description` CLEARED it and got a 200
// for doing so. That is the full replace this conversion removes, and re-adding a `?? null`
// below would quietly bring it back.
//
// `definition` is the exception: the server refuses an explicit null on it, because a
// dashboard with no definition is not a thing. Omit it to keep the stored document.
//
// expectedUpdatedAt is an optimistic-concurrency precondition: pass the updatedAt the
// editor loaded so a save fails (CONFLICT) if another writer changed the dashboard since.
// It returns the new updatedAt so the caller can advance its baseline for the next save.
const UPDATE_DASHBOARD = graphql(`
  mutation UpdateDashboard(
    $token: String!
    $request: DashboardUpdateRequest!
    $expectedUpdatedAt: String
  ) {
    updateDashboard(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {
      token
      updatedAt
    }
  }
`);

export async function updateDashboard(
  token: string,
  input: {
    name?: string | null;
    description?: string | null;
    definition?: string;
    expectedUpdatedAt?: string | null;
  },
): Promise<{ token: string; updatedAt: string | null }> {
  // Built key by key rather than as an object literal: only the fields the caller
  // actually named reach the wire, which is what makes the omitted ones survive.
  const request: DashboardUpdateRequest = {};
  if (input.name !== undefined) request.name = input.name;
  if (input.description !== undefined) request.description = input.description;
  if (input.definition !== undefined) request.definition = input.definition;

  const data = await gql('dashboard-management', UPDATE_DASHBOARD, {
    request,
    token,
    expectedUpdatedAt: input.expectedUpdatedAt ?? null,
  });
  return data.updateDashboard;
}

// CONFLICT_MARKER matches the backend's ErrConflict message (dashboard-management
// model/api.go). A save that fails the optimistic precondition surfaces as a
// GraphQL error carrying this text; the editor uses it to show a reload-and-retry
// hint rather than a generic error.
export const CONFLICT_MARKER = 'modified by another writer';

// INVALID_DEFINITION_MARKER prefixes the error publishDashboard throws when the board it
// was handed carries option issues.
//
// 🔴 IT IS NOT A SECOND CONFLICT_MARKER, AND THE DRAWER DELIBERATELY DOES NOT MATCH ON
// IT. That constant exists because a backend error cannot carry a type across the wire;
// this refusal never reaches the network. The drawer names every issue and disables
// Publish before a call gets here, so reaching this throw means the UI's own check was
// bypassed — at which point the useful thing to show is WHAT WAS WRONG, not a friendly
// sentence. An earlier version matched on it and replaced the detail with a generic
// translated string: a branch that could not be reached, which threw away the only
// information worth having if it ever were.
//
// So this is a stable, greppable prefix for the message and for the tests, nothing more.
export const INVALID_DEFINITION_MARKER = 'dashboard definition is not publishable';

// UNRESOLVED_WIDGETS_MARKER prefixes the refusal for a board carrying a widget this console
// cannot resolve to an installed implementation (see publishDashboard). Same status as
// INVALID_DEFINITION_MARKER: the drawer names the widgets and disables Publish first, so
// this text is read by whoever bypassed it.
export const UNRESOLVED_WIDGETS_MARKER = 'dashboard has widgets this console cannot resolve';

// LAYOUT_BLOCKED_MARKER prefixes the refusal for a board whose placements are outside what their
// widgets can show (see the layout check in publishDashboard). The drawer lists the widgets and
// disables Publish first, as for the other two.
export const LAYOUT_BLOCKED_MARKER = 'dashboard layout is not publishable';

// ── Versioning (ADR-039) ──────────────────────────────────────────────────

const DASHBOARD_VERSIONS = graphql(`
  query DashboardVersions($token: String!, $limit: Int) {
    dashboardVersions(token: $token, limit: $limit) {
      version
      label
      description
      publishedAt
      publishedBy
    }
  }
`);

// Newest first; the server clamps `limit` to its maximum page size.
export async function listDashboardVersions(
  token: string,
  limit?: number,
): Promise<DashboardVersion[]> {
  const data = await gql('dashboard-management', DASHBOARD_VERSIONS, {
    token,
    limit: limit ?? null,
  });
  return data.dashboardVersions;
}

const DASHBOARD_VERSION_DEFINITION = graphql(`
  query DashboardVersionDefinition($token: String!, $version: Int!) {
    dashboardVersion(token: $token, version: $version) {
      definition
    }
  }
`);

// The stored definition JSON of one published version, so the drawer can tell the author
// what rolling back to or re-serving it would bring along before they confirm.
export async function getDashboardVersionDefinition(token: string, version: number): Promise<string | null> {
  const data = await gql('dashboard-management', DASHBOARD_VERSION_DEFINITION, { token, version });
  return data.dashboardVersion?.definition ?? null;
}

const PUBLISH_DASHBOARD = graphql(`
  mutation PublishDashboard(
    $token: String!
    $label: String
    $description: String
    $expectedUpdatedAt: String
  ) {
    publishDashboard(
      token: $token
      label: $label
      description: $description
      expectedUpdatedAt: $expectedUpdatedAt
    ) {
      version {
        version
      }
      dashboard {
        updatedAt
      }
    }
  }
`);

// publishDashboard freezes the current (saved) draft into a new immutable version and
// makes it the version viewers are served. A publish is not a draft edit, so the returned
// updatedAt is the draft's UNCHANGED one -- keep using it as the save baseline.
// expectedUpdatedAt is the same precondition as updateDashboard: publish fails with
// CONFLICT if the server draft moved on since — so it can't freeze another writer's
// content while the author believes they published their own view.
//
// 🔴 IT ALSO REFUSES TO PUBLISH A BOARD WHOSE WIDGETS ARE MISCONFIGURED, and the two
// paragraphs below are why the signature looks the way it does.
//
// WHY THE CHECK IS HERE AND NOT ON THE BUTTON. A disabled Publish button is a claim
// about one rendering of one drawer; this is the console's publish ACTION, the single
// place every publish passes through. Wiring the gate to the button would make it
// evidence only about that button, which is exactly how a validator becomes decoration
// — the UI below still disables the button, but as an explanation, not as the gate.
//
// 🔴 WHY IT TAKES A `definition` AT ALL, WHEN THE MUTATION SENDS NONE. The server
// freezes the draft IT holds; nothing about the definition travels with this request.
// So a caller validating "the definition" and a server publishing "the draft" can
// disagree, and the check would be green over a different document — the same
// one-construction-path trap the dashboard fixtures already hit. Naming the definition
// in the signature makes the caller say which document it believes is being frozen,
// and expectedUpdatedAt is what makes the belief true: it must be the baseline THAT
// definition was loaded or saved at, so a draft that moved on fails CONFLICT rather
// than being frozen unvalidated.
export async function publishDashboard(
  token: string,
  input: {
    // The definition the caller believes the server will freeze — the last SAVED
    // definition, at expectedUpdatedAt. Not the unsaved editor copy: that is a
    // document the server has never seen.
    //
    // The WHOLE definition, canvas included: the layout checks below read the grid.
    definition: DashboardDefinition;
    label?: string;
    description?: string;
    // REQUIRED, though nullable: this is the whole tie between the definition
    // validated above and the draft the server freezes. Optional, a caller could omit
    // it and get a check over a document the server may not hold — which is the exact
    // failure the `definition` parameter exists to prevent, re-entering by the back
    // door. `null` is still expressible, and means "publish whatever is there".
    expectedUpdatedAt: string | null;
    // The catalog `definition` was parsed with. Defaults to the deployment's own, so a
    // caller cannot validate an extension widget against a built-ins-only view by omission.
    catalog?: WidgetCatalog;
    // The registry the widgets' declared size limits are read from. Defaults to the
    // deployment's own, like `catalog`.
    registry?: WidgetRegistry;
  },
): Promise<{ version: number; updatedAt: string | null }> {
  // 🔴 A WIDGET THIS CONSOLE CANNOT RESOLVE BLOCKS A NEW VERSION. A published version is
  // immutable and is what viewers are served; freezing one that carries a widget this build
  // has no registration for (a package not installed here, or a settings version it cannot
  // read) would publish something the author could not see rendered or configure. The
  // DRAFT is deliberately unrestricted -- loading, layout edits and saving all keep the
  // widget's stored JSON byte for byte -- so the author resolves it (install the package,
  // or remove the widget) and then publishes. Checked before the option check and before
  // the network, for the same reason as below.
  const unresolved = unresolvedWidgets(input.definition);
  if (unresolved.length > 0) {
    throw new Error(
      `${UNRESOLVED_WIDGETS_MARKER}: ${unresolved
        .map((u) => `${u.widgetId} (${u.type || 'unknown'}): ${u.reason.message}`)
        .join('; ')}`,
    );
  }
  const issues = validateDefinitionOptions(input.definition, { catalog: input.catalog ?? hostWidgetCatalog() });
  if (issues.length > 0) {
    throw new Error(
      `${INVALID_DEFINITION_MARKER}: ${issues
        .map((i) => `${i.widgetId} (${i.widgetType}): ${i.message}`)
        .join('; ')}`,
    );
  }
  // 🔴 A PLACEMENT OUTSIDE WHAT ITS WIDGET CAN SHOW BLOCKS A NEW VERSION too: a size past the
  // widget's declared limits, a footprint running off the right edge, a relative widget pushed off
  // the canvas. The editor cannot normally produce them -- they arrive with an import, a
  // descriptor that has since changed, or a definition the API wrote -- which is exactly why the
  // check is on the publish action and not in the editor. The draft is unrestricted; the author
  // fits or fixes the board (the editor's "fit to limits") and publishes.
  const registry = input.registry ?? hostWidgetRegistry();
  const layout = blockingLayoutFindings(
    lintLayout(input.definition, {
      columns: input.definition.canvas.grid.columns,
      spanLimitsOf: (w) =>
        w.type === UNKNOWN_WIDGET_TYPE ? undefined : spanLimitsOfLayout(registry.descriptor(w.type)?.layout),
    }),
  );
  if (layout.length > 0) {
    throw new Error(
      `${LAYOUT_BLOCKED_MARKER}: ${layout.map((f) => `${f.widgetId} (${f.code}): ${f.message}`).join('; ')}`,
    );
  }
  const data = await gql('dashboard-management', PUBLISH_DASHBOARD, {
    token,
    label: input.label?.trim() ? input.label.trim() : null,
    description: input.description?.trim() ? input.description.trim() : null,
    expectedUpdatedAt: input.expectedUpdatedAt,
  });
  return {
    version: data.publishDashboard.version.version,
    updatedAt: data.publishDashboard.dashboard.updatedAt ?? null,
  };
}

const ACTIVATE_DASHBOARD_VERSION = graphql(`
  mutation ActivateDashboardVersion($token: String!, $version: Int!) {
    activateDashboardVersion(token: $token, version: $version) {
      publishedVersion
    }
  }
`);

// activateDashboardVersion re-serves an existing version to viewers WITHOUT touching the
// draft, unlike rollbackDashboard, which overwrites it. Returns the version now served.
export async function activateDashboardVersion(
  token: string,
  version: number,
): Promise<number | null> {
  const data = await gql('dashboard-management', ACTIVATE_DASHBOARD_VERSION, { token, version });
  return data.activateDashboardVersion.publishedVersion ?? null;
}

const ROLLBACK_DASHBOARD = graphql(`
  mutation RollbackDashboard($token: String!, $version: Int!, $expectedUpdatedAt: String) {
    rollbackDashboard(token: $token, version: $version, expectedUpdatedAt: $expectedUpdatedAt) {
      definition
      updatedAt
    }
  }
`);

// rollbackDashboard re-drafts a published version into the draft, returning the new
// draft definition + updatedAt so the editor can re-baseline without a reload.
//
// expectedUpdatedAt is the same precondition as updateDashboard's, and it is REQUIRED
// (though nullable) for the reason publishDashboard's is: a rollback replaces the whole
// draft, so one issued from a stale view must fail (the CONFLICT_MARKER error) instead of
// overwriting an edit it never saw. `null` means "roll back whatever is there".
export async function rollbackDashboard(
  token: string,
  version: number,
  expectedUpdatedAt: string | null,
): Promise<{ definition: string; updatedAt: string | null }> {
  const data = await gql('dashboard-management', ROLLBACK_DASHBOARD, {
    token,
    version,
    expectedUpdatedAt,
  });
  return data.rollbackDashboard;
}

const DELETE_DASHBOARD = graphql(`
  mutation DeleteDashboard($token: String!) {
    deleteDashboard(token: $token)
  }
`);

export async function deleteDashboard(token: string): Promise<boolean> {
  const data = await gql('dashboard-management', DELETE_DASHBOARD, { token });
  return data.deleteDashboard;
}

// ── Sharing with customers ───────────────────────────────────────────────
//
// Kept OUT of the list and detail selections above on purpose: `sharedWith` is refused to a
// member whose access is limited to part of the tenant, and the list query is exactly the
// one such a member uses to see what is shared with them. Selecting it there would turn
// their whole list into an error.

const DASHBOARD_SHARING = graphql(`
  query DashboardSharing($token: String!) {
    dashboard(token: $token) {
      token
      sharedWith {
        kind
        token
      }
    }
  }
`);

/** The customer tokens the dashboard is shared with, or null when there is no such dashboard. */
export async function getDashboardSharing(token: string): Promise<string[] | null> {
  const data = await gql('dashboard-management', DASHBOARD_SHARING, { token });
  if (!data.dashboard) return null;
  return data.dashboard.sharedWith.filter((a) => a.kind === 'CUSTOMER').map((a) => a.token);
}

const SHARE_DASHBOARD = graphql(`
  mutation ShareDashboard($token: String!, $anchors: [ScopeAnchorInput!]!) {
    shareDashboard(token: $token, anchors: $anchors) {
      token
      sharedWith {
        kind
        token
      }
    }
  }
`);

/**
 * Replace the customers the dashboard is shared with. Members limited to one of them can
 * then list it and open its PUBLISHED version; an empty list shares it with nobody.
 */
export async function shareDashboard(token: string, customerTokens: string[]): Promise<string[]> {
  const data = await gql('dashboard-management', SHARE_DASHBOARD, {
    token,
    anchors: customerTokens.map((t) => ({ kind: 'CUSTOMER' as ScopeAnchorKind, token: t })),
  });
  return data.shareDashboard.sharedWith.map((a) => a.token);
}
