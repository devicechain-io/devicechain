// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import {
  WidgetCatalogError,
  collectCatalogIssues,
  sealCatalog,
  type DescriptorIssue,
  type WidgetCatalog,
  type WidgetDescriptor,
} from '@devicechain/dashboards';

import type { ErasedWidgetDefinition } from './define-widget';

// Thrown by createWidgetRegistry with EVERY problem found, so a host fixes its composition
// in one pass instead of one failed start per fault.
export class WidgetRegistryError extends WidgetCatalogError {
  constructor(issues: readonly DescriptorIssue[]) {
    super(issues, 'widget registry');
    this.name = 'WidgetRegistryError';
  }
}

// A sealed registry: descriptors (through the React-free catalog the parser sees) plus the
// trusted implementations. Immutable — there is no register(), no process-global state and
// no replacement while a dashboard is mounted. Two registries are independent.
export interface WidgetRegistry {
  // The React-free view of this registry, for parsing and headless validation.
  readonly catalog: WidgetCatalog;
  descriptor(id: string): WidgetDescriptor | undefined;
  implementation(id: string): ErasedWidgetDefinition['implementation'] | undefined;
  // Definitions in the order they were supplied.
  list(): readonly ErasedWidgetDefinition[];
}

function isComponent(value: unknown): boolean {
  if (typeof value === 'function') return true;
  // memo / forwardRef / lazy components are objects tagged with $$typeof.
  return typeof value === 'object' && value !== null && '$$typeof' in value;
}

type Loose = Partial<ErasedWidgetDefinition> | null | undefined;

// createWidgetRegistry validates every definition (descriptor invariants, duplicate ids,
// the implementation shape) and returns a deeply frozen registry, or throws one
// WidgetRegistryError listing all the problems.
export function createWidgetRegistry(definitions: readonly ErasedWidgetDefinition[]): WidgetRegistry {
  if (!Array.isArray(definitions)) {
    throw new WidgetRegistryError([{ id: '#?', path: '', message: 'definitions must be an array' }]);
  }
  const issues: DescriptorIssue[] = collectCatalogIssues(definitions.map((d) => (d as Loose)?.descriptor));

  definitions.forEach((definition, index) => {
    const id: unknown = (definition as Loose)?.descriptor?.id;
    const label = typeof id === 'string' && id !== '' ? id : `#${index}`;
    const add = (path: string, message: string): void => {
      issues.push({ id: label, path, message });
    };
    const impl = (definition as Loose)?.implementation;
    if (typeof impl !== 'object' || impl === null) {
      add('implementation', 'a definition needs an implementation');
      return;
    }
    if (impl.kind !== 'trusted-react') {
      add('implementation.kind', `kind must be "trusted-react", got ${JSON.stringify(impl.kind)}`);
    }
    if (!isComponent(impl.component)) add('implementation.component', 'component must be a React component');
    if (impl.editor !== undefined && !isComponent(impl.editor)) {
      add('implementation.editor', 'editor, when given, must be a React component');
    }
  });

  if (issues.length > 0) throw new WidgetRegistryError(issues);

  const catalog = sealCatalog(definitions.map((d) => d.descriptor));
  const byId = new Map<string, ErasedWidgetDefinition>();
  for (const definition of definitions) {
    // Copy the implementation before freezing it: the caller's own object is never mutated.
    byId.set(
      definition.descriptor.id,
      Object.freeze({
        descriptor: catalog.get(definition.descriptor.id) as WidgetDescriptor,
        implementation: Object.freeze({ ...definition.implementation }),
      }),
    );
  }
  const ordered: readonly ErasedWidgetDefinition[] = Object.freeze([...byId.values()]);

  return Object.freeze({
    catalog,
    descriptor: (id: string) => catalog.get(id),
    implementation: (id: string) => byId.get(id)?.implementation,
    list: () => ordered,
  });
}
