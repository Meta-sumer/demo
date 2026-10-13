import { useState, type FormEvent, type ReactNode } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { useAuthStore } from '../stores/authStore';
import { ApiRequestError } from '../services/http';
import { Spinner } from '../components/ui';
import { firstIssue, loginSchema } from '../features/auth/schemas';

export function AuthShell({
  title,
  subtitle,
  children,
  footer,
}: {
  title: string;
  subtitle: string;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <div className="grid min-h-screen place-items-center bg-surface-950 px-4 py-10">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex items-center gap-3">
          <span className="grid h-10 w-10 place-items-center rounded-xl bg-accent text-sm font-black text-surface-950">
            BT
          </span>
          <div>
            <h1 className="text-lg font-semibold tracking-tight">{title}</h1>
            <p className="text-sm text-content-400">{subtitle}</p>
          </div>
        </div>

        <div className="panel p-5">{children}</div>

        {footer ? <div className="mt-4 text-center">{footer}</div> : null}
      </div>
    </div>
  );
}

export function LoginPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const login = useAuthStore((s) => s.login);

  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const parsed = loginSchema.safeParse({ email, password });
    if (!parsed.success) {
      setError(firstIssue(parsed.error));
      return;
    }

    setSubmitting(true);
    setError(null);
    try {
      await login(parsed.data.email, parsed.data.password);
      const from = (location.state as { from?: string } | null)?.from;
      navigate(from ?? '/home', { replace: true });
    } catch (err) {
      setError(err instanceof ApiRequestError ? err.message : 'Could not sign in');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <AuthShell
      title="Sign in"
      subtitle="Access your trading account"
      footer={
        <p className="text-sm text-content-400">
          No account yet?{' '}
          <Link to="/register" className="font-medium text-accent hover:underline">
            Create one
          </Link>
        </p>
      }
    >
      <form onSubmit={onSubmit} className="space-y-4">
        <div>
          <label className="label" htmlFor="email">
            Email
          </label>
          <input
            id="email"
            type="email"
            autoComplete="email"
            className="field"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            placeholder="trader@binary.local"
          />
        </div>

        <div>
          <label className="label" htmlFor="password">
            Password
          </label>
          <input
            id="password"
            type="password"
            autoComplete="current-password"
            className="field"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            placeholder="Your password"
          />
        </div>

        {error ? (
          <p className="rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-sm text-down">{error}</p>
        ) : null}

        <button type="submit" className="btn-primary w-full" disabled={submitting}>
          {submitting ? <Spinner /> : null}
          Sign in
        </button>
      </form>
    </AuthShell>
  );
}
