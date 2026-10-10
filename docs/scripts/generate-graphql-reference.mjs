// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Renders one reference page per published GraphQL schema, in every locale, from the
// same sanitized SDL that generate-schema.mjs publishes at /schema/.
//
// Generated at build time and never committed, for the reason the SDL itself is: a
// copy in the tree is a copy that can drift from the schemas the services parse.
//
// The pages feed a SECOND instance of the docs plugin (id graphql-reference, served
// at /reference/graphql/), not the hand-written docs tree. That is deliberate:
//
//   - the docs plugin reads its folder when Docusaurus starts, so the pages have to
//     exist before it does — which is why this runs as a prebuild step, beside the
//     schema publisher, rather than inside a plugin of its own;
//   - the guards that walk docs/docs and its translations (locale parity, translation
//     freshness, the docs GraphQL example check) see only what people wrote. A
//     generated page there would be checked as though someone had written it, and
//     would exist only on machines that had run a build.
//
// Only the page chrome is translated (title, introduction, labels). The reference
// body — the schema's own descriptions — is English in every locale, as the dcctl
// command reference is.
//
// Descriptions are the schema's """ strings, read by graphql-js. A # comment is a
// maintainer note and never reaches these pages: graphql-js does not read comments.

import { readFileSync, writeFileSync, mkdirSync, rmSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

import {
  buildSchema, isObjectType, isInterfaceType, isUnionType, isEnumType, isInputObjectType,
  isScalarType, isSpecifiedScalarType, isIntrospectionType, getNamedType, print,
} from 'graphql';

import { REPO, GenerateError, buildArtifacts, resolve } from './generate-schema.mjs';
import { scan, formatFindings } from './gate.mjs';
import { PLANES, SCHEMAS, REQUIRED_OUTPUTS } from './schemas.manifest.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
export const DOCS = join(HERE, '..');

/** The docs-plugin instance the pages belong to. docusaurus.config.ts names the same id. */
export const PLUGIN_ID = 'graphql-reference';
/** Where the English pages are written: the instance's `path`. */
export const CONTENT_DIR = join('generated', 'graphql-reference');

/** Where a locale's copy lives, relative to docs/. English is the instance's own path. */
export function localeDir(locale) {
  return locale === 'en'
    ? CONTENT_DIR
    : join('i18n', locale, `docusaurus-plugin-content-docs-${PLUGIN_ID}`, 'current');
}

/**
 * The page chrome, per locale. Every locale the site builds must have an entry: a
 * locale without one would get the English page through Docusaurus's fallback, which
 * builds green and is exactly the silent gap the tests exist to rule out.
 */
export const CHROME = {
  en: {
    indexTitle: 'GraphQL API reference',
    indexIntro:
      'One page per GraphQL schema DeviceChain serves, generated from the schemas the '
      + 'services parse at startup, so it cannot fall behind them. Each page lists the '
      + 'queries, mutations and subscriptions first, then every type. For endpoints, '
      + 'tokens, conventions and limits, see [the GraphQL API overview](/reference/graphql-api).',
    pageTitle: (name) => `GraphQL reference: ${name}`,
    pageIntro:
      'Generated from the schema this service serves, so it cannot fall behind it. The '
      + 'same schema is published as a file for tools and agents.',
    englishOnly: null,
    schema: 'Schema',
    plane: 'Auth plane',
    endpoint: 'Endpoint',
    token: 'Authorize with',
    file: 'Schema file',
    note: 'Note',
    described: 'Described',
    coverage: (d, t) => `${d} of ${t} elements`,
  },
  es: {
    indexTitle: 'Referencia de la API GraphQL',
    indexIntro:
      'Una página por cada esquema GraphQL que sirve DeviceChain, generada a partir de los '
      + 'esquemas que los servicios analizan al arrancar, así que no puede quedarse atrás. '
      + 'Cada página enumera primero las consultas, mutaciones y suscripciones, y después '
      + 'todos los tipos. Para endpoints, tokens, convenciones y límites, consulta '
      + '[la descripción general de la API GraphQL](/reference/graphql-api).',
    pageTitle: (name) => `Referencia GraphQL: ${name}`,
    pageIntro:
      'Generada a partir del esquema que sirve este servicio, así que no puede quedarse '
      + 'atrás. El mismo esquema se publica como archivo para herramientas y agentes.',
    englishOnly: 'La referencia que sigue se genera a partir del esquema y está solo en inglés.',
    schema: 'Esquema',
    plane: 'Plano de autenticación',
    endpoint: 'Endpoint',
    token: 'Autorización',
    file: 'Archivo del esquema',
    note: 'Nota',
    described: 'Descritos',
    coverage: (d, t) => `${d} de ${t} elementos`,
  },
  'zh-CN': {
    indexTitle: 'GraphQL API 参考',
    indexIntro:
      'DeviceChain 提供的每个 GraphQL Schema 各有一页，由各服务启动时解析的 Schema 自动生成，因此不会落后于实现。'
      + '每页先列出查询、变更和订阅，然后列出所有类型。有关端点、令牌、约定和限制，请参阅'
      + '[GraphQL API 概述](/reference/graphql-api)。',
    pageTitle: (name) => `GraphQL 参考：${name}`,
    pageIntro: '本页由该服务提供的 Schema 自动生成，因此不会落后于实现。同一 Schema 也以文件形式发布，供工具和智能体使用。',
    englishOnly: '下面的参考内容由 Schema 自动生成，仅提供英文版本。',
    schema: 'Schema',
    plane: '认证平面',
    endpoint: '端点',
    token: '授权方式',
    file: 'Schema 文件',
    note: '说明',
    described: '已描述',
    coverage: (d, t) => `${t} 个元素中的 ${d} 个`,
  },
};

export const LOCALES = Object.keys(CHROME);

const NO_DESCRIPTION = '_No description yet._';

// ---------------------------------------------------------------------------
// MDX escaping.
//
// Docusaurus parses these pages as MDX, where a bare `<x>` is an unclosed JSX tag and a
// bare `{` opens an expression — either one fails the whole site build. MDX also has no
// indented code blocks, so an indented run would reflow into a paragraph (or, for a `#`
// line, become a heading). The rules mirror the dcctl reference generator
// (backend/cli/cmd/docs.go), which met all three: fences and inline code spans are
// literal and left alone, an indented run is fenced verbatim, and everything else gets
// `<`, `{`, `}` and `&` backslash-escaped.
// ---------------------------------------------------------------------------

const ESCAPES = { '<': '\\<', '{': '\\{', '}': '\\}', '&': '\\&' };
const escapeProse = (s) => s.replace(/[<{}&]/g, (c) => ESCAPES[c]);

/** Escapes one line of prose, leaving inline code spans (a run of N backticks to the next run of exactly N) alone. */
export function escapeInline(line) {
  let out = '';
  let i = 0;
  while (i < line.length) {
    if (line[i] !== '`') {
      let j = line.indexOf('`', i);
      if (j < 0) j = line.length;
      out += escapeProse(line.slice(i, j));
      i = j;
      continue;
    }
    let n = 0;
    while (line[i + n] === '`') n++;
    const end = closingRun(line, i + n, n);
    if (end < 0) {
      // No closer: the backticks are literal text, as in CommonMark.
      out += line.slice(i, i + n);
      i += n;
      continue;
    }
    out += line.slice(i, end + n);
    i = end + n;
  }
  return out;
}

function closingRun(line, from, n) {
  for (let i = from; i < line.length;) {
    if (line[i] !== '`') { i++; continue; }
    let m = 0;
    while (line[i + m] === '`') m++;
    if (m === n) return i;
    i += m;
  }
  return -1;
}

function fenceRun(s) {
  if (!s || (s[0] !== '`' && s[0] !== '~')) return [null, 0];
  let n = 0;
  while (s[n] === s[0]) n++;
  if (n < 3) return [null, 0];
  // CommonMark: a backtick fence's info string cannot contain a backtick. A line like
  // ```code``` {x} is an inline span, not a fence — taking it for one would leave its
  // {x} unescaped (MDX evaluates it at build time) and let the next ``` "close" a
  // fence that never opened, swallowing the page.
  if (s[0] === '`' && s.slice(n).includes('`')) return [null, 0];
  return [s[0], n];
}

// A line starting with `import` or `export` is MDX ESM: it is executed at build time,
// and ordinary prose ("export the data as CSV.") fails to parse and breaks the whole
// site build. A character reference for the first letter keeps the word on the page
// and out of the ESM grammar.
const ESM_LINE = /^([ \t]*)(?:(i)(?=mport\b)|(e)(?=xport\b))/;
export function escapeEsm(line) {
  return line.replace(ESM_LINE, (_, indent, i) => `${indent}${i ? '&#105;' : '&#101;'}`);
}

const isIndented = (line) => line.startsWith('    ') || line.startsWith('\t');

/** Escapes a multi-line description for use as a block of markdown. */
export function escapeBlock(text) {
  const lines = text.split('\n');
  const out = [];
  let fenceCh = null;
  let fenceLen = 0;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const trimmed = line.trimStart();
    if (fenceLen > 0) {
      out.push(line);
      const [ch, n] = fenceRun(trimmed);
      if (ch === fenceCh && n >= fenceLen && trimmed.slice(n).trim() === '') fenceLen = 0;
      continue;
    }
    const [ch, n] = fenceRun(trimmed);
    if (n >= 3) {
      fenceCh = ch;
      fenceLen = n;
      out.push(line);
      continue;
    }
    if (isIndented(line)) {
      let j = i;
      for (let k = i; k < lines.length; k++) {
        if (isIndented(lines[k])) j = k;
        else if (lines[k].trim() !== '') break;
      }
      out.push('```', ...lines.slice(i, j + 1), '```');
      i = j;
      continue;
    }
    out.push(escapeEsm(escapeInline(line)));
  }
  // An unterminated fence would swallow the rest of the page, so close it here.
  if (fenceLen > 0) out.push(fenceCh.repeat(fenceLen));
  return out.join('\n');
}

