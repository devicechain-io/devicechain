// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A typed input form for one command's declared parameters (ADR-043).
//
// The parsing, validation, and payload serialization are imported from
// @devicechain/widgets rather than reimplemented, so the payload this form sends is
// byte-identical to the one a dashboard command-button widget sends for the same
// command. Two forms over one contract that disagreed about coercion would produce
// commands that behave differently depending on where an operator clicked.

import { useTranslation } from 'react-i18next';
import type { CommandParameter } from '@devicechain/dashboards';
import { isScalar } from '@devicechain/widgets';
import { Input } from '@/components/ui/input';
import { Checkbox } from '@/components/ui/checkbox';
import { FormField } from '@/components/ui/form-field';
import { HintText } from '@/components/ui/hint-text';
import { Combobox } from '@/components/ui/combobox';

// numericStep picks the <Input type="number"> step for a scalar param: whole
// steps for INT, unconstrained for DOUBLE. Hoisted out of the JSX tree (rather
// than an inline ternary) so its 'any' step token — a technical HTML attribute
// value, not user text — never reaches the i18n literal-string lint, which walks
// every node under a JSX element.
function numericStep(dataType: string | undefined): number | 'any' {
  return dataType === 'INT' ? 1 : 'any';
}

// checkedToParamValue mirrors the original inline ternary's truthiness check
// (any non-false value, including radix's 'indeterminate', serializes as the
// scalar-param string "true") rather than String(checked), which would leak
// "indeterminate" as a distinct payload value this form never intends to send.
// Hoisted for the same lint reason as numericStep above.
function checkedToParamValue(checked: boolean | 'indeterminate'): 'true' | 'false' {
  return checked ? 'true' : 'false';
}

// describe builds the muted helper line under an input from whatever the definition
// declared: its description, unit, and bounds. Omitted entirely when it would say
// nothing — an empty hint is visual noise, not help.
function describe(
  param: CommandParameter,
  t: (key: string, options?: Record<string, unknown>) => string,
): string | undefined {
  const parts: string[] = [];
  if (param.description) parts.push(param.description);
  if (param.unit) parts.push(t('unitHint', { unit: param.unit }));
  if (param.minValue != null && param.maxValue != null) {
    parts.push(`${param.minValue}–${param.maxValue}`);
  } else if (param.minValue != null) {
    parts.push(`≥ ${param.minValue}`);
  } else if (param.maxValue != null) {
    parts.push(`≤ ${param.maxValue}`);
  }
  return parts.length > 0 ? parts.join(' · ') : undefined;
}

export function CommandParameterForm({
  params,
  values,
  errors,
  onChange,
  disabled,
}: {
  params: CommandParameter[];
  values: Record<string, string>;
  errors: Record<string, string>;
  onChange: (name: string, value: string) => void;
  disabled?: boolean;
}) {
  const { t } = useTranslation('devices');
  if (params.length === 0) return null;

  return (
    <div className="grid gap-3 sm:grid-cols-2">
      {params.map((param) => {
        const label = param.required ? `${param.name} *` : param.name;
        const error = errors[param.name];

        // A structured parameter has no typed control here. Say so rather than
        // rendering a text box that would send a string where an object is declared.
        if (!isScalar(param)) {
          return (
            <FormField key={param.name} label={label} description={describe(param, t)}>
              <HintText>{t('structuredParameterHint')}</HintText>
            </FormField>
          );
        }

        if (param.dataType === 'BOOLEAN') {
          return (
            <FormField key={param.name} label={label} description={describe(param, t)}>
              <div className="flex h-10 items-center">
                <Checkbox
                  checked={values[param.name] === 'true'}
                  disabled={disabled}
                  onCheckedChange={(checked) => onChange(param.name, checkedToParamValue(checked))}
                />
              </div>
            </FormField>
          );
        }

        if (param.enum && param.enum.length > 0) {
          return (
            <FormField key={param.name} label={label} description={describe(param, t)}>
              <Combobox
                options={param.enum.map((value) => ({ value }))}
                value={values[param.name] ?? ''}
                disabled={disabled}
                allowClear={!param.required}
                onChange={(value) => onChange(param.name, value)}
                placeholder={t('selectValuePlaceholder')}
              />
              {error && <p className="mt-1 text-xs text-destructive">{error}</p>}
            </FormField>
          );
        }

        const numeric = param.dataType === 'INT' || param.dataType === 'DOUBLE';
        return (
          <FormField key={param.name} label={label} description={describe(param, t)}>
            <Input
              type={numeric ? 'number' : 'text'}
              step={numericStep(param.dataType)}
              value={values[param.name] ?? ''}
              disabled={disabled}
              placeholder={param.default ?? ''}
              onChange={(e) => onChange(param.name, e.target.value)}
            />
            {error && <p className="mt-1 text-xs text-destructive">{error}</p>}
          </FormField>
        );
      })}
    </div>
  );
}
