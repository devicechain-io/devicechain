// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 updateTenant is a FULL REPLACE of a tenant's governance overrides — a field the
// request omits is written as NULL, which clears the override back to the tier's value
// and then the platform default. So every override this form does not carry is an
// override the next save destroys, no matter how unrelated the edit that triggered it.
// Renaming a tenant is enough. There is no warning and no diff; the toast says saved.
//
// That is not hypothetical, and it has now happened TWICE. `heldCommandCeiling` shipped
// on the admin API and in the full-replace set while this form built its governance
// payload from the ingest, outbound and AI fields alone. The fix carried that one field
// and wrote the rule down beside it — and `shedPriority`, already shipped and already in
// the same full-replace set, stayed uncarried for another release. An operator could not
// even author one from the console, and any save destroyed one set through the API.
//
// 🔑 So the lesson is not "remember the new field". It is that a per-field assertion list
// is the SAME artifact as the bug: both are someone's memory of which fields exist. This
// file therefore derives its expectation set from `AdminTenantUpdateRequest` — the
// generated input type — so a governance column added to the schema is a COMPILE error
// here until it is accounted for. The previous version of this test asserted on four
// hand-picked keys out of twelve and passed throughout the entire shedPriority window.
//
// The compile gate's other half lives on the callees: `updateTenant`/`createTenant` take
// `Required<…>`, so an omitted field cannot compile at the CALL SITE either. That proves
// the caller considered every field; it cannot prove one was carried over, because `null`
// compiles and `null` is precisely the erasure. That is this file's job.
//
// The transport is the only seam faked. The form, the tier picker and the request it
// builds all run for real, which is the only reason asserting on what went out means
// anything.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

// The CREATE form renders a TokenField, which reads the entity token masks over
// whichever session is live and therefore needs the auth flags. An admin screen holds
// an identity session and — for the operator creating the instance's first tenant — no
// tenant session, so that is the pair given here.
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isAuthenticated: false, isIdentityAuthenticated: true }),
}));

import { TenantForm } from './TenantForm';
import { listTenants } from '@/lib/api/admin';
import type { AdminTenant, AdminTenantUpdateRequest } from '@/lib/api/admin';

const TIER = { token: 'gold', name: 'Gold', color: '#d4af37' };

// Every override carries a DISTINCT non-null value. Distinct because a fixture that
// repeats a number lets "the value survived" be confused with "the form sent some other
// field's value"; non-null because a null override cannot demonstrate preservation at
// all — there is nothing to lose.
//
// Deliberately NOT cast with `as AdminTenant`. The old fixture was, which is the second
// half of how this stayed invisible: the cast let the object omit `shedPriority` while
// still typechecking, so the test data had the same hole as the code under test.
function tenant(overrides: Partial<AdminTenant> = {}): AdminTenant {
  return {
    id: 't-1',
    token: 'acme',
    name: 'Acme Corp',
    enabled: true,
    purgeState: 'NONE',
    purgeEpoch: null,
    tier: TIER,
    config: '{"region":"eu-west"}',
    ingestMessagesPerSecond: 500,
    ingestBurst: 1000,
    outboundMessagesPerSecond: 250,
    outboundBurst: 750,
    aiExternalEnabled: true,
    aiInferenceRequestsPerMinute: 12,
    aiInferenceBurst: 30,
    heldCommandCeiling: 2500,
    shedPriority: 77,
    geoFenceVertexCeiling: 640,
    geoFenceCeiling: 320,
    geoFenceVertexBudget: 44000,
    effectiveSettings: [],
    createdAt: '2026-08-01T00:00:00Z',
    updatedAt: '2026-08-01T00:00:00Z',
    ...overrides,
  };
}

// What an unrelated edit must send for EVERY field of the update request.
//
// 🔑 The `Record<keyof AdminTenantUpdateRequest, unknown>` annotation is the gate: add a
// governance column to the schema, regenerate, and this object stops compiling until
// someone states what the form is expected to do with it. That is the check the previous
// hand-written list could not make.
const PRESERVED: Record<keyof AdminTenantUpdateRequest, unknown> = {
  // The edit itself must arrive, or "nothing was destroyed" is satisfied by a form that
  // saved nothing at all.
  name: 'Acme Industrial',
  tierToken: 'gold',
  config: '{"region":"eu-west"}',
  ingestMessagesPerSecond: 500,
  ingestBurst: 1000,
  outboundMessagesPerSecond: 250,
  outboundBurst: 750,
  aiExternalEnabled: true,
  aiInferenceRequestsPerMinute: 12,
  aiInferenceBurst: 30,
  heldCommandCeiling: 2500,
  shedPriority: 77,
  // The three geofence caps. They arrive here by the same route the two above did — the
  // `Record<keyof AdminTenantUpdateRequest, unknown>` annotation stopped compiling the day
  // the schema gained them — which is the whole reason this file derives its key set rather
  // than listing one.
  geoFenceVertexCeiling: 640,
  geoFenceCeiling: 320,
  geoFenceVertexBudget: 44000,
};

