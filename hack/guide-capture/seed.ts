// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// seed-guide: the authoring objects the user guide's screenshots need and the simulator does
// not create. Run as its own step, after the simulator has populated the hero tenant:
//
//   npm run seed                      # hero tenant (sim `guide`, manifest buildingpulse)
//
// GraphQL as the hero tenant's admin (the sim identity, session.ts), IDEMPOTENT, and
// ASSERTING: every object is created only when missing, and the run ends by reading each
// one back by its token and failing loudly if it is not there. A capture run against a
// half-seeded tenant must fail here, not produce a screenshot of an empty screen.
//
// Names read like a real building operator ("Rooftop AHU 2"), not test data.
//
// The instance-administration objects (chapter 15) are seeded as the SUPERUSER, last
// (seedAdmin): a tier, people, a role, a Google Workspace connection and a throwaway tenant that
// is deleted. Creating the connection fetches Google's discovery document, so it needs network.
//
// Imports carry their .ts extension so Node can run this file directly.

import { createHash, generateKeyPairSync, randomBytes, sign, type KeyObject } from 'node:crypto';
import { existsSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { readSimHandshake, signIn } from './session.ts';

const BASE = process.env.GUIDE_BASE_URL ?? 'http://localhost';
const SIM = process.env.GUIDE_SIM ?? 'acme';

let accessToken = '';

type Vars = Record<string, unknown>;

class GqlError extends Error {
  constructor(area: string, messages: string[]) {
    super(`${area}: ${messages.join('; ')}`);
  }
}

async function gql<T = any>(area: string, query: string, variables: Vars = {}): Promise<T> {
  const res = await fetch(`${BASE}/api/${area}/graphql`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${accessToken}` },
    body: JSON.stringify({ query, variables }),
  });
  const body = (await res.json()) as { data?: T; errors?: { message: string }[] };
  if (!res.ok || body.errors?.length) {
    throw new GqlError(area, body.errors?.map((e) => e.message) ?? [`HTTP ${res.status}`]);
  }
  return body.data as T;
}

const DM = 'device-management';
const CD = 'command-delivery';
const EP = 'event-processing';
const OC = 'outbound-connectors';
const UM = 'user-management';

const created: string[] = [];
const kept: string[] = [];
const problems: string[] = [];

/** Create-if-missing. `exists` reads by token; `create` runs only when it answered false. */
async function ensure(what: string, exists: () => Promise<boolean>, create: () => Promise<unknown>) {
  if (await exists()) {
    kept.push(what);
    return;
  }
  try {
    await create();
    created.push(what);
  } catch (err) {
    problems.push(`${what}: ${err instanceof Error ? err.message : String(err)}`);
  }
}

/** The assertion half: the object must be readable by its token after the seed. */
const assertions: { what: string; check: () => Promise<boolean> }[] = [];
function assertThat(what: string, check: () => Promise<boolean>) {
  assertions.push({ what, check });
}

// ---------------------------------------------------------------------------------------
// Names. The sim creates bp-therm-001..012 with no name; these are the building's real
// places. Order matches the sim's assignment to buildings 1,2,3 round-robin
// (bp-therm-001 is in building 3, 002 in building 2 ...) only by coincidence of reading, so
// the names are not claimed to line up with the area assignments.
// ---------------------------------------------------------------------------------------
const DEVICE_NAMES: [string, string][] = [
  ['bp-therm-001', 'Lobby thermostat'],
  ['bp-therm-002', 'Rooftop AHU 1 return air'],
  ['bp-therm-003', 'Rooftop AHU 2 return air'],
  ['bp-therm-004', 'Conference room 2A'],
  ['bp-therm-005', 'Server room'],
  ['bp-therm-006', 'Open office, east wing'],
  ['bp-therm-007', 'Open office, west wing'],
  ['bp-therm-008', 'Cafeteria'],
  ['bp-therm-009', 'Loading dock'],
  ['bp-therm-010', 'Fitness centre'],
  ['bp-therm-011', 'Stairwell B'],
  ['bp-therm-012', 'Mechanical room'],
];

// Facet values per device: where it sits. `zone` is the facet the saved dynamic group selects on.
const FLOOR: Record<string, number> = {
  'bp-therm-001': 1, 'bp-therm-002': 6, 'bp-therm-003': 6, 'bp-therm-004': 2, 'bp-therm-005': 1, 'bp-therm-006': 3,
  'bp-therm-007': 3, 'bp-therm-008': 1, 'bp-therm-009': 0, 'bp-therm-010': 0, 'bp-therm-011': 2, 'bp-therm-012': 0,
};
const ZONE: Record<string, string> = {
  'bp-therm-001': 'lobby', 'bp-therm-002': 'rooftop', 'bp-therm-003': 'rooftop', 'bp-therm-004': 'offices',
  'bp-therm-005': 'technical', 'bp-therm-006': 'offices', 'bp-therm-007': 'offices', 'bp-therm-008': 'amenities',
  'bp-therm-009': 'technical', 'bp-therm-010': 'amenities', 'bp-therm-011': 'circulation', 'bp-therm-012': 'technical',
};

const PROFILE = 'bp-thermostat-profile';
const DEVICE_A = 'bp-therm-001'; // the device the device-detail shots are about
const DEVICE_B = 'bp-therm-002';
const SPARE = 'bp-therm-boiler'; // a device the seed owns and the simulator never drives: the replacement shot's subject

const RULE_GRAPH = {
  schemaVersion: 1,
  nodes: [
    {
      id: 'source',
      type: 'source',
      config: { scope: { kind: 'profile', profileToken: PROFILE } },
      ui: { x: 60, y: 20 },
    },
    {
      id: 'early-heat',
      type: 'threshold',
      config: {
        name: 'Early heat warning',
        description: 'A space is warming past 29 degrees, a degree before the over-temperature alarm.',
        severity: 'warning',
        when: { metric: 'temperature', op: 'gt', threshold: { kind: 'literal', value: 29 } },
      },
      ui: { x: 60, y: 150 },
    },
    {
      id: 'raise',
      type: 'action',
      config: { action: 'raiseAlarm', alarmKey: 'bp-early-heat' },
      ui: { x: 60, y: 280 },
    },
  ],
  edges: [
    { from: 'source:out', to: 'early-heat:in' },
    { from: 'early-heat:signal', to: 'raise:in' },
  ],
};

async function seedNames() {
  for (const [token, name] of DEVICE_NAMES) {
    const d = await gql(DM, `query($t:[String!]!){devicesByToken(tokens:$t){token name}}`, { t: [token] });
    const cur = d.devicesByToken[0];
    if (!cur) {
      problems.push(`device ${token}: not found; has the simulator populated the tenant?`);
      continue;
    }
    if (cur.name === name) {
      kept.push(`device name ${token}`);
      continue;
    }
    await gql(DM, `mutation($t:String!,$r:DeviceUpdateRequest!){updateDevice(token:$t,request:$r){token}}`, {
      t: token,
      r: { name },
    });
    created.push(`device name ${token}`);
  }
  assertThat('12 thermostats carry their display names', async () => {
    const d = await gql(DM, `query($t:[String!]!){devicesByToken(tokens:$t){token name}}`, {
      t: DEVICE_NAMES.map(([t]) => t),
    });
    return DEVICE_NAMES.every(([t, n]) => d.devicesByToken.some((x: any) => x.token === t && x.name === n));
  });
}

async function seedProfile() {
  const profile = async () =>
    (await gql(DM, `query($t:[String!]!){deviceProfilesByToken(tokens:$t){token activeVersion
      commandDefinitions{commandKey} configurationDeclaration{key}}}`, { t: [PROFILE] })).deviceProfilesByToken[0];

  // A typed command: setpoint with bounds, so the parameter form has something to show.
  await ensure(
    'command definition set-setpoint',
    async () => (await profile())?.commandDefinitions.some((c: any) => c.commandKey === 'set-setpoint'),
    () =>
      gql(DM, `mutation($r:CommandDefinitionCreateRequest!){createCommandDefinition(request:$r){token}}`, {
        r: {
          token: `${PROFILE}-set-setpoint`,
          deviceProfileToken: PROFILE,
          commandKey: 'set-setpoint',
          name: 'Set temperature setpoint',
          description: 'Moves the thermostat to a new target temperature.',
          parameterSchema: JSON.stringify([
            { name: 'setpoint', description: 'Target temperature', dataType: 'DOUBLE', unit: 'Cel', required: true, minValue: 10, maxValue: 30 },
            { name: 'mode', description: 'Operating mode', dataType: 'STRING', enum: ['heat', 'cool', 'auto'] },
          ]),
        },
      }),
  );
  await ensure(
    'command definition identify',
    async () => (await profile())?.commandDefinitions.some((c: any) => c.commandKey === 'identify'),
    () =>
      gql(DM, `mutation($r:CommandDefinitionCreateRequest!){createCommandDefinition(request:$r){token}}`, {
        r: {
          token: `${PROFILE}-identify`,
          deviceProfileToken: PROFILE,
          commandKey: 'identify',
          name: 'Flash the display',
          description: 'Blinks the display so a technician can find the unit.',
          parameterSchema: JSON.stringify([
            { name: 'seconds', dataType: 'INT', unit: 's', minValue: 1, maxValue: 60 },
          ]),
        },
      }),
  );

  // A configuration declaration: what an operator may push to the device.
  await ensure(
    'configuration declaration',
    async () => {
      const have = new Set<string>((await profile())?.configurationDeclaration.map((c: any) => c.key));
      return ['setpoint', 'schedule', 'reportIntervalSeconds'].every((k) => have.has(k));
    },
    () =>
      gql(DM, `mutation($t:String!,$k:[ConfigurationKeyInput!]!){setDeviceProfileConfigurationDeclaration(token:$t,keys:$k){token}}`, {
        t: PROFILE,
        k: [
          { key: 'setpoint', valueType: 'DOUBLE', description: 'Target temperature in degrees Celsius' },
          { key: 'schedule', valueType: 'STRING', description: 'Occupancy schedule name' },
          { key: 'reportIntervalSeconds', valueType: 'LONG', description: 'How often the thermostat reports' },
        ],
      }),
  );

  // Publish so the draft above reaches devices, and so there are two versions to roll back between.
  const versions = async () =>
    (await gql(DM, `query($t:String!){deviceProfileVersions(token:$t){version label}}`, { t: PROFILE }))
      .deviceProfileVersions as { version: number; label: string | null }[];
  await ensure(
    'profile version 2 (commands + configuration)',
    async () => (await versions()).some((v) => v.version >= 2),
    () =>
      gql(DM, `mutation($t:String!,$l:String,$d:String){publishDeviceProfile(token:$t,label:$l,description:$d){version}}`, {
        t: PROFILE,
        l: 'Remote setpoint',
        d: 'Adds the set-setpoint and identify commands and declares the configuration an operator can push.',
      }),
  );
  assertThat('profile has a published version after the sim\'s, and declares set-setpoint, identify and its 3 configuration keys', async () => {
    const p = await profile();
    const keys = new Set<string>(p?.commandDefinitions.map((c: any) => c.commandKey));
    const cfg = new Set<string>(p?.configurationDeclaration.map((c: any) => c.key));
    return (
      (await versions()).length >= 2 &&
      (p?.activeVersion ?? 0) >= 2 &&
      keys.has('set-setpoint') &&
      keys.has('identify') &&
      ['setpoint', 'schedule', 'reportIntervalSeconds'].every((k) => cfg.has(k))
    );
  });
}

async function seedFacetsAndGroup() {
  await ensure(
    'facet key floor',
    async () =>
      (await gql(DM, `query{facetKeys(criteria:{pageNumber:1,pageSize:50,memberType:"device"}){results{key}}}`)).facetKeys.results.some((k: any) => k.key === 'floor'),
    () => gql(DM, `mutation($r:FacetKeySetRequest){setFacetKey(request:$r){key}}`, { r: { memberType: 'device', key: 'floor', valueType: 'LONG', label: 'Floor' } }),
  );
  await ensure(
    'facet key zone',
    async () =>
      (await gql(DM, `query{facetKeys(criteria:{pageNumber:1,pageSize:50,memberType:"device"}){results{key}}}`)).facetKeys.results.some((k: any) => k.key === 'zone'),
    () =>
      gql(DM, `mutation($r:FacetKeySetRequest){setFacetKey(request:$r){key}}`, {
        r: { memberType: 'device', key: 'zone', valueType: 'STRING', values: ['lobby', 'rooftop', 'offices', 'amenities', 'technical', 'circulation'], label: 'Zone' },
      }),
  );

  // The attribute values the facets read. Browse sees only the SHARED scope.
  // Read per device, filtered to the two keys, so no page limit can hide one.
  const facetAttrs = async (token: string) =>
    (await gql(DM, `query($e:String!){entityAttributes(criteria:{pageNumber:1,pageSize:10,entityType:"device",entity:$e,scope:"SHARED",attrKeys:["zone","floor"]}){results{attrKey value}}}`, { e: token }))
      .entityAttributes.results as { attrKey: string; value: string }[];
  for (const token of Object.keys(ZONE)) {
    const have = new Set((await facetAttrs(token)).map((a) => `${token}/${a.attrKey}`));
    for (const [key, valueType, value] of [
      ['zone', 'STRING', ZONE[token]],
      ['floor', 'LONG', String(FLOOR[token])],
    ] as const) {
      if (have.has(`${token}/${key}`)) {
        kept.push(`facet value ${token}/${key}`);
        continue;
      }
      await gql(DM, `mutation($r:EntityAttributeSetRequest!){setEntityAttribute(request:$r){attrKey}}`, {
        r: { entityType: 'device', entity: token, scope: 'SHARED', attrKey: key, valueType, value },
      });
      created.push(`facet value ${token}/${key}`);
    }
  }

  // Two separate ensures: a failure between create and publish converges on the next run.
  await ensure(
    'dynamic group rooftop-units',
    async () => (await gql(DM, `query($t:[String!]!){entityGroupsByToken(tokens:$t){token}}`, { t: ['rooftop-units'] })).entityGroupsByToken.length > 0,
    () =>
      gql(DM, `mutation($r:EntityGroupCreateRequest){createEntityGroup(request:$r){token}}`, {
        r: {
          token: 'rooftop-units',
          memberType: 'device',
          membershipMode: 'dynamic',
          selector: 'attr["zone"] == "rooftop"',
          name: 'Rooftop units',
          description: 'Every thermostat on the roof: the air handlers and their return-air sensors.',
        },
      }),
  );
  const groupVersions = async () =>
    (await gql(DM, `query($t:String!){entityGroupVersions(token:$t){version}}`, { t: 'rooftop-units' })).entityGroupVersions as unknown[];
  await ensure(
    'dynamic group rooftop-units first version',
    async () => (await groupVersions()).length > 0,
    () => gql(DM, `mutation($t:String!,$l:String){publishEntityGroup(token:$t,label:$l){version}}`, { t: 'rooftop-units', l: 'First cut' }),
  );
  assertThat('facet keys, every device\'s zone and floor, and the published dynamic group exist', async () => {
    const k = (await gql(DM, `query{facetKeys(criteria:{pageNumber:1,pageSize:50,memberType:"device"}){results{key}}}`)).facetKeys.results.map((x: any) => x.key);
    const g = await gql(DM, `query($t:[String!]!){entityGroupsByToken(tokens:$t){token}}`, { t: ['rooftop-units'] });
    for (const token of Object.keys(ZONE)) {
      const a = await facetAttrs(token);
      if (a.find((x) => x.attrKey === 'zone')?.value !== ZONE[token] || a.find((x) => x.attrKey === 'floor')?.value !== String(FLOOR[token])) return false;
    }
    return k.includes('floor') && k.includes('zone') && g.entityGroupsByToken.length === 1 && (await groupVersions()).length > 0;
  });
}

async function seedConfigurationHistory() {
  const revs = async (d: string) =>
    (await gql(DM, `query($d:String!){deviceConfigurationRevisions(deviceToken:$d,pagination:{pageNumber:1,pageSize:20}){results{revision}}}`, { d })).deviceConfigurationRevisions.results.length as number;
  // Device A: three revisions, so restore and diff have something to compare.
  const steps: [string, string, string][] = [
    ['setpoint', 'DOUBLE', '21.0'],
    ['schedule', 'STRING', 'weekday-standard'],
    ['setpoint', 'DOUBLE', '22.5'],
  ];
  if ((await revs(DEVICE_A)) < 3) {
    for (const [attrKey, valueType, value] of steps) {
      await gql(DM, `mutation($r:EntityAttributeSetRequest!){setEntityAttribute(request:$r){attrKey}}`, {
        r: { entityType: 'device', entity: DEVICE_A, scope: 'SHARED', attrKey, valueType, value },
      });
    }
    created.push(`configuration history ${DEVICE_A}`);
  } else kept.push(`configuration history ${DEVICE_A}`);
  // Device B: a different configuration, the source for a copy.
  if ((await revs(DEVICE_B)) < 1) {
    for (const [attrKey, valueType, value] of [
      ['setpoint', 'DOUBLE', '19.5'],
      ['reportIntervalSeconds', 'LONG', '300'],
    ] as const) {
      await gql(DM, `mutation($r:EntityAttributeSetRequest!){setEntityAttribute(request:$r){attrKey}}`, {
        r: { entityType: 'device', entity: DEVICE_B, scope: 'SHARED', attrKey, valueType, value },
      });
    }
    created.push(`configuration history ${DEVICE_B}`);
  } else kept.push(`configuration history ${DEVICE_B}`);
  assertThat('configuration revisions exist on the two devices', async () => (await revs(DEVICE_A)) >= 3 && (await revs(DEVICE_B)) >= 1);
}

async function seedCommands() {
  const FAR = '2027-06-30T00:00:00Z';
  const cmds = async (criteria: Vars) =>
    (await gql(CD, `query($c:CommandSearchCriteria!){commands(criteria:$c){results{token status batchToken} pagination{totalRecords}}}`, { c: { pageNumber: 1, pageSize: 20, ...criteria } })).commands;
  await ensure(
    'issued command',
    async () => (await cmds({ deviceToken: DEVICE_A })).results.length > 0,
    async () => {
      const r = await gql(CD, `mutation($r:CommandCreateRequest!){createCommand(request:$r){command{token status} rejection{code reason}}}`, {
        r: { token: 'cmd-lobby-setpoint-1', deviceToken: DEVICE_A, name: 'set-setpoint', payload: JSON.stringify({ setpoint: 22.5, mode: 'auto' }), expiresAt: FAR },
      });
      if (r.createCommand.rejection) throw new Error(`rejected ${r.createCommand.rejection.code}: ${r.createCommand.rejection.reason}`);
    },
  );

  // Three batches over the rooftop units, each created and (where it is called off) cancelled
  // by its own ensure, so a failure between the two converges on the next run.
  const batch = async (token: string) => (await gql(CD, `query($t:[String!]!){commandBatchesByToken(tokens:$t){token accepted cancelledAt}}`, { t: [token] })).commandBatchesByToken[0];
  const rooftop = ['bp-therm-002', 'bp-therm-003'];
  const makeBatch = (token: string, req: Record<string, unknown>) => async () => {
    const r = await gql(CD, `mutation($r:CommandBatchCreateRequest!){createCommandBatch(request:$r){batch{token} rejection{code reason}}}`, { r: { token, deviceTokens: rooftop, ...req } });
    if (r.createCommandBatch.rejection) throw new Error(`rejected ${r.createCommandBatch.rejection.code}: ${r.createCommandBatch.rejection.reason}`);
  };
  const cancel = (token: string) => () => gql(CD, `mutation($t:String!){cancelCommandBatch(token:$t){cancelled matched}}`, { t: token });
  await ensure(
    'command batch (identify, completed)',
    async () => !!(await batch('batch-identify-rooftop')),
    // Short expiry: the units are not connected, so the batch resolves by running out.
    makeBatch('batch-identify-rooftop', { name: 'identify', payload: JSON.stringify({ seconds: 10 }), expiresAt: new Date(Date.now() + 4000).toISOString(), allowPartial: false }),
  );
  await ensure(
    'command batch (set-setpoint)',
    async () => !!(await batch('batch-setpoint-cancelled')),
    makeBatch('batch-setpoint-cancelled', { name: 'set-setpoint', payload: JSON.stringify({ setpoint: 17, mode: 'auto' }), expiresAt: FAR, allowPartial: true }),
  );
  await ensure('command batch (set-setpoint) called off', async () => !!(await batch('batch-setpoint-cancelled'))?.cancelledAt, cancel('batch-setpoint-cancelled'));
  // A batch whose command the profile does not declare: every device is refused, which is the
  // refusals table the batch detail page exists to show. It is then called off.
  await ensure(
    'command batch (night-setback, refused)',
    async () => !!(await batch('batch-night-setback')),
    makeBatch('batch-night-setback', { name: 'night-setback', payload: JSON.stringify({ mode: 'auto', setpoint: 17 }), expiresAt: FAR, allowPartial: true }),
  );
  await ensure('command batch (night-setback) called off', async () => !!(await batch('batch-night-setback'))?.cancelledAt, cancel('batch-night-setback'));
  assertThat('an issued command and three command batches (two called off, one refused)', async () => {
    const a = await batch('batch-identify-rooftop');
    const b = await batch('batch-setpoint-cancelled');
    const c = await batch('batch-night-setback');
    return (await cmds({ deviceToken: DEVICE_A })).results.length > 0 && !!a && !!b?.cancelledAt && !!c?.cancelledAt;
  });
}

async function seedRule() {
  const rule = async () => (await gql(DM, `query($t:[String!]!){detectionRulesByToken(tokens:$t){token authoringGraph}}`, { t: ['bp-early-heat'] })).detectionRulesByToken[0];
  await ensure(
    'canvas-authored detection rule',
    async () => !!(await rule()),
    async () => {
      const graph = JSON.stringify(RULE_GRAPH);
      const c = await gql(EP, `query($g:String!,$p:String!){compileCanvas(graph:$g,profileToken:$p){ok definition diagnostics{severity message}}}`, { g: graph, p: PROFILE });
      if (!c.compileCanvas.ok) throw new Error(`canvas did not compile: ${JSON.stringify(c.compileCanvas.diagnostics)}`);
      await gql(DM, `mutation($r:DetectionRuleCreateRequest!){createDetectionRule(request:$r){token}}`, {
        r: {
          token: 'bp-early-heat',
          deviceProfileToken: PROFILE,
          name: 'Early heat warning',
          description: 'A space is warming past 29 degrees, a degree before the over-temperature alarm.',
          definition: c.compileCanvas.definition,
          authoringGraph: graph,
          enabled: true,
        },
      });
    },
  );
  assertThat('the canvas-authored rule is stored with its graph', async () => !!(await rule())?.authoringGraph);
}

async function seedGeofence() {
  await ensure(
    'geofence',
    async () => (await gql(DM, `query($t:[String!]!){geoFencesByToken(tokens:$t){token}}`, { t: ['acme-campus'] })).geoFencesByToken.length > 0,
    () =>
      gql(DM, `mutation($r:GeoFenceCreateRequest!){createGeoFence(request:$r){token}}`, {
        r: {
          token: 'acme-campus',
          name: 'Acme campus perimeter',
          description: 'The property line of the main campus: anything outside it is off site.',
          geometry: JSON.stringify({
            kind: 'POLYGON_2D',
            geometry: { type: 'Polygon', coordinates: [[[-87.6334, 41.8803], [-87.6281, 41.8803], [-87.6281, 41.8768], [-87.6334, 41.8768], [-87.6334, 41.8803]]] },
          }),
        },
      }),
  );
  assertThat('the geofence exists', async () => (await gql(DM, `query($t:[String!]!){geoFencesByToken(tokens:$t){token}}`, { t: ['acme-campus'] })).geoFencesByToken.length === 1);
}

async function seedConnector() {
  // Only when the outbound-connectors area is deployed; otherwise the connectors chapter
  // cannot be shot at all and the seed says so instead of passing.
  let deployed = true;
  try {
    await gql(OC, `{connectorTypes}`);
  } catch {
    deployed = false;
  }
  if (!deployed) {
    problems.push('outbound-connectors is not deployed: the connectors chapter cannot be seeded or shot (deploy it, see hack/guide-capture/README notes)');
    return;
  }
  const connector = async () => (await gql(OC, `query($t:String!){connector(token:$t){token type}}`, { t: 'facilities-broker' }).catch(() => ({ connector: null }))).connector;
  const versions = async () => (await gql(OC, `query($t:String!){connectorVersions(token:$t){version}}`, { t: 'facilities-broker' })).connectorVersions as unknown[];
  const URLS = ['mqtts://broker.facilities.acme-properties.example:8883'];
  await ensure(
    'connector facilities-broker',
    async () => !!(await connector()),
    () =>
      gql(OC, `mutation($r:ConnectorCreateRequest!){createConnector(request:$r){token}}`, {
        r: {
          token: 'facilities-broker',
          name: 'Facilities event broker',
          description: 'Publishes alarm events to the facilities team broker.',
          type: 'mqtt',
          config: JSON.stringify({ urls: URLS, topic: 'acme/alarms', qos: 1, clientId: 'devicechain' }),
          secret: 'example-broker-password',
        },
      }),
  );
  // Version 1 is the draft as created; version 2 is the draft after the topic moved. Each is its
  // own ensure, so a failure part-way converges: the draft is only edited once version 1 exists.
  await ensure(
    'connector facilities-broker version 1',
    async () => (await versions()).length >= 1,
    () => gql(OC, `mutation($t:String!,$l:String){publishConnector(token:$t,label:$l){version}}`, { t: 'facilities-broker', l: 'Initial' }),
  );
  await ensure(
    'connector facilities-broker version 2',
    async () => (await versions()).length >= 2,
    async () => {
      await gql(OC, `mutation($t:String!,$r:ConnectorUpdateRequest!){updateConnector(token:$t,request:$r){token}}`, {
        t: 'facilities-broker',
        r: { config: JSON.stringify({ urls: URLS, topic: 'acme/building-ops/alarms', qos: 2, clientId: 'devicechain' }) },
      });
      await gql(OC, `mutation($t:String!,$l:String){publishConnector(token:$t,label:$l){version}}`, { t: 'facilities-broker', l: 'Route to building-ops topic' });
    },
  );
  assertThat('the connector exists with two versions', async () => !!(await connector()) && (await versions()).length >= 2);
}

async function seedConnectAreas() {
  // A throwaway CA, generated once for this seed and its private key discarded: the guide only
  // ever shows the certificate, which is public. Trust anchors and integrations live in
  // device-management; an instance that predates them fails the ensure() with the server's own
  // message, which is the right outcome for a capture of a screen that does not exist there.
  const CA_PEM =
    '-----BEGIN CERTIFICATE-----\n' +
    'MIIBzjCCAXSgAwIBAgIUXEhJacONEjite9XnSj3n5UlozV8wCgYIKoZIzj0EAwIw\n' +
    'MzEYMBYGA1UECgwPQWNtZSBQcm9wZXJ0aWVzMRcwFQYDVQQDDA5BY21lIERldmlj\n' +
    'ZSBDQTAeFw0yNjEwMjgxMzE3MTRaFw0zNjEwMjUxMzE3MTRaMDMxGDAWBgNVBAoM\n' +
    'D0FjbWUgUHJvcGVydGllczEXMBUGA1UEAwwOQWNtZSBEZXZpY2UgQ0EwWTATBgcq\n' +
    'hkjOPQIBBggqhkjOPQMBBwNCAARccInN5/feWG7vPFNHkCcXpziQlsUw6IsYSA86\n' +
    'mPbd+u8y5dMeUQfqrHwhdqb/ToaiRW+sqH1ntni31Mrmuccyo2YwZDAdBgNVHQ4E\n' +
    'FgQU8cetIJ+iFCwoxiUfZxbcaA3EE1IwHwYDVR0jBBgwFoAU8cetIJ+iFCwoxiUf\n' +
    'ZxbcaA3EE1IwEgYDVR0TAQH/BAgwBgEB/wIBADAOBgNVHQ8BAf8EBAMCAQYwCgYI\n' +
    'KoZIzj0EAwIDSAAwRQIhAKewFj/8MvnlK3ebgy16b9fuJ8gYCNNWGIpGap6Ju+nd\n' +
    'AiBtOo/BrtGOSF0dIHgrX99EVbumjZ+kDwTCol3ffWuUrQ==\n' +
    '-----END CERTIFICATE-----\n';
  const anchor = async () =>
    (await gql(DM, `query($t:[String!]!){deviceTrustAnchorsByToken(tokens:$t){token}}`, { t: ['acme-device-ca'] })).deviceTrustAnchorsByToken.length === 1;
  await ensure('trust anchor acme-device-ca', anchor, () =>
    gql(DM, `mutation($r:DeviceTrustAnchorCreateRequest!){createDeviceTrustAnchor(request:$r){token}}`, {
      r: { token: 'acme-device-ca', name: 'Acme device CA', description: 'Signs the certificates flashed into Acme thermostats at the factory.', certificatePem: CA_PEM },
    }),
  );
  assertThat('the trust anchor exists', anchor);

  const integration = async () =>
    (await gql(DM, `query($c:IntegrationSearchCriteria!){integrations(criteria:$c){results{token}}}`, { c: { pageNumber: 1, pageSize: 50 } })).integrations.results.some(
      (i: { token: string }) => i.token === 'chirpstack-eu868',
    );
  await ensure('integration chirpstack-eu868', integration, () =>
    gql(DM, `mutation($r:IntegrationCreateRequest!){createIntegration(request:$r){integration{token}}}`, {
      r: {
        token: 'chirpstack-eu868',
        name: 'ChirpStack EU868',
        description: 'The LoRaWAN network server for the Acme Properties sensors.',
        type: 'CHIRPSTACK_V4',
        enabled: true,
        deviceTypeTokens: ['bp-thermostat'],
        linkMetrics: true,
      },
    }),
  );
  assertThat('the integration exists', integration);
}

async function seedAlarmAck() {
  const acked = async () =>
    (await gql(DM, `query{alarms(criteria:{pageNumber:1,pageSize:5,acknowledged:true}){pagination{totalRecords}}}`)).alarms.pagination.totalRecords as number;
  await ensure(
    'acknowledged alarm',
    async () => (await acked()) > 0,
    async () => {
      // The simulator's rule raises and clears alarms as it runs. A CLEARED alarm is finished (a
      // new raise is a new alarm), so its acknowledgement stays put; an ACTIVE one can be re-armed.
      let list: any[] = [];
      for (let i = 0; i < 30 && list.length === 0; i++) {
        list = (await gql(DM, `query{alarms(criteria:{pageNumber:1,pageSize:5,acknowledged:false,state:"CLEARED"}){results{token}}}`)).alarms.results;
        if (list.length === 0) await new Promise((r) => setTimeout(r, 2000));
      }
      if (list.length === 0) throw new Error('no alarm to acknowledge: has the simulator been running long enough?');
      await gql(DM, `mutation($t:String!){acknowledgeAlarm(token:$t){token}}`, { t: list[list.length - 1].token });
    },
  );
  // No assertion on purpose. The engine re-opens an alarm (same token) the next time its
  // device crosses the threshold, which clears the acknowledgement, so this state is
  // perishable. The alarms recipe acknowledges one itself, seconds before it shoots.
}

async function seedAssignment() {
  const has = async (token: string) => (await gql(DM, `query($t:[String!]!){entityRelationshipsByToken(tokens:$t){token}}`, { t: [token] })).entityRelationshipsByToken.length > 0;
  // The lobby thermostat also measures an HVAC unit: assignments are several-to-one.
  await ensure(
    `assignment ${DEVICE_A} -> bp-hvac-01`,
    () => has(`assign-${DEVICE_A}-bp-hvac-01`),
    () =>
      gql(DM, `mutation($r:[EntityRelationshipCreateRequest!]!){createEntityRelationships(requests:$r){token}}`, {
        r: [{ token: `assign-${DEVICE_A}-bp-hvac-01`, sourceType: 'device', source: DEVICE_A, targetType: 'asset', target: 'bp-hvac-01', relationshipType: 'assigned' }],
      }),
  );
  // The sim assigns each device to the customer and to one building; this adds the asset.
  assertThat('the lobby thermostat is assigned to an asset, plus the customer and area the sim gave it', async () => {
    const edges = await gql(DM, `query($d:String){entityRelationships(criteria:{pageNumber:1,pageSize:20,sourceType:"device",source:$d}){pagination{totalRecords}}}`, { d: DEVICE_A });
    return (await has(`assign-${DEVICE_A}-bp-hvac-01`)) && edges.entityRelationships.pagination.totalRecords >= 3;
  });
}

async function seedAssets() {
  const typeOf = async (t: string) =>
    (await gql(DM, `query($t:[String!]!){assetTypesByToken(tokens:$t){token propertySchema activeVersion}}`, { t: [t] })).assetTypesByToken[0];
  // The HVAC unit type declares what an HVAC unit carries; publishing it makes the contract
  // binding. Declare and publish are separate ensures.
  await ensure(
    'asset type bp-hvac property contract',
    async () => !!(await typeOf('bp-hvac'))?.propertySchema,
    () =>
      gql(DM, `mutation($t:String!,$r:AssetTypeUpdateRequest!){updateAssetType(token:$t,request:$r){token}}`, {
        t: 'bp-hvac',
        r: {
          propertySchema: JSON.stringify([
            { name: 'manufacturer', dataType: 'STRING', required: true },
            { name: 'ratedCapacityKw', dataType: 'DOUBLE', unit: 'kW', minValue: 1, maxValue: 500 },
            { name: 'installedYear', dataType: 'INT', minValue: 1990, maxValue: 2100 },
            { name: 'serviceTier', dataType: 'STRING', enum: ['standard', 'critical'] },
          ]),
        },
      }),
  );
  await ensure(
    'asset type bp-hvac published',
    async () => ((await typeOf('bp-hvac'))?.activeVersion ?? 0) >= 1,
    () => gql(DM, `mutation($t:String!,$l:String){publishAssetType(token:$t,label:$l){version}}`, { t: 'bp-hvac', l: 'Nameplate properties' }),
  );
  const asset = async (t: string) =>
    (await gql(DM, `query($t:[String!]!){assetsByToken(tokens:$t){token properties}}`, { t: [t] })).assetsByToken[0];
  await ensure(
    'asset properties on bp-hvac-01',
    async () => !!(await asset('bp-hvac-01'))?.properties,
    () =>
      gql(DM, `mutation($t:String!,$r:AssetUpdateRequest!){updateAsset(token:$t,request:$r){token}}`, {
        t: 'bp-hvac-01',
        r: { properties: JSON.stringify({ manufacturer: 'Carrier', ratedCapacityKw: 120, installedYear: 2019, serviceTier: 'critical' }) },
      }),
  );
  // Two components filed under the first HVAC unit, so the hierarchy tab has a tree to show.
  // Create and file are separate ensures (the parent is read back from the hierarchy).
  const parentOf = async (t: string) =>
    (await gql(DM, `query($t:String!){assetParent(token:$t){token}}`, { t }))?.assetParent?.token as string | undefined;
  for (const [token, name] of [
    ['bp-hvac-01-compressor', 'Compressor'],
    ['bp-hvac-01-supply-fan', 'Supply fan motor'],
  ] as const) {
    await ensure(
      `asset ${token}`,
      async () => !!(await asset(token)),
      () =>
        gql(DM, `mutation($r:AssetCreateRequest){createAsset(request:$r){token}}`, {
          r: { token, name, assetTypeToken: 'bp-hvac', properties: JSON.stringify({ manufacturer: 'Carrier' }) },
        }),
    );
    await ensure(
      `asset ${token} filed under bp-hvac-01`,
      async () => (await parentOf(token)) === 'bp-hvac-01',
      () => gql(DM, `mutation($c:String!,$p:String!){setAssetParent(childToken:$c,parentToken:$p){id}}`, { c: token, p: 'bp-hvac-01' }),
    );
  }
  assertThat('HVAC unit has properties, a published contract, and both components filed under it', async () => {
    return (
      !!(await asset('bp-hvac-01'))?.properties &&
      ((await typeOf('bp-hvac'))?.activeVersion ?? 0) >= 1 &&
      (await parentOf('bp-hvac-01-compressor')) === 'bp-hvac-01' &&
      (await parentOf('bp-hvac-01-supply-fan')) === 'bp-hvac-01'
    );
  });
}

async function seedSpareDevice() {
  await ensure(
    'boiler-room thermostat (replacement subject)',
    async () => (await gql(DM, `query($t:[String!]!){devicesByToken(tokens:$t){token}}`, { t: [SPARE] })).devicesByToken.length > 0,
    () =>
      gql(DM, `mutation($r:DeviceCreateRequest!){createDevice(request:$r){token}}`, {
        r: { token: SPARE, name: 'Boiler room thermostat', externalId: 'AST-00099', description: 'Installed with the boiler retrofit; not part of the building automation loop.', deviceTypeToken: 'bp-thermostat' },
      }),
  );
  assertThat('the boiler-room device exists', async () => (await gql(DM, `query($t:[String!]!){devicesByToken(tokens:$t){token}}`, { t: [SPARE] })).devicesByToken.length === 1);
}

async function seedMqttCredentialAndReplacement() {
  const mq = async () => (await gql(DM, `query($d:String){deviceCredentials(criteria:{pageNumber:1,pageSize:10,device:$d,credentialType:"MQTT_BASIC"}){results{token}}}`, { d: 'bp-therm-003' })).deviceCredentials.results;
  // MQTT_BASIC is the type that carries a password, so it is the one the shown-once banner is
  // shot on (a rotation, repeatable on every run).
  await ensure(
    'MQTT credential on bp-therm-003',
    async () => (await mq()).length > 0,
    () =>
      gql(DM, `mutation($r:DeviceCredentialCreateRequest){createDeviceCredential(request:$r){token}}`, {
        r: { token: 'cred-rooftop-ahu-2-mqtt', deviceToken: 'bp-therm-003', credentialType: 'MQTT_BASIC', credentialId: 'rooftop-ahu-2', credentialValue: 'Rooftop-AHU2-9f3kQ7vL', enabled: true },
      }),
  );
  // One completed replacement on the boiler-room thermostat, so its Replacement tab has history.
  // 🔴 The device must be one the SEED owns and the simulator never drives: a replacement retires
  // the device's credentials, and the simulator never re-enables its own (bp-therm-005 once went
  // offline for good this way).
  const reps = async () => (await gql(DM, `query($d:String){deviceReplacements(criteria:{pageNumber:1,pageSize:5,device:$d}){results{id}}}`, { d: SPARE })).deviceReplacements.results;
  await ensure(
    `credential for ${SPARE}`,
    async () => (await gql(DM, `query($d:String){deviceCredentials(criteria:{pageNumber:1,pageSize:5,device:$d}){results{token}}}`, { d: SPARE })).deviceCredentials.results.length > 0,
    () =>
      gql(DM, `mutation($r:DeviceCredentialCreateRequest){createDeviceCredential(request:$r){token}}`, {
        r: { token: `cred-${SPARE}-1`, deviceToken: SPARE, credentialType: 'ACCESS_TOKEN', credentialId: 'boiler-room-thermostat-key', enabled: true },
      }),
  );
  await ensure(
    `replacement history on ${SPARE}`,
    async () => (await reps()).length > 0,
    () =>
      gql(DM, `mutation($r:DeviceReplaceRequest!){replaceDevice(request:$r){device{token}}}`, {
        r: { deviceToken: SPARE, reason: 'Failed sensor, swapped for the stockroom spare', unitIdentifier: 'SN-4471-B' },
      }),
  );
  assertThat('an MQTT credential and a replacement exist', async () => (await mq()).length > 0 && (await reps()).length > 0);
}

async function seedCredential() {
  const creds = async () => (await gql(DM, `query($d:String){deviceCredentials(criteria:{pageNumber:1,pageSize:10,device:$d}){results{token credentialType}}}`, { d: DEVICE_A })).deviceCredentials.results;
  await ensure(
    `credential for ${DEVICE_A}`,
    async () => (await creds()).length > 0,
    () =>
      gql(DM, `mutation($r:DeviceCredentialCreateRequest){createDeviceCredential(request:$r){token}}`, {
        r: { token: `cred-${DEVICE_A}-1`, deviceToken: DEVICE_A, credentialType: 'ACCESS_TOKEN', credentialId: 'lobby-thermostat-gateway-key', enabled: true },
      }),
  );
  assertThat('a credential exists on the lobby thermostat', async () => (await creds()).length > 0);
}

async function seedBrandingAndProfile() {
  // The guide shoots the console in its DEFAULT look; the branding screen shows a live preview of
  // values its recipe types in without saving. So the seed leaves branding UNSET (and asserts it).
  await gql(UM, `mutation($i:TenantBrandingInput!){setTenantBranding(input:$i){token}}`, {
    i: { title: null, logoMaxHeight: null, primary: null, background: null, foreground: null, accent: null },
  });
  await gql(UM, `mutation($r:ProfileUpdateRequest!){updateProfile(request:$r){email}}`, { r: { firstName: 'Dana', lastName: 'Okafor' } }).catch((e) => {
    problems.push(`profile name: ${e.message}`);
  });
  created.push('tenant branding cleared + operator display name (idempotent set)');
  assertThat('tenant branding is unset', async () => {
    const d = await gql(UM, `{tenant{brandingOverride{title primary accent}}}`);
    const o = d.tenant.brandingOverride;
    return !o.title && !o.primary && !o.accent;
  });
}

// ---------------------------------------------------------------------------------------
// Over-the-air updates (chapter 16). Everything goes through the real API: declare over
// GraphQL, PUT the bytes to the content endpoint (which verifies size and digest), register
// signers, sign over GraphQL, assign to a device.
//
// 🔴 The signing keys are generated here, per run, and the private halves live only in this
// process: nothing writes them anywhere. A re-run that finds an artifact already SIGNED has no
// key to sign with and needs none. One that finds the release signer registered but an artifact
// still unsigned (a run that failed between the two) registers a fresh release key, because the
// old private key is gone by design.
//
// The images are deterministic pseudo-random bytes, so a declared-but-not-uploaded artifact left
// by a failed run is completed by the next one with the same digest.
// ---------------------------------------------------------------------------------------
const UPD = 'update-management';
const RELEASE_SIGNER = 'Acme release signing key';
const OLD_SIGNER = 'Contract manufacturer key (2025)';

interface FirmwareSpec {
  token: string;
  version: string;
  size: number;
  seed: number;
  source: string;
  notes: string;
  sign: boolean;
  retire: boolean;
}

const FIRMWARE: FirmwareSpec[] = [
  {
    token: 'thermostat-fw-2-3-1',
    version: '2.3.1',
    size: 1_802_240,
    seed: 0x231,
    source: 'https://builds.acme-properties.example/thermostat/2.3.1',
    notes: 'Superseded by 2.4.0.',
    sign: true,
    retire: true,
  },
  {
    token: 'thermostat-fw-2-4-0',
    version: '2.4.0',
    size: 1_843_712,
    seed: 0x240,
    source: 'https://builds.acme-properties.example/thermostat/2.4.0',
    notes: 'Release build: faster reconnect after a broker failover, quieter fan control.',
    sign: true,
    retire: false,
  },
  {
    token: 'thermostat-fw-2-5-0-rc1',
    version: '2.5.0-rc.1',
    size: 1_871_360,
    seed: 0x250,
    source: 'https://builds.acme-properties.example/thermostat/2.5.0-rc.1',
    notes: 'Release candidate from the nightly pipeline. Not signed yet.',
    sign: false,
    retire: false,
  },
];
const ASSIGNED = 'thermostat-fw-2-4-0';

/** xorshift32 bytes: the same image for the same seed, every run. */
function firmwareImage(seed: number, size: number): Buffer {
  const out = Buffer.alloc(size);
  let x = seed >>> 0 || 1;
  for (let i = 0; i < size; i++) {
    x ^= x << 13;
    x >>>= 0;
    x ^= x >>> 17;
    x ^= x << 5;
    x >>>= 0;
    out[i] = x & 0xff;
  }
  return out;
}

async function seedUpdates() {
  try {
    await gql(UPD, `{updateManagementInfo{area}}`);
  } catch (err) {
    problems.push(
      `update-management is not deployed or not answering (${err instanceof Error ? err.message : err}): ` +
        'chapter 16 cannot be seeded or shot. Bootstrap with --enable-area update-management',
    );
    return;
  }

  const artifact = async (t: string) =>
    (await gql(UPD, `query($t:String!){artifact(token:$t){token state trustStatus}}`, { t })).artifact as
      | { token: string; state: string; trustStatus: string }
      | null;
  const signers = async () =>
    (await gql(UPD, `{artifactSigners(criteria:{pageNumber:1,pageSize:100}){results{token name state}}}`)).artifactSigners
      .results as { token: string; name: string; state: string }[];

  // The private halves, in memory only, for keys registered by THIS run.
  const keys = new Map<string, KeyObject>();
  const register = async (name: string, type: 'ed25519' | 'ec') => {
    const pair = type === 'ed25519' ? generateKeyPairSync('ed25519') : generateKeyPairSync('ec', { namedCurve: 'P-256' });
    const pem = pair.publicKey.export({ type: 'spki', format: 'pem' }).toString();
    const r = await gql(UPD, `mutation($n:String!,$k:String!){registerArtifactSigner(name:$n,publicKeyPem:$k){token}}`, { n: name, k: pem });
    keys.set(r.registerArtifactSigner.token, pair.privateKey);
    return r.registerArtifactSigner.token as string;
  };

  // A retired signer, so the registry shows both states. It is registered and revoked; it signs nothing.
  await ensure(
    `signer "${OLD_SIGNER}" (revoked)`,
    async () => (await signers()).some((s) => s.name === OLD_SIGNER && s.state === 'REVOKED'),
    async () => {
      const token = (await signers()).find((s) => s.name === OLD_SIGNER)?.token ?? (await register(OLD_SIGNER, 'ec'));
      await gql(UPD, `mutation($t:String!){revokeArtifactSigner(token:$t){state}}`, { t: token });
    },
  );
  await ensure(
    `signer "${RELEASE_SIGNER}"`,
    async () => (await signers()).some((s) => s.name === RELEASE_SIGNER && s.state === 'ACTIVE'),
    () => register(RELEASE_SIGNER, 'ed25519'),
  );

  for (const fw of FIRMWARE) {
    const bytes = firmwareImage(fw.seed, fw.size);
    const digestBytes = createHash('sha256').update(bytes).digest();

    await ensure(
      `artifact ${fw.token} declared`,
      async () => !!(await artifact(fw.token)),
      () =>
        gql(UPD, `mutation($r:ArtifactCreateRequest!){createArtifact(request:$r){token}}`, {
          r: {
            token: fw.token,
            kind: 'FIRMWARE',
            name: 'Thermostat firmware',
            version: fw.version,
            component: 'firmware',
            sizeBytes: String(fw.size),
            digest: `sha256:${digestBytes.toString('hex')}`,
            mediaType: 'application/octet-stream',
            compatibleProfiles: [PROFILE],
            source: fw.source,
            notes: fw.notes,
          },
        }),
    );
    await ensure(
      `artifact ${fw.token} content uploaded`,
      async () => (await artifact(fw.token))?.state !== 'UPLOADING',
      async () => {
        const res = await fetch(`${BASE}/api/${UPD}/updates/artifacts/${fw.token}/content`, {
          method: 'PUT',
          headers: { authorization: `Bearer ${accessToken}`, 'content-type': 'application/octet-stream' },
          body: bytes,
        });
        if (!res.ok) throw new Error(`content upload: HTTP ${res.status} ${(await res.text()).trim()}`);
      },
    );
    if (fw.sign) {
      await ensure(
        `artifact ${fw.token} signed`,
        async () => (await artifact(fw.token))?.trustStatus === 'SIGNED',
        async () => {
          // Only a key registered by this run can sign (see the note above).
          const active = (await signers()).filter((s) => s.name === RELEASE_SIGNER && s.state === 'ACTIVE');
          const signer = active.map((s) => s.token).find((t) => keys.has(t)) ?? (await register(RELEASE_SIGNER, 'ed25519'));
          // Ed25519 over the raw 32 digest bytes, which is what the service verifies.
          const signature = sign(null, digestBytes, keys.get(signer)!).toString('base64');
          await gql(
            UPD,
            `mutation($a:String!,$s:String!,$g:String!){addArtifactSignature(artifactToken:$a,signerToken:$s,signature:$g){trustStatus}}`,
            { a: fw.token, s: signer, g: signature },
          );
        },
      );
    }
    if (fw.retire) {
      await ensure(
        `artifact ${fw.token} retired`,
        async () => (await artifact(fw.token))?.state === 'RETIRED',
        () => gql(UPD, `mutation($t:String!){retireArtifact(token:$t){state}}`, { t: fw.token }),
      );
    }
  }
  keys.clear();

  const current = async () =>
    (
      await gql(
        UPD,
        `query($d:String){updateAssignments(criteria:{pageNumber:1,pageSize:5,deviceToken:$d,currentOnly:true}){results{token artifact{token}}}}`,
        { d: DEVICE_A },
      )
    ).updateAssignments.results as { token: string; artifact: { token: string } }[];
  await ensure(
    `update assignment ${ASSIGNED} -> ${DEVICE_A}`,
    async () => (await current()).some((a) => a.artifact.token === ASSIGNED),
    () => gql(UPD, `mutation($d:String!,$a:String!){assignArtifact(deviceToken:$d,artifactToken:$a){token}}`, { d: DEVICE_A, a: ASSIGNED }),
  );

  assertThat('three firmware artifacts (signed + retired, signed, unverified), both signer states, and an assignment on the lobby thermostat', async () => {
    const [a231, a240, a250] = await Promise.all(FIRMWARE.map((f) => artifact(f.token)));
    const s = await signers();
    return (
      a231?.state === 'RETIRED' && a231.trustStatus === 'SIGNED' &&
      a240?.state === 'AVAILABLE' && a240.trustStatus === 'SIGNED' &&
      a250?.state === 'AVAILABLE' && a250.trustStatus === 'UNVERIFIED' &&
      s.some((x) => x.name === RELEASE_SIGNER && x.state === 'ACTIVE') &&
      s.some((x) => x.name === OLD_SIGNER && x.state === 'REVOKED') &&
      (await current()).some((a) => a.artifact.token === ASSIGNED)
    );
  });
}

// The thermostat type's nameplate: the sim creates the type with no manufacturer or model, so
// the identity tab would show two empty fields (chapter 4).
async function seedDeviceTypeNameplate() {
  const type = async () =>
    (await gql(DM, `query($t:[String!]!){deviceTypesByToken(tokens:$t){token manufacturer model}}`, { t: ['bp-thermostat'] }))
      .deviceTypesByToken[0] as { manufacturer: string | null; model: string | null } | undefined;
  await ensure(
    'device type bp-thermostat nameplate',
    async () => (await type())?.manufacturer === 'Honeywell' && (await type())?.model === 'T10 Pro',
    () =>
      gql(DM, `mutation($t:String!,$r:DeviceTypeUpdateRequest!){updateDeviceType(token:$t,request:$r){token}}`, {
        t: 'bp-thermostat',
        r: { manufacturer: 'Honeywell', model: 'T10 Pro' },
      }),
  );
  assertThat('the thermostat type carries its manufacturer and model', async () => (await type())?.manufacturer === 'Honeywell' && (await type())?.model === 'T10 Pro');
}

// ---------------------------------------------------------------------------------------
// Administering the instance (chapter 15), as the SUPERUSER over the admin API. The password is
// resolved in-process exactly as global-setup does (superuser.ts) and never leaves this process.
//
// A tier, a colour on each tier, two people with memberships in the hero tenant and one
// disabled account, a tenant-scoped role, a Google Workspace connection for the hero tenant,
// and a THROWAWAY tenant that is deleted so the deletion screens have something to show. The
// hero tenant is never touched: its deletion shot stops at the confirmation.
//
// The people's passwords are random per create and never stored: nobody signs in as them.
// ---------------------------------------------------------------------------------------
const ADM = 'user-management/admin';
const HERO_TENANT = `sim-${SIM}`;
const THROWAWAY_TENANT = 'harbor-view-pilot';
const IDP = 'acme-google';
const PEOPLE = [
  { email: 'maria.chen@acme-properties.example', firstName: 'Maria', lastName: 'Chen', roles: ['facilities-viewer'], enabled: true },
  { email: 'jonas.weber@acme-properties.example', firstName: 'Jonas', lastName: 'Weber', roles: ['tenant-admin'], enabled: true },
  { email: 'lee.park@acme-properties.example', firstName: 'Lee', lastName: 'Park', roles: ['viewer'], enabled: false },
];
const TIER_COLORS: [string, string][] = [['bronze', 'orange'], ['silver', 'slate'], ['gold', 'amber'], ['pilot', 'teal']];

async function seedAdmin() {
  const { resolveAdminPassword } = await import('./superuser.ts');
  const admin = await resolveAdminPassword();
  if (admin.password === null) {
    problems.push(`superuser: ${admin.reason}; chapter 15 cannot be seeded`);
    return;
  }
  const su = await signIn(BASE, process.env.DC_ADMIN_EMAIL ?? 'superuser@devicechain.local', admin.password);
  const prev = accessToken;
  accessToken = su.identity.identityToken;
  try {
    const tiers = async () => (await gql(ADM, `{tenantTiers{token color}}`)).tenantTiers as { token: string; color: string | null }[];
    await ensure(
      'tier pilot',
      async () => (await tiers()).some((t) => t.token === 'pilot'),
      () =>
        gql(ADM, `mutation($r:AdminTenantTierCreateRequest!){createTenantTier(request:$r){token}}`, {
          r: {
            token: 'pilot',
            name: 'Pilot',
            description: 'Time-boxed evaluations: a small ingest ceiling and the first to be shed under load.',
            config: JSON.stringify({ ingestReadingsPerSecond: 50, ingestBurst: 100, outboundCallsPerSecond: 5, outboundBurst: 10, shedPriority: 10 }),
          },
        }),
    );
    for (const [token, color] of TIER_COLORS) {
      await ensure(
        `tier ${token} colour ${color}`,
        async () => (await tiers()).find((t) => t.token === token)?.color === color,
        () => gql(ADM, `mutation($t:String!,$r:AdminTenantTierUpdateRequest!){updateTenantTier(token:$t,request:$r){token}}`, { t: token, r: { color } }),
      );
    }

    const roles = async () => (await gql(ADM, `{roles{scope token}}`)).roles as { scope: string; token: string }[];
    await ensure(
      'tenant role facilities-viewer',
      async () => (await roles()).some((r) => r.scope === 'tenant' && r.token === 'facilities-viewer'),
      () =>
        gql(ADM, `mutation($r:AdminRoleCreateRequest!){createRole(request:$r){token}}`, {
          r: {
            scope: 'tenant',
            token: 'facilities-viewer',
            name: 'Facilities viewer',
            description: 'Reads devices, live state, telemetry and alarms, and acknowledges alarms. Changes nothing else.',
            authorities: ['device:read', 'state:read', 'event:read', 'alarm:read', 'alarm:write', 'dashboard:read', 'location:read'],
          },
        }),
    );

    const identities = async () =>
      (await gql(ADM, `{identities{email enabled memberships{tenant}}}`)).identities as { email: string; enabled: boolean; memberships: { tenant: string }[] }[];
    for (const p of PEOPLE) {
      await ensure(
        `identity ${p.email}`,
        async () => (await identities()).some((i) => i.email === p.email),
        () =>
          gql(ADM, `mutation($r:AdminIdentityCreateRequest!){createIdentity(request:$r){email}}`, {
            r: { email: p.email, password: randomBytes(24).toString('base64url'), firstName: p.firstName, lastName: p.lastName, enabled: p.enabled, systemRoles: [] },
          }),
      );
      // 🔴 Set explicitly, not trusted to the create: createIdentity(enabled:false) currently stores
      // an ENABLED identity (the column's gorm `default:true` swallows the zero value).
      await ensure(
        `identity ${p.email} ${p.enabled ? 'enabled' : 'disabled'}`,
        async () => (await identities()).find((i) => i.email === p.email)?.enabled === p.enabled,
        () => gql(ADM, `mutation($e:String!,$v:Boolean!){setIdentityEnabled(email:$e,enabled:$v){email}}`, { e: p.email, v: p.enabled }),
      );
      await ensure(
        `membership ${p.email} in ${HERO_TENANT}`,
        async () => (await identities()).find((i) => i.email === p.email)?.memberships.some((m) => m.tenant === HERO_TENANT) ?? false,
        () => gql(ADM, `mutation($e:String!,$t:String!,$r:[String!]!){addMembership(email:$e,tenant:$t,roleTokens:$r){email}}`, { e: p.email, t: HERO_TENANT, r: p.roles }),
      );
    }

    // A tenant connection to Google Workspace. Creating one fetches the provider's discovery
    // document, so this needs the cluster to reach accounts.google.com; Google connections must
    // be pinned to the organisation with an `hd` requirement.
    const idp = async () =>
      (await gql(ADM, `{idpConnections{token tenants domains{domain} discovery{fingerprint approved}}}`)).idpConnections.find((c: any) => c.token === IDP) as
        | { tenants: string[]; domains: { domain: string }[]; discovery: { fingerprint: string; approved: boolean } }
        | undefined;
    await ensure(
      `identity provider ${IDP}`,
      async () => !!(await idp()),
      () =>
        gql(ADM, `mutation($r:IdpConnectionCreateRequest!){createIdpConnection(request:$r){token}}`, {
          r: {
            token: IDP,
            name: 'Acme Google Workspace',
            kind: 'tenant',
            issuer: 'https://accounts.google.com',
            clientId: '482910375512-acmeprops.apps.googleusercontent.com',
            requiredClaims: [{ name: 'hd', value: 'acme-properties.example' }],
          },
        }),
    );
    await ensure(
      `identity provider ${IDP} discovery approved`,
      async () => !!(await idp())?.discovery.approved,
      async () => gql(ADM, `mutation($t:String!,$f:String!){approveIdpDiscovery(token:$t,fingerprint:$f){token}}`, { t: IDP, f: (await idp())!.discovery.fingerprint }),
    );
    await ensure(
      `identity provider ${IDP} serves ${HERO_TENANT}`,
      async () => !!(await idp())?.tenants.includes(HERO_TENANT),
      () => gql(ADM, `mutation($t:String!,$n:[String!]!){setIdpConnectionTenants(token:$t,tenants:$n){token}}`, { t: IDP, n: [HERO_TENANT] }),
    );
    await ensure(
      `identity provider ${IDP} domain`,
      async () => !!(await idp())?.domains.some((d) => d.domain === 'acme-properties.example'),
      () => gql(ADM, `mutation($t:String!,$d:String!){addIdpDomain(token:$t,domain:$d){token}}`, { t: IDP, d: 'acme-properties.example' }),
    );

    // The throwaway tenant: created at the pilot tier, then deleted. Its deletion record
    // outlives it (/admin/deletions), so "already deleted" is read from either place.
    const tenants = async () => (await gql(ADM, `{tenants{token purgeState}}`)).tenants as { token: string; purgeState: string }[];
    const deletions = async () =>
      (await gql(ADM, `query($c:TenantDeletionSearchCriteria!){tenantDeletions(criteria:$c){results{token}}}`, { c: { pageNumber: 1, pageSize: 50 } }))
        .tenantDeletions.results as { token: string }[];
    const deleted = async () =>
      (await tenants()).some((t) => t.token === THROWAWAY_TENANT && t.purgeState !== 'active') || (await deletions()).some((d) => d.token === THROWAWAY_TENANT);
    await ensure(
      `throwaway tenant ${THROWAWAY_TENANT}`,
      async () => (await tenants()).some((t) => t.token === THROWAWAY_TENANT) || (await deleted()),
      () =>
        gql(ADM, `mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}`, {
          r: { token: THROWAWAY_TENANT, name: 'Harbor View pilot', tierToken: 'pilot' },
        }),
    );
    await ensure(`throwaway tenant ${THROWAWAY_TENANT} deleted`, deleted, () =>
      gql(ADM, `mutation($t:String!){deleteTenant(token:$t)}`, { t: THROWAWAY_TENANT }),
    );

    assertThat('chapter 15: the pilot tier, the role, three people, the approved Google connection and the deleted throwaway tenant', async () => {
      const prevA = accessToken;
      accessToken = su.identity.identityToken;
      try {
        const i = await identities();
        const c = await idp();
        return (
          (await tiers()).some((t) => t.token === 'pilot') &&
          (await roles()).some((r) => r.token === 'facilities-viewer') &&
          PEOPLE.every((p) => i.some((x) => x.email === p.email && x.enabled === p.enabled && x.memberships.some((m) => m.tenant === HERO_TENANT))) &&
          !!c?.discovery.approved && c.tenants.includes(HERO_TENANT) &&
          (await deleted()) &&
          (await tenants()).some((t) => t.token === HERO_TENANT && t.purgeState === 'active')
        );
      } finally {
        accessToken = prevA;
      }
    });
  } finally {
    accessToken = prev;
  }
}

// The widgetlab tenant (dashboards chapter) is authored by its simulator; the only thing the
// guide adds is a human name on its operator, so the sidebar footer is not an e-mail address.
async function seedWidgetsOperator() {
  const name = process.env.GUIDE_WIDGETS_SIM ?? 'widgets';
  const file = join(homedir(), '.devicechain', 'sims', `${name}.json`);
  if (!existsSync(file)) {
    kept.push(`widgets operator name (no ${name} sim, skipped)`);
    return;
  }
  const w = readSimHandshake(name);
  const s = await signIn(BASE, w.email, w.password, w.tenant);
  const prev = accessToken;
  accessToken = s.auth!.accessToken;
  try {
    await gql(UM, `mutation($r:ProfileUpdateRequest!){updateProfile(request:$r){email}}`, { r: { firstName: 'Sam', lastName: 'Rivera' } });
    created.push('widgets operator display name (idempotent set)');
  } finally {
    accessToken = prev;
  }
}

async function main() {
  const sim = readSimHandshake(SIM);
  const s = await signIn(BASE, sim.email, sim.password, sim.tenant);
  accessToken = s.auth!.accessToken;

  // Fail fast if the simulator has not populated the tenant.
  const n = (await gql(DM, `{devices(criteria:{pageNumber:1,pageSize:1}){pagination{totalRecords}}}`)).devices.pagination.totalRecords;
  if (n < 12) throw new Error(`the hero tenant has ${n} devices; start the simulator first (hack/guide-capture/bringup.sh up)`);

  const steps: [string, () => Promise<void>][] = [
    ['names', seedNames],
    ['profile', seedProfile],
    ['facets and group', seedFacetsAndGroup],
    ['configuration history', seedConfigurationHistory],
    ['commands', seedCommands],
    ['rule', seedRule],
    ['geofence', seedGeofence],
    ['connector', seedConnector],
    ['trust anchor and integration', seedConnectAreas],
    ['alarm acknowledgement', seedAlarmAck],
    ['assignment', seedAssignment],
    ['assets', seedAssets],
    ['spare device', seedSpareDevice],
    ['credential', seedCredential],
    ['mqtt credential + replacement', seedMqttCredentialAndReplacement],
    ['branding', seedBrandingAndProfile],
    ['over-the-air updates', seedUpdates],
    ['widgets operator', seedWidgetsOperator],
    ['device type nameplate', seedDeviceTypeNameplate],
    ['instance administration (superuser)', seedAdmin],
  ];
  for (const [name, fn] of steps) {
    try {
      await fn();
    } catch (err) {
      problems.push(`${name}: ${err instanceof Error ? err.message : String(err)}`);
    }
  }

  for (const a of assertions) {
    let ok = false;
    try {
      ok = await a.check();
    } catch (err) {
      problems.push(`assert "${a.what}": ${err instanceof Error ? err.message : String(err)}`);
      continue;
    }
    if (!ok) problems.push(`assert "${a.what}": FAILED`);
  }

  console.log(`seed-guide: created ${created.length}, already present ${kept.length}, ${assertions.length} assertions`);
  for (const c of created) console.log(`  + ${c}`);
  if (problems.length) {
    console.error(`seed-guide: ${problems.length} problem(s):`);
    for (const p of problems) console.error(`  - ${p}`);
    process.exit(1);
  }
  console.log('seed-guide: every assertion holds');
}

main().catch((err) => {
  console.error(`seed-guide: ${err instanceof Error ? err.message : err}`);
  process.exit(1);
});
