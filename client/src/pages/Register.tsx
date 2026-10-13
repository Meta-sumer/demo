import { useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useAuthStore } from '../stores/authStore';
import { ApiRequestError } from '../services/http';
import { Spinner } from '../components/ui';
import { AuthShell } from './Login';
import { firstIssue, registerSchema } from '../features/auth/schemas';

export function RegisterPage() {
  const navigate = useNavigate();
  const register = useAuthStore((s) => s.register);

  const [form, setForm] = useState({ email: '', username: '', fullName: '', password: '' });
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  function update(field: keyof typeof form, value: string) {
    setForm((previous) => ({ ...previous, [field]: value }));
  }

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const parsed = registerSchema.safeParse(form);
    if (!parsed.success) {
      setError(firstIssue(parsed.error));
      return;
    }

    setSubmitting(true);
    setError(null);
    try {
      await register(parsed.data);
      // A new account gets the five-step introduction once, ending in demo.
      navigate('/onboarding', { replace: true });
    } catch (err) {
      setError(err instanceof ApiRequestError ? err.message : 'Could not create the account');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <AuthShell
      title="Create account"
      subtitle="Start trading in a minute"
      footer={
        <p className="text-sm text-content-400">
          Already registered?{' '}
          <Link to="/login" className="font-medium text-accent hover:underline">
            Sign in
          </Link>
        </p>
      }
    >
      <form onSubmit={onSubmit} className="space-y-4">
        <div>
          <label className="label" htmlFor="fullName">
            Full name
          </label>
          <input
            id="fullName"
            className="field"
            value={form.fullName}
            onChange={(event) => update('fullName', event.target.value)}
          />
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <label className="label" htmlFor="username">
              Username
            </label>
            <input
              id="username"
              className="field"
              value={form.username}
              onChange={(event) => update('username', event.target.value)}
            />
          </div>
          <div>
            <label className="label" htmlFor="email">
              Email
            </label>
            <input
              id="email"
              type="email"
              className="field"
              value={form.email}
              onChange={(event) => update('email', event.target.value)}
            />
          </div>
        </div>

        <div>
          <label className="label" htmlFor="password">
            Password
          </label>
          <input
            id="password"
            type="password"
            autoComplete="new-password"
            className="field"
            value={form.password}
            onChange={(event) => update('password', event.target.value)}
          />
          <p className="mt-1 text-[11px] text-content-400">
            At least 8 characters with upper case, lower case and a digit.
          </p>
        </div>

        {error ? (
          <p className="rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-sm text-down">{error}</p>
        ) : null}

        <button type="submit" className="btn-primary w-full" disabled={submitting}>
          {submitting ? <Spinner /> : null}
          Create account
        </button>
      </form>
    </AuthShell>
  );
}
