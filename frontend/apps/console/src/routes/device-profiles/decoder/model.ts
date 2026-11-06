// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Decoder tab's editing model, and the one-way compiler from its field table to CEL.
//
// THE DIRECTION IS ONE WAY, ON PURPOSE. A table row compiles to an expression; nothing parses
// an expression back into a row. The structured form of every table row is stored beside the
// compiled document (`table`) and is the source of truth for those rows. On load a stored row
// is believed ONLY if recompiling it reproduces the stored expression exactly; a document
// edited elsewhere (the API, a script) therefore never shows a table row that disagrees with
// what will actually run: that row falls back to a raw-CEL row showing the expression as stored.
// A row converted to raw CEL leaves the table for good.

import {
  andJoin,
  celDouble,
  celInt,
  celString,
  isIdentifier,
  isSelectable,
  parseFinite,
  wrappable,
} from './cel';

// Limits the platform enforces. The server is the authority; these catch the obvious mistake
// before the round trip, and bound the form.
export const LIMITS = {
  frameBytes: 4096,
  document: 32 << 10,
  expression: 2048,
  lets: 16,
  sections: 8,
  vectors: 20,
  readings: 64,
  name: 64,
  key: 128,
} as const;
export const RESERVED_KEY_PREFIX = 'lorawan.';
export const TABLE_VERSION = 1;

export type InputKind = 'FRAME' | 'JSON' | 'DECODED';
export const INPUT_KINDS: readonly InputKind[] = ['FRAME', 'JSON', 'DECODED'];

export const FRAME_TYPES = ['u8', 'i8', 'u16', 'i16', 'u24', 'i24', 'u32', 'i32', 'f32', 'f64', 'bit', 'bits', 'sbits', 'bcd'] as const;
export type FrameType = (typeof FRAME_TYPES)[number];
export type Endian = 'be' | 'le';

/** Types whose function name takes a `be` / `le` suffix. */
const MULTI_BYTE = new Set<FrameType>(['u16', 'i16', 'u24', 'i24', 'u32', 'i32', 'f32', 'f64']);
export const hasEndianness = (t: FrameType) => MULTI_BYTE.has(t);

export type IssueCode =
  | 'keyRequired' | 'keyReserved' | 'keyTooLong' | 'keyDuplicate'
  | 'offsetInvalid' | 'bitIndexInvalid' | 'widthInvalid' | 'lengthInvalid'
  | 'numberInvalid' | 'divideByZero' | 'pathRequired' | 'pathSegmentEmpty' | 'absentNeedsPlainPath'
  | 'unbalanced' | 'exprRequired' | 'exprTooLong' | 'sourceMismatch' | 'maxKeysInvalid'
  | 'nameRequired' | 'nameTooLong' | 'nameDuplicate' | 'nameInvalid' | 'portInvalid'
  | 'vectorJsonInvalid' | 'vectorHexInvalid' | 'vectorBase64Invalid' | 'vectorExpectInvalid' | 'vectorPortInvalid'
  | 'tooManyLets' | 'tooManySections' | 'tooManyVectors' | 'tooManyReadings' | 'documentTooLarge' | 'noSections';

/** A problem with the form, located by the document path it will have. `code` is an i18n key suffix. */
export interface Issue {
  path: string;
  code: IssueCode;
  params?: Record<string, string | number>;
}

// ---------------------------------------------------------------------------------------------
// State

/** One field-table row. Numeric inputs are kept as the TEXT typed, so a half-typed value is not lost. */
export interface Field {
  source: 'frame' | 'json';
  key: string;
  // frame source
  type: FrameType;
  endian: Endian;
  offset: string;
  /** bit: the bit index; bits and sbits: the bit offset. */
  bitIndex: string;
  bitWidth: string;
  /** bcd: how many bytes. */
  bcdLength: string;
  // json source: a dotted path under `json` / `decoded`
  path: string;
  jsonType: 'number' | 'boolean';
  /** Skip the row, silently, when the path is not in the message. */
  skipIfAbsent: boolean;
  // shaping: value * mul / div + add; blank = none
  mul: string;
  div: string;
  add: string;
  /** A raw-CEL guard on this row. */
  when: string;
}

