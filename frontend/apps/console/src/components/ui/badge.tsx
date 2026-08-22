// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from '@/lib/utils'

const badgeVariants = cva(
  "inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-semibold transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2",
  {
    variants: {
      variant: {
        default:
          "border-transparent bg-primary text-primary-foreground hover:bg-primary/80",
        secondary:
          "border-transparent bg-secondary text-secondary-foreground hover:bg-secondary/80",
        destructive:
          "border-transparent bg-destructive-fill text-destructive-foreground hover:bg-destructive-fill/80",
        success:
          "border-transparent bg-success-fill text-success-foreground hover:bg-success-fill/80",
        // `warning` is for a state that is UNRESOLVED rather than bad: pending,
        // provisioning, degraded — or a device we simply have not heard from, which
        // is neither online nor known to be down. It reads as its own thing beside
        // `success` and `destructive` rather than borrowing either one's meaning.
        // Painted from the --warning-FILL token, not --warning: a badge writes white
        // on this surface, so it needs the dark fill in both themes. --warning is the
        // ink used for text on the page background and is far too light to sit under
        // white. See the token block in index.css.
        warning:
          "border-transparent bg-warning-fill text-warning-foreground hover:bg-warning-fill/80",
        outline: "text-foreground",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
)

export interface BadgeProps
  extends React.HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: BadgeProps) {
  return (
    <div className={cn(badgeVariants({ variant }), className)} {...props} />
  )
}

export { Badge, badgeVariants }