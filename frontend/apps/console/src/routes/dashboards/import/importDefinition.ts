// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The import / clone / start-from-a-template pipeline, as pure functions.
//
// ONE PIPELINE FOR THREE DOORS. Pasting or uploading an exported file, cloning a dashboard
// and starting from a shipped template all end up here as a JSON TEXT, and all go through
// the same steps the editor uses to open a stored board: the same parser (with the
// deployment's widget catalog, so a widget this console cannot resolve becomes a
// placeholder and publishing stays blocked while one remains), the same slot migration,
// and the same option validation the publish gate runs. Nothing here writes: the caller
// creates a NEW dashboard, as a draft, under a token the user chose.
//
// 🔴 BINDINGS ARE THE ONE THING AN IMPORT CARRIES THAT ONLY MEANS SOMETHING IN ITS OWN
// TENANT. A device, customer, area or asset token inside a slot's default binding names an
// entity of the tenant that exported it. In another tenant it names nothing, and the board
// would save, open and render empty with no sign of why. So the pipeline LISTS every slot
// binding (collectBindingRefs), asks the tenant which of them exist, and lets the caller
// rebind or clear the ones that do not (applyBindingChoices) before anything is saved.
// A token that does exist is kept untouched.

import {
  migrateToSlots,
  parseDashboardDefinition,
  type DashboardDefinitionError,
  serializeDefinition,
  setTitle,
  unresolvedWidgets,
  type DashboardDefinition,
  type SlotBinding,
  type UnresolvedWidgetReport,
  type WidgetCatalog,
} from '@devicechain/dashboards';
import { validateDefinitionOptions, type DefinitionOptionIssue } from '@devicechain/widgets';

// The server refuses a stored definition above this many bytes (dashboard-management
// maxDefinitionBytes, 1 MiB). The limit is on the COMPACT form, which is what a save writes
// and what an import sends; checking it here as well refuses an oversized board with a
// message that names the limit instead of a bare server error. The server remains the
// authority.
export const MAX_DEFINITION_BYTES = 1 << 20;

// The bound on the TEXT pasted or uploaded, checked before it is read into the page or
// parsed. It is a multiple of the definition limit, not the limit itself, because an export
// is pretty-printed: indentation can make the file of a board well inside 1 MiB larger than
// 1 MiB, and that file must still import. Whatever the text's size, the definition it
// parses to must fit MAX_DEFINITION_BYTES compact.
export const MAX_IMPORT_TEXT_BYTES = 4 * MAX_DEFINITION_BYTES;

// The deepest nesting of objects and arrays an import accepts. A real definition nests a
// handful of levels (an option holding a list of threshold objects is about eight); the
// bound exists because the compact-size check serializes the parsed board, and serializing
// is far slower than linear in nesting depth -- text nested millions of levels deep, which
// the text cap allows, would hold the page for seconds before the size check could refuse
// it. Checked on the text, before it is parsed, in one linear pass.
export const MAX_IMPORT_NESTING = 128;

export type ImportErrorCode = 'empty' | 'tooLarge' | 'notJson' | 'invalid' | 'tooNew';

export class DashboardImportError extends Error {
  constructor(
    readonly code: ImportErrorCode,
    message: string,
    // For 'tooLarge': the limit, in bytes, that was exceeded.
    readonly limitBytes?: number,
  ) {
    super(message);
    this.name = 'DashboardImportError';
  }
}

function tooLarge(limitBytes: number, what: string): DashboardImportError {
  return new DashboardImportError(
    'tooLarge',
    `The ${what} is larger than the ${limitBytes / (1 << 20)} MiB limit.`,
    limitBytes,
  );
}

export function byteLength(text: string): number {
  return new TextEncoder().encode(text).length;
}

// nestsDeeperThan reports whether JSON text opens more than `max` objects/arrays inside one
// another. Brackets inside string literals do not count. It does not validate the JSON --
// JSON.parse does that next -- so malformed text only has to be scanned, not understood.
export function nestsDeeperThan(text: string, max: number): boolean {
  let depth = 0;
  let inString = false;
  for (let i = 0; i < text.length; i++) {
    const c = text.charCodeAt(i);
    if (inString) {
      if (c === 0x5c /* \ */) i++;
      else if (c === 0x22 /* " */) inString = false;
    } else if (c === 0x22) {
      inString = true;
    } else if (c === 0x7b /* { */ || c === 0x5b /* [ */) {
      if (++depth > max) return true;
    } else if (c === 0x7d /* } */ || c === 0x5d /* ] */) {
      depth--;
    }
  }
  return false;
}

// The entity kinds a slot binding can name.
export type BindingKind = 'device' | 'customer' | 'area' | 'asset';