export type Row =
  | { id: string; kind: 'field'; field: Field }
  | { id: string; kind: 'cel'; key: string; expr: string; when: string }
  | { id: string; kind: 'map'; map: string; prefix: string; maxKeys: string; when: string };

export interface SectionState {
  id: string;
  name: string;
  /** The fPort shortcut: blank, or the port number the section matches. */
  fPort: string;
  /** A raw-CEL condition, combined with the shortcut when both are set. */
  when: string;
  ignore: boolean;
  rows: Row[];
}

export interface LetState {
  id: string;
  name: string;
  expr: string;
}

export type VectorInputKind = 'frameHex' | 'frameBase64' | 'json' | 'decoded';
export const VECTOR_INPUT_KINDS: readonly VectorInputKind[] = ['frameHex', 'frameBase64', 'json', 'decoded'];

export interface VectorState {
  id: string;
  name: string;
  kind: VectorInputKind;
  text: string;
  fPort: string;
  fCnt: string;
  expect: { id: string; key: string; value: string }[];
  /** Input members this form does not edit (the link metrics), kept so a round trip loses nothing. */
  inputExtra: Record<string, unknown>;
}

export interface EditorState {
  envVersion: number;
  input: InputKind;
  lets: LetState[];
  sections: SectionState[];
  vectors: VectorState[];
  /** Document members this form does not know, kept verbatim. */
  extra: Record<string, unknown>;
}

let seq = 0;
export const newId = (): string => `r${++seq}`;

export function blankField(source: 'frame' | 'json' = 'frame'): Field {
  return {
    source, key: '', type: 'u8', endian: 'be', offset: '0', bitIndex: '0', bitWidth: '1', bcdLength: '1',
    path: '', jsonType: 'number', skipIfAbsent: false, mul: '', div: '', add: '', when: '',
  };
}
export const blankSection = (name = 'telemetry'): SectionState => ({
  id: newId(), name, fPort: '', when: '', ignore: false, rows: [],
});
export function blankVector(kind: VectorInputKind = 'frameHex'): VectorState {
  return { id: newId(), name: '', kind, text: '', fPort: '', fCnt: '', expect: [], inputExtra: {} };
}
/** A new row of the given kind. `source` is the field table's read source for the decoder input. */
export function newRow(kind: Row['kind'], source: 'frame' | 'json'): Row {
  if (kind === 'field') return { id: newId(), kind, field: blankField(source) };
  if (kind === 'cel') return { id: newId(), kind, key: '', expr: '', when: '' };
  return { id: newId(), kind, map: '', prefix: '', maxKeys: '16', when: '' };
}

/** The vector input kind a new vector starts with for a decoder input. */
export const defaultVectorKind = (input: InputKind): VectorInputKind =>
  input === 'FRAME' ? 'frameHex' : input === 'DECODED' ? 'decoded' : 'json';

export const emptyState = (input: InputKind = 'FRAME'): EditorState => ({
  envVersion: 1, input, lets: [], sections: [blankSection()], vectors: [], extra: {},
});

// ---------------------------------------------------------------------------------------------
// Field table -> CEL (one way)

export interface Output {
  key?: string;
  expr?: string;
  when?: string;
  map?: string;
  prefix?: string;
  maxKeys?: number;
}

const nonNegInt = (s: string, max: number): number | null => {
  if (!/^\d+$/.test(s.trim())) return null;
  const n = Number(s.trim());
  return Number.isSafeInteger(n) && n <= max ? n : null;
};

const sourceVar = (input: InputKind): 'json' | 'decoded' | null =>
  input === 'JSON' ? 'json' : input === 'DECODED' ? 'decoded' : null;

