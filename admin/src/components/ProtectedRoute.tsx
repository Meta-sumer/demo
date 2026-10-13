import { Navigate } from 'react-router-dom';
import type { ReactNode } from 'react';
import { useAuthStore } from '../stores/authStore';
import { Spinner } from './ui';

export function ProtectedRoute({ children }: { children: ReactNode }) {
  const status = useAuthStore((s) => s.status);

  if (status === 'idle' || status === 'loading') {
    return (
      <div className="grid h-screen place-items-center text-content-400">
        <Spinner className="text-accent" />
      </div>
    );
  }

  if (status !== 'authenticated') return <Navigate to="/admin/login" replace />;
  return <>{children}</>;
}

/** Blocks a screen when the signed-in role lacks the permission it needs. */
export function RequirePermission({ permission, children }: { permission: string; children: ReactNode }) {
  const can = useAuthStore((s) => s.can);
  if (!can(permission)) {
    return (
      <div className="panel p-8 text-center">
        <p className="text-sm font-medium text-content-200">You do not have access to this section</p>
        <p className="mt-1 text-xs text-content-400">Required permission: {permission}</p>
      </div>
    );
  }
  return <>{children}</>;
}
