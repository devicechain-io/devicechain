// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The table -> CEL compiler. What these pin: the generated expressions (so a refactor cannot
// change what a saved decoder does), that free text can never reshape an expression (the JSON
// path is the one place the table writes a person's text into CEL), and that a stored table is
// believed only when it still reproduces the stored expression.
import { describe, expect, it } from 'vitest';
import { andJoin, celDouble, celString, parseFinite, wrappable } from './cel';
import {
  blankField,
  blankSection,
  buildDocument,
  canonical,
  compactHex,
  compileField,
  compileSectionWhen,
  emptyState,
  expressionAt,
  newId,
  parseDocument,
  pathHits,
  pathsIn,
  toRaw,
  type EditorState,
  type Field,
  type Row,
} from './model';

const field = (over: Partial<Field>): Field => ({ ...blankField(), key: 'temperature', ...over });
const compile = (f: Field, input: 'FRAME' | 'JSON' | 'DECODED' = f.source === 'frame' ? 'FRAME' : 'JSON') =>
  compileField(f, input, 'sections[0].outputs[0]');

function stateWith(rows: Row[], over: Partial<EditorState> = {}): EditorState {
  const st = emptyState();
  st.sections[0].rows = rows;
  return { ...st, ...over };
}
const fieldRow = (f: Field): Row => ({ id: newId(), kind: 'field', field: f });

describe('celString', () => {
  it('quotes and escapes what could end or reshape the literal', () => {
    expect(celString('plain')).toBe('"plain"');
    expect(celString('a"b')).toBe('"a\\"b"');
    expect(celString('a\\b')).toBe('"a\\\\b"');
    expect(celString('a\nb\rc\td')).toBe('"a\\nb\\rc\\td"');
    expect(celString('\u0000\u001f\u007f')).toBe('"\\u0000\\u001F\\u007F"');
    expect(celString(' ')).toBe('"\\u2028"');
    expect(celString('é€😀')).toBe('"é€😀"');
    expect(celString('\ud800')).toBe('"\\uFFFD"');
  });

  it('keeps a backslash-quote pair from escaping the closing quote', () => {
    // The classic break: input ends in a backslash, so an un-escaped writer yields "...\" and
    // the quote that closes the literal is read as part of it.
    expect(celString('x\\')).toBe('"x\\\\"');
    expect(celString('x\\"')).toBe('"x\\\\\\""');
  });
});

describe('numbers', () => {
  it('parses only plain decimal numbers', () => {
    expect(parseFinite('0.1')).toBe(0.1);
    expect(parseFinite(' -5 ')).toBe(-5);
    expect(parseFinite('1e3')).toBe(1000);
    for (const bad of ['', 'abc', '0x10', 'Infinity', 'NaN', '1,5', '1 2', '--1', '1e999']) expect(parseFinite(bad)).toBeNull();
  });
  it('writes a double literal, never a bare int or a broken exponent', () => {
    expect(celDouble(10)).toBe('10.0');
    expect(celDouble(0.1)).toBe('0.1');
    expect(celDouble(-2.5)).toBe('2.5');
    expect(celDouble(1e21)).toBe('1e+21');
    expect(celDouble(1.5e-7)).toBe('1.5e-7');
  });
});