function keyIssues(key: string, path: string): Issue[] {
  if (key.trim() === '') return [{ path, code: 'keyRequired' }];
  if (key.startsWith(RESERVED_KEY_PREFIX)) return [{ path, code: 'keyReserved' }];
  if (new TextEncoder().encode(key).length > LIMITS.key) return [{ path, code: 'keyTooLong', params: { max: LIMITS.key } }];
  return [];
}

/** The CEL that reads one field's raw value, before shaping. Only call after the field validated. */
function readExpr(f: Field, input: InputKind): string {
  if (f.source === 'json') {
    const root = sourceVar(input) as string;
    const chain = f.path.split('.').map((seg) => `[${celString(seg)}]`).join('');
    return `${f.jsonType === 'boolean' ? 'bool' : 'double'}(${root}${chain})`;
  }
  const off = celInt(Number(f.offset));
  switch (f.type) {
    case 'bit': return `bit(frame, ${off}, ${celInt(Number(f.bitIndex))})`;
    case 'bits': return `bits(frame, ${off}, ${celInt(Number(f.bitIndex))}, ${celInt(Number(f.bitWidth))})`;
    case 'sbits': return `sbits(frame, ${off}, ${celInt(Number(f.bitIndex))}, ${celInt(Number(f.bitWidth))})`;
    case 'bcd': return `bcd(frame, ${off}, ${celInt(Number(f.bcdLength))})`;
    default: return `${f.type}${hasEndianness(f.type) ? f.endian : ''}(frame, ${off})`;
  }
}

/** `has(json.a) && has(json.a.b)`: each step must exist before the next is read. */
function absentGuard(f: Field, input: InputKind): string {
  const root = sourceVar(input) as string;
  const segs = f.path.split('.');
  return segs.map((_, i) => `has(${root}.${segs.slice(0, i + 1).join('.')})`).join(' && ');
}

export interface Compiled {
  out: Output | null;
  issues: Issue[];
}

/** Compiles one table row. `path` is the row's document path; any issue is located under it. */
export function compileField(f: Field, input: InputKind, path: string): Compiled {
  const issues: Issue[] = [...keyIssues(f.key, path + '.key')];
  const want = input === 'FRAME' ? 'frame' : 'json';
  if (f.source !== want) issues.push({ path, code: 'sourceMismatch' });

  if (f.source === 'frame') {
    if (nonNegInt(f.offset, LIMITS.frameBytes) === null) issues.push({ path: path + '.offset', code: 'offsetInvalid' });
    if (f.type === 'bit' || f.type === 'bits' || f.type === 'sbits') {
      if (nonNegInt(f.bitIndex, LIMITS.frameBytes * 8) === null) issues.push({ path: path + '.bitIndex', code: 'bitIndexInvalid' });
    }
    if (f.type === 'bits' || f.type === 'sbits') {
      const w = nonNegInt(f.bitWidth, 32);
      if (w === null || w < 1) issues.push({ path: path + '.bitWidth', code: 'widthInvalid' });
    }
    if (f.type === 'bcd') {
      const l = nonNegInt(f.bcdLength, 8);
      if (l === null || l < 1) issues.push({ path: path + '.bcdLength', code: 'lengthInvalid' });
    }
  } else {
    const segs = f.path.split('.');
    if (f.path.trim() === '') issues.push({ path: path + '.path', code: 'pathRequired' });
    else if (segs.some((s) => s === '')) issues.push({ path: path + '.path', code: 'pathSegmentEmpty' });
    else if (f.skipIfAbsent && !segs.every(isSelectable)) issues.push({ path: path + '.path', code: 'absentNeedsPlainPath' });
  }

  const mul = f.mul.trim() === '' ? null : parseFinite(f.mul);
  const div = f.div.trim() === '' ? null : parseFinite(f.div);
  const add = f.add.trim() === '' ? null : parseFinite(f.add);
  if (f.mul.trim() !== '' && mul === null) issues.push({ path: path + '.mul', code: 'numberInvalid' });
  if (f.div.trim() !== '' && div === null) issues.push({ path: path + '.div', code: 'numberInvalid' });
  if (f.add.trim() !== '' && add === null) issues.push({ path: path + '.add', code: 'numberInvalid' });
  if (div === 0) issues.push({ path: path + '.div', code: 'divideByZero' });
  if (f.when.trim() !== '' && !wrappable(f.when)) issues.push({ path: path + '.when', code: 'unbalanced' });
  if (issues.length > 0) return { out: null, issues };

  let expr = readExpr(f, input);
  if (mul !== null || div !== null || add !== null) {
    // A frame read is an int; shaping needs a double. A json read is already one.
    if (f.source === 'frame') expr = `double(${expr})`;
    if (mul !== null) expr += ` * ${mul < 0 ? `(-${celDouble(mul)})` : celDouble(mul)}`;
    if (div !== null) expr += ` / ${div < 0 ? `(-${celDouble(div)})` : celDouble(div)}`;
    if (add !== null && add !== 0) expr += add < 0 ? ` - ${celDouble(add)}` : ` + ${celDouble(add)}`;
  }

  const guard = f.source === 'json' && f.skipIfAbsent ? absentGuard(f, input) : '';
  const when = andJoin([guard, f.when]);
  const out: Output = { key: f.key, expr };
  if (when !== '') out.when = when;
  return { out, issues: [] };
}