/**
 * Escapes a description for one table cell. A cell is one line, so paragraphs are
 * joined with explicit breaks and the lines within one are joined with spaces; a `|`
 * would end the cell, so it is escaped everywhere, code spans included (GFM unescapes
 * it there too).
 */
export function escapeCell(text) {
  return text
    .split(/\n[ \t]*\n/)
    .map((para) => escapeInline(para.split('\n').map((l) => l.trim()).join(' ')).replace(/\|/g, '\\|'))
    .filter((p) => p !== '')
    .join('<br /><br />');
}

// ---------------------------------------------------------------------------
// Rendering.
// ---------------------------------------------------------------------------

/** A type we render a section for — the schema's own types, not the built-ins. */
function isOwnType(t) {
  return !isIntrospectionType(t) && !(isScalarType(t) && isSpecifiedScalarType(t));
}

/** `[Device!]!`, linked to Device's section when Device is one of the schema's own types. */
function typeRef(type) {
  const label = `\`${String(type)}\``;
  const named = getNamedType(type);
  return isOwnType(named) ? `[${label}](#${named.name})` : label;
}

function deprecation(el) {
  if (el.deprecationReason == null) return '';
  return `**Deprecated.** ${escapeInline(el.deprecationReason.split('\n').join(' '))}`;
}

