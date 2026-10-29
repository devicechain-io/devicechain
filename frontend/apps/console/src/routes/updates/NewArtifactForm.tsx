// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DECLARE then UPLOAD: the two-step birth of an artifact, as one flow.
//
// The service wants the exact size and SHA-256 BEFORE it accepts any bytes, because the
// upload is judged against them. So the form starts from the FILE: choosing it fixes the
// size, and the digest is computed here, in the browser, from the bytes about to be sent —
// the operator never types a number that would have to equal one the file already knows.
//
// Order of refusals, each before the next costs anything:
//   1. the file is over maxArtifactBytes      -> refused here, nothing declared;
//   2. the limit could not be read            -> refused (fail closed: unknown is not unlimited);
//   3. the declaration is refused             -> the server's message, nothing uploaded;
//   4. the upload is refused or mismatched    -> the server's body, verbatim. The artifact now
//      exists in UPLOADING; this form offers a retry of the upload and a link to the artifact
//      rather than a second declaration, which would collide on its token.

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Upload } from 'lucide-react';
import { isValidToken } from '@devicechain/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { Combobox } from '@/components/ui/combobox';
import { MultiSelect } from '@/components/ui/multi-select';
import { FormField } from '@/components/ui/form-field';
import { HintText } from '@/components/ui/hint-text';
import { ErrorBanner } from '@/components/ui/error-banner';
import { FilePicker } from '@/components/ui/file-picker';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import { listDeviceProfiles } from '@/lib/api/device-management';
import {
  createArtifact,
  getUpdateManagementInfo,
  type ArtifactKind,
} from '@/lib/api/update-management';
import { formatBytes, parseByteCount, sha256Digest } from '@/lib/artifactUpload';
import { artifactKindLabel } from '@/lib/enumLabel';
import { shortDigest } from './badges';
import { UploadPanel, useArtifactUpload } from './useArtifactUpload';
import '@/i18n/lazy/updates';

const KINDS: ArtifactKind[] = ['FIRMWARE', 'SOFTWARE', 'CONFIGURATION'];

