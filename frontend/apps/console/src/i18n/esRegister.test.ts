// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

// The Spanish console addresses the operator as TÚ ("Selecciona", "tus cambios") and calls a
// tenant an "inquilino". About a hundred strings were moved off the formal USTED register in
// one pass; this keeps the next string a translator adds from quietly reverting it.
//
// Deliberately narrow: only forms that are formal imperatives (or the pronoun itself) in
// every position a UI string uses them. Ambiguous ones are left out on purpose — "su" is
// usually "its", and "revise"/"seleccione" after "que" are ordinary subjunctives — because a
// guard that fires on correct Spanish gets switched off.
const files = import.meta.glob('./locales/es/*.json', { eager: true }) as Record<
  string,
  { default: Record<string, unknown> }
>;

const FORMAL =
  /\b(Seleccione|Elija|Haga|Introduzca|Escriba|Pulse|Asegúrese|Ingrese|Utilice|Inténtelo|Recargue|Compruebe|Cópiela|Cópielo|Debe corregir|haga clic|usted|Usted)\b/u;
const ANGLICISM = /\btenants?\b/u;

function strings(value: unknown, path: string, out: Array<[string, string]>) {
  if (typeof value === 'string') out.push([path, value]);
  else if (value && typeof value === 'object') {
    for (const [k, v] of Object.entries(value)) strings(v, `${path}.${k}`, out);
  }
}

describe('es register', () => {
  const all: Array<[string, string]> = [];
  for (const [file, mod] of Object.entries(files)) strings(mod.default, file, all);

  it('reads every es catalog', () => {
    expect(all.length).toBeGreaterThan(100);
  });

  it('uses tú, not usted', () => {
    const hits = all.filter(([, s]) => FORMAL.test(s)).map(([p, s]) => `${p}: ${s}`);
    expect(hits).toEqual([]);
  });

  it('says inquilino, not tenant (interpolation variables aside)', () => {
    const hits = all
      .filter(([, s]) => ANGLICISM.test(s.replace(/\{\{[^}]*\}\}/g, '')))
      .map(([p, s]) => `${p}: ${s}`);
    expect(hits).toEqual([]);
  });
});