describe('frame fields', () => {
  it.each([
    [{ type: 'u8', offset: '3' }, 'u8(frame, 3)'],
    [{ type: 'i16', endian: 'be', offset: '1' }, 'i16be(frame, 1)'],
    [{ type: 'u32', endian: 'le', offset: '0' }, 'u32le(frame, 0)'],
    [{ type: 'f32', endian: 'be', offset: '4' }, 'f32be(frame, 4)'],
    [{ type: 'i8', endian: 'le', offset: '2' }, 'i8(frame, 2)'],
    [{ type: 'bit', offset: '3', bitIndex: '0' }, 'bit(frame, 3, 0)'],
    [{ type: 'bits', offset: '2', bitIndex: '4', bitWidth: '3' }, 'bits(frame, 2, 4, 3)'],
    [{ type: 'sbits', offset: '2', bitIndex: '0', bitWidth: '12' }, 'sbits(frame, 2, 0, 12)'],
    [{ type: 'bcd', offset: '1', bcdLength: '3' }, 'bcd(frame, 1, 3)'],
  ] as [Partial<Field>, string][])('%j reads %s', (over, expr) => {
    expect(compile(field(over)).out).toEqual({ key: 'temperature', expr });
  });

  it('shapes a read as value * mul / div + add, through double()', () => {
    const f = field({ type: 'i16', offset: '1', div: '10' });
    expect(compile(f).out?.expr).toBe('double(i16be(frame, 1)) / 10.0');
    expect(compile({ ...f, div: '', mul: '0.5', add: '-40' }).out?.expr).toBe('double(i16be(frame, 1)) * 0.5 - 40.0');
    expect(compile({ ...f, div: '', mul: '-2', add: '3' }).out?.expr).toBe('double(i16be(frame, 1)) * (-2.0) + 3.0');
    expect(compile({ ...f, div: '', add: '0' }).out?.expr).toBe('double(i16be(frame, 1))');
  });

  it('refuses what the platform would refuse, and says where', () => {
    const codes = (over: Partial<Field>) => compile(field(over)).issues.map((i) => `${i.path.split('.').pop()}:${i.code}`);
    expect(codes({ key: '' })).toEqual(['key:keyRequired']);
    expect(codes({ key: 'lorawan.rssi' })).toEqual(['key:keyReserved']);
    expect(codes({ offset: '-1' })).toEqual(['offset:offsetInvalid']);
    expect(codes({ offset: '4097' })).toEqual(['offset:offsetInvalid']);
    expect(codes({ offset: '1.5' })).toEqual(['offset:offsetInvalid']);
    expect(codes({ type: 'bits', bitWidth: '33' })).toEqual(['bitWidth:widthInvalid']);
    expect(codes({ type: 'bcd', bcdLength: '9' })).toEqual(['bcdLength:lengthInvalid']);
    expect(codes({ mul: 'x' })).toEqual(['mul:numberInvalid']);
    expect(codes({ div: '0' })).toEqual(['div:divideByZero']);
    expect(codes({ when: 'a == (1' })).toEqual(['when:unbalanced']);
  });

  it('refuses a row whose source does not match the decoder input', () => {
    expect(compile(field({}), 'JSON').issues.map((i) => i.code)).toEqual(['sourceMismatch']);
    expect(compile(field({ source: 'json', path: 'a' }), 'FRAME').issues.map((i) => i.code)).toEqual(['sourceMismatch']);
  });
});

describe('json fields', () => {
  const j = (over: Partial<Field>) => field({ source: 'json', path: 'sensor.temp', ...over });

  it('indexes by constant string keys and converts', () => {
    expect(compile(j({})).out?.expr).toBe('double(json["sensor"]["temp"])');
    expect(compile(j({ jsonType: 'boolean' })).out?.expr).toBe('bool(json["sensor"]["temp"])');
    expect(compile(j({ div: '100' }), 'DECODED').out?.expr).toBe('double(decoded["sensor"]["temp"]) / 100.0');
  });

  it('guards a row that may be absent, one step at a time', () => {
    expect(compile(j({ skipIfAbsent: true })).out?.when).toBe('has(json.sensor) && has(json.sensor.temp)');
    expect(compile(j({ skipIfAbsent: true, when: 'size(frame) > 0' })).out?.when).toBe(
      '(has(json.sensor) && has(json.sensor.temp)) && (size(frame) > 0)',
    );
  });

  it('cannot guard a path that is not a plain selection', () => {
    expect(compile(j({ path: 'a-b.c', skipIfAbsent: true })).issues.map((i) => i.code)).toEqual(['absentNeedsPlainPath']);
    expect(compile(j({ path: 'in.x', skipIfAbsent: true })).issues.map((i) => i.code)).toEqual(['absentNeedsPlainPath']);
    // Without the guard such a path is fine: it is only ever a string constant.
    expect(compile(j({ path: 'a-b.c' })).out?.expr).toBe('double(json["a-b"]["c"])');
  });

  it('requires a path with no empty segment', () => {
    expect(compile(j({ path: '' })).issues.map((i) => i.code)).toEqual(['pathRequired']);
    expect(compile(j({ path: 'a..b' })).issues.map((i) => i.code)).toEqual(['pathSegmentEmpty']);
  });
});

