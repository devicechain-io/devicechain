// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import * as React from "react"

import { cn } from '@/lib/utils'
import { useFormFieldId } from './form-field-context'

const Input = React.forwardRef<HTMLInputElement, React.ComponentProps<"input">>(
  ({ className, type, ...props }, ref) => {
    const id = useFormFieldId(props.id);
    return (
      <input
        type={type}
        className={cn(
          "flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-base ring-offset-background file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 md:text-sm",
          className
        )}
        ref={ref}
        {...props}
        id={id}
      />
    )
  }
)
Input.displayName = "Input"

export { Input }
