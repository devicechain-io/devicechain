// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// FIRE: compose one command and fan it out to many devices.
//
// This is DeviceCommandsPanel's issue form generalized from one device to a fleet, and
// deliberately not a second implementation of it. The picker, the typed argument form,
// the parse/validate/serialize helpers and the published-vs-draft rule are all the SAME
// modules the single-device path and the dashboard command-button use
// (`commandChoices`, `CommandParameterForm`, and parseParameterSchema / defaultValues /
// validateParams / buildPayload from @devicechain/widgets). That is what makes the
// payload this drawer sends BYTE-IDENTICAL to the one those paths send for the same
// inputs — two serializers that disagreed about coercion would make a command behave
// differently depending on where the operator clicked, and a fleet is the worst place to
// discover that.
//
// 🔴 ONE THING A BATCH HAS THAT A SINGLE COMMAND DOES NOT: THERE IS NO ONE DEVICE WHOSE
// VOCABULARY TYPES THE FORM. A batch sends one command key and one payload to N devices
// that may resolve to different profiles, and the enqueue gate validates EACH of them
// separately. So the form is typed against a device the operator names — the first one
// they targeted, by default — and that is a HINT, not a contract: the devices that
// disagree with it come back as per-device refusals on the batch record. The screen says
// so rather than implying the whole fleet was checked.

import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Send } from 'lucide-react';
import type { CommandParameter } from '@devicechain/dashboards';
import {
  parseParameterSchema,
  defaultValues,
  validateParams,
  buildPayload,
  isScalar,
} from '@devicechain/widgets';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { MultiSelect } from '@/components/ui/multi-select';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group';
import { FormField } from '@/components/ui/form-field';
import { HintText } from '@/components/ui/hint-text';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import {
  createCommandBatch,
  type CommandBatchCreateRequest,
  type CommandBatchRejection,
  type CreatedCommandBatch,
} from '@/lib/api/command-delivery';
import {
  listDevices,
  listDeviceGroupTargets,
  listEntityGroupVersions,
  getDeviceCommandVocabulary,
  listCommandDefinitionsForDevice,
} from '@/lib/api/device-management';
import { commandChoices } from '@/routes/devices/commandVocabulary';
import { CommandParameterForm } from '@/routes/devices/CommandParameterForm';
import { groupRefusals } from './refusals';
import { RefusalGroupList } from './RefusalGroups';
import { buildDeviceTargets, isVersionedGroup, resolvedClaim, MAX_BATCH_DEVICES } from './batchTarget';

// One large page of each picker's options. Both controls filter over fully-materialized
// options, so a page they never ask for is an entry the operator cannot select and
// cannot tell is missing — which is also why the device picker is PAIRED with a paste
// box below rather than relied on alone: the service's ceiling is far past any page size
// worth loading, and only pasted text can express a target set that large.
const optionPageSize = 1000;

type TargetKind = 'devices' | 'group';

// Wire-independent: this picks which FIELD of the request is populated, not a value the
// service ever sees. `deviceTokens` and `groupToken` are alternatives and sending both
// is refused, so the form can only ever be in one of these two states.
const TARGET_KINDS: { value: TargetKind; labelKey: string }[] = [
  { value: 'devices', labelKey: 'targetDeviceList' },
  { value: 'group', labelKey: 'targetGroup' },
];

// The two answers to allowPartial. There is no third, and there is deliberately no
// DEFAULT — see the radio group below.
const ALL_OR_NOTHING = 'allOrNothing';
const PARTIAL = 'partial';
// DOM ids, so each radio's sentence is explicitly its accessible name rather than
// relying on the wrapping label alone.
const ALL_OR_NOTHING_ID = 'batch-all-or-nothing';
const PARTIAL_ID = 'batch-partial';

