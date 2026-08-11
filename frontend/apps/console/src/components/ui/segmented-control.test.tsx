// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THESE TESTS ARE ABOUT THE PROPERTY THAT WAS MISSING, NOT THE ONE THAT WAS THERE.
// The seven hand-rolled segmented controls this replaces all worked: clicking a
// segment changed the value, and the selected one was visibly filled. What none of
// four of them did was SAY which was selected — the state existed only as a background
// colour. So the assertions below are on `aria-checked` and on keyboard reachability,
// not on which classes landed; a styling assertion would have passed against every one
// of the originals.

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { SegmentedControl } from './segmented-control';

afterEach(cleanup);

const FRUIT = [
  { value: 'apple', label: 'Apple' },
  { value: 'pear', label: 'Pear' },
  { value: 'plum', label: 'Plum' },
] as const;

type Fruit = (typeof FRUIT)[number]['value'];

function renderControl(props: Partial<React.ComponentProps<typeof SegmentedControl<Fruit>>> = {}) {
  const onValueChange = vi.fn();
  render(
    <SegmentedControl<Fruit>
      ariaLabel="Fruit"
      options={FRUIT}
      value="pear"
      onValueChange={onValueChange}
      {...props}
    />,
  );
  return { onValueChange };
}

describe('SegmentedControl', () => {
  it('publishes the selection, and publishes it on the UNSELECTED options too', () => {
    renderControl();
    // Both halves matter. Asserting only that "Pear" is checked would pass against a
    // control that marked every option checked, which is exactly as uninformative to a
    // screen reader as marking none.
    expect(screen.getByRole('radio', { name: 'Pear' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('radio', { name: 'Apple' }).getAttribute('aria-checked')).toBe('false');
    expect(screen.getByRole('radio', { name: 'Plum' }).getAttribute('aria-checked')).toBe('false');
  });

  it('names the group itself, so the options are not announced in a vacuum', () => {
    renderControl();
    expect(screen.getByRole('radiogroup', { name: 'Fruit' })).toBeTruthy();
  });

  it('reports the chosen value on click', () => {
    const { onValueChange } = renderControl();
    fireEvent.click(screen.getByRole('radio', { name: 'Plum' }));
    expect(onValueChange).toHaveBeenCalledWith('plum');
  });

  it('does not re-report the value already selected', () => {
    const { onValueChange } = renderControl();
    fireEvent.click(screen.getByRole('radio', { name: 'Pear' }));
    expect(onValueChange).not.toHaveBeenCalled();
  });

  // 🔴 The behaviour change the component's comment CLAIMS: before, each segment was its
  // own tab stop and arrow keys did nothing; now the group is ONE tab stop with arrow
  // navigation. An unverified claim in a comment is worse than no comment.
  //
  // Note where the tab stop actually is. The first version of this test asserted
  // tabindex=0 on the CHECKED SEGMENT and failed — Radix puts the tab stop on the
  // GROUP and moves focus inward on entry, so every segment starts at -1. The claim
  // ("one tab stop") was right; the mechanism I assumed was not.
  it('is a single tab stop: the group is tabbable and no segment is', () => {
    renderControl();
    expect(screen.getByRole('radiogroup').getAttribute('tabindex')).toBe('0');
    for (const radio of screen.getAllByRole('radio')) {
      expect(radio.getAttribute('tabindex')).toBe('-1');
    }
  });

  it('lands on the selected segment when tabbed into, not on the first one', async () => {
    renderControl();
    fireEvent.focus(screen.getByRole('radiogroup'));
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('radio', { name: 'Pear' })),
    );
  });

  // Radix moves roving focus inside a setTimeout, so these await rather than assert
  // straight after the keydown — synchronously they see nothing and report a working
  // control as broken.
  it('selects with the arrow keys', async () => {
    const { onValueChange } = renderControl();
    const pear = screen.getByRole('radio', { name: 'Pear' });
    pear.focus();
    fireEvent.keyDown(pear, { key: 'ArrowRight' });
    await waitFor(() => expect(onValueChange).toHaveBeenCalledWith('plum'));
  });

  it('wraps at the end rather than dead-ending', async () => {
    const { onValueChange } = renderControl({ value: 'plum' });
    const plum = screen.getByRole('radio', { name: 'Plum' });
    plum.focus();
    fireEvent.keyDown(plum, { key: 'ArrowRight' });
    await waitFor(() => expect(onValueChange).toHaveBeenCalledWith('apple'));
  });

  it('honours a disabled option without disabling its neighbours', () => {
    const { onValueChange } = renderControl({
      options: [
        { value: 'apple', label: 'Apple', disabled: true },
        { value: 'pear', label: 'Pear' },
        { value: 'plum', label: 'Plum' },
      ],
    });
    expect((screen.getByRole('radio', { name: 'Apple' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole('radio', { name: 'Plum' }));
    expect(onValueChange).toHaveBeenCalledWith('plum');
  });

  // An icon-only segment renders no text, so `ariaLabel` is the ONLY thing standing
  // between it and an unnamed control. This is the case a screenshot cannot catch.
  it('names an icon-only segment from ariaLabel', () => {
    renderControl({
      options: [
        { value: 'apple', label: <svg />, ariaLabel: 'Apple' },
        { value: 'pear', label: <svg />, ariaLabel: 'Pear' },
      ],
    });
    expect(screen.getByRole('radio', { name: 'Apple' })).toBeTruthy();
  });

  // Every tone must still be a radio group. `bare` is the one at risk: it contributes
  // NO styling at all, so a refactor that made it render a plain element would look
  // identical and silently drop the semantics that are its entire reason to exist.
  it.each(['inset', 'joined', 'loose', 'bare'] as const)(
    'keeps radio-group semantics in the %s tone',
    (tone) => {
      renderControl({ tone });
      expect(screen.getByRole('radiogroup', { name: 'Fruit' })).toBeTruthy();
      expect(screen.getAllByRole('radio')).toHaveLength(3);
      expect(screen.getByRole('radio', { name: 'Pear' }).getAttribute('aria-checked')).toBe('true');
    },
  );

  // `bare` exists so a caller can size its own segments — the tier palette's swatches
  // are 24px circles. Applying the tone scale's h-7/px-2 on top would stretch them into
  // pills, and since `bare` contributes no other styling there would be nothing else
  // wrong to notice.
  it('applies no size classes in the bare tone, and does in the others', () => {
    renderControl({ tone: 'bare' });
    expect(screen.getByRole('radio', { name: 'Pear' }).className).not.toMatch(/\bh-7\b|\bpx-2\b/);
    cleanup();
    renderControl({ tone: 'inset' });
    expect(screen.getByRole('radio', { name: 'Pear' }).className).toMatch(/\bh-7\b/);
  });

  it('applies a per-option className, which is the only way to style a bare segment', () => {
    renderControl({
      tone: 'bare',
      options: [
        { value: 'apple', label: 'Apple', className: 'apple-swatch' },
        { value: 'pear', label: 'Pear', className: 'pear-swatch' },
      ],
    });
    expect(screen.getByRole('radio', { name: 'Apple' }).className).toContain('apple-swatch');
    expect(screen.getByRole('radio', { name: 'Pear' }).className).not.toContain('apple-swatch');
  });

  it('renders leading content inside the group without it becoming an option', () => {
    renderControl({ leading: <span data-testid="lead">L</span> });
    expect(screen.getByTestId('lead')).toBeTruthy();
    expect(screen.getAllByRole('radio')).toHaveLength(3);
  });

  // A value matching no option is how "not resolved yet" is expressed (the locale
  // switcher, before i18next settles). It must render as nothing-selected rather than
  // fall back to the first option, which would show a language the user is not using.
  it('selects nothing when the value matches no option', () => {
    renderControl({ value: '' as Fruit });
    for (const radio of screen.getAllByRole('radio')) {
      expect(radio.getAttribute('aria-checked')).toBe('false');
    }
  });
});