/** The section guard: the fPort shortcut and the raw condition. */
export function compileSectionWhen(fPort: string, when: string): { when: string; issues: IssueCode[] } {
  const issues: IssueCode[] = [];
  const parts: string[] = [];
  if (fPort.trim() !== '') {
    const n = nonNegInt(fPort, 255);
    if (n === null) issues.push('portInvalid');
    else parts.push(`fPort == ${n}`);
  }
  if (when.trim() !== '') {
    if (!wrappable(when)) issues.push('unbalanced');
    else parts.push(when.trim());
  }
  if (issues.length > 0) return { when: '', issues };
  return { when: andJoin(parts), issues };
}

// ---------------------------------------------------------------------------------------------
// State -> document

const byteLen = (s: string) => new TextEncoder().encode(s).length;

function nameIssues(name: string, path: string): Issue[] {
  if (name.trim() === '') return [{ path, code: 'nameRequired' }];
  if (byteLen(name) > LIMITS.name) return [{ path, code: 'nameTooLong', params: { max: LIMITS.name } }];
  return [];
}

function exprIssues(expr: string, path: string): Issue[] {
  if (expr.trim() === '') return [{ path, code: 'exprRequired' }];
  if (byteLen(expr) > LIMITS.expression) return [{ path, code: 'exprTooLong', params: { max: LIMITS.expression } }];
  return [];
}

interface TableRowDoc { at: number; field: Field }
interface TableSectionDoc { fPort: string; when: string; rows: TableRowDoc[] }

export interface Built {
  /** The document as the server stores it, or null when the form has issues that stop it being built. */
  document: Record<string, unknown> | null;
  text: string | null;
  issues: Issue[];
  /** The decoder's static output count: the largest section's scalar rows plus its map rows' maxKeys. */
  maxReadings: number;
}

export function countReadings(sections: SectionState[]): number {
  let max = 0;
  for (const s of sections) {
    if (s.ignore) continue;
    let n = 0;
    for (const r of s.rows) {
      if (r.kind === 'map') n += nonNegInt(r.maxKeys, LIMITS.readings) ?? 0;
      else n += 1;
    }
    max = Math.max(max, n);
  }
  return max;
}