const BINDING_KINDS: ReadonlySet<string> = new Set(['device', 'customer', 'area', 'asset']);

// One slot's entity reference, as the review step shows it.
export interface BindingRef {
  slot: string;
  label: string;
  // What kind of entity the picker offers. For an anchor binding this is its targetType.
  kind: BindingKind;
  // The token the definition names, or null when the slot carries no default binding (a
  // template, or an export-as-template). A null token is never "missing": there is nothing
  // foreign to repair, only an optional binding to make.
  token: string | null;
  // False when the kind is not one this console can look up (a hand-edited targetType).
  supported: boolean;
}

export interface BindingRefStatus extends BindingRef {
  // Does the token exist in THIS tenant? Always true for a null token.
  exists: boolean;
}

export interface PreparedImport {
  definition: DashboardDefinition;
  // Placeholders: widgets this console cannot resolve. They survive into the draft with
  // their stored JSON intact, and publishing stays blocked while any remains.
  unresolved: UnresolvedWidgetReport[];
  // Options the renderer does not read or a widget requires. Informational for a draft.
  optionIssues: DefinitionOptionIssue[];
  // Widgets still carrying a concrete (non-slot) entity selector after slot migration; they
  // are not rebindable here and may name entities this tenant does not have.
  concreteSelectors: number;
  // Origins (scheme://host) of every absolute URL the definition carries: an image widget's
  // picture, a map's tile server, the canvas background. The browser fetches these as soon
  // as the board is opened, so an imported file decides which hosts see the viewer's
  // requests. Informational, like optionIssues: the user sees them before anything is saved.
  externalOrigins: string[];
}

// Values nested deeper than this are not searched for URLs. Every URL-bearing option a
// widget defines sits one or two levels down; the cap keeps a pathologically nested (but
// size-bounded) options object from costing anything.
const MAX_URL_SEARCH_DEPTH = 8;

// externalOrigins lists, sorted and once each, the origin of every string in the canvas
// background or any widget (a placeholder's stored object included) that is an absolute or
// protocol-relative http(s) URL.
export function externalOrigins(definition: DashboardDefinition): string[] {
  const found = new Set<string>();
  const visit = (value: unknown, depth: number) => {
    if (depth > MAX_URL_SEARCH_DEPTH) return;
    if (typeof value === 'string') {
      const origin = originOf(value);
      if (origin) found.add(origin);
    } else if (Array.isArray(value)) {
      for (const item of value) visit(item, depth + 1);
    } else if (value !== null && typeof value === 'object') {
      for (const item of Object.values(value)) visit(item, depth + 1);
    }
  };
  visit(definition.canvas?.background, 0);
  for (const widget of definition.widgets) visit(widget, 0);
  return [...found].sort();
}