function slug(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

interface Picked {
  file: File;
  digest: string;
}

export function NewArtifactForm({ onOpen }: { onOpen: (token: string) => void }) {
  const { t } = useTranslation('updates');
  const info = useQuery(getUpdateManagementInfo, []);
  const profiles = useQuery(() => listDeviceProfiles({ pageNumber: 1, pageSize: 1000 }), []);

  const [picked, setPicked] = useState<Picked | null>(null);
  const [hashing, setHashing] = useState(false);
  const [fileError, setFileError] = useState<string | null>(null);

  const [kind, setKind] = useState<ArtifactKind | ''>('');
  const [name, setName] = useState('');
  const [version, setVersion] = useState('');
  const [component, setComponent] = useState('');
  const [token, setToken] = useState('');
  const [tokenTouched, setTokenTouched] = useState(false);
  const [mediaType, setMediaType] = useState('');
  const [profileTokens, setProfileTokens] = useState<string[]>([]);
  const [source, setSource] = useState('');
  const [notes, setNotes] = useState('');

  const [declared, setDeclared] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [declareError, setDeclareError] = useState<string | null>(null);

  const effectiveToken = tokenTouched ? token : slug(`${name}-${version}`);
  const expected = useMemo(
    () => ({ sizeBytes: String(picked?.file.size ?? ''), digest: picked?.digest ?? '' }),
    [picked],
  );
  const upload = useArtifactUpload(declared ?? effectiveToken, expected);

  // The ceiling. null means the limit could not be read, which blocks the flow: an
  // unknown limit is not an unlimited one.
  const maxBytes = info.data ? parseByteCount(info.data.maxArtifactBytes) : null;
  const limitUnknown = !info.loading && maxBytes == null;

  const onPick = async (file: File) => {
    setFileError(null);
    setPicked(null);
    upload.reset();
    if (maxBytes == null) {
      setFileError(t('limitUnknown'));
      return;
    }
    if (BigInt(file.size) > maxBytes) {
      // Refused before hashing, declaring or sending a byte.
      setFileError(t('fileTooLarge', { size: formatBytes(file.size), max: formatBytes(maxBytes) }));
      return;
    }
    if (file.size === 0) {
      setFileError(t('fileEmpty'));
      return;
    }
    setHashing(true);
    try {
      const digest = await sha256Digest(file);
      setPicked({ file, digest });
      if (!name) setName(file.name.replace(/\.[^.]+$/, ''));
      if (!mediaType) setMediaType(file.type);
    } catch (err) {
      setFileError(t('hashFailed', { error: errMessage(err) }));
    } finally {
      setHashing(false);
    }
  };

  const tokenOk = isValidToken(effectiveToken);
  const ready =
    !!picked && !!kind && !!name.trim() && !!version.trim() && !!component.trim() && tokenOk && profileTokens.length > 0;

  const submit = async () => {
    if (!picked || !kind || busy) return;
    setBusy(true);
    setDeclareError(null);
    try {
      if (declared == null) {
        const created = await createArtifact({
          token: effectiveToken,
          kind,
          name: name.trim(),
          version: version.trim(),
          component: component.trim(),
          sizeBytes: String(picked.file.size),
          digest: picked.digest,
          mediaType: mediaType.trim() || undefined,
          compatibleProfiles: profileTokens,
          source: source.trim() || undefined,
          notes: notes.trim() || undefined,
        });
        setDeclared(created.token);
      }
      // The upload hook is bound to the token the form is showing; the service stores the
      // token as sent, so it is the declared one on both the first attempt and a retry.
      await upload.start(picked.file);
    } catch (err) {
      setDeclareError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const profileOptions = (profiles.data?.results ?? []).map((p) => ({ value: p.token, label: p.name ? `${p.name} (${p.token})` : p.token }));
  const locked = declared != null;

  return (
    <div className="space-y-5" data-testid="new-artifact-form">
      {limitUnknown && <ErrorBanner message={t('limitUnknown')} />}
      <FormField label={t('fieldFile')} description={t('fieldFileHelp', { max: maxBytes == null ? '?' : formatBytes(maxBytes) })}>
        <div className="flex items-center gap-3">
          <FilePicker onPick={onPick} disabled={locked || busy || hashing || info.loading}>
            <Upload size={14} /> {t('chooseFile')}
          </FilePicker>
          {picked && (
            <span className="min-w-0 truncate text-sm text-muted-foreground">
              {t('pickedFile', { name: picked.file.name, size: formatBytes(picked.file.size) })}
            </span>
          )}
          {hashing && <span className="text-sm text-muted-foreground">{t('hashing')}</span>}
        </div>
        {picked && (
          <HintText size="md" className="mt-1 font-mono">
            {shortDigest(picked.digest)}
          </HintText>
        )}
        {fileError && <p className="mt-1 text-sm text-destructive">{fileError}</p>}
      </FormField>

      <FormField label={t('fieldKind')} htmlFor="artifact-kind">
        <Combobox
          id="artifact-kind"
          options={KINDS.map((k) => ({ value: k, label: artifactKindLabel(t, k) }))}
          value={kind}
          onChange={(v) => setKind(v as ArtifactKind | '')}
          disabled={locked}
          allowClear={false}
          ariaLabel={t('fieldKind')}
        />
      </FormField>
      <FormField label={t('fieldName')} htmlFor="artifact-name">
        <Input id="artifact-name" value={name} onChange={(e) => setName(e.target.value)} disabled={locked} />
      </FormField>
      <FormField label={t('fieldVersion')} htmlFor="artifact-version" description={t('fieldVersionHelp')}>
        <Input id="artifact-version" value={version} onChange={(e) => setVersion(e.target.value)} disabled={locked} />
      </FormField>
      <FormField label={t('fieldComponent')} htmlFor="artifact-component" description={t('fieldComponentHelp')}>
        <Input id="artifact-component" value={component} onChange={(e) => setComponent(e.target.value)} disabled={locked} />
      </FormField>
      <FormField label={t('fieldToken')} htmlFor="artifact-token">
        <Input
          id="artifact-token"
          value={effectiveToken}
          onChange={(e) => {
            setTokenTouched(true);
            setToken(e.target.value);
          }}
          disabled={locked}
        />
        {effectiveToken && !tokenOk && <p className="mt-1 text-sm text-destructive">{t('tokenInvalid')}</p>}
      </FormField>
      <FormField label={t('fieldProfiles')} htmlFor="artifact-profiles" description={t('fieldProfilesHelp')}>
        <MultiSelect
          id="artifact-profiles"
          options={profileOptions}
          value={profileTokens}
          onChange={setProfileTokens}
          disabled={locked}
        />
        {profiles.error && !profiles.data && <p className="mt-1 text-sm text-destructive">{profiles.error}</p>}
      </FormField>
      <FormField label={t('fieldMediaType')} htmlFor="artifact-media-type" description={t('fieldMediaTypeHelp')}>
        <Input id="artifact-media-type" value={mediaType} onChange={(e) => setMediaType(e.target.value)} disabled={locked} />
      </FormField>
      <FormField label={t('fieldSource')} htmlFor="artifact-source">
        <Input id="artifact-source" value={source} onChange={(e) => setSource(e.target.value)} disabled={locked} />
      </FormField>
      <FormField label={t('fieldNotes')} htmlFor="artifact-notes">
        <Textarea id="artifact-notes" value={notes} onChange={(e) => setNotes(e.target.value)} disabled={locked} rows={3} />
      </FormField>

      {declareError && <ErrorBanner message={declareError} onDismiss={() => setDeclareError(null)} />}
      <UploadPanel state={upload} expected={expected} />

      {upload.phase === 'done' ? (
        <Button onClick={() => onOpen(declared ?? effectiveToken)}>{t('openArtifact')}</Button>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <Button onClick={submit} loading={busy} disabled={!ready || busy || limitUnknown}>
            {locked ? t('retryUpload') : t('declareAndUpload')}
          </Button>
          {locked && (
            <Button variant="outline" onClick={() => onOpen(declared ?? effectiveToken)} disabled={busy}>
              {t('openArtifact')}
            </Button>
          )}
        </div>
      )}
      {locked && upload.phase !== 'done' && <HintText size="md">{t('declaredNotUploaded')}</HintText>}
    </div>
  );
}
