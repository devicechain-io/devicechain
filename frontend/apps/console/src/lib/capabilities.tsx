// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Area capability discovery for the console: which functional areas this instance
// deployed, so a surface can say "this was never deployed here" instead of
// rendering a raw HTTP status from a request that reached the wrong server.
//
// WHY THIS IS FAIL-OPEN, AGAINST THE HOUSE STYLE. Everything else here fails
// closed, and this deliberately does not, because THIS GATE IS PRESENTATION, NOT
// AUTHORIZATION. An absent area has no endpoint to protect, and a deployed area
// re-authenticates and re-authorizes every real call server-side — hiding a
// surface grants nothing and protects nothing. Failing closed would blank working
// features across the whole console on one transient blip, which is a worse
// defect than the 405 this replaces. So an unanswered question yields 'unknown',
// and 'unknown' renders exactly what the console rendered before this existed.
//
// 🔴 The corollary: a silent fail-open is a control that cannot fail. Nothing on
// screen would distinguish "capability discovery is broken, gating is off" from
// "every area is present". So the failure path warns, and a test asserts the
// warning is emitted — otherwise the mechanism could rot into a no-op and every
// test would still pass.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { useLocation } from 'react-router-dom';
import { areaKey, type Area } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { listFunctionalAreas } from '@/lib/api/user-management';
import { listAdminFunctionalAreas } from '@/lib/api/admin';

/**
 * What is known about an area.
 *
 * 'unknown' is a first-class answer, not an error: it means the question could
 * not be asked (signed out, or discovery failed). Callers must treat it like
 * 'available' — see the fail-open note above.
 */
export type AreaStatus = 'available' | 'absent' | 'unknown';

/**
 * The areas the console gates on, named rather than written inline.
 *
 * A bare string literal inside a JSX attribute trips the i18n literal-string
 * lint even when it is a technical identifier that is never shown to anyone —
 * the same reason lib/entity-types.ts exports ENTITY_TYPE. Naming them once also
 * means `satisfies` checks each against the Area union, so a typo in a gate does
 * not compile rather than silently gating an area that does not exist (which
 * would read as "always absent" and hide a working feature).
 */
export const AREA = {
  aiInferenceAdmin: 'ai-inference/admin',
  outboundConnectors: 'outbound-connectors',
} as const satisfies Record<string, Area>;

interface CapabilityValue {
  statusOf: (area: Area) => AreaStatus;
}

const CapabilityContext = createContext<CapabilityValue | null>(null);

// The area that serves this query. It is deployed by every profile — it is a core
// area, and it is the one that just answered — so its absence from a reply means
// the reply is not a real area set.
const SELF_AREA = 'user-management';

/** How long a fetched area set is trusted before the next navigation re-asks. */
const AREAS_TTL_MS = 5 * 60 * 1000;