function cell(el) {
  const parts = [];
  parts.push(el.description ? escapeCell(el.description) : NO_DESCRIPTION);
  const dep = deprecation(el);
  if (dep) parts.push(dep.replace(/\|/g, '\\|'));
  return parts.join('<br /><br />');
}

function block(el) {
  const parts = [el.description ? escapeBlock(el.description) : NO_DESCRIPTION];
  const dep = deprecation(el);
  if (dep) parts.push(`:::warning\n${dep}\n:::`);
  return parts.join('\n\n');
}

const byName = (a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0);

function argsTable(args) {
  if (!args.length) return '';
  const rows = [...args].sort(byName)
    .map((a) => `| \`${a.name}\` | ${typeRef(a.type)}${defaultSuffix(a)} | ${cell(a)} |`);
  return ['| Argument | Type | Description |', '|---|---|---|', ...rows].join('\n');
}

/**
 * The SDL default of an argument or input field, printed as GraphQL, or undefined.
 * graphql 17 keeps it as the parsed literal (`default.literal`); `defaultValue` is the
 * graphql 16 coerced form, read only as a fallback.
 */
export function defaultOf(el) {
  if (el.default?.literal) return print(el.default.literal);
  if (el.default && 'value' in el.default) return JSON.stringify(el.default.value);
  if (el.defaultValue !== undefined) return JSON.stringify(el.defaultValue);
  return undefined;
}