export function buildDocument(st: EditorState): Built {
  const issues: Issue[] = [];
  const maxReadings = countReadings(st.sections);
  if (st.sections.length === 0) issues.push({ path: 'sections', code: 'noSections' });
  if (st.sections.length > LIMITS.sections) issues.push({ path: 'sections', code: 'tooManySections', params: { max: LIMITS.sections } });
  if (st.lets.length > LIMITS.lets) issues.push({ path: 'let', code: 'tooManyLets', params: { max: LIMITS.lets } });
  if (st.vectors.length > LIMITS.vectors) issues.push({ path: 'vectors', code: 'tooManyVectors', params: { max: LIMITS.vectors } });
  if (maxReadings > LIMITS.readings) issues.push({ path: 'sections', code: 'tooManyReadings', params: { max: LIMITS.readings } });

  const lets = st.lets.map((l, i) => {
    const p = `let[${i}]`;
    issues.push(...nameIssues(l.name, p + '.name'), ...exprIssues(l.expr, p + '.expr'));
    if (l.name.trim() !== '' && !isIdentifier(l.name.trim())) issues.push({ path: p + '.name', code: 'nameInvalid' });
    if (st.lets.findIndex((o) => o.name.trim() === l.name.trim()) !== i) issues.push({ path: p + '.name', code: 'nameDuplicate' });
    return { name: l.name.trim(), expr: l.expr };
  });

  const table: TableSectionDoc[] = [];
  const sections = st.sections.map((s, si) => {
    const sp = `sections[${si}]`;
    issues.push(...nameIssues(s.name, sp + '.name'));
    if (st.sections.findIndex((o) => o.name.trim() === s.name.trim()) !== si) issues.push({ path: sp + '.name', code: 'nameDuplicate' });
    const cw = compileSectionWhen(s.fPort, s.when);
    for (const code of cw.issues) issues.push({ path: sp + '.when', code });
    const tableRows: TableRowDoc[] = [];
    const seenKeys = new Set<string>();
    const outputs: Output[] = [];
    // An ignore section emits no outputs, so its rows are not validated: a hidden blank row must not
    // keep Save disabled. Valid rows are still kept in the table so un-ignoring restores them.
    const rowIssues: Issue[] = s.ignore ? [] : issues;
    s.rows.forEach((r, oi) => {
      const op = `${sp}.outputs[${oi}]`;
      let out: Output | null = null;
      if (r.kind === 'field') {
        const c = compileField(r.field, st.input, op);
        rowIssues.push(...c.issues);
        out = c.out;
        if (out) tableRows.push({ at: oi, field: r.field });
      } else if (r.kind === 'cel') {
        rowIssues.push(...keyIssues(r.key, op + '.key'), ...exprIssues(r.expr, op + '.expr'));
        if (r.when.trim() !== '' && !wrappable(r.when)) rowIssues.push({ path: op + '.when', code: 'unbalanced' });
        out = { key: r.key, expr: r.expr };
        if (r.when.trim() !== '') out.when = r.when;
      } else {
        rowIssues.push(...exprIssues(r.map, op + '.map'));
        const mk = nonNegInt(r.maxKeys, LIMITS.readings);
        if (mk === null || mk < 1) rowIssues.push({ path: op + '.maxKeys', code: 'maxKeysInvalid', params: { max: LIMITS.readings } });
        if (r.prefix.startsWith(RESERVED_KEY_PREFIX)) rowIssues.push({ path: op + '.prefix', code: 'keyReserved' });
        if (byteLen(r.prefix) > LIMITS.key) rowIssues.push({ path: op + '.prefix', code: 'keyTooLong', params: { max: LIMITS.key } });
        if (r.when.trim() !== '' && !wrappable(r.when)) rowIssues.push({ path: op + '.when', code: 'unbalanced' });
        out = { map: r.map, maxKeys: mk ?? 0 };
        if (r.prefix !== '') out.prefix = r.prefix;
        if (r.when.trim() !== '') out.when = r.when;
      }
      if (r.kind !== 'map') {
        const key = r.kind === 'field' ? r.field.key : r.key;
        if (key.trim() !== '' && seenKeys.has(key)) rowIssues.push({ path: op + '.key', code: 'keyDuplicate' });
        seenKeys.add(key);
      }
      outputs.push(out ?? {});
    });
    table.push({ fPort: s.fPort, when: s.when, rows: tableRows });
    const sec: Record<string, unknown> = { name: s.name };
    if (cw.when !== '') sec.when = cw.when;
    if (s.ignore) sec.ignore = true;
    else sec.outputs = outputs;
    return sec;
  });

  const vectors = st.vectors.map((v, vi) => buildVector(v, vi, issues));

  const document: Record<string, unknown> = {
    ...st.extra,
    kind: 'CEL',
    envVersion: st.envVersion,
    input: st.input,
    ...(lets.length > 0 ? { let: lets } : {}),
    sections,
    ...(vectors.length > 0 ? { vectors } : {}),
    table: { v: TABLE_VERSION, sections: table },
  };
  if (issues.length > 0) return { document: null, text: null, issues, maxReadings };
  const text = JSON.stringify(document);
  if (byteLen(text) > LIMITS.document) {
    return { document: null, text: null, issues: [{ path: '', code: 'documentTooLarge', params: { max: LIMITS.document } }], maxReadings };
  }
  return { document, text, issues, maxReadings };
}

