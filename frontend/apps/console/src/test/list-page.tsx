// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Rendering a list page under test: a router that shows where it went (so "the row
// links to its detail route" is asserted as a destination, not a click), the providers
// the pages reach for, and the one failure shape a query can take.

import { render } from '@testing-library/react';
import type { ReactElement } from 'react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';

import '@/i18n/config';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { ToastProvider } from '@/components/ui/toast';

function Where() {
  const loc = useLocation();
  return <div data-testid="where">{loc.pathname}</div>;
}

export function renderListPage(page: ReactElement, at = '/') {
  return render(
    <MemoryRouter initialEntries={[at]}>
      <ToastProvider>
        <ConfirmProvider>
          <Routes>
            <Route
              path="*"
              element={
                <>
                  {page}
                  <Where />
                </>
              }
            />
          </Routes>
        </ConfirmProvider>
      </ToastProvider>
    </MemoryRouter>,
  );
}

/** A query that never settles: the page stays in its loading state. */
export const never = <T,>() => new Promise<T>(() => undefined);

/** A one-page result, in the shape every paged list reads. */
export function pageOf<T>(results: T[], total = results.length) {
  return {
    results,
    pagination: { pageStart: results.length ? 1 : null, pageEnd: results.length || null, totalRecords: total },
  };
}