/** ` = `value`` for a table cell, or ''. A default holding backticks gets a wider span. */
function defaultSuffix(el) {
  const d = defaultOf(el);
  if (d === undefined) return '';
  const span = d.includes('`') ? `\`\` ${d} \`\`` : `\`${d}\``;
  return ` = ${span}`.replace(/\|/g, '\\|');
}

/** A field cell that names its arguments, for the rare non-root field that takes some. */
function fieldCell(f) {
  const base = cell(f);
  if (!f.args?.length) return base;
  const args = [...f.args].sort(byName)
    .map((a) => `\`${a.name}\`: ${typeRef(a.type)}${defaultSuffix(a)} — ${cell(a)}`)
    .join('<br />');
  return `${base}<br /><br />**Arguments:**<br />${args}`;
}

function fieldsTable(fields, head = 'Field') {
  const rows = [...fields].sort(byName)
    .map((f) => `| \`${f.name}\` | ${typeRef(f.type)}${defaultSuffix(f)} | ${fieldCell(f)} |`);
  return [`| ${head} | Type | Description |`, '|---|---|---|', ...rows].join('\n');
}

const ROOTS = [
  { key: 'getQueryType', heading: 'Queries', prefix: 'query' },
  { key: 'getMutationType', heading: 'Mutations', prefix: 'mutation' },
  { key: 'getSubscriptionType', heading: 'Subscriptions', prefix: 'subscription' },
];

const KINDS = [
  { heading: 'Objects', anchor: 'objects', is: isObjectType, kind: 'object' },
  { heading: 'Interfaces', anchor: 'interfaces', is: isInterfaceType, kind: 'interface' },
  { heading: 'Unions', anchor: 'unions', is: isUnionType, kind: 'union' },
  { heading: 'Input types', anchor: 'input-types', is: isInputObjectType, kind: 'input' },
  { heading: 'Enums', anchor: 'enums', is: isEnumType, kind: 'enum' },
  { heading: 'Scalars', anchor: 'scalars', is: isScalarType, kind: 'scalar' },
];

/**
 * Counts what a description could be on — every element the sdldesc guard requires one
 * for: own non-root types, fields, input fields, arguments and enum values.
 */
export function coverage(schema) {
  const roots = new Set(ROOTS.map((r) => schema[r.key]()).filter(Boolean));
  let total = 0;
  let described = 0;
  const count = (el) => { total++; if (el.description) described++; };
  for (const t of Object.values(schema.getTypeMap())) {
    if (!isOwnType(t)) continue;
    if (!roots.has(t)) count(t);
    if (isObjectType(t) || isInterfaceType(t) || isInputObjectType(t)) {
      for (const f of Object.values(t.getFields())) {
        count(f);
        for (const a of f.args ?? []) count(a);
      }
    }
    if (isEnumType(t)) t.getValues().forEach(count);
  }
  return { described, total };
}