describe('free text cannot reshape an expression (injection)', () => {
  // The hostile segments a person could type. Each must come out as ONE string literal: the
  // expression is the same shape as for a harmless key, with only the literal's content changed.
  const hostile = [
    'a"] || true || json["b',
    'a\\"] || true || json["b',
    'x\\',
    '"',
    '\\',
    'a\n) || (true',
    "a'] || json['b",
    'a") ; drop',
    'a b',
  ];

  it.each(hostile)('keeps %j inside its literal', (seg) => {
    const out = compile(field({ source: 'json', path: seg })).out;
    // A path is dot-split, so a hostile segment may become several; every piece must stay a literal.
    expect(out).not.toBeNull();
    const expr = out?.expr as string;
    // Strip every string literal as CEL reads them; what is left must be exactly the fixed shell.
    const shell = expr.replace(/"(?:[^"\\\n\r]|\\.)*"/g, '""');
    expect(shell).toMatch(/^double\(json(\[""\])+\)$/);
  });

  it('is shown to fail when the escaping is removed (negative control for the matcher)', () => {
    // The same check against an UNescaped writer must flag the hostile path. If it did not, the
    // test above would be unable to fail and would prove nothing.
    const unescaped = (s: string) => `double(json["${s}"])`;
    const expr = unescaped('a"] || true || json["b');
    const shell = expr.replace(/"(?:[^"\\\n\r]|\\.)*"/g, '""');
    expect(shell).not.toMatch(/^double\(json(\[""\])+\)$/);
  });

  it('never lets a raw condition close the parenthesis that wraps it', () => {
    expect(wrappable('x > 1')).toBe(true);
    expect(wrappable('f(a, b) && c[0]')).toBe(true);
    expect(wrappable('s == ")"')).toBe(true);
    expect(wrappable('s == "a\\"b)"')).toBe(true);
    expect(wrappable('size(br"\") == 1')).toBe(true);
    expect(wrappable('s == r"\"')).toBe(true);
    expect(wrappable('s == "\" && (1')).toBe(false);
    expect(wrappable('1) || (true')).toBe(false);
    expect(wrappable('(1')).toBe(false);
    expect(wrappable('x // trailing')).toBe(false);
    expect(wrappable('"unterminated')).toBe(false);
    expect(wrappable('a ]')).toBe(false);
    expect(() => andJoin(['fPort == 2', '1) || (true'])).toThrow();
    expect(() => andJoin(['fPort == 2', 'x // c'])).toThrow();
    expect(andJoin(['a', '', 'b'])).toBe('(a) && (b)');
    expect(andJoin(['only'])).toBe('only');
  });

  it('refuses to build a document with an unbalanced condition', () => {
    const f = field({ when: '1) || (true' });
    const built = buildDocument(stateWith([fieldRow(f)]));
    expect(built.document).toBeNull();
    expect(built.issues.map((i) => i.code)).toContain('unbalanced');
  });
});

describe('section guard', () => {
  it('writes the fPort shortcut and combines it with a raw condition', () => {
    expect(compileSectionWhen('2', '')).toEqual({ when: 'fPort == 2', issues: [] });
    expect(compileSectionWhen('', 'fCnt > 3')).toEqual({ when: 'fCnt > 3', issues: [] });
    expect(compileSectionWhen('2', 'fCnt > 3')).toEqual({ when: '(fPort == 2) && (fCnt > 3)', issues: [] });
    expect(compileSectionWhen('', '')).toEqual({ when: '', issues: [] });
    expect(compileSectionWhen('abc', '').issues).toEqual(['portInvalid']);
    expect(compileSectionWhen('256', '').issues).toEqual(['portInvalid']);
    expect(compileSectionWhen('2', '1) || (true').issues).toEqual(['unbalanced']);
  });
});

