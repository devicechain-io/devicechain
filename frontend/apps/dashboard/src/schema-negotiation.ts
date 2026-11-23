// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Whether the dashboard viewer checks its requests against the schema the connected instance
// serves (see @devicechain/client enableSchemaNegotiation). The same rule as the console's.
//
// The viewer calls the SDK's relative /api/<area>/graphql paths, so in a production build it is
// served by, and talks to, one instance at one origin: it is the release that instance runs,
// every document it sends is one that instance serves, and the check could only add a request per
// endpoint and a way to fail (a proxy or WAF that answers OPTIONS itself makes discovery fail
// closed). It is therefore OFF in a production build.
//
// It is ON where the viewer and its instance can be different releases: `vite dev` (the dev proxy
// can point at any instance), and a build made with VITE_SCHEMA_NEGOTIATION=on, for a viewer an
// embedder hosts apart from the instance it reaches through its own proxy.
// VITE_SCHEMA_NEGOTIATION=off turns it off even in dev.

import { enableSchemaNegotiation } from '@devicechain/client';

export interface NegotiationEnv {
  DEV?: boolean;
  VITE_SCHEMA_NEGOTIATION?: string;
}

export function schemaNegotiationWanted(env: NegotiationEnv): boolean {
  const setting = env.VITE_SCHEMA_NEGOTIATION?.trim().toLowerCase();
  if (setting === 'on' || setting === 'true') return true;
  if (setting === 'off' || setting === 'false') return false;
  return env.DEV === true;
}

/**
 * Turns negotiation on for this viewer when the environment calls for it, and says whether it did.
 * `enable` is a seam for the test; the real one is the SDK's.
 */
export function configureSchemaNegotiation(env: NegotiationEnv, enable: () => void = enableSchemaNegotiation): boolean {
  if (!schemaNegotiationWanted(env)) return false;
  enable();
  return true;
}
