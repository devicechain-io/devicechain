// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { HintText } from '@/components/ui/hint-text';

interface FormFieldProps {
  /** HTML `<label>` text for the wrapped control (canonical — HTML-label semantics). */
  label: string;
  htmlFor?: string;
  /** Muted helper copy rendered under the control. */
  description?: string;
  children: React.ReactNode;
}

export function FormField({ label, htmlFor, description, children }: FormFieldProps) {
  return (
    <div>
      <label htmlFor={htmlFor} className="block text-sm font-medium text-foreground mb-1">
        {label}
      </label>
      {children}
      {description && (
        <HintText size="md" className="mt-1">
          {description}
        </HintText>
      )}
    </div>
  );
}