// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The connectivity helper: a ready-to-run mosquitto_pub or curl that sends this device's
// first measurement. The commands come from connectSnippet.ts, whose output the platform's
// own ingest tests run (see that file); this panel only decides WHETHER to offer one.
//
// 🔴 IT OFFERS NO COMMAND THE PLATFORM WOULD DISCARD. Two states would make any command
// a silent failure, so each shows what to fix instead of a snippet:
//   - a disabled device: its events are refused on arrival, and nothing tells the sender;
//   - no usable credential: the default device-auth mode requires one on every event and
//     on every MQTT connection, so a command without one is refused, and a disabled or
//     expired credential is refused the same way.
// An unassigned device is NOT one of them: its events are stored, with no assignment.
//
// 🔴 It is mounted only for a caller with device:write, like the Credentials tab: for an
// ACCESS_TOKEN the credential id IS the bearer secret and is written into the command, and
// listing credentials is gated on that authority server-side.
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { AlertTriangle, Copy } from 'lucide-react';
import { useAuth } from '@/auth/AuthProvider';
import { Combobox } from '@/components/ui/combobox';
import { FormField } from '@/components/ui/form-field';
import { IconButton } from '@/components/ui/icon-button';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { listDeviceCredentials } from '@/lib/api/credentials';
import { getDeviceMetricVocabulary } from '@/lib/api/device-management';
import {
  CA_FILE,
  exampleMeasurements,
  httpCommand,
  mqttCommand,
  PLACEHOLDER,
  usableCredential,
  type SnippetInput,
} from '@/routes/devices/connectSnippet';

const TRANSPORTS = ['mqtt', 'http'] as const;
type Transport = (typeof TRANSPORTS)[number];
const TRANSPORT_LABEL_KEYS = { mqtt: 'connectTransportMqtt', http: 'connectTransportHttp' } as const;

export function DeviceConnectPanel({ deviceToken, deviceEnabled }: { deviceToken: string; deviceEnabled: boolean }) {
  const { t } = useTranslation('devices');
  const { claims } = useAuth();
  const tenant = claims?.tenant ?? '';
  const { toast } = useToast();
  const [transport, setTransport] = useState<Transport>('mqtt');
  const [picked, setPicked] = useState('');

  const creds = useQuery(() => listDeviceCredentials(deviceToken), [deviceToken]);
  const vocab = useQuery(() => getDeviceMetricVocabulary(deviceToken), [deviceToken]);

  const usable = useMemo(() => {
    const now = new Date();
    return (creds.data ?? []).flatMap((c) =>
      usableCredential(c, now)
        ? [{ token: c.token, credentialType: c.credentialType, credentialId: c.credentialId }]
        : [],
    );
  }, [creds.data]);

  if (!deviceEnabled) {
    return (
      <Precondition title={t('connectDisabledTitle')} body={t('connectDisabledBody')} />
    );
  }
  if ((creds.loading && !creds.data) || (vocab.loading && vocab.data === undefined)) {
    return <LoadingState description={t('connectLoading')} />;
  }
  if (creds.error && !creds.data) return <ErrorState description={creds.error} />;
  if (vocab.error && vocab.data === undefined) return <ErrorState description={vocab.error} />;
  if (usable.length === 0) {
    return <Precondition title={t('connectNoCredentialTitle')} body={t('connectNoCredentialBody')} />;
  }

  const chosen = usable.find((c) => c.token === picked) ?? usable[0];
  // A calculated metric is computed by the platform and a device-sent value under its
  // key is dropped, so the example a device is told to copy must not include it.
  const metrics = (vocab.data ?? []).filter((m) => !m.calculated);
  const input: SnippetInput = {
    tenant,
    deviceToken,
    credential: { credentialType: chosen.credentialType, credentialId: chosen.credentialId },
    metrics,
  };
  const example = exampleMeasurements(metrics);
  const command = transport === 'mqtt' ? mqttCommand(input) : httpCommand(input);

  const copy = async () => {
    try {
      if (!navigator.clipboard) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(command);
      toast(t('copiedToClipboard'));
    } catch {
      toast(t('connectCopyFailed'), 'error');
    }
  };

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">{t('connectIntro')}</p>
      <div className="flex flex-wrap items-end gap-4">
        <SegmentedControl<Transport>
          ariaLabel={t('connectTransportLabel')}
          value={transport}
          onValueChange={setTransport}
          options={TRANSPORTS.map((v) => ({ value: v, label: t(TRANSPORT_LABEL_KEYS[v]) }))}
        />
        {usable.length > 1 && (
          <div className="w-72">
            <FormField label={t('connectCredentialLabel')}>
              <Combobox
                options={usable.map((c) => ({ value: c.token, label: `${c.token} (${c.credentialType})` }))}
                value={chosen.token}
                onChange={setPicked}
                allowClear={false}
              />
            </FormField>
          </div>
        )}
      </div>

      <div className="relative">
        <pre
          data-testid="connect-command"
          data-shot-mask
          className="overflow-x-auto rounded-md border border-border bg-muted/40 p-3 pr-10 font-mono text-xs text-foreground"
        >
          {command}
        </pre>
        <IconButton
          label={t('connectCopyAriaLabel')}
          variant="quiet"
          size="xs"
          className="absolute right-2 top-2"
          onClick={() => void copy()}
        >
          <Copy size={13} />
        </IconButton>
      </div>

      <ul className="list-disc space-y-1 pl-5 text-xs text-muted-foreground">
        <li>{t('connectShell')}</li>
        <li>{t('connectPlaceholderInstance', { placeholder: PLACEHOLDER.instanceId })}</li>
        {transport === 'mqtt' ? (
          <>
            <li>{t('connectPlaceholderMqttHost', { placeholder: PLACEHOLDER.mqttHost, ca: CA_FILE })}</li>
            <li>{t('connectMqttClientId')}</li>
          </>
        ) : (
          <li>{t('connectPlaceholderIngestHost', { placeholder: PLACEHOLDER.ingestHost })}</li>
        )}
        {chosen.credentialType === 'MQTT_BASIC' && (
          <li>{t('connectPlaceholderPassword', { placeholder: PLACEHOLDER.password })}</li>
        )}
        <li>
          {example.fallback
            ? t('connectExampleFallback')
            : example.omitted > 0
              ? t('connectExamplePartial', { count: example.omitted })
              : t('connectExamplePublished')}
        </li>
        <li>{t('connectAcceptedIsNotStored')}</li>
      </ul>
    </div>
  );
}

function Precondition({ title, body }: { title: string; body: string }) {
  return (
    <div role="status" className="space-y-1 rounded-md border border-warning/40 bg-warning/5 p-3">
      <p className="flex items-center gap-2 text-sm font-medium text-warning">
        <AlertTriangle size={16} /> {title}
      </p>
      <p className="text-sm text-muted-foreground">{body}</p>
    </div>
  );
}