describe('document round trip', () => {
  const rich = (): EditorState => {
    const st = stateWith([
      fieldRow(field({ key: 'temperature', type: 'i16', offset: '1', div: '10' })),
      fieldRow(field({ key: 'door', type: 'bit', offset: '3', bitIndex: '0', when: 'hdr == 1' })),
      { id: newId(), kind: 'cel', key: 'battery', expr: 'double(u8(frame, 5)) / 2.0', when: '' },
      { id: newId(), kind: 'map', map: 'lpp.decode(slice(frame, 6, size(frame) - 6))', prefix: 'lpp.', maxKeys: '16', when: '' },
    ]);
    st.sections[0].fPort = '2';
    st.sections.push({ ...blankSection('status'), when: 'fPort == 3', ignore: true });
    st.lets = [{ id: newId(), name: 'hdr', expr: 'u8(frame, 0)' }];
    st.vectors = [
      {
        id: newId(), name: 'room sensor', kind: 'frameHex', text: '0100D7', fPort: '2', fCnt: '', inputExtra: { link: { rssi: -90 } },
        expect: [{ id: newId(), key: 'temperature', value: '21.5' }],
      },
      {
        id: newId(), name: 'json one', kind: 'json', text: '{"a":1}', fPort: '', fCnt: '', inputExtra: {},
        expect: [],
      },
    ];
    st.extra = { futureMember: { x: 1 } };
    return st;
  };

  it('builds, re-reads and rebuilds to the same document', () => {
    const first = buildDocument(rich());
    expect(first.issues).toEqual([]);
    const parsed = parseDocument(first.text as string);
    expect(parsed.ok).toBe(true);
    if (!parsed.ok) return;
    const second = buildDocument(parsed.state);
    expect(canonical(second.text)).toBe(canonical(first.text));
    // Table rows come back as table rows, raw as raw, maps as maps.
    expect(parsed.state.sections[0].rows.map((r) => r.kind)).toEqual(['field', 'field', 'cel', 'map']);
    expect(parsed.state.sections[0].fPort).toBe('2');
    expect(parsed.state.sections[1].ignore).toBe(true);
    expect(first.maxReadings).toBe(2 + 1 + 16);
  });

  it('keeps what the form does not edit: unknown members and vector link metrics', () => {
    const first = buildDocument(rich());
    const doc = first.document as Record<string, unknown>;
    expect(doc.futureMember).toEqual({ x: 1 });
    const vec = (doc.vectors as { input: Record<string, unknown> }[])[0];
    expect(vec.input).toEqual({ frameHex: '0100D7', fPort: 2, link: { rssi: -90 } });
  });

  it('expresses expectations as numbers', () => {
    const doc = buildDocument(rich()).document as { vectors: { expect: Record<string, unknown> }[] };
    expect(doc.vectors[0].expect).toEqual({ temperature: 21.5 });
  });

  it('believes a stored table row only while it reproduces the stored expression', () => {
    const built = buildDocument(stateWith([fieldRow(field({ type: 'u16', offset: '0', div: '10' }))]));
    const edited = JSON.parse(built.text as string);
    // Someone edits the expression through the API; the stored table still says u16 / 10.
    edited.sections[0].outputs[0].expr = 'double(u16be(frame, 0)) / 100.0';
    const parsed = parseDocument(JSON.stringify(edited));
    expect(parsed.ok && parsed.state.sections[0].rows[0]).toMatchObject({ kind: 'cel', expr: 'double(u16be(frame, 0)) / 100.0' });
    // And once rebuilt, the expression that runs is the one that was stored.
    if (parsed.ok) {
      const again = buildDocument(parsed.state).document as { sections: { outputs: { expr: string }[] }[] };
      expect(again.sections[0].outputs[0].expr).toBe('double(u16be(frame, 0)) / 100.0');
    }
  });

  it('falls back to a raw section guard when the stored one was edited elsewhere', () => {
    const st = stateWith([]);
    st.sections[0].fPort = '2';
    const edited = JSON.parse(buildDocument(st).text as string);
    edited.sections[0].when = 'fPort == 9';
    const parsed = parseDocument(JSON.stringify(edited));
    expect(parsed.ok && parsed.state.sections[0]).toMatchObject({ fPort: '', when: 'fPort == 9' });
  });

  it('reads a document that has no table at all (written by hand or by another client)', () => {
    const parsed = parseDocument(
      JSON.stringify({ kind: 'CEL', envVersion: 1, input: 'FRAME', sections: [{ name: 's', outputs: [{ key: 'k', expr: 'u8(frame, 0)' }] }] }),
    );
    expect(parsed.ok && parsed.state.sections[0].rows[0]).toMatchObject({ kind: 'cel', key: 'k' });
  });

  it('reports a kind it cannot edit instead of guessing', () => {
    expect(parseDocument(JSON.stringify({ kind: 'WASM', sections: [] }))).toEqual({ ok: false, reason: 'unsupportedKind', kind: 'WASM' });
    expect(parseDocument('{nope')).toEqual({ ok: false, reason: 'malformed' });
    expect(parseDocument('[]')).toEqual({ ok: false, reason: 'malformed' });
  });

  it('converting a row to raw CEL keeps what it computed and drops it from the table', () => {
    const f = field({ type: 'i16', offset: '1', div: '10', when: 'x > 1' });
    const raw = toRaw(fieldRow(f), 'FRAME');
    expect(raw).toMatchObject({ kind: 'cel', key: 'temperature', expr: 'double(i16be(frame, 1)) / 10.0', when: 'x > 1' });
    const doc = buildDocument(stateWith([raw])).document as { table: { sections: { rows: unknown[] }[] } };
    expect(doc.table.sections[0].rows).toEqual([]);
  });
});