// A tenant with no overrides at all must send an explicit null for each, never a value
// it invented. Same derived key set, so a new column is accounted for on both sides.
const CLEARED: Record<keyof AdminTenantUpdateRequest, unknown> = {
  name: 'Acme Industrial',
  tierToken: 'gold',
  config: null,
  ingestMessagesPerSecond: null,
  ingestBurst: null,
  outboundMessagesPerSecond: null,
  outboundBurst: null,
  // Not an override but a recorded decision: an unchecked box means "not opted in",
  // which is false rather than absent.
  aiExternalEnabled: false,
  aiInferenceRequestsPerMinute: null,
  aiInferenceBurst: null,
  heldCommandCeiling: null,
  shedPriority: null,
  geoFenceVertexCeiling: null,
  geoFenceCeiling: null,
  geoFenceVertexBudget: null,
};

/** The mutation requests actually sent — the tier picker's query is not one. */
function requests(): Record<string, unknown>[] {
  return gqlMock.mock.calls
    .filter((call) => call[0] === 'user-management/admin')
    .map((call) => (call[2] as { request?: Record<string, unknown> } | undefined)?.request)
    .filter((r): r is Record<string, unknown> => r != null);
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  gqlMock.mockImplementation(() =>
    Promise.resolve({
      // The tier picker. Save stays DISABLED until a tier is selected, so an empty
      // list here would leave every assertion below about a button that never fired.
      tenantTiers: [{ id: 'tier-1', ...TIER, description: null, displayOrder: 1 }],
      updateTenant: {},
      createTenant: {},
    }),
  );
});

/** Renders the real tabbed edit form (Effective tab supplied, as the detail page does). */
function renderEdit(entity: AdminTenant) {
  render(<TenantForm tenant={entity} onDone={vi.fn()} effectiveSettingsPanel={<div />} />);
}

async function clickSave() {
  const save = await screen.findByRole('button', { name: 'Save changes' });
  await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(save);
  await waitFor(() => expect(requests()).toHaveLength(1));
  return requests()[0];
}

/**
 * Asserts the request carries exactly the expected fields and nothing else.
 *
 * Both directions matter. Missing keys are the defect this file exists for; EXTRA keys
 * mean the form is sending something the schema does not declare, which the forked
 * graphql-go now rejects at the server — better to fail here than to ship a request the
 * API refuses. `toStrictEqual` covers both, and also catches a key present with an
 * `undefined` value, which is the shape a half-applied fix would take.
 */
function expectRequest(request: Record<string, unknown>, want: Record<string, unknown>) {
  expect(request).toStrictEqual(want);
}

describe('editing a tenant', () => {
  it('carries every governance override through an edit that touched none of them', async () => {
    renderEdit(tenant());
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Acme Industrial' } });

    expectRequest(await clickSave(), PRESERVED);
  });

  // The counterweight. Without it the assertion above would pass just as well against a
  // form that had begun inventing limits for a tenant that never carried any — which
  // under a full replace is a silently imposed bound, not a preserved one.
  it('sends an explicit null for every override a tenant does not have', async () => {
    renderEdit(
      tenant({
        config: null,
        ingestMessagesPerSecond: null,
        ingestBurst: null,
        outboundMessagesPerSecond: null,
        outboundBurst: null,
        aiExternalEnabled: false,
        aiInferenceRequestsPerMinute: null,
        aiInferenceBurst: null,
        heldCommandCeiling: null,
        shedPriority: null,
        geoFenceVertexCeiling: null,
        geoFenceCeiling: null,
        geoFenceVertexBudget: null,
      }),
    );
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Acme Industrial' } });

    expectRequest(await clickSave(), CLEARED);
  });

  it('sends the held-command ceiling the operator typed', async () => {
    renderEdit(tenant());
    // Radix activates a tab on mousedown, not click.
    fireEvent.mouseDown(await screen.findByRole('tab', { name: 'Settings' }));
    fireEvent.change(await screen.findByLabelText('Held command ceiling'), { target: { value: '400' } });

    expect((await clickSave()).heldCommandCeiling).toBe(400);
  });

  // Shed priority had no input control at all, so it could not be authored from the
  // console even before the save destroyed it. Typing it is the other half of the fix.
  it('sends the shed priority the operator typed', async () => {
    renderEdit(tenant());
    fireEvent.mouseDown(await screen.findByRole('tab', { name: 'Settings' }));
    fireEvent.change(await screen.findByLabelText('Shed priority'), { target: { value: '42' } });

    expect((await clickSave()).shedPriority).toBe(42);
  });

  // The three geofence caps, each typed into its own control and read back off the request.
  //
  // 🔑 THEY ARE EDITED ONE AT A TIME AND ASSERTED TOGETHER, which the two tests above do not
  // need to do and these do: three same-typed numeric inputs whose labels differ by one word
  // is the shape in which one control writes another's state, and a test that only checked
  // the field it typed would pass while the other two were quietly overwritten.
  it.each([
    ['Points per geofence', 'geoFenceVertexCeiling', 900, { geoFenceCeiling: 320, geoFenceVertexBudget: 44000 }],
    ['Geofence count', 'geoFenceCeiling', 150, { geoFenceVertexCeiling: 640, geoFenceVertexBudget: 44000 }],
    ['Geofence point budget', 'geoFenceVertexBudget', 30000, { geoFenceVertexCeiling: 640, geoFenceCeiling: 320 }],
  ])('sends the %s the operator typed, and leaves the other caps alone', async (label, field, typed, others) => {
    renderEdit(tenant());
    fireEvent.mouseDown(await screen.findByRole('tab', { name: 'Settings' }));
    fireEvent.change(await screen.findByLabelText(label as string), { target: { value: String(typed) } });

    const sent = await clickSave();
    expect(sent[field as string]).toBe(typed);
    expect(sent).toMatchObject(others as Record<string, unknown>);
  });
});