/**
 * The text a person pastes as a hex frame, in the one form the server's vector reader accepts:
 * whitespace removed and a leading `0x` dropped from each token ("01 00 D7", "0x01 0x00 0xD7" and
 * "0100d7" all mean the same bytes). Case is kept; the server takes either.
 */
export function compactHex(text: string): string {
  return text
    .trim()
    .split(/\s+/)
    .map((t) => t.replace(/^0x/i, ''))
    .join('');
}

function buildVector(v: VectorState, vi: number, issues: Issue[]): Record<string, unknown> {
  const p = `vectors[${vi}]`;
  issues.push(...nameIssues(v.name, p + '.name'));
  const input: Record<string, unknown> = { ...v.inputExtra };
  if (v.kind === 'frameHex') {
    // The server reads the stored text with hex.DecodeString: no spaces, no 0x. Store that form.
    const compact = compactHex(v.text);
    if (!/^([0-9a-fA-F]{2})*$/.test(compact) || compact.length / 2 > LIMITS.frameBytes) issues.push({ path: p + '.input', code: 'vectorHexInvalid' });
    input.frameHex = compact;
  } else if (v.kind === 'frameBase64') {
    const compact = v.text.replace(/\s+/g, '');
    if (!/^[A-Za-z0-9+/]*={0,2}$/.test(compact) || compact.length % 4 !== 0) issues.push({ path: p + '.input', code: 'vectorBase64Invalid' });
    input.frameBase64 = compact;
  } else {
    try {
      const parsed: unknown = JSON.parse(v.text);
      if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('not an object');
      input[v.kind] = parsed;
    } catch {
      issues.push({ path: p + '.input', code: 'vectorJsonInvalid' });
    }
  }
  for (const [member, text] of [['fPort', v.fPort], ['fCnt', v.fCnt]] as const) {
    if (text.trim() === '') continue;
    const n = nonNegInt(text, 4294967295);
    if (n === null) issues.push({ path: p + '.input', code: 'vectorPortInvalid' });
    else input[member] = n;
  }
  const expect: Record<string, number> = {};
  v.expect.forEach((e, ei) => {
    const ep = `${p}.expect[${ei}]`;
    issues.push(...keyIssues(e.key, ep + '.key'));
    const n = parseFinite(e.value);
    if (n === null) issues.push({ path: ep + '.value', code: 'vectorExpectInvalid' });
    else if (Object.prototype.hasOwnProperty.call(expect, e.key)) issues.push({ path: ep + '.key', code: 'keyDuplicate' });
    else expect[e.key] = n;
  });
  return { name: v.name, input, expect };
}

// ---------------------------------------------------------------------------------------------
// Document -> state

export type Parsed =
  | { ok: true; state: EditorState }
  | { ok: false; reason: 'unsupportedKind'; kind: string }
  | { ok: false; reason: 'malformed' };

const isObj = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v);
const str = (v: unknown): string => (typeof v === 'string' ? v : '');
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);

const KNOWN_TOP = new Set(['kind', 'envVersion', 'input', 'let', 'sections', 'vectors', 'table']);

