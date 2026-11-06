// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The few pieces of CEL SYNTAX the field table writes. Everything a person typed into a form
// field reaches an expression through one of these, so each one either produces a token that
// cannot change the shape of the expression around it, or refuses.
//
// 🔴 A JSON path segment is the only free text the table embeds in an expression (offsets and
// scales are numbers, and the metric key is a document value, not CEL). `celString` is what
// stops `a"] || true || json["` from closing the literal and rewriting the expression, so it
// is the function the injection tests and the negative control aim at.

/** A CEL string literal for `s`: double-quoted, every character that could end or reshape it escaped. */
export function celString(s: string): string {
  let out = '"';
  for (const ch of s) {
    const cp = ch.codePointAt(0) as number;
    if (ch === '\\') out += '\\\\';
    else if (ch === '"') out += '\\"';
    else if (ch === '\n') out += '\\n';
    else if (ch === '\r') out += '\\r';
    else if (ch === '\t') out += '\\t';
    // A lone surrogate is not a character CEL can hold; it cannot be written, so it is replaced.
    else if (cp >= 0xd800 && cp <= 0xdfff) out += '\\uFFFD';
    else if (cp < 0x20 || (cp >= 0x7f && cp <= 0x9f) || cp === 0x2028 || cp === 0x2029) {
      out += '\\u' + cp.toString(16).toUpperCase().padStart(4, '0');
    } else out += ch;
  }
  return out + '"';
}

const NUMBER = /^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/;

/** Parses a number typed into a field. Strict: no hex, no Infinity, no surrounding junk. null when it is not one. */
export function parseFinite(s: string): number | null {
  const t = s.trim();
  if (!NUMBER.test(t)) return null;
  const n = Number(t);
  return Number.isFinite(n) ? n : null;
}

/** A CEL double literal for the magnitude of a finite number: `10` becomes `10.0`, never a bare int. */
export function celDouble(n: number): string {
  let s = String(Math.abs(n));
  if (/^\d+$/.test(s)) s += '.0';
  return s;
}

/** A CEL int literal for a non-negative integer. */
export function celInt(n: number): string {
  return String(Math.trunc(Math.abs(n)));
}

const IDENT = /^[A-Za-z_][A-Za-z0-9_]*$/;
// Words CEL reserves: `json.in` would not parse, so such a segment cannot be a `has()` select.
const RESERVED = new Set([
  'as', 'break', 'const', 'continue', 'else', 'for', 'function', 'if', 'import', 'let', 'loop',
  'package', 'namespace', 'return', 'var', 'void', 'while', 'in', 'true', 'false', 'null',
]);

/** Whether `s` can follow a dot in a field selection (`json.s`). */
export function isSelectable(s: string): boolean {
  return IDENT.test(s) && !RESERVED.has(s);
}

/** Whether `s` is a legal name for a `let` binding. */
export function isIdentifier(s: string): boolean {
  return IDENT.test(s) && !RESERVED.has(s);
}

/**
 * Whether `expr` is safe to place inside parentheses: its quotes close, its brackets balance
 * and never go negative, and it holds no comment (a `//` would swallow the closing paren
 * that follows). A hand-written expression is the author's own program, but it must not be
 * able to break out of the wrapper the table puts around it.
 */
export function wrappable(expr: string): boolean {
  const stack: string[] = [];
  const pair: Record<string, string> = { ')': '(', ']': '[', '}': '{' };
  for (let i = 0; i < expr.length; i++) {
    const c = expr[i];
    if (c === '"' || c === "'") {
      // A raw string is prefixed r or R, optionally with a b or B (bytes) before or after it: r, br, rb.
      const m = /(?:^|[^A-Za-z0-9_])([bB]?[rR]|[rR][bB])$/.exec(expr.slice(0, i));
      const raw = m !== null;
      const triple = expr.startsWith(c.repeat(3), i);
      const quote = triple ? c.repeat(3) : c;
      i += quote.length;
      let closed = false;
      for (; i < expr.length; i++) {
        if (!raw && expr[i] === '\\') {
          i++;
          continue;
        }
        if (expr.startsWith(quote, i)) {
          i += quote.length - 1;
          closed = true;
          break;
        }
        if (!triple && expr[i] === '\n') return false;
      }
      if (!closed) return false;
    } else if (c === '/' && expr[i + 1] === '/') {
      return false;
    } else if (c === '(' || c === '[' || c === '{') {
      stack.push(c);
    } else if (c === ')' || c === ']' || c === '}') {
      if (stack.pop() !== pair[c]) return false;
    }
  }
  return stack.length === 0;
}

/**
 * `(a) && (b)`; a single part is returned as written. It refuses a part that is not
 * `wrappable`, because a stray `)` or `//` would otherwise let one side rewrite the other.
 */
export function andJoin(parts: string[]): string {
  const live = parts.map((p) => p.trim()).filter((p) => p !== '');
  if (live.length <= 1) return live[0] ?? '';
  for (const p of live) {
    if (!wrappable(p)) throw new Error('an expression cannot be combined: its brackets or quotes do not balance, or it holds a comment');
  }
  return live.map((p) => `(${p})`).join(' && ');
}
