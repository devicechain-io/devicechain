// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Navigate, Outlet } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuth } from '@/auth/AuthProvider';
import { LoadingState } from '@/components/ui/loading-state';

// Guards the /admin console (ADR-033): it requires an identity session held by a
// superuser. A non-superuser identity (or no identity session) is sent to login.
// The admin API additionally enforces a system authority on every operation, so
// this is the UI gate, not the security boundary.
export default function AdminProtectedRoute() {
  const { t } = useTranslation('common');
  const { isIdentityAuthenticated, superuser, isLoading } = useAuth();

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <LoadingState description={t('loading')} />
      </div>
    );
  }
  if (!isIdentityAuthenticated || !superuser) {
    return <Navigate to="/login" replace />;
  }
  return <Outlet />;
}
