import { Navigate, useLocation } from 'react-router-dom';
import type { ReactNode } from 'react';
import { useAuthStore } from '../stores/authStore';
import { Spinner } from './ui';

export function ProtectedRoute({ children }: { children: ReactNode }) {
  const status = useAuthStore((s) => s.status);
  const location = useLocation();

  if (status === 'idle' || status === 'loading') {
    return (
      <div className="flex h-screen items-center justify-center text-content-400">
        <Spinner className="text-accent" />
      </div>
    );
  }

  if (status !== 'authenticated') {
    // Remember where they were headed so login can send them back - with the
    // query, which is what the USDT payment return page is about.
    return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />;
  }

  return <>{children}</>;
}

export function PublicOnlyRoute({ children }: { children: ReactNode }) {
  const status = useAuthStore((s) => s.status);
  // Home is the front door once signed in (brief UX-01): balance, favourites
  // and a way into everything else, rather than dropping straight onto a chart.
  if (status === 'authenticated') return <Navigate to="/home" replace />;
  return <>{children}</>;
}