function sameOutput(a: Output, b: Record<string, unknown>): boolean {
  return a.key === b.key && a.expr === b.expr && (a.when ?? '') === str(b.when) && b.map === undefined;
}

const FIELD_ENUMS: Partial<Record<keyof Field, readonly unknown[]>> = {
  source: ['frame', 'json'],
  type: FRAME_TYPES,
  endian: ['be', 'le'],
  jsonType: ['number', 'boolean'],
};

/** A stored table field with every property the right type, or null. An API-written table may hold anything. */
function readField(raw: Record<string, unknown>): Field | null {
  const blank = blankField();
  const out: Record<string, unknown> = { ...blank };
  for (const k of Object.keys(blank) as (keyof Field)[]) {
    if (raw[k] === undefined) continue;
    if (typeof raw[k] !== typeof blank[k]) return null;
    const allowed = FIELD_ENUMS[k];
    if (allowed && !allowed.includes(raw[k])) return null;
    out[k] = raw[k];
  }
  return out as unknown as Field;
}

function readTable(raw: unknown): TableSectionDoc[] | null {
  if (!isObj(raw) || raw.v !== TABLE_VERSION) return null;
  return arr(raw.sections).map((s) => {
    const o = isObj(s) ? s : {};
    return {
      fPort: str(o.fPort),
      when: str(o.when),
      rows: arr(o.rows).flatMap((r) => {
        if (!isObj(r) || typeof r.at !== 'number' || !isObj(r.field)) return [];
        const field = readField(r.field);
        return field ? [{ at: r.at, field }] : [];
      }),
    };
  });
}

/** Reads a stored document into editor state. Never throws. */
export function parseDocument(text: string): Parsed {
  let doc: unknown;
  try {
    doc = JSON.parse(text);
  } catch {
    return { ok: false, reason: 'malformed' };
  }
  if (!isObj(doc)) return { ok: false, reason: 'malformed' };
  if (doc.kind !== 'CEL') return { ok: false, reason: 'unsupportedKind', kind: String(doc.kind) };
  const input: InputKind = doc.input === 'JSON' || doc.input === 'DECODED' ? doc.input : 'FRAME';
  const table = readTable(doc.table);

  const sections: SectionState[] = arr(doc.sections).map((raw, si) => {
    const s = isObj(raw) ? raw : {};
    const t = table?.[si];
    const outputs = arr(s.outputs).map((o) => (isObj(o) ? o : {}));
    const storedWhen = str(s.when);
    // The shortcut is believed only if it reproduces the stored guard.
    const believe = t !== undefined && compileSectionWhen(t.fPort, t.when).when === storedWhen;
    const rows: Row[] = outputs.map((o, oi) => {
      const tr = t?.rows.find((r) => r.at === oi);
      if (tr) {
        const c = compileField(tr.field, input, '');
        if (c.out && sameOutput(c.out, o)) return { id: newId(), kind: 'field', field: tr.field };
      }
      if (typeof o.map === 'string') {
        return {
          id: newId(), kind: 'map', map: o.map, prefix: str(o.prefix),
          maxKeys: String(typeof o.maxKeys === 'number' ? o.maxKeys : 16), when: str(o.when),
        };
      }
      return { id: newId(), kind: 'cel', key: str(o.key), expr: str(o.expr), when: str(o.when) };
    });
    return {
      id: newId(),
      name: str(s.name),
      fPort: believe && t ? t.fPort : '',
      when: believe && t ? t.when : storedWhen,
      ignore: s.ignore === true,
      rows,
    };
  });

  const vectors: VectorState[] = arr(doc.vectors).map((raw) => {
    const v = isObj(raw) ? raw : {};
    const inp = isObj(v.input) ? v.input : {};
    const kind = VECTOR_INPUT_KINDS.find((k) => inp[k] !== undefined) ?? 'frameHex';
    const inputExtra: Record<string, unknown> = {};
    for (const [k, val] of Object.entries(inp)) {
      if (!(VECTOR_INPUT_KINDS as readonly string[]).includes(k) && k !== 'fPort' && k !== 'fCnt') inputExtra[k] = val;
    }
    const body = inp[kind];
    return {
      id: newId(),
      name: str(v.name),
      kind,
      text: typeof body === 'string' ? body : body === undefined ? '' : JSON.stringify(body, null, 2),
      fPort: typeof inp.fPort === 'number' ? String(inp.fPort) : '',
      fCnt: typeof inp.fCnt === 'number' ? String(inp.fCnt) : '',
      expect: Object.entries(isObj(v.expect) ? v.expect : {}).map(([key, value]) => ({ id: newId(), key, value: String(value) })),
      inputExtra,
    };
  });

  const extra: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(doc)) if (!KNOWN_TOP.has(k)) extra[k] = v;

  return {
    ok: true,
    state: {
      envVersion: typeof doc.envVersion === 'number' ? doc.envVersion : 1,
      input,
      lets: arr(doc.let).map((l) => {
        const o = isObj(l) ? l : {};
        return { id: newId(), name: str(o.name), expr: str(o.expr) };
      }),
      sections,
      vectors,
      extra,
    },
  };
}

