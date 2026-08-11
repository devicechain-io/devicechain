// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { createRef } from 'react';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { IconButton } from './icon-button';

afterEach(cleanup);

describe('IconButton', () => {
  // The whole reason this is a component rather than a Button variant: an icon-only
  // control has no text, so without `label` it is a button a screen reader announces
  // as nothing at all. The type makes it mandatory; this checks it actually lands on
  // the element rather than being accepted and dropped.
  it('names the control from label', () => {
    render(
      <IconButton label="Close panel">
        <svg />
      </IconButton>,
    );
    expect(screen.getByRole('button', { name: 'Close panel' })).toBeTruthy();
  });

  it('shows the same text as a tooltip by default, and drops it when asked', () => {
    const { rerender } = render(
      <IconButton label="Close panel">
        <svg />
      </IconButton>,
    );
    expect(screen.getByRole('button', { name: 'Close panel' }).getAttribute('title')).toBe(
      'Close panel',
    );
    rerender(
      <IconButton label="Close panel" tooltip={false}>
        <svg />
      </IconButton>,
    );
    // The accessible name must survive losing the tooltip — they are two different
    // affordances, and collapsing them would make `tooltip={false}` silently unname
    // the control.
    expect(screen.getByRole('button', { name: 'Close panel' }).getAttribute('title')).toBeNull();
  });

  it('never submits the form it sits in', () => {
    const onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault());
    render(
      <form onSubmit={onSubmit}>
        <IconButton label="Copy">
          <svg />
        </IconButton>
      </form>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }));
    expect(onSubmit).not.toHaveBeenCalled();
  });

  // 🔴 The ref is load-bearing, not a convenience. The tier table's drag grip hands it
  // to dnd-kit's setActivatorNodeRef; a component that quietly swallowed the ref would
  // leave keyboard focus restoration after a drop pointing at nothing, which is not
  // visible in a screenshot and not caught by clicking around.
  it('forwards its ref to the underlying element', () => {
    const ref = createRef<HTMLButtonElement>();
    render(
      <IconButton ref={ref} label="Reorder">
        <svg />
      </IconButton>,
    );
    expect(ref.current).toBe(screen.getByRole('button', { name: 'Reorder' }));
  });

  it('passes arbitrary button props through, so a caller can spread a library handle', () => {
    render(
      <IconButton label="Reorder" data-testid="grip" disabled>
        <svg />
      </IconButton>,
    );
    const btn = screen.getByTestId('grip') as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });
});