// The create half of the same form. It was untested, and it shares one payload object
// with the edit half — so a field dropped from `gov` breaks both, while a field the
// create path alone forgets (token, tierToken) breaks only here.
//
// It is also the only mode that renders a TokenField, which is why the auth mock above
// exists: without it this whole describe throws rather than fails, and "throws on mount"
// is the kind of breakage that gets a test deleted rather than fixed.
describe('creating a tenant', () => {
  it('sends every field of the create request', async () => {
    render(<TenantForm onDone={vi.fn()} />);
    fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'newco' } });
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'NewCo' } });

    // A tenant is never un-tiered, so save stays disabled until one is picked — the
    // client-side half of the required FK. The edit cases get this from the fixture;
    // create has to make the choice, which means driving the real Combobox. Addressed
    // by id: its trigger takes its accessible name from the selected option, so a
    // name-based query would be querying the state we are trying to establish.
    const tierTrigger = document.getElementById('t-tier');
    expect(tierTrigger).not.toBeNull();
    fireEvent.click(tierTrigger!);
    fireEvent.click(await screen.findByText('Gold'));

    const save = await screen.findByRole('button', { name: 'Create tenant' });
    await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(save);
    await waitFor(() => expect(requests()).toHaveLength(1));

    // Every governance key present and null (nothing was typed), plus the two the
    // create path owns. Asserted whole, so a key the form stops sending fails here.
    expectRequest(requests()[0], { ...CLEARED, name: 'NewCo', token: 'newco' });
  });
});

// 🔴 The other half of the round trip, and the half the tests above structurally CANNOT
// see: they hand the form a fixture, so a field the form can no longer READ BACK still
// arrives. On a real screen it would arrive as undefined and be saved as null — which is
// the defect, unobserved by every assertion above.
//
// The form's only data source is listTenants (TenantDetailPage), so a governance field
// that query does not select is a field the next save destroys. Asserting it here keys
// off `PRESERVED`, whose exhaustiveness over `keyof AdminTenantUpdateRequest` is checked
// by the compiler — so this walks the same derived key set rather than a second list
// someone would have to remember to update.
describe('the query that feeds the form', () => {
  it('reads back every field the update request will replace', async () => {
    await listTenants();

    const document = String(gqlMock.mock.calls[0][1]);

    // 🔴 Match whole LINES at the top level of the tenant selection, not the field name
    // anywhere in the document. Two things would otherwise produce false passes, and the
    // second one bit:
    //
    //   1. `#` comments — harmless as it happens, because codegen prints the document
    //      and strips them (verified in the generated TenantsDocument). Worth stating,
    //      since this query's own comment names shedPriority and heldCommandCeiling in
    //      prose and a future codegen that preserved comments would silently gut this.
    //   2. NESTED selections — live. `name` appears inside `tier { token name color }`,
    //      so a substring match reported `name` as selected even with the top-level
    //      `name` deleted. The check passed for a field it was not looking at.
    //
    // Top-level fields are indented exactly four spaces by the printer; `tier`'s children
    // get six. Anchoring on that is what makes this measure the selection it names.
    const selected = new Set(
      [...document.matchAll(/^ {4}(\w+)/gm)].map((m) => m[1]),
    );
    // tierToken is written as `tier { token }` — an object selection rather than a scalar
    // named for the request field, and the one field whose read-back shape differs from
    // its write-back shape.
    expect(selected.has('tier')).toBe(true);
    for (const field of Object.keys(PRESERVED).filter((f) => f !== 'tierToken')) {
      expect({ field, selected: selected.has(field) }).toStrictEqual({ field, selected: true });
    }
  });
});
