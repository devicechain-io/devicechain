// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The hand-written inventory of GraphQL schemas, and the floor under the
// generator's discovery.
//
// 🔴 THIS FILE EXISTS BECAUSE A GLOB CANNOT BE ITS OWN FLOOR. The obvious design
// — glob backend/services/*/graphql/*.{graphql,gql}, then "verify" by listing
// backend/services/*/graphql/ directories — walks the same declaration twice.
// Rename graphql/ to anything and the directory drops out of BOTH at once: zero
// files discovered, zero directories missing, build green, schemas silently
// unpublished. The check has to come from a different direction than the thing
// it checks, so it is written down here by hand:
//
//   forward   a globbed file with no entry here      -> fail (a new schema, unpublished)
//   backward  an entry here whose file does not exist -> fail (a moved or renamed schema)
//
// The backward direction is the one that fires on a rename, and it is the whole
// reason for the file. Keeping 14 paths in step is mechanical, and getting it
// wrong is loud.

/**
 * Auth planes. The reason a plane has to be published alongside the schema: an
 * agent handed an undifferentiated pile of SDL will offer a tenant developer an
 * admin mutation they can never authorize, and the failure arrives as a 403 with
 * no explanation of why the call was never possible.
 */
export const PLANES = {
  tenant: {
    token: 'tenant access token',
    description:
      'The ordinary application plane. Obtain a tenant access token by calling login '
      + 'then selectTenant on user-management, and authorize each call with the '
      + 'capability it names (for example device:write).',
  },
  identity: {
    token: 'identity token',
    description:
      'Instance-scoped administration. Takes the identity token login returns BEFORE a '
      + 'tenant is selected, and is authorized for the superuser or an operator. A tenant '
      + 'access token is rejected here.',
  },
  service: {
    token: 'service token',
    description:
      'Called by another DeviceChain service, not by an application. Documented for '
      + 'completeness; there is no supported way for a tenant client to call it.',
  },
};

/**
 * The one extension a GraphQL schema artifact may carry, and the schema-shaped
 * extensions that are refused rather than ignored.
 *
 * 🔴 REFUSED, NOT IGNORED, and the difference is the whole reason this is written
 * down. Three of these schemas were spelled .gql for a long time. Two consumers
 * dropped them in silence for it: a *.graphql glob (the first inventory of this tree
 * reported 11 files and looked complete, missing login) and addlicense, the SPDX
 * header gate in CI, which has no handler for the extension and skips such a file
 * without reporting it. Both failures look exactly like a clean run.
 *
 * Mirrored in backend/core/graphql/schemaplane, which is the same rule for the Go
 * tools that read a services tree.
 */
export const SCHEMA_EXTENSION = '.graphql';
export const REFUSED_SCHEMA_EXTENSIONS = ['.gql', '.graphqls', '.gqls', '.sdl'];

/**
 * The mount each filename convention maps to, verified against the servers that
 * register them: /graphql in the shared core, /admin/graphql and /settings/graphql
 * in user-management and ai-inference. Deriving the plane from the filename is
 * what keeps the hand-written exception list below down to three entries.
 */
export const FILENAME_CONVENTIONS = [
  { match: /^admin_schema\.graphql$/, mount: '/admin/graphql', suffix: '-admin', plane: 'identity' },
  { match: /^settings_schema\.graphql$/, mount: '/settings/graphql', suffix: '-settings', plane: 'identity' },
  { match: /^schema\.graphql$/, mount: '/graphql', suffix: '', plane: 'tenant' },
];

/**
 * Every schema in the tree. `plane`, `publish` and `note` are overrides; anything
 * omitted is derived from the filename convention above.
 */
export const SCHEMAS = [
  { source: 'backend/services/ai-inference/graphql/admin_schema.graphql', area: 'ai-inference' },
  {
    source: 'backend/services/ai-inference/graphql/schema.graphql',
    area: 'ai-inference',
    plane: 'service',
    note: 'Called by event-processing under its own service token when compiling a '
      + 'natural-language rule description. Not reachable with a tenant access token.',
  },
  { source: 'backend/services/command-delivery/graphql/schema.graphql', area: 'command-delivery' },
  { source: 'backend/services/dashboard-management/graphql/schema.graphql', area: 'dashboard-management' },
  { source: 'backend/services/device-management/graphql/schema.graphql', area: 'device-management' },
  { source: 'backend/services/device-state/graphql/schema.graphql', area: 'device-state' },
  { source: 'backend/services/event-management/graphql/schema.graphql', area: 'event-management' },
  { source: 'backend/services/event-processing/graphql/schema.graphql', area: 'event-processing' },
  {
    source: 'backend/services/event-sources/graphql/schema.graphql',
    area: 'event-sources',
    publish: false,
    note: 'Inbound device transport. It has no GraphQL API of its own — the schema in the '
      + 'repository declares a placeholder field only, because the runtime requires a '
      + 'non-empty type. Telemetry reaches this service over MQTT and NATS, not here.',
  },
  { source: 'backend/services/notification-management/graphql/schema.graphql', area: 'notification-management' },
  { source: 'backend/services/outbound-connectors/graphql/schema.graphql', area: 'outbound-connectors' },
  { source: 'backend/services/user-management/graphql/admin_schema.graphql', area: 'user-management' },
  {
    source: 'backend/services/user-management/graphql/schema.graphql',
    area: 'user-management',
    note: 'login and refresh take no token — this is where a tenant access token comes '
      + 'from. Every other field on this schema requires one.',
  },
  { source: 'backend/services/user-management/graphql/settings_schema.graphql', area: 'user-management' },
];

/**
 * Published filenames that must exist after every run.
 *
 * These three are user-management's, and they are named here because they were once
 * the set an inventory of this tree silently dropped: they carried a different file
 * extension from every other schema, a *.graphql glob missed all three, and 11 files
 * looked exactly as complete as 14 does. The missing set contained login, the call
 * every other call depends on. The extensions match now — that is what the
 * discovery refusal in generate-schema.mjs keeps true — and these stay asserted BY
 * NAME because a count would look just as complete either way.
 *
 * 🔴 Note carefully what this does and does not cover, because the obvious reading
 * is wrong. It does NOT catch one of these files being renamed or deleted: the
 * manifest floor above fires on that first, in both directions. What it catches is a
 * change to the NAMING SCHEME — an edited suffix in FILENAME_CONVENTIONS, or an area
 * relabelled here — which the floor cannot see at all, because the sources still
 * reconcile perfectly while the published URLs move out from under every link and
 * every agent that cached them.
 */
export const REQUIRED_OUTPUTS = [
  'user-management.graphql',
  'user-management-admin.graphql',
  'user-management-settings.graphql',
];
