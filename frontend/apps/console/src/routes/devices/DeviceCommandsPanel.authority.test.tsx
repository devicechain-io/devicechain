// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the device's Commands tab offers by authority. Sending a command is command:write and
// a customer user holding it may send to their devices; cancelling one is refused to a member
// limited to part of the tenant. A member without the authority sees the history alone.
import "@/i18n/config";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock("@devicechain/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@devicechain/client")>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock("./HeldWhileDisabledNotice", () => ({
  HeldWhileDisabledNotice: () => null,
}));

import { DeviceCommandsPanel } from "./DeviceCommandsPanel";

afterEach(cleanup);

const queued = {
  id: "c1",
  token: "c1",
  deviceToken: "valve-1",
  name: "reboot",
  status: "QUEUED",
  queuedTime: "2026-10-27T12:00:00Z",
  sentTime: null,
  respondedTime: null,
  expiresAt: null,
  responsePayload: null,
  error: null,
};

function renderPanel(canIssue: boolean, canCancel: boolean) {
  gqlMock.mockImplementation((service: string) =>
    Promise.resolve(
      service === "command-delivery"
        ? {
            commands: {
              results: [queued],
              pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 },
            },
          }
        : { deviceCommandVocabulary: null },
    ),
  );
  render(
    <MemoryRouter>
      <DeviceCommandsPanel
        deviceToken="valve-1"
        canIssue={canIssue}
        canCancel={canCancel}
      />
    </MemoryRouter>,
  );
}

describe("the device commands tab by authority", () => {
  it("offers neither sending nor cancelling without command:write", async () => {
    renderPanel(false, false);
    await screen.findByText("reboot");
    expect(screen.queryByRole("button", { name: "Issue command" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
  });

  it("offers sending but not cancelling to a customer user who holds command:write", async () => {
    renderPanel(true, false);
    await screen.findByText("reboot");
    expect(screen.getByRole("button", { name: "Issue command" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
  });

  // The negative control: with both granted, both are offered.
  it("offers both to an unscoped member with command:write", async () => {
    renderPanel(true, true);
    await screen.findByText("reboot");
    expect(screen.getByRole("button", { name: "Issue command" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeTruthy();
  });
});
