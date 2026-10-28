// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A certificate in PEM, pasted or read from a file the operator chooses. Choosing a file puts
// its text in the box, so the operator sees exactly what will be sent.
//
// 🔴 CERTIFICATES ONLY, AND NEVER A KEY. Text that looks like a private key is flagged before
// anything is sent. The server refuses it as well; this only keeps it from leaving the browser.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Upload } from 'lucide-react';
import { FilePicker } from '@/components/ui/file-picker';
import { Textarea } from '@/components/ui/textarea';
import { PEM_MAX_BYTES, checkPem } from './anchorRules';

export function PemInput({
  id,
  label,
  value,
  onChange,
  exactlyOne,
  placeholder,
  disabled,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (text: string) => void;
  /** An anchor is exactly one CA certificate; a device credential may add intermediates. */
  exactlyOne: boolean;
  placeholder?: string;
  disabled?: boolean;
}) {
  const { t } = useTranslation('trustAnchors');
  const [fileError, setFileError] = useState<string | null>(null);
  // Empty is not a complaint while the operator has not typed anything yet; the form's submit
  // is what insists on content.
  const problem = value.trim() === '' ? null : checkPem(value, { exactlyOne });

  const pick = async (file: File) => {
    setFileError(null);
    if (file.size > PEM_MAX_BYTES) {
      setFileError(t('pemTooLarge'));
      return;
    }
    try {
      onChange(await file.text());
    } catch {
      setFileError(t('pemUnreadable'));
    }
  };

  return (
    <div className="space-y-2">
      <Textarea
        id={id}
        aria-label={label}
        value={value}
        rows={8}
        spellCheck={false}
        disabled={disabled}
        placeholder={placeholder}
        className="font-mono text-xs"
        onChange={(e) => onChange(e.target.value)}
      />
      <FilePicker onPick={(f) => void pick(f)} accept=".pem,.crt,.cer,text/plain" disabled={disabled}>
        <Upload size={14} /> {t('pemChooseFile')}
      </FilePicker>
      {(fileError || problem) && (
        <p role="alert" className="text-sm text-destructive">
          {fileError ?? t(problem as string)}
        </p>
      )}
    </div>
  );
}