export function CreateBatchForm({ onFired }: { onFired: (batch: CreatedCommandBatch) => void }) {
  const { t } = useTranslation('commandBatches');

  // ── target ───────────────────────────────────────────────────────────────
  const [targetKind, setTargetKind] = useState<TargetKind>('devices');
  const [picked, setPicked] = useState<string[]>([]);
  const [pasted, setPasted] = useState('');
  const [groupToken, setGroupToken] = useState('');
  const [groupVersion, setGroupVersion] = useState('');

  // ── command ──────────────────────────────────────────────────────────────
  const [typeAgainstChoice, setTypeAgainstChoice] = useState('');
  const [typeAgainstTouched, setTypeAgainstTouched] = useState(false);
  const [selectedKey, setSelectedKey] = useState('');
  const [freeName, setFreeName] = useState('');
  const [rawPayload, setRawPayload] = useState('');
  const [paramValues, setParamValues] = useState<Record<string, string>>({});
  const [paramErrors, setParamErrors] = useState<Record<string, string>>({});

  // 🔴 NULL IS THE STARTING VALUE AND IT IS THE POINT. `allowPartial` is `Boolean!` with
  // NO default in the schema, because the safe answer depends on what is being fired: a
  // firmware push where a half-updated fleet is worse than none wants all-or-nothing,
  // while a "unlock every door you can reach" wants partial. A form that preselected
  // either one would hand the operator an intent they never formed, on the field that
  // decides whether a physical actuation may reach part of a fleet. So it starts unset
  // and the submit is blocked until it is not.
  const [allowPartial, setAllowPartial] = useState<boolean | null>(null);

  // ── outcome ──────────────────────────────────────────────────────────────
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  // 🔴 A FAILURE CARRIES WHAT IS KNOWN ABOUT THE FLEET, NOT JUST A MESSAGE — because the
  // two ways this can fail differ in exactly the fact the operator needs before they
  // press the button again:
  //   'undecided'  — the call threw. The service guarantees an undecided batch creates
  //                  nothing, so the token is unspent and a retry is safe.
  //   'unreadable' — the call succeeded with NEITHER arm populated. That says nothing
  //                  about what happened server-side: a batch may exist and may already
  //                  be actuating hardware. Offering the same "just try again" here
  //                  would invite a second fleet write on top of a first one nobody can
  //                  see.
  const [failure, setFailure] = useState<{ message: string; kind: 'undecided' | 'unreadable' } | null>(
    null,
  );
  const [rejection, setRejection] = useState<CommandBatchRejection | null>(null);
  // Synchronous double-submit guard. `busy` disables the button, but state lands on the
  // next render and a token must never be spent twice — see submit().
  const inFlight = useRef(false);

  const { data: devices } = useQuery(
    () => listDevices({ pageNumber: 1, pageSize: optionPageSize }).then((r) => r.results),
    [],
  );
  const { data: groups } = useQuery(() => listDeviceGroupTargets(), []);

  const deviceOptions: ComboboxOption[] = (devices ?? []).map((d) => ({
    value: d.token,
    label: d.name ? `${d.name} (${d.token})` : d.token,
  }));
  const groupOptions: ComboboxOption[] = (groups ?? []).map((g) => ({
    value: g.token,
    label: g.name ? `${g.name} (${g.token})` : g.token,
  }));

  const target = useMemo(() => buildDeviceTargets(picked, pasted), [picked, pasted]);
  const selectedGroup = (groups ?? []).find((g) => g.token === groupToken);
  // Only a dynamic group is versioned at all; naming a version for a static one is
  // REFUSED rather than ignored, so the input is not merely hidden — it must not exist.
  const versioned = isVersionedGroup(selectedGroup?.membershipMode);

  const { data: versions } = useQuery(
    () => (versioned && groupToken ? listEntityGroupVersions(groupToken) : Promise.resolve([])),
    [groupToken, versioned],
  );
  const versionOptions: ComboboxOption[] = (versions ?? []).map((v) => ({
    value: String(v.version),
    label: v.label
      ? t('versionOptionLabelled', { version: v.version, label: v.label })
      : t('versionOption', { version: v.version }),
  }));

  // The device whose published vocabulary types the form. Derived, with an override:
  // until the operator picks one, it FOLLOWS the head of the device list they are
  // building — which is the device the batch's first admitted command goes to — and a
  // group target has no such device, so it starts empty (free text) instead.
  const typeAgainst = typeAgainstTouched ? typeAgainstChoice : targetKind === 'devices' ? (target.tokens[0] ?? '') : '';

  const {
    data: choices,
    loading: vocabularyLoading,
    error: vocabularyError,
  } = useQuery(async () => {
    if (!typeAgainst) return commandChoices(null, []);
    const [vocabulary, drafts] = await Promise.all([
      getDeviceCommandVocabulary(typeAgainst),
      listCommandDefinitionsForDevice(typeAgainst),
    ]);
    return commandChoices(vocabulary, drafts);
  }, [typeAgainst]);

  const selectable = choices?.selectable ?? [];
  const draftOnly = choices?.draftOnly ?? [];
  const constrained = choices?.constrained ?? false;
  // A picker is offered only when there is something to pick. With no reference device —
  // or one whose profile offers nothing — the command key is free text, which is exactly
  // what the enqueue gate accepts from an unconstrained device.
  const usingPicker = typeAgainst !== '' && selectable.length > 0;
  const selected = usingPicker ? selectable.find((c) => c.commandKey === selectedKey) : undefined;

  const params: CommandParameter[] = useMemo(
    () => parseParameterSchema(selected?.parameterSchema),
    [selected],
  );
  // A REQUIRED structured parameter cannot be expressed by the generated form, so such a
  // command falls back to a raw payload box rather than dead-ending the operator. The
  // threshold is `required` for the same reason it is on the single-device path: it
  // matches what validateParams blocks.
  const needsRawPayload = params.some((p) => !isScalar(p) && p.required);
  const useTypedForm = usingPicker && selected !== undefined && !needsRawPayload;

  // Re-picking the reference device changes what the picker offers, so a key chosen
  // against the previous one is not necessarily offered by this one — clear it rather
  // than leave a selection the picker no longer shows.
  useEffect(() => {
    setSelectedKey('');
  }, [typeAgainst]);

  // Seed the value map whenever the selected command changes, so declared defaults are
  // pre-filled and a previous command's values never leak into the next one's payload.
  useEffect(() => {
    setParamValues(defaultValues(params));
    setParamErrors({});
    setRawPayload('');
  }, [params]);

  // A version pinned on one group means nothing on another.
  useEffect(() => {
    setGroupVersion('');
  }, [groupToken]);

  const commandKey = usingPicker ? selectedKey : freeName.trim();

  const submit = async () => {
    // 🔴 SYNCHRONOUS, and before anything else. The button is disabled while `busy`, but
    // that is React state and it lands on the next render; this ref closes the window in
    // between. What it protects is not "one extra request" — it is the TOKEN. Re-issuing
    // a token that already names a batch is an idempotent replay that returns the
    // ORIGINAL batch, so a double-fire would paint a previous batch's stored counts as
    // the outcome of the write just made.
    if (inFlight.current) return;

    // 🔴 THE UNSET CHECK IS FIRST AND IT IS NOT ONLY A DISABLED BUTTON. The type is
    // `boolean | null` precisely so that this narrowing is what produces the value sent —
    // there is no path from here to the request that could substitute a default.
    if (allowPartial === null) {
      setFormError(t('choosePartialFirst'));
      return;
    }
    if (!commandKey) {
      setFormError(usingPicker ? t('selectCommandFirst') : t('commandKeyRequired'));
      return;
    }
    if (targetKind === 'devices') {
      if (target.tokens.length === 0) {
        setFormError(t('nameDevicesFirst'));
        return;
      }
      if (target.overCap) {
        setFormError(t('overCap', { count: target.tokens.length, max: MAX_BATCH_DEVICES }));
        return;
      }
    } else if (!groupToken) {
      setFormError(t('chooseGroupFirst'));
      return;
    }

    let body: string | undefined;
    if (useTypedForm) {
      const errors = validateParams(params, paramValues);
      if (Object.keys(errors).length > 0) {
        setParamErrors(errors);
        setFormError(t('fixHighlightedParameters'));
        return;
      }
      setParamErrors({});
      body = buildPayload(params, paramValues);
    } else {
      body = rawPayload.trim() || undefined;
    }

    const request: CommandBatchCreateRequest = {
      // 🔴 A FRESH TOKEN PER ATTEMPT, minted here rather than held in state. It is the
      // idempotency key for the whole fan-out; reusing one replays the original batch.
      token: crypto.randomUUID(),
      name: commandKey,
      payload: body,
      allowPartial,
      // Exactly one target field is populated. Sending both is refused rather than
      // resolved by a precedence rule, and rightly so.
      ...(targetKind === 'devices'
        ? { deviceTokens: target.tokens }
        : {
            groupToken,
            // Omitted means the ACTIVE published version, which is what an unpinned
            // dynamic group should follow.
            groupVersion: versioned && groupVersion ? Number(groupVersion) : undefined,
          }),
    };

    inFlight.current = true;
    setBusy(true);
    setFormError(null);
    setFailure(null);
    setRejection(null);
    try {
      const result = await createCommandBatch(request);
      // 🔴 A REFUSAL IS NOT A FAILURE. The service decided, and said why in terms the
      // operator can act on — so it is rendered in place and the form KEEPS what was
      // typed, to be corrected and re-fired. (Re-firing mints a new token, so the
      // corrected request is a new batch rather than a replay of the refused one.)
      if (result.rejection) {
        setRejection(result.rejection);
        return;
      }
      // 🔴 `CreateCommandBatchResult` IS TWO NULLABLE FIELDS, NOT A UNION, so "neither"
      // is a shape the type permits and one this has to answer for. It is NOT a success:
      // reporting a batch that may not exist would leave a fleet actuation nobody can
      // audit. It is also NOT the same as the throw below — a call that answered says
      // nothing about whether it created anything first, so this must not tell the
      // operator a retry is free.
      if (!result.batch) {
        setFailure({ message: t('unreadableAnswer'), kind: 'unreadable' });
        return;
      }
      onFired(result.batch);
    } catch (err) {
      // The server's own message, unlike the single-command path's fixed string. The
      // difference is what can arrive here: a batch targeting a GROUP additionally
      // requires device:read, so an authorization refusal is a realistic throw and names
      // only an authority the operator can ask for. The reassurance beside it is the
      // service's own guarantee — an undecided batch creates nothing.
      setFailure({ message: errMessage(err), kind: 'undecided' });
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };

  return (
    <div className="space-y-5">
      {failure && (
        <div>
          <ErrorBanner message={failure.message} onDismiss={() => setFailure(null)} />
          <HintText size="md">
            {failure.kind === 'undecided'
              ? t('undecidedNothingCreated')
              : t('unreadableNothingKnown')}
          </HintText>
        </div>
      )}

      <FormField label={t('targetKindLabel')} description={t('targetKindHint')}>
        <SegmentedControl
          options={TARGET_KINDS.map((k) => ({ value: k.value, label: t(k.labelKey) }))}
          value={targetKind}
          onValueChange={setTargetKind}
          ariaLabel={t('targetKindLabel')}
          tone="joined"
          size="md"
        />
      </FormField>

      {targetKind === 'devices' ? (
        <>
          <FormField label={t('devicesLabel')} description={t('devicesHint')}>
            <MultiSelect
              options={deviceOptions}
              value={picked}
              onChange={setPicked}
              placeholder={t('devicesPlaceholder')}
            />
          </FormField>
          {/* 🔴 THE PASTE BOX IS NOT A CONVENIENCE. The picker can only offer options that
              were loaded, and a batch may name up to MAX_BATCH_DEVICES devices — so this
              is the only control through which the form can express the target sets the
              service actually accepts. It also preserves ORDER, which is meaningful: a
              partially-admitted batch admits in the order given, so a pasted list is how
              an operator states priority. */}
          <FormField
            label={t('pasteLabel')}
            description={t('pasteHint', { max: MAX_BATCH_DEVICES })}
          >
            <Textarea
              rows={4}
              value={pasted}
              onChange={(e) => setPasted(e.target.value)}
              placeholder={t('pastePlaceholder')}
              aria-label={t('pasteLabel')}
            />
          </FormField>
          <div className="space-y-1">
            <HintText size="md">{t('targetSummary', { count: target.tokens.length })}</HintText>
            {target.duplicates > 0 && (
              <HintText size="md">{t('duplicatesCollapsed', { count: target.duplicates })}</HintText>
            )}
            {target.overCap && (
              <p className="text-sm text-destructive">
                {t('overCap', { count: target.tokens.length, max: MAX_BATCH_DEVICES })}
              </p>
            )}
          </div>
        </>
      ) : (
        <>
          <FormField label={t('groupLabel')} description={t('groupHint')}>
            <Combobox
              options={groupOptions}
              value={groupToken}
              onChange={setGroupToken}
              placeholder={t('groupPlaceholder')}
            />
          </FormField>
          {versioned && (
            <FormField label={t('groupVersionLabel')} description={t('groupVersionHint')}>
              <Combobox
                options={versionOptions}
                value={groupVersion}
                onChange={setGroupVersion}
                placeholder={t('groupVersionActive')}
              />
            </FormField>
          )}
        </>
      )}

      <FormField label={t('typeAgainstLabel')} description={t('typeAgainstHint')}>
        <Combobox
          options={deviceOptions}
          value={typeAgainst}
          onChange={(v) => {
            setTypeAgainstTouched(true);
            setTypeAgainstChoice(v);
          }}
          placeholder={t('typeAgainstPlaceholder')}
        />
      </FormField>

      {vocabularyLoading ? (
        <HintText size="md">{t('loadingVocabulary')}</HintText>
      ) : (
        <>
          {vocabularyError && <HintText size="md">{t('vocabularyUnavailable')}</HintText>}
          {usingPicker ? (
            <FormField label={t('commandLabel')} description={t('commandHint')}>
              <Combobox
                options={selectable.map((c) => ({
                  value: c.commandKey,
                  label: c.name ? `${c.name} (${c.commandKey})` : c.commandKey,
                }))}
                value={selectedKey}
                onChange={setSelectedKey}
                placeholder={t('selectCommandFirst')}
              />
            </FormField>
          ) : (
            <FormField label={t('commandKeyLabel')} description={t('commandKeyHint')}>
              <Input
                value={freeName}
                onChange={(e) => setFreeName(e.target.value)}
                placeholder={t('commandKeyPlaceholder')}
                aria-label={t('commandKeyLabel')}
              />
            </FormField>
          )}
          {usingPicker && !constrained && (
            <HintText size="md">{t('unconstrainedHint')}</HintText>
          )}
          {draftOnly.length > 0 && (
            <div className="space-y-1 rounded-md bg-muted/50 p-2">
              <HintText size="md">{t('draftOnlyLabel')}</HintText>
              {/* Actual command keys — data, not prose. */}
              <p className="text-xs font-medium text-muted-foreground">{draftOnly.join(', ')}</p>
            </div>
          )}
        </>
      )}

      {useTypedForm ? (
        <CommandParameterForm
          params={params}
          values={paramValues}
          errors={paramErrors}
          disabled={busy}
          onChange={(name, value) => setParamValues((prev) => ({ ...prev, [name]: value }))}
        />
      ) : (
        <FormField
          label={t('payloadLabel')}
          description={needsRawPayload ? t('payloadStructuredHint') : t('payloadFreeformHint')}
        >
          <Textarea
            rows={3}
            value={rawPayload}
            onChange={(e) => setRawPayload(e.target.value)}
            placeholder={t('payloadPlaceholder')}
            aria-label={t('payloadLabel')}
          />
        </FormField>
      )}

      {typeAgainst && (
        <HintText size="md">{t('typedAgainstCaveat', { device: typeAgainst })}</HintText>
      )}

      {/* ── allowPartial ──────────────────────────────────────────────────────
          Two full sentences rather than a checkbox labelled "allow partial": the
          question is what should happen to a fleet when part of it cannot receive the
          command, and a checkbox states only one of the two answers while leaving the
          other implied by its absence — which is indistinguishable from not having
          answered. Nothing is preselected. */}
      <div>
        <p className="mb-1 block text-sm font-medium text-foreground">{t('partialLabel')}</p>
        <RadioGroup
          value={allowPartial === null ? '' : allowPartial ? PARTIAL : ALL_OR_NOTHING}
          onValueChange={(v) => setAllowPartial(v === PARTIAL)}
          aria-label={t('partialLabel')}
        >
          <label
            htmlFor={ALL_OR_NOTHING_ID}
            className="flex cursor-pointer items-start gap-2 text-sm text-foreground"
          >
            <RadioGroupItem value={ALL_OR_NOTHING} id={ALL_OR_NOTHING_ID} className="mt-0.5" />
            <span>{t('partialOffOption')}</span>
          </label>
          <label
            htmlFor={PARTIAL_ID}
            className="flex cursor-pointer items-start gap-2 text-sm text-foreground"
          >
            <RadioGroupItem value={PARTIAL} id={PARTIAL_ID} className="mt-0.5" />
            <span>{t('partialOnOption')}</span>
          </label>
        </RadioGroup>
        {allowPartial === null && <HintText size="md" className="mt-1">{t('partialUnsetHint')}</HintText>}
      </div>

      {formError && <p className="text-sm text-destructive">{formError}</p>}

      {rejection && <RejectionPanel rejection={rejection} />}

      <Button
        onClick={submit}
        loading={busy}
        disabled={busy || allowPartial === null}
      >
        <Send size={14} /> {t('fireBatch')}
      </Button>
    </div>
  );
}

// ── the refusal ────────────────────────────────────────────────────────────

// What a refused batch says, rendered in place so the operator can correct and re-fire.
//
// 🔴 NOTHING WAS CREATED. A rejection means no batch record and no commands — there is
// nothing to navigate to and nothing that will show up in the list, which is exactly why
// this has to be readable HERE and cannot be deferred to a detail page.
function RejectionPanel({ rejection }: { rejection: CommandBatchRejection }) {
  const { t } = useTranslation('commandBatches');
  const claim = resolvedClaim(rejection.resolved);
  const groups = groupRefusals(rejection.refusalCounts, rejection.refusals);
  return (
    <div className="space-y-3 rounded-md border border-destructive/40 bg-destructive/5 p-4">
      <div className="flex flex-wrap items-center gap-2">
        {/* The code is the backend's stable classification, English by policy. Verbatim. */}
        <Badge variant="destructive">{rejection.code}</Badge>
        <span className="text-sm font-medium text-foreground">{t('rejectedTitle')}</span>
      </div>
      {/* Client-safe prose from the service, English by policy. Verbatim. */}
      <p className="text-sm text-foreground">{rejection.reason}</p>
      {/* 🔴 THREE DIFFERENT STATEMENTS, AND `?? 0` COLLAPSES TWO OF THEM. A null
          `resolved` means no target set was ever established — the refusal happened
          first — which is not the same claim as a target that resolved to no devices.
          Printing "0 devices" for the null case sends an operator to debug a group that
          may be perfectly healthy. */}
      <HintText size="md">
        {claim.kind === 'unknown'
          ? t('resolvedUnknown')
          : claim.kind === 'none'
            ? t('resolvedNone')
            : t('resolvedSome', { count: claim.count })}
      </HintText>
      {groups.length > 0 && <RefusalGroupList groups={groups} />}
      <HintText size="md">{t('rejectionNothingCreated')}</HintText>
    </div>
  );
}