function originOf(text: string): string | null {
  const value = text.trim();
  if (!/^(https?:)?\/\//i.test(value)) return null;
  try {
    // A protocol-relative URL takes the page's scheme; https stands in for it here.
    return new URL(value, 'https://origin.invalid').origin;
  } catch {
    return null;
  }
}

// prepareImport parses `text` exactly as the editor parses a stored board. It throws a
// DashboardImportError for anything that must not become a dashboard: empty, oversized, not
// JSON, or not a definition the parser accepts.
//
// Two size checks, in this order: the text against MAX_IMPORT_TEXT_BYTES before anything
// reads it, then the parsed definition, compact, against MAX_DEFINITION_BYTES -- the limit
// the server applies to what is saved.
export function prepareImport(text: string, catalog: WidgetCatalog | undefined): PreparedImport {
  if (text.trim() === '') throw new DashboardImportError('empty', 'Nothing to import.');
  if (byteLength(text) > MAX_IMPORT_TEXT_BYTES) throw tooLarge(MAX_IMPORT_TEXT_BYTES, 'text');
  if (nestsDeeperThan(text, MAX_IMPORT_NESTING)) {
    throw new DashboardImportError('invalid', `Nested more than ${MAX_IMPORT_NESTING} levels deep.`);
  }
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch (err) {
    throw new DashboardImportError('notJson', err instanceof Error ? err.message : 'Not valid JSON.');
  }
  let definition: DashboardDefinition;
  try {
    definition = migrateToSlots(parseDashboardDefinition(raw, { catalog }));
  } catch (err) {
    // The message is the drawer's to word (and translate); this carries only the class.
    if ((err as DashboardDefinitionError | null)?.code === 'schemaTooNew') throw new DashboardImportError('tooNew', (err as Error).message);
    throw new DashboardImportError('invalid', err instanceof Error ? err.message : 'Not a dashboard definition.');
  }
  if (byteLength(serializeDefinition(definition)) > MAX_DEFINITION_BYTES) {
    throw tooLarge(MAX_DEFINITION_BYTES, 'definition');
  }
  return {
    definition,
    unresolved: unresolvedWidgets(definition),
    optionIssues: validateDefinitionOptions(definition, { catalog }),
    concreteSelectors: definition.widgets.filter(
      (w) => 'datasource' in w && w.datasource !== undefined && w.datasource.kind !== 'slot',
    ).length,
    externalOrigins: externalOrigins(definition),
  };
}

// collectBindingRefs lists every slot that names, or could name, an entity.
//
// A device slot with no default binding is included (token null) so the review can offer to
// bind it; an anchor slot with no default is not, because binding one needs a relationship
// and a target type the review does not ask for, and the editor's slot panel does.
export function collectBindingRefs(definition: DashboardDefinition): BindingRef[] {
  const refs: BindingRef[] = [];
  for (const [slot, def] of Object.entries(definition.slots ?? {})) {
    const label = def.label || slot;
    const binding = def.defaultBinding;
    if (!binding) {
      if (def.type === 'device') refs.push({ slot, label, kind: 'device', token: null, supported: true });
      continue;
    }
    if (binding.kind === 'device') {
      refs.push({ slot, label, kind: 'device', token: binding.deviceToken, supported: true });
    } else {
      const kind = binding.anchor.targetType;
      const supported = BINDING_KINDS.has(kind);
      refs.push({ slot, label, kind: supported ? kind : 'device', token: binding.anchor.targetToken, supported });
    }
  }
  return refs;
}

// Looks up whether one entity exists in the caller's tenant. A rejection is a failed
// lookup, not "missing" -- see checkBindingRefs.
export type EntityExists = (kind: BindingKind, token: string) => Promise<boolean>;

// checkBindingRefs asks the tenant about every named token once (several slots may share
// one). A lookup that FAILS rejects the whole check: reading a network error as "does not
// exist" would present a healthy board as foreign, and reading it as "exists" would let a
// foreign one through. The caller shows the failure and the user retries.
export async function checkBindingRefs(refs: BindingRef[], exists: EntityExists): Promise<BindingRefStatus[]> {
  const answers = new Map<string, Promise<boolean>>();
  const ask = (kind: BindingKind, token: string) => {
    const key = `${kind}\u0000${token}`;
    let answer = answers.get(key);
    if (!answer) {
      answer = exists(kind, token);
      answers.set(key, answer);
    }
    return answer;
  };
  return Promise.all(
    refs.map(async (ref) => ({
      ...ref,
      exists: ref.token === null ? true : ref.supported ? await ask(ref.kind, ref.token) : false,
    })),
  );
}

// What to do with a binding slot: pick a token of this tenant ('' or undefined = leave the
// slot unbound).
export type BindingChoices = Record<string, string | undefined>;

// applyBindingChoices repairs the slots the review flagged. A slot whose token exists is
// not touched whatever the choices say. For a missing one, a chosen token replaces the
// entity (an anchor keeps its relationship and target type) and no choice drops the default
// binding, leaving the slot unbound. An unbound device slot takes a chosen token as a new
// default binding. Pure: returns a new definition.
export function applyBindingChoices(
  definition: DashboardDefinition,
  statuses: BindingRefStatus[],
  choices: BindingChoices,
): DashboardDefinition {
  if (!definition.slots) return definition;
  const slots = { ...definition.slots };
  for (const status of statuses) {
    if (status.exists && status.token !== null) continue;
    const current = slots[status.slot];
    if (!current) continue;
    const chosen = choices[status.slot]?.trim() || undefined;
    const { defaultBinding: previous, ...rest } = current;
    let next: SlotBinding | undefined;
    if (chosen) {
      next =
        previous?.kind === 'anchor'
          ? { kind: 'anchor', anchor: { ...previous.anchor, targetToken: chosen } }
          : { kind: 'device', deviceToken: chosen };
    }
    slots[status.slot] = next ? { ...rest, defaultBinding: next } : rest;
  }
  return { ...definition, slots };
}

// buildImportPayload is the last step before the create call: the board gets the title the
// user chose and is serialized the way a save serializes it.
export function buildImportPayload(definition: DashboardDefinition, title: string): string {
  const titled = title.trim() === '' ? definition : setTitle(definition, title.trim());
  const serialized = serializeDefinition(titled);
  // The repaired document can differ in size from the text that was checked.
  if (byteLength(serialized) > MAX_DEFINITION_BYTES) {
    throw tooLarge(MAX_DEFINITION_BYTES, 'definition');
  }
  return serialized;
}