describe('form limits and checks', () => {
  it('refuses duplicate keys in a section, a duplicate section, and a reserved prefix', () => {
    const dup = buildDocument(stateWith([fieldRow(field({})), fieldRow(field({ offset: '2' }))]));
    expect(dup.issues.map((i) => `${i.path}:${i.code}`)).toEqual(['sections[0].outputs[1].key:keyDuplicate']);
    const st = stateWith([]);
    st.sections.push(blankSection('telemetry'));
    expect(buildDocument(st).issues.map((i) => i.code)).toContain('nameDuplicate');
    const map = buildDocument(stateWith([{ id: newId(), kind: 'map', map: 'lpp.decode(frame)', prefix: 'lorawan.', maxKeys: '4', when: '' }]));
    expect(map.issues.map((i) => i.code)).toContain('keyReserved');
  });

  it('counts readings as scalar rows plus map maxKeys, the largest section winning, ignore sections free', () => {
    const st = stateWith([
      fieldRow(field({ key: 'a' })),
      { id: newId(), kind: 'map', map: 'lpp.decode(frame)', prefix: '', maxKeys: '10', when: '' },
    ]);
    const big = blankSection('big');
    big.rows = [fieldRow(field({ key: 'x' }))];
    const ign = { ...blankSection('ign'), ignore: true, rows: [fieldRow(field({ key: 'y' })), fieldRow(field({ key: 'z' }))] };
    st.sections.push(big, ign);
    expect(buildDocument(st).maxReadings).toBe(11);
    st.sections[0].rows.push({ id: newId(), kind: 'map', map: 'lpp.decode(frame)', prefix: 'b.', maxKeys: '60', when: '' });
    expect(buildDocument(st).issues.map((i) => i.code)).toContain('tooManyReadings');
  });

  it('checks a vector: hex, JSON object, ports and expectations', () => {
    const v = (over: object) => ({
      id: newId(), name: 'v', kind: 'frameHex' as const, text: '0102', fPort: '', fCnt: '', inputExtra: {}, expect: [], ...over,
    });
    const codes = (over: object) => {
      const st = stateWith([fieldRow(field({}))]);
      st.vectors = [v(over)];
      return buildDocument(st).issues.map((i) => i.code);
    };
    expect(codes({})).toEqual([]);
    expect(codes({ text: '010' })).toEqual(['vectorHexInvalid']);
    expect(codes({ text: 'zz' })).toEqual(['vectorHexInvalid']);
    expect(codes({ kind: 'frameBase64', text: 'AQI=' })).toEqual([]);
    expect(codes({ kind: 'frameBase64', text: 'AQI' })).toEqual(['vectorBase64Invalid']);
    expect(codes({ kind: 'json', text: '[1]' })).toEqual(['vectorJsonInvalid']);
    expect(codes({ kind: 'json', text: '{' })).toEqual(['vectorJsonInvalid']);
    expect(codes({ kind: 'json', text: '{"a":1}' })).toEqual([]);
    expect(codes({ fPort: 'x' })).toEqual(['vectorPortInvalid']);
    expect(codes({ expect: [{ id: 'e', key: 'temperature', value: 'warm' }] })).toEqual(['vectorExpectInvalid']);
    expect(codes({ name: '' })).toEqual(['nameRequired']);
  });
});

