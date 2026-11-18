// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { createContext, useContext, useId, useLayoutEffect } from 'react';

export const FormFieldContext = createContext<{
  id: string;
  labelId: string;
  controlId?: string;
  register: (id: string) => () => void;
} | undefined>(undefined);

export function useFormFieldLabelId(id?: string) {
  const field = useContext(FormFieldContext);
  return id && field?.controlId === id ? field.labelId : undefined;
}

/** Register the actual control, including controls behind component wrappers. */
export function useFormFieldId(id?: string) {
  const field = useContext(FormFieldContext);
  const generatedId = useId();
  // Each control needs its own id even when a field contains several controls.
  const controlId = id ?? (field ? `${field.id}-${generatedId}` : undefined);
  const register = field?.register;
  useLayoutEffect(() => {
    if (controlId && register) return register(controlId);
  }, [controlId, register]);
  return controlId;
}
