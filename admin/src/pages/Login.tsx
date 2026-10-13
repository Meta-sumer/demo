import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAuthStore } from '../stores/authStore';
import { ApiRequestError } from '../services/http';
import { Spinner } from '../components/ui';

/** Admin sign-in. The server refuses a non-administrative account here. */
export function AdminLoginPage() {
  const navigate = useNavigate();
  const login = useAuthStore((s) => s.login);

  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      await login(email.trim(), password);
      navigate('/admin/dashboard', { replace: true });
    } catch (err) {
      setError(err instanceof ApiRequestError ? err.message : 'Could not sign in');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="grid min-h-screen place-items-center bg-surface-950 px-4">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex items-center gap-3">
          <span className="grid h-10 w-10 place-items-center rounded-xl bg-accent text-sm font-black text-white">
            BT
          </span>
          <div>
            <h1 className="text-lg font-semibold tracking-tight">Admin Console</h1>
            <p className="text-sm text-content-400">Operations, risk and finance</p>
          </div>
        </div>

        <form onSubmit={onSubmit} className="panel space-y-4 p-5">
          <div>
            <label className="label" htmlFor="email">
              Email
            </label>
            <input
              id="email"
              type="email"
              autoComplete="username"
              className="field"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              placeholder="admin@binary.local"
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
            />
          </div>

          {error ? (
            <p className="rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-sm text-down">{error}</p>
          ) : null}

          <button type="submit" className="btn-primary w-full" disabled={submitting}>
            {submitting ? <Spinner /> : null}
            Sign in
          </button>

          <p className="text-center text-[11px] text-content-400">
            Every action in this console is written to the append-only audit log.
          </p>
        </form>
      </div>
    </div>
  );
}