/** Converts a table row to a raw-CEL row. It leaves the table for good: nothing turns it back. */
export function toRaw(r: Row, input: InputKind): Row {
  if (r.kind !== 'field') return r;
  const c = compileField(r.field, input, '');
  if (!c.out) return { id: r.id, kind: 'cel', key: r.field.key, expr: '', when: r.field.when };
  return { id: r.id, kind: 'cel', key: c.out.key as string, expr: c.out.expr as string, when: c.out.when ?? '' };
}

// ---------------------------------------------------------------------------------------------
// Comparison and error mapping

/** A document in a canonical form (sorted keys, no whitespace), so two spellings of one decoder compare equal. */
export function canonical(text: string | null | undefined): string {
  if (!text) return '';
  try {
    const sort = (v: unknown): unknown =>
      Array.isArray(v) ? v.map(sort)
        : isObj(v) ? Object.fromEntries(Object.keys(v).sort().map((k) => [k, sort(v[k])]))
        : v;
    return JSON.stringify(sort(JSON.parse(text)));
  } catch {
    return text;
  }
}

// Paths the server names in a refusal: sections[1].outputs[2].expr, let[0].expr, vectors[3] ...
const PATH_RE = /(?:sections\[\d+\](?:\.(?:outputs\[\d+\]|name|when|ignore)(?:\.[A-Za-z]+)?)?|let\[\d+\](?:\.[A-Za-z]+)?|vectors\[\d+\](?:\.[A-Za-z]+(?:\[\d+\])?)?)/g;

/** Every document path a refusal message names, in order. */
export function pathsIn(message: string): string[] {
  return [...new Set(message.match(PATH_RE) ?? [])];
}

/** Whether one of the named paths is the row at `rowPath` or a member of it. */
export function pathHits(named: readonly string[], rowPath: string): boolean {
  return named.some((p) => p === rowPath || p.startsWith(rowPath + '.') || p.startsWith(rowPath + '['));
}

/** The expression text a path points at in a built document, or null. */
export function expressionAt(doc: Record<string, unknown> | null, path: string): string | null {
  if (!doc) return null;
  const lm = /^let\[(\d+)\]\.expr$/.exec(path);
  if (lm) {
    const l = arr(doc.let)[Number(lm[1])];
    return isObj(l) ? str(l.expr) || null : null;
  }
  const m = /^sections\[(\d+)\](?:\.outputs\[(\d+)\])?\.(expr|when|map)$/.exec(path);
  if (!m) return null;
  const s = arr(doc.sections)[Number(m[1])];
  if (!isObj(s)) return null;
  const target = m[2] === undefined ? s : arr(s.outputs)[Number(m[2])];
  return isObj(target) ? str(target[m[3]]) || null : null;
}