/** The reference body: identical in every locale. */
export function renderBody(schema) {
  const out = [];
  const roots = [];
  for (const r of ROOTS) {
    const root = schema[r.key]();
    if (!root) continue;
    roots.push(root);
    const fields = Object.values(root.getFields()).sort(byName);
    out.push(`## ${r.heading} {#${r.prefix}}`, '');
    out.push(fields.map((f) => `[\`${f.name}\`](#${r.prefix}-${f.name})`).join(' · '), '');
    for (const f of fields) {
      out.push(`### \`${f.name}\` {#${r.prefix}-${f.name}}`, '');
      out.push(block(f), '');
      out.push(`**Returns** ${typeRef(f.type)}`, '');
      const args = argsTable(f.args);
      if (args) out.push(args, '');
    }
  }

  const own = Object.values(schema.getTypeMap())
    .filter((t) => isOwnType(t) && !roots.includes(t))
    .sort(byName);
  for (const k of KINDS) {
    const types = own.filter(k.is);
    if (!types.length) continue;
    out.push(`## ${k.heading} {#${k.anchor}}`, '');
    out.push(types.map((t) => `[\`${t.name}\`](#${t.name})`).join(' · '), '');
    for (const t of types) {
      out.push(`### \`${t.name}\` {#${t.name}}`, '');
      out.push(`_${k.kind}_`, '');
      out.push(block(t), '');
      if (isObjectType(t) || isInterfaceType(t)) {
        const ifaces = t.getInterfaces();
        if (ifaces.length) out.push(`**Implements** ${ifaces.map(typeRef).join(', ')}`, '');
        out.push(fieldsTable(Object.values(t.getFields())), '');
      } else if (isInputObjectType(t)) {
        out.push(fieldsTable(Object.values(t.getFields()), 'Input field'), '');
      } else if (isUnionType(t)) {
        out.push(`**One of** ${t.getTypes().map(typeRef).join(', ')}`, '');
      } else if (isEnumType(t)) {
        const rows = t.getValues().map((v) => `| \`${v.name}\` | ${cell(v)} |`);
        out.push(['| Value | Description |', '|---|---|', ...rows].join('\n'), '');
      }
    }
  }
  return out.join('\n');
}

const frontMatter = (fields) => [
  '---',
  ...Object.entries(fields).map(([k, v]) => `${k}: ${typeof v === 'string' ? JSON.stringify(v) : v}`),
  '---',
  '',
].join('\n');

/** Name a page is published under: the schema file's name without its extension. */
export const pageName = (published) => published.replace(/\.graphql$/, '');

function renderPage(c, s, schema, body, position) {
  const name = pageName(s.published);
  const { described, total } = coverage(schema);
  const plane = PLANES[s.plane];
  const rows = [
    [c.endpoint, `\`https://<your-host>${s.endpoint}\``],
    [c.plane, `**${s.plane}** — ${escapeCell(plane.description)}`],
    [c.token, escapeCell(plane.token)],
    [c.file, `[\`/schema/${s.published}\`](pathname:///schema/${s.published})`],
    [c.described, c.coverage(described, total)],
  ];
  if (s.note) rows.push([c.note, escapeCell(s.note)]);
  return [
    frontMatter({
      title: c.pageTitle(name),
      sidebar_label: name,
      sidebar_position: position,
      toc_max_heading_level: 2,
      custom_edit_url: null,
    }),
    c.pageIntro,
    '',
    '| | |',
    '|---|---|',
    ...rows.map(([k, v]) => `| **${k}** | ${v} |`),
    '',
    ...(c.englishOnly ? [`:::note\n${c.englishOnly}\n:::`, ''] : []),
    body,
  ].join('\n');
}

function renderIndex(c, entries) {
  const rows = entries.map(({ s, cov }) => {
    const name = pageName(s.published);
    return `| [${name}](./${name}.md) | ${s.plane} | \`${s.endpoint}\` | ${c.coverage(cov.described, cov.total)} |`;
  });
  return [
    frontMatter({
      title: c.indexTitle,
      sidebar_label: c.indexTitle,
      sidebar_position: 0,
      slug: '/',
      custom_edit_url: null,
    }),
    c.indexIntro,
    '',
    `| ${c.schema} | ${c.plane} | ${c.endpoint} | ${c.described} |`,
    '|---|---|---|---|',
    ...rows,
    '',
  ].join('\n');
}

/**
 * Every page, in every locale, as { path (relative to docs/), text }. Throws rather
 * than returning a partial set.
 */
