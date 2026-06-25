// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Navigate, Outlet, Route, Routes } from 'react-router-dom';
import { useAuth } from '@/auth/AuthProvider';
import { LoadingState } from '@/components/ui/loading-state';
import LoginPage from '@/routes/Login';
import AppLayout from '@/routes/AppLayout';
import Dashboard from '@/routes/Dashboard';
import UsersPage from '@/routes/users/UsersPage';
import RolesPage from '@/routes/roles/RolesPage';
import DevicesPage from '@/routes/devices/DevicesPage';
import DeviceTypesPage from '@/routes/device-types/DeviceTypesPage';

function ProtectedRoute() {
  const { isAuthenticated, isLoading } = useAuth();

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <LoadingState description="Loading…" />
      </div>
    );
  }
  if (!isAuthenticated) {
    return <Navigate to="/login" replace />;
  }
  return <Outlet />;
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />

      <Route element={<ProtectedRoute />}>
        <Route path="/" element={<AppLayout />}>
          <Route index element={<Dashboard />} />
          <Route path="devices" element={<DevicesPage />} />
          <Route path="device-types" element={<DeviceTypesPage />} />
          <Route path="users" element={<UsersPage />} />
          <Route path="roles" element={<RolesPage />} />
        </Route>
      </Route>

      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}