describe('error paths', () => {
  it('finds every path a refusal names', () => {
    const msg = 'cannot save: sections[1].outputs[2].expr: unknown function foo; let[0].expr: bad; vectors[3].expect[0]: x; sections[0].when: y';
    expect(pathsIn(msg)).toEqual(['sections[1].outputs[2].expr', 'let[0].expr', 'vectors[3].expect[0]', 'sections[0].when']);
    expect(pathsIn('no path here')).toEqual([]);
  });

  it('matches a row by its prefix', () => {
    const named = ['sections[1].outputs[2].expr'];
    expect(pathHits(named, 'sections[1].outputs[2]')).toBe(true);
    expect(pathHits(named, 'sections[1].outputs[20]')).toBe(false);
    expect(pathHits(named, 'sections[1].outputs[3]')).toBe(false);
    expect(pathHits(['vectors[3]'], 'vectors[3]')).toBe(true);
    expect(pathHits(['vectors[3].expect[0]'], 'vectors[3]')).toBe(true);
  });

  it('shows the expression a path points at', () => {
    const doc = buildDocument(stateWith([fieldRow(field({ type: 'u16', offset: '2' }))])).document;
    expect(expressionAt(doc, 'sections[0].outputs[0].expr')).toBe('u16be(frame, 2)');
    expect(expressionAt(doc, 'sections[0].outputs[7].expr')).toBeNull();
    expect(expressionAt(doc, 'nonsense')).toBeNull();
  });
});

// The server reads a stored vector with hex.DecodeString / base64.StdEncoding: no spaces, no `0x`.
// What the form accepts (it strips whitespace to validate) must be what it stores.
describe('vector frames are stored in the form the server decodes', () => {
  const vec = (kind: 'frameHex' | 'frameBase64', text: string) => {
    const st = stateWith([fieldRow(field({}))]);
    st.vectors = [{ id: newId(), name: 'v', kind, text, fPort: '', fCnt: '', inputExtra: {}, expect: [] }];
    const built = buildDocument(st);
    const input = (built.document?.vectors as { input: Record<string, unknown> }[] | undefined)?.[0]?.input;
    return { built, input };
  };

  it('stores spaced, mixed-case and 0x-prefixed hex compactly', () => {
    for (const text of ['01 00 D7', '  01\t00\nd7 ', '0x01 0x00 0xD7', '0X01 0x00 0xd7', '0100D7']) {
      const { built, input } = vec('frameHex', text);
      expect(built.issues, text).toEqual([]);
      expect((input?.frameHex as string).toLowerCase(), text).toBe('0100d7');
      expect(input?.frameHex, text).toMatch(/^[0-9a-fA-F]*$/);
    }
    expect(compactHex('0x01 0x00 0xD7')).toBe('0100D7');
  });

  it('stores spaced base64 compactly', () => {
    expect(vec('frameBase64', 'AQDX\nAQ==').input?.frameBase64).toBe('AQDXAQ==');
  });

  it('still refuses what the server could not read', () => {
    expect(vec('frameHex', '01 0').built.issues.map((i) => i.code)).toEqual(['vectorHexInvalid']);
    expect(vec('frameHex', '0x0x01').built.issues.map((i) => i.code)).toEqual(['vectorHexInvalid']);
  });

  // Negative control: the pre-fix store (`text.trim()`) keeps the spaces the server refuses.
  it('negative control: the old trim-only form is not server-readable', () => {
    const oldForm = '01 00 D7'.trim();
    expect(oldForm).toMatch(/\s/);
    expect(vec('frameHex', '01 00 D7').input?.frameHex).not.toBe(oldForm);
  });
});

describe('ignored sections and API-written tables', () => {
  it('does not validate the rows of an ignored section', () => {
    const st = stateWith([fieldRow(field({ key: '' }))]);
    expect(buildDocument(st).issues.length).toBeGreaterThan(0);
    st.sections[0].ignore = true;
    expect(buildDocument(st).issues).toEqual([]);
  });

  it('drops a stored table field with a wrongly typed property instead of throwing', () => {
    const st = stateWith([fieldRow(field({ key: 'temperature', offset: '1' }))]);
    const text = buildDocument(st).text as string;
    const doc = JSON.parse(text);
    doc.table.sections[0].rows[0].field.offset = null;
    const parsed = parseDocument(JSON.stringify(doc));
    expect(parsed.ok).toBe(true);
    if (parsed.ok) expect(parsed.state.sections[0].rows[0].kind).toBe('cel');
  });
});