export function AreaCapabilityProvider({ children }: { children: ReactNode }) {
  const { isAuthenticated, isIdentityAuthenticated, isLoading } = useAuth();
  const [areas, setAreas] = useState<readonly string[] | null>(null);
  const location = useLocation();
  // Guards against overlapping fetches when a navigation lands while one is in
  // flight; without it the retry-on-navigation below could stack requests.
  const inFlight = useRef(false);
  // When the current answer was obtained. An area set changes only on a helm
  // upgrade, so it is worth caching — but caching it FOREVER means an operator who
  // enables an area leaves every open session gating the new surfaces as absent
  // until someone reloads, which is the hide-a-deployed-feature direction this
  // whole design treats as the worst outcome. A TTL bounds that to minutes without
  // turning the cache into a per-navigation fetch.
  const fetchedAt = useRef<number | null>(null);
  // Warn once per session, not once per failed navigation retry: a repeated
  // warning while the backend is down is noise that trains people to ignore it.
  const warned = useRef(false);

  const load = useCallback(async () => {
    if (inFlight.current) return;
    // Ask over whichever session is live, preferring the identity lane because
    // the admin surfaces are the ones most likely to be on screen when only that
    // session exists. A 401 on one lane means "try the other", NOT "discovery
    // failed" — collapsing those would forfeit gating for a caller that holds a
    // perfectly good session on the other plane, which is exactly the
    // intermittent phantom this mechanism exists to remove.
    const lanes: Array<() => Promise<string[]>> = [];
    if (isIdentityAuthenticated) lanes.push(listAdminFunctionalAreas);
    if (isAuthenticated) lanes.push(listFunctionalAreas);
    if (lanes.length === 0) return;

    inFlight.current = true;
    try {
      for (const lane of lanes) {
        try {
          const reported = await lane();
          // 🔴 Sanity-check the answer before trusting it. Reporting 'absent' HIDES
          // a surface, so a wrong answer in this direction is worse than the 405
          // this replaces — and the only cheap way to get one is a response that
          // parsed but means nothing (an empty list, a shape change, a proxy
          // returning something bland). user-management is what just answered, so
          // its own area MUST be in any genuine reply; a set without it is not a
          // small instance, it is a broken instrument. Stay 'unknown' and say so.
          if (!reported.includes(SELF_AREA)) {
            console.warn(
              `[capabilities] ignoring an implausible functional-area response ` +
                `(it does not include ${SELF_AREA}, yet ${SELF_AREA} answered it)`,
            );
            // Fall through to the next lane rather than giving up, for the same
            // reason a 401 does: one lane answering badly says nothing about the
            // other, and giving up here would forfeit gating for a session that
            // could still get a good answer.
            continue;
          }
          setAreas(reported);
          fetchedAt.current = Date.now();
          warned.current = false;
          return;
        } catch {
          // Try the next lane before giving up.
        }
      }
      // Every lane failed. Stay 'unknown' (fail open) but SAY SO — an invisible
      // fail-open is indistinguishable from "everything is deployed".
      if (!warned.current) {
        warned.current = true;
        console.warn(
          '[capabilities] could not read the deployed functional areas; ' +
            'feature gating is disabled for this session and surfaces for undeployed ' +
            'areas may show transport errors instead of an explanation',
        );
      }
    } finally {
      inFlight.current = false;
    }
  }, [isAuthenticated, isIdentityAuthenticated]);

  useEffect(() => {
    if (isLoading) return;
    void load();
  }, [isLoading, load]);

  // Re-ask on navigation when the answer is missing or stale. Fetch-once-per-load
  // would forfeit gating for the whole session on a single boot-time blip, and
  // would also pin a stale area set for as long as the tab stays open.
  useEffect(() => {
    if (isLoading) return;
    const stale = fetchedAt.current !== null && Date.now() - fetchedAt.current > AREAS_TTL_MS;
    if (areas !== null && !stale) return;
    void load();
  }, [location.pathname, isLoading, areas, load]);

  const value = useMemo<CapabilityValue>(
    () => ({
      statusOf: (area: Area) => {
        if (areas === null) return 'unknown';
        return areas.includes(areaKey(area)) ? 'available' : 'absent';
      },
    }),
    [areas],
  );

  return <CapabilityContext.Provider value={value}>{children}</CapabilityContext.Provider>;
}

/**
 * The status of one area.
 *
 * Returns 'unknown' when no provider is mounted, so a component rendered outside
 * it (a test, a storybook) behaves exactly as it did before gating existed rather
 * than throwing or hiding itself.
 */
export function useAreaStatus(area: Area): AreaStatus {
  const ctx = useContext(CapabilityContext);
  if (!ctx) return 'unknown';
  return ctx.statusOf(area);
}

/**
 * Whether an area is known to be undeployed.
 *
 * The double negative is deliberate: 'unknown' must read as "carry on", so the
 * question a caller asks is "am I sure it is missing?" rather than "is it
 * there?". Written the other way, an unanswered question would hide a working
 * feature.
 */
export function useAreaAbsent(area: Area): boolean {
  return useAreaStatus(area) === 'absent';
}
