// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the device list offers by authority. Creating, importing and bulk-creating devices
// are device:write, which the server refuses to a member limited to part of the tenant (a
// customer user holding the built-in viewer role) and to any member without the authority,
// so the list does not offer them. Only the transport is faked.
import "@/i18n/config";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
  claims: { username: "ann", authorities: ["*"], scopeAnchors: [] as string[] },
}));

vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({ claims: h.claims }),
}));
vi.mock("@devicechain/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@devicechain/client")>();
  return {
    ...actual,
    gql: (service: string, doc: unknown) =>
      Promise.resolve(
        /query\s+Devices\b/.test(String(doc))
          ? {
              devices: {
                results: [
                  {
                    id: "d1",
                    token: "pump-1",
                    name: "Pump 1",
                    description: null,
                    externalId: null,
                    metadata: null,
                    createdAt: "2026-10-01T00:00:00Z",
                    enabled: true,
                    disabledAt: null,
                    deviceType: {
                      id: "dt",
                      token: "pump",
                      name: "Pump",
                      icon: null,
                      backgroundColor: null,
                      foregroundColor: null,
                      borderColor: null,
                    },
                  },
                ],
                pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 },
              },
            }
          : service === "user-management/settings"
            ? { tokenMasks: "{}" }
            : {},
      ),
  };
});

import DevicesPage from "./DevicesPage";

const WRITES = ["New device", "Import", "Bulk create"];

beforeEach(() => {
  h.claims = { username: "ann", authorities: ["*"], scopeAnchors: [] };
});
afterEach(cleanup);

const renderPage = () =>
  render(
    <MemoryRouter>
      <DevicesPage />
    </MemoryRouter>,
  );

describe("the device list write actions", () => {
  it("offers none of them to a customer user, even one whose role names device:write", async () => {
    h.claims = {
      ...h.claims,
      authorities: ["device:read", "device:write"],
      scopeAnchors: ["customer:acme"],
    };
    renderPage();
    await screen.findByText("Pump 1");
    for (const name of WRITES)
      expect(
        screen.queryByRole("button", { name: new RegExp(name) }),
        name,
      ).toBeNull();
  });

  it("offers none of them to an unscoped member without device:write", async () => {
    h.claims = { ...h.claims, authorities: ["device:read"] };
    renderPage();
    await screen.findByText("Pump 1");
    for (const name of WRITES)
      expect(
        screen.queryByRole("button", { name: new RegExp(name) }),
        name,
      ).toBeNull();
  });

  // The negative control: the same page for an unscoped writer offers all three, so the cut
  // above is the gate's doing and not the fixture's.
  it("offers all of them to an unscoped member with device:write", async () => {
    h.claims = { ...h.claims, authorities: ["device:read", "device:write"] };
    renderPage();
    await screen.findByText("Pump 1");
    for (const name of WRITES)
      expect(
        screen.getByRole("button", { name: new RegExp(name) }),
        name,
      ).toBeTruthy();
  });
});