export function buildPages(repo = REPO, schemas = SCHEMAS, chrome = CHROME) {
  // The same artifacts /schema/ publishes: reconciled against the tree, sanitized, gated.
  const artifacts = new Map(buildArtifacts(repo, schemas).map((a) => [a.name, a.text]));
  const published = schemas.map(resolve).filter((s) => s.publish !== false)
    .sort((a, b) => (a.published < b.published ? -1 : 1));

  const entries = published.map((s) => {
    const sdl = artifacts.get(s.published);
    if (sdl === undefined) {
      throw new GenerateError(`no published SDL for ${s.published}`, 'The schema publisher and this generator disagree about what is published.');
    }
    let schema;
    try {
      schema = buildSchema(sdl);
    } catch (err) {
      throw new GenerateError(`${s.source} does not build as a GraphQL schema`, String(err.message ?? err));
    }
    return { s, schema, body: renderBody(schema), cov: coverage(schema) };
  });

  const pages = [];
  const locales = Object.keys(chrome);
  for (const locale of locales) {
    const dir = localeDir(locale);
    pages.push({ locale, name: 'index', path: join(dir, 'index.md'), text: renderIndex(chrome[locale], entries) });
    entries.forEach(({ s, schema, body }, i) => {
      const name = pageName(s.published);
      pages.push({ locale, name, path: join(dir, `${name}.md`), text: renderPage(chrome[locale], s, schema, body, i + 1) });
    });
  }

  assertCoverage(pages, published, locales);

  const findings = pages.flatMap((p) => scan(p.path, p.text));
  if (findings.length) {
    throw new GenerateError(
      `${findings.length} unpublishable reference(s) in the generated GraphQL reference`,
      `${formatFindings(findings)}\n\nReword the description in the schema source.`,
    );
  }
  return pages;
}

/**
 * The floor: every published schema has a page in every locale, checked BY NAME — a
 * count would look just as complete with one schema missing and another doubled.
 */
export function assertCoverage(pages, published, locales = LOCALES) {
  const want = published.map((s) => pageName(s.published));
  for (const required of REQUIRED_OUTPUTS) {
    if (!want.includes(pageName(required))) {
      throw new GenerateError(`required schema ${required} is not among the published schemas`);
    }
  }
  const missing = [];
  for (const locale of locales) {
    const have = new Set(pages.filter((p) => p.locale === locale).map((p) => p.name));
    for (const name of ['index', ...want]) if (!have.has(name)) missing.push(`${locale}: ${name}`);
  }
  if (missing.length) {
    throw new GenerateError(
      `${missing.length} GraphQL reference page(s) were not generated`,
      missing.map((m) => `  ${m}`).join('\n'),
    );
  }
}

/** The locales docusaurus.config.ts builds, read from its source. */
export function configuredLocales(docs = DOCS) {
  const config = readFileSync(join(docs, 'docusaurus.config.ts'), 'utf8');
  const m = /locales:\s*\[([^\]]*)\]/.exec(config);
  if (!m) throw new GenerateError('cannot find the locales list in docusaurus.config.ts');
  return [...m[1].matchAll(/'([^']+)'|"([^"]+)"/g)].map((x) => x[1] ?? x[2]);
}

function main() {
  let pages;
  try {
    const missing = configuredLocales().filter((l) => !CHROME[l]);
    if (missing.length) {
      throw new GenerateError(
        `no GraphQL reference chrome for locale(s): ${missing.join(', ')}`,
        'Add an entry to CHROME in scripts/generate-graphql-reference.mjs. Without one the\n'
        + 'locale would silently get the English pages.',
      );
    }
    pages = buildPages();
  } catch (err) {
    if (!(err instanceof GenerateError)) throw err;
    console.error(`\ngenerate-graphql-reference: ${err.message}\n`);
    process.exit(1);
  }

  // Own each output directory whole: a schema removed upstream must lose its page.
  for (const locale of LOCALES) {
    const dir = join(DOCS, localeDir(locale));
    rmSync(dir, { recursive: true, force: true });
    mkdirSync(dir, { recursive: true });
  }
  for (const p of pages) writeFileSync(join(DOCS, p.path), p.text);
  console.log(`generate-graphql-reference: wrote ${pages.length} page(s) in ${LOCALES.length} locale(s)`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main();

