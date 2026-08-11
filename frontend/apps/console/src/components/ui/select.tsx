// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A themed select for CLOSED enums — a fixed vocabulary the user picks from rather
// than types into. For an open set (a facet value, an existing token) reach for
// Combobox or SuggestField instead; those allow free text, which is the distinction.
//
// 🔴 THIS WAS A NATIVE `<select>` AND THAT WAS A BUG, for a reason worth recording
// because the reasoning that produced it was sound and still wrong.
//
// The argument for native was accessibility: a `<select>` gets keyboard interaction,
// type-ahead, screen-reader semantics and the platform's touch picker for free. All
// true. What it misses is that **the option list is drawn by the operating system and
// takes none of our theme** — `<option>` is not styleable in any meaningful way in
// Chromium today. On the console's dark surface that renders a white popup with
// near-invisible text: a control that is perfectly accessible and unreadable.
//
// The lesson generalises past this file: an a11y argument for a native control is an
// argument about SEMANTICS, and it says nothing about whether the thing can be seen.
// The two have to be checked separately, and the second one needs eyes on a render in
// both themes rather than reasoning.
//
// Radix gives back everything the native element was chosen for — roving focus,
// type-ahead, `aria-activedescendant`, Escape/Home/End, click-outside, collision-aware
// positioning — over markup we own and therefore theme. `eslint.config.js` forbids a
// raw `<select>` outside this directory so the native one cannot come back by accident.

import * as React from 'react';
import * as SelectPrimitive from '@radix-ui/react-select';
import { Check, ChevronDown, ChevronUp } from 'lucide-react';
import { cn } from '@/lib/utils';

// 🔴 RADIX REFUSES AN ITEM WITH AN EMPTY VALUE — `value=""` is how it spells "nothing
// selected", so an `<option value="">None</option>` (which the native element handles
// fine, and which three call sites in this app rely on for "default", "no severity"
// and a loading placeholder) throws.
//
// Rather than push that semantic up and make every call site invent its own sentinel —
// three separate chances to map it back wrongly, in forms whose state is already
// fiddly — the mapping lives here and the component keeps the NATIVE contract exactly:
// pass '' and get '' back. The sentinel is a control character precisely because no
// real vocabulary value can collide with it.
const EMPTY = '\u0000__dc_empty__';
const toRadix = (v: string) => (v === '' ? EMPTY : v);
const fromRadix = (v: string) => (v === EMPTY ? '' : v);

const Root = SelectPrimitive.Root;
const Group = SelectPrimitive.Group;
const Value = SelectPrimitive.Value;

const Trigger = React.forwardRef<
  React.ElementRef<typeof SelectPrimitive.Trigger>,
  React.ComponentPropsWithoutRef<typeof SelectPrimitive.Trigger>
>(({ className, children, ...props }, ref) => (
  <SelectPrimitive.Trigger
    ref={ref}
    className={cn(
      'flex h-9 w-full items-center justify-between rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm',
      'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
      'disabled:cursor-not-allowed disabled:opacity-50',
      '[&>span]:line-clamp-1 [&>span]:text-left',
      className,
    )}
    {...props}
  >
    {children}
    <SelectPrimitive.Icon asChild>
      <ChevronDown className="size-4 shrink-0 opacity-50" />
    </SelectPrimitive.Icon>
  </SelectPrimitive.Trigger>
));
Trigger.displayName = SelectPrimitive.Trigger.displayName;

const ScrollUpButton = React.forwardRef<
  React.ElementRef<typeof SelectPrimitive.ScrollUpButton>,
  React.ComponentPropsWithoutRef<typeof SelectPrimitive.ScrollUpButton>
>(({ className, ...props }, ref) => (
  <SelectPrimitive.ScrollUpButton
    ref={ref}
    className={cn('flex cursor-default items-center justify-center py-1', className)}
    {...props}
  >
    <ChevronUp className="size-4" />
  </SelectPrimitive.ScrollUpButton>
));
ScrollUpButton.displayName = SelectPrimitive.ScrollUpButton.displayName;

const ScrollDownButton = React.forwardRef<
  React.ElementRef<typeof SelectPrimitive.ScrollDownButton>,
  React.ComponentPropsWithoutRef<typeof SelectPrimitive.ScrollDownButton>
>(({ className, ...props }, ref) => (
  <SelectPrimitive.ScrollDownButton
    ref={ref}
    className={cn('flex cursor-default items-center justify-center py-1', className)}
    {...props}
  >
    <ChevronDown className="size-4" />
  </SelectPrimitive.ScrollDownButton>
));
ScrollDownButton.displayName = SelectPrimitive.ScrollDownButton.displayName;

// The popup. `bg-popover`/`text-popover-foreground` are the theme tokens the rest of
// the kit's overlays use — the whole point of the rewrite, since these are exactly
// what a native option list ignores.
const Content = React.forwardRef<
  React.ElementRef<typeof SelectPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof SelectPrimitive.Content>
>(({ className, children, position = 'popper', ...props }, ref) => (
  <SelectPrimitive.Portal>
    <SelectPrimitive.Content
      ref={ref}
      position={position}
      className={cn(
        'relative z-50 max-h-96 min-w-[8rem] overflow-hidden rounded-md border bg-popover text-popover-foreground shadow-md',
        'data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0',
        position === 'popper' &&
          'data-[side=bottom]:translate-y-1 data-[side=left]:-translate-x-1 data-[side=right]:translate-x-1 data-[side=top]:-translate-y-1',
        className,
      )}
      {...props}
    >
      <ScrollUpButton />
      <SelectPrimitive.Viewport
        className={cn(
          'p-1',
          // Match the trigger's width so the popup never renders narrower than the
          // value it is showing.
          position === 'popper' && 'h-[var(--radix-select-trigger-height)] w-full min-w-[var(--radix-select-trigger-width)]',
        )}
      >
        {children}
      </SelectPrimitive.Viewport>
      <ScrollDownButton />
    </SelectPrimitive.Content>
  </SelectPrimitive.Portal>
));
Content.displayName = SelectPrimitive.Content.displayName;

const Label = React.forwardRef<
  React.ElementRef<typeof SelectPrimitive.Label>,
  React.ComponentPropsWithoutRef<typeof SelectPrimitive.Label>
>(({ className, ...props }, ref) => (
  <SelectPrimitive.Label
    ref={ref}
    className={cn('px-2 py-1.5 text-xs font-medium text-muted-foreground', className)}
    {...props}
  />
));
Label.displayName = SelectPrimitive.Label.displayName;

const Item = React.forwardRef<
  React.ElementRef<typeof SelectPrimitive.Item>,
  React.ComponentPropsWithoutRef<typeof SelectPrimitive.Item>
>(({ className, children, value, ...props }, ref) => (
  <SelectPrimitive.Item
    ref={ref}
    // Mapped through the same sentinel as the Root's value, so a call site can write
    // value="" and have it behave the way the native element did.
    value={toRadix(value)}
    className={cn(
      'relative flex w-full cursor-default select-none items-center rounded-sm py-1.5 pl-2 pr-8 text-sm outline-none',
      'focus:bg-accent focus:text-accent-foreground',
      'data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
      className,
    )}
    {...props}
  >
    <SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText>
    <span className="absolute right-2 flex size-3.5 items-center justify-center">
      <SelectPrimitive.ItemIndicator>
        <Check className="size-4" />
      </SelectPrimitive.ItemIndicator>
    </span>
  </SelectPrimitive.Item>
));
Item.displayName = SelectPrimitive.Item.displayName;

export interface SelectProps {
  value: string;
  onChange: (v: string) => void;
  children: React.ReactNode;
  id?: string;
  /** Accessible name when no visible <label> is bound to the trigger. */
  ariaLabel?: string;
  disabled?: boolean;
  /** Shown when `value` matches no item — e.g. while an async vocabulary loads. */
  placeholder?: string;
}

/**
 * The everyday form: `<Select value onChange><SelectItem value="a">A</SelectItem></Select>`.
 *
 * Deliberately the same `value`/`onChange` contract the native version had, so a call
 * site's state handling is untouched by the swap — only `<option>` becomes
 * `<SelectItem>`.
 *
 * An empty-string value round-trips exactly as it did on the native element; see the
 * EMPTY sentinel above for why that needs help and why the help lives here.
 */
export function Select({
  value,
  onChange,
  children,
  id,
  ariaLabel,
  disabled,
  placeholder,
}: SelectProps) {
  return (
    <Root value={toRadix(value)} onValueChange={(v) => onChange(fromRadix(v))} disabled={disabled}>
      <Trigger id={id} aria-label={ariaLabel}>
        <Value placeholder={placeholder} />
      </Trigger>
      <Content>{children}</Content>
    </Root>
  );
}

export {
  Root as SelectRoot,
  Group as SelectGroup,
  Value as SelectValue,
  Trigger as SelectTrigger,
  Content as SelectContent,
  Label as SelectLabel,
  Item as SelectItem,
};
