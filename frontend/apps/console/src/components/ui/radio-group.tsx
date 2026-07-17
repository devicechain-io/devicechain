// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A radio group over @radix-ui/react-radio-group, styled to match Checkbox.
//
// Radix gives the group its semantics: exactly one item selected, roving tabindex so
// the group is one tab stop, arrow keys to move between items, and role="radiogroup"
// on the container. Items need only be DESCENDANTS of <RadioGroup> — not immediate
// children — so the group may wrap a table and its items sit in cells.
//
// Selection is reported by the group's `onValueChange`, not per item, which is usually
// what a caller wants: one handler receiving the newly selected value.

import * as React from 'react';
import * as RadioGroupPrimitive from '@radix-ui/react-radio-group';
import { Circle } from 'lucide-react';

import { cn } from '@/lib/utils';

const RadioGroup = React.forwardRef<
  React.ComponentRef<typeof RadioGroupPrimitive.Root>,
  React.ComponentPropsWithoutRef<typeof RadioGroupPrimitive.Root>
>(({ className, ...props }, ref) => (
  <RadioGroupPrimitive.Root ref={ref} className={cn('grid gap-2', className)} {...props} />
));
RadioGroup.displayName = RadioGroupPrimitive.Root.displayName;

const RadioGroupItem = React.forwardRef<
  React.ComponentRef<typeof RadioGroupPrimitive.Item>,
  React.ComponentPropsWithoutRef<typeof RadioGroupPrimitive.Item>
>(({ className, ...props }, ref) => (
  <RadioGroupPrimitive.Item
    ref={ref}
    className={cn(
      'aspect-square size-4 rounded-full border border-primary text-primary shadow',
      'focus:outline-none focus-visible:ring-1 focus-visible:ring-ring',
      'disabled:cursor-not-allowed disabled:opacity-50',
      className,
    )}
    {...props}
  >
    <RadioGroupPrimitive.Indicator className="flex items-center justify-center">
      <Circle className="size-2 fill-current text-current" />
    </RadioGroupPrimitive.Indicator>
  </RadioGroupPrimitive.Item>
));
RadioGroupItem.displayName = RadioGroupPrimitive.Item.displayName;

export { RadioGroup, RadioGroupItem };
