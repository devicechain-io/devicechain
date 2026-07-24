// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

"use client"

import * as React from "react"
import * as SheetPrimitive from "@radix-ui/react-dialog"
import { cva, type VariantProps } from "class-variance-authority"
import { X } from "lucide-react"
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

// `SheetContent` reads this to decide whether to render the full-page
// scrim. Radix's `Dialog.Root` already accepts `modal` and controls
// pointer-events outside the content, but the overlay is a separate DOM
// node — so we mirror the value into context so `SheetContent` can skip
// the scrim when modal is off. Callers only set `modal` on
// `<Sheet modal={false}>` and both behaviors line up.
const SheetModalContext = React.createContext<boolean>(true)

type SheetRootProps = React.ComponentProps<typeof SheetPrimitive.Root>

const Sheet = ({ modal = true, ...props }: SheetRootProps) => (
  <SheetModalContext.Provider value={modal}>
    <SheetPrimitive.Root modal={modal} {...props} />
  </SheetModalContext.Provider>
)
Sheet.displayName = "Sheet"

const SheetTrigger = SheetPrimitive.Trigger

const SheetClose = SheetPrimitive.Close

const SheetPortal = SheetPrimitive.Portal

const SheetOverlay = React.forwardRef<
  React.ElementRef<typeof SheetPrimitive.Overlay>,
  React.ComponentPropsWithoutRef<typeof SheetPrimitive.Overlay>
>(({ className, ...props }, ref) => (
  <SheetPrimitive.Overlay
    className={cn(
      "fixed inset-0 z-50 bg-black/80  data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
      className
    )}
    {...props}
    ref={ref}
  />
))
SheetOverlay.displayName = SheetPrimitive.Overlay.displayName

const sheetVariants = cva(
  "fixed z-50 gap-4 bg-background p-6 shadow-lg transition ease-in-out data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:duration-200 data-[state=open]:duration-300 data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95",
  {
    variants: {
      side: {
        top: "inset-x-0 top-0 border-b origin-top",
        bottom:
          "inset-x-0 bottom-0 border-t origin-bottom",
        left: "inset-y-0 left-0 h-full w-3/4 border-r origin-left sm:max-w-sm",
        right:
          "inset-y-0 right-0 h-full w-3/4 border-l origin-right sm:max-w-sm",
      },
    },
    defaultVariants: {
      side: "right",
    },
  }
)

interface SheetContentProps
  extends React.ComponentPropsWithoutRef<typeof SheetPrimitive.Content>,
    VariantProps<typeof sheetVariants> {
  /**
   * When `false`, skips the full-page scrim so the rest of the page
   * stays interactive while the sheet is open. Defaults to whatever
   * the parent `<Sheet modal=...>` was set to (which defaults to `true`).
   * Callers rarely need to pass this directly — setting `modal={false}`
   * on `<Sheet>` flows through automatically.
   */
  modal?: boolean
  /**
   * When `false`, prevents Radix from auto-focusing the first focusable
   * element on open. Use for sheets whose first control is a
   * tooltip-wrapped icon button (the auto-focus draws a focus ring AND
   * pops the tooltip the moment the sheet appears — the scene-editor
   * drawers hit this). Defaults to `true` (Radix behavior — right for
   * sheets that open onto a search box or text input).
   */
  autoFocusOnOpen?: boolean
}

const SheetContent = React.forwardRef<
  React.ElementRef<typeof SheetPrimitive.Content>,
  SheetContentProps
>(({ side = "right", className, children, modal, autoFocusOnOpen = true, onOpenAutoFocus, ...props }, ref) => {
  const { t } = useTranslation('common')
  const ctxModal = React.useContext(SheetModalContext)
  const isModal = modal ?? ctxModal
  return (
    <SheetPortal>
      {isModal ? <SheetOverlay /> : null}
      <SheetPrimitive.Content
        ref={ref}
        className={cn(sheetVariants({ side }), className)}
        onOpenAutoFocus={(e) => {
          if (!autoFocusOnOpen) e.preventDefault()
          onOpenAutoFocus?.(e)
        }}
        {...props}
      >
        {children}
        <SheetPrimitive.Close className="absolute right-4 top-4 rounded-sm opacity-70 ring-offset-background transition-opacity hover:opacity-100 focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:pointer-events-none data-[state=open]:bg-secondary">
          <X className="h-4 w-4" />
          <span className="sr-only">{t('close')}</span>
        </SheetPrimitive.Close>
      </SheetPrimitive.Content>
    </SheetPortal>
  )
})
SheetContent.displayName = SheetPrimitive.Content.displayName

const SheetHeader = ({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) => (
  <div
    className={cn(
      "flex flex-col space-y-2 text-center sm:text-left",
      className
    )}
    {...props}
  />
)
SheetHeader.displayName = "SheetHeader"

const SheetFooter = ({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) => (
  <div
    className={cn(
      "flex flex-col-reverse sm:flex-row sm:justify-end sm:space-x-2",
      className
    )}
    {...props}
  />
)
SheetFooter.displayName = "SheetFooter"

const SheetTitle = React.forwardRef<
  React.ElementRef<typeof SheetPrimitive.Title>,
  React.ComponentPropsWithoutRef<typeof SheetPrimitive.Title>
>(({ className, ...props }, ref) => (
  <SheetPrimitive.Title
    ref={ref}
    className={cn("text-lg font-semibold text-foreground", className)}
    {...props}
  />
))
SheetTitle.displayName = SheetPrimitive.Title.displayName

const SheetDescription = React.forwardRef<
  React.ElementRef<typeof SheetPrimitive.Description>,
  React.ComponentPropsWithoutRef<typeof SheetPrimitive.Description>
>(({ className, ...props }, ref) => (
  <SheetPrimitive.Description
    ref={ref}
    className={cn("text-sm text-muted-foreground", className)}
    {...props}
  />
))
SheetDescription.displayName = SheetPrimitive.Description.displayName

export {
  Sheet,
  SheetPortal,
  SheetOverlay,
  SheetTrigger,
  SheetClose,
  SheetContent,
  SheetHeader,
  SheetFooter,
  SheetTitle,
  SheetDescription,
}