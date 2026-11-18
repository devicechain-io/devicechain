// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useCallback, useId, useMemo, useRef, useState } from 'react';
import { HintText } from '@/components/ui/hint-text';
import { FormFieldContext } from './form-field-context';

interface FormFieldProps {
  /** HTML `<label>` text for the wrapped control (canonical — HTML-label semantics). */
  label: string;
  htmlFor?: string;
  /** Muted helper copy rendered under the control. */
  description?: string;
  children: React.ReactNode;
}

export function FormField({ label, htmlFor, description, children }: FormFieldProps) {
  const generatedId = useId();
  const controls = useRef(new Set<string>());
  const [controlId, setControlId] = useState<string>();
  const register = useCallback((id: string) => {
    controls.current.add(id);
    setControlId(controls.current.values().next().value);
    return () => {
      controls.current.delete(id);
      setControlId(controls.current.values().next().value);
    };
  }, []);
  const field = useMemo(
    () => ({ id: generatedId, labelId: `${generatedId}-label`, controlId: htmlFor ?? controlId, register }),
    [generatedId, htmlFor, controlId, register],
  );
  // Preserve explicit associations; otherwise label the first mounted control.
  const id = htmlFor ?? controlId ?? generatedId;
  return (
    <div>
      <label id={field.labelId} htmlFor={id} className="block text-sm font-medium text-foreground mb-1">
        {label}
      </label>
      <FormFieldContext.Provider value={field}>{children}</FormFieldContext.Provider>
      {description && (
        <HintText size="md" className="mt-1">
          {description}
        </HintText>
      )}
    </div>
  );
}
