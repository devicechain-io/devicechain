// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The user-guide screenshot recipes (hack/guide-capture) select on data-shot* attributes
// the shared primitives render. hack/guide-capture/check-anchors.mjs proves an anchor is
// present in the source; THESE tests prove the primitives render the right attribute on
// the right element (e.g. a drawer is "drawer", not "sheet"). See CONVENTIONS.md.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { useEffect } from 'react';

import { FormDrawer } from '@/components/registry/FormDrawer';
import { Sheet, SheetContent, SheetDescription, SheetTitle } from '@/components/ui/sheet';
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { DataTable, DataTableBody, DataTableCell, DataTableRow } from '@/components/ui/data-table';
import { ConfirmProvider, useConfirm } from '@/components/ui/confirm-dialog';
import { EmptyState } from '@/components/ui/empty-state';
import { ErrorState } from '@/components/ui/error-state';
import { LoadingState } from '@/components/ui/loading-state';
import { Combobox } from '@/components/ui/combobox';
import { MultiSelect } from '@/components/ui/multi-select';
import { SegmentedControl } from '@/components/ui/segmented-control';

afterEach(() => cleanup());

describe('guide anchors on the shared primitives', () => {
  it('FormDrawer is data-shot="drawer" and carries its shot id as data-shot-drawer', () => {
    render(
      <FormDrawer open onOpenChange={() => {}} title="T" shot="device-new">
        <p>body</p>
      </FormDrawer>,
    );
    expect(document.querySelectorAll('[data-shot="drawer"][data-shot-drawer="device-new"]')).toHaveLength(1);
    expect(document.querySelectorAll('[data-shot="sheet"]')).toHaveLength(0);
  });

  it('a FormDrawer without a shot id renders no data-shot-drawer at all', () => {
    render(
      <FormDrawer open onOpenChange={() => {}} title="T">
        <p>body</p>
      </FormDrawer>,
    );
    const el = document.querySelector('[data-shot="drawer"]');
    expect(el).not.toBeNull();
    expect(el!.hasAttribute('data-shot-drawer')).toBe(false);
  });

  it('a plain Sheet is data-shot="sheet"', () => {
    render(
      <Sheet open>
        <SheetContent>
          <SheetTitle>T</SheetTitle>
          <SheetDescription>D</SheetDescription>
        </SheetContent>
      </Sheet>,
    );
    expect(document.querySelectorAll('[data-shot="sheet"]')).toHaveLength(1);
  });

  it('a TabsTrigger renders data-shot-tab with its value', () => {
    render(
      <Tabs defaultValue="basic">
        <TabsList>
          <TabsTrigger value="basic">Basic</TabsTrigger>
          <TabsTrigger value="events">Events</TabsTrigger>
        </TabsList>
      </Tabs>,
    );
    expect(screen.getByText('Events').getAttribute('data-shot-tab')).toBe('events');
    expect(screen.getByText('Basic').getAttribute('data-shot-tab')).toBe('basic');
  });

  it('DataTableCell passes data-shot-mask through and is unmarked otherwise', () => {
    render(
      <DataTable>
        <DataTableBody>
          <DataTableRow>
            <DataTableCell data-shot-mask>live</DataTableCell>
            <DataTableCell>steady</DataTableCell>
          </DataTableRow>
        </DataTableBody>
      </DataTable>,
    );
    expect(screen.getByText('live').hasAttribute('data-shot-mask')).toBe(true);
    expect(screen.getByText('steady').hasAttribute('data-shot-mask')).toBe(false);
  });

  it('the confirm dialog is data-shot="confirm"', async () => {
    function Opener() {
      const confirm = useConfirm();
      useEffect(() => {
        void confirm({ title: 'Sure?' });
      }, [confirm]);
      return null;
    }
    render(
      <ConfirmProvider>
        <Opener />
      </ConfirmProvider>,
    );
    expect(await screen.findByText('Sure?')).toBeTruthy();
    expect(document.querySelectorAll('[data-shot="confirm"]')).toHaveLength(1);
  });

  it('a Combobox marks its trigger, and each option carries its value once it is open', () => {
    render(<Combobox options={[{ value: 'a', label: 'Alpha' }, { value: 'b', label: 'Beta' }]} value="" onChange={() => {}} />);
    const trigger = document.querySelector('[data-shot="combobox"]');
    expect(trigger).not.toBeNull();
    fireEvent.click(trigger!);
    const options = [...document.querySelectorAll('[data-shot="combobox-option"]')];
    expect(options.map((o) => o.getAttribute('data-shot-value'))).toEqual(['a', 'b']);
  });

  it('a MultiSelect marks its trigger and options the same way', () => {
    render(<MultiSelect options={[{ value: 'x', label: 'Ex' }]} value={[]} onChange={() => {}} />);
    fireEvent.click(document.querySelector('[data-shot="combobox"]')!);
    expect(document.querySelector('[data-shot="combobox-option"]')?.getAttribute('data-shot-value')).toBe('x');
  });

  it('a SegmentedControl option carries its value as data-shot-option', () => {
    render(<SegmentedControl ariaLabel="mode" value="form" onValueChange={() => {}} options={[{ value: 'form', label: 'Form' }, { value: 'canvas', label: 'Canvas' }]} />);
    expect([...document.querySelectorAll('[data-shot-option]')].map((o) => o.getAttribute('data-shot-option'))).toEqual(['form', 'canvas']);
  });

  it('the shared state components mark themselves empty / error / loading', () => {
    render(
      <>
        <EmptyState description="e" />
        <ErrorState description="x" />
        <LoadingState description="l" />
      </>,
    );
    expect(document.querySelectorAll('[data-shot="empty"]')).toHaveLength(1);
    expect(document.querySelectorAll('[data-shot="error"]')).toHaveLength(1);
    expect(document.querySelectorAll('[data-shot="loading"]')).toHaveLength(1);
  });
});
