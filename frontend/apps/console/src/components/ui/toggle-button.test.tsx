// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ToggleButton } from './toggle-button';

afterEach(cleanup);

describe('ToggleButton', () => {
  // 🔴 THE OFF STATE IS THE ONE WORTH ASSERTING. Testing only `pressed` would pass
  // against a component that emitted aria-pressed="true" unconditionally — and "off"
  // is the state nearly every control on a page is in, so it is the one a screen
  // reader announces most often.
  it('publishes its state in both directions', () => {
    render(
      <>
        <ToggleButton pressed={false}>off</ToggleButton>
        <ToggleButton pressed>on</ToggleButton>
      </>,
    );
    expect(screen.getByRole('button', { name: 'off' }).getAttribute('aria-pressed')).toBe('false');
    expect(screen.getByRole('button', { name: 'on' }).getAttribute('aria-pressed')).toBe('true');
  });

  // Inside a <form>, a button with no explicit type submits it. Every caller here sits
  // in or near a form, so the default is fixed rather than left to the caller — a
  // toggle that saved the form on click is the failure this prevents.
  it('never submits the form it sits in', () => {
    const onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault());
    render(
      <form onSubmit={onSubmit}>
        <ToggleButton pressed={false}>toggle</ToggleButton>
      </form>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'toggle' }));
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it('forwards click and disabled through to the element', () => {
    const onClick = vi.fn();
    render(
      <ToggleButton pressed={false} disabled onClick={onClick}>
        toggle
      </ToggleButton>,
    );
    const btn = screen.getByRole('button', { name: 'toggle' }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    fireEvent.click(btn);
    expect(onClick).not.toHaveBeenCalled();
  });

  it('keeps the caller className alongside the kit treatment', () => {
    render(
      <ToggleButton pressed={false} className="bespoke-shape">
        toggle
      </ToggleButton>,
    );
    const btn = screen.getByRole('button', { name: 'toggle' });
    expect(btn.className).toContain('bespoke-shape');
    // The reason the primitive exists at all: focus is visible without the caller
    // remembering to ask for it.
    expect(btn.className).toContain('focus-visible:ring-ring');
  });
});
