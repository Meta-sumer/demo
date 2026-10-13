import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { NavLink, useNavigate } from 'react-router-dom';
import clsx from 'clsx';
import { Eye, EyeOff, X } from 'lucide-react';
import { useAuthStore } from '../../stores/authStore';
import { ApiRequestError } from '../../services/http';
import { Spinner } from '../../components/ui';
import { firstIssue, loginSchema, registerSchema } from './schemas';

export type AuthTab = 'login' | 'register';

/**
 * Sign in and sign up, as a panel over the landing page.
 *
 * A visitor deciding whether to open an account is reading the page; sending
 * them to a separate screen to type an email throws that away and makes coming
 * back their problem. The panel keeps the page behind it, so closing it
 * returns them to the paragraph they were on.
 *
 * The standalone /login and /register routes still exist and are what a deep
 * link or an expired session lands on. Both paths call the same store actions
 * and validate with the same schemas, so there is one way in with two doors
 * rather than two implementations.
 *
 * What is deliberately not here: buttons for Google or Facebook, a currency
 * picker, and a "forgot my password" link. The server has no OAuth, creates
 * every wallet in INR, and has no password-reset endpoint or mailer, so each
 * of those would be a control that looks live and does nothing.
 */
export function AuthPanel({
  tab,
  onTab,
  onClose,
}: {
  tab: AuthTab | null;
  onTab: (tab: AuthTab) => void;
  onClose: () => void;
}) {
  const open = tab !== null;
  const panelRef = useRef<HTMLDivElement>(null);

  // Escape closes, and the page behind must not scroll under the panel.
  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKeyDown);
    const previous = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKeyDown);
      document.body.style.overflow = previous;
    };
  }, [open, onClose]);

  // The first field is where the visitor is going next, so put them there.
  useEffect(() => {
    if (!open) return;
    const timer = window.setTimeout(() => {
      panelRef.current?.querySelector<HTMLInputElement>('input[type="email"]')?.focus();
    }, 220);
    return () => window.clearTimeout(timer);
  }, [open, tab]);

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-[60] flex justify-end">
      <button
        type="button"
        aria-label="Close"
        onClick={onClose}
        className="absolute inset-0 bg-black/70 backdrop-blur-sm"
      />

      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label={tab === 'login' ? 'Log in' : 'Create an account'}
        className="relative flex h-full w-full max-w-[420px] animate-panel-in flex-col overflow-y-auto bg-[#26282c]"
      >
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="absolute right-3 top-3 z-10 rounded-lg p-2 text-white/40 transition hover:bg-white/10 hover:text-white"
        >
          <X className="h-5 w-5" />
        </button>

        <Tabs tab={tab} onTab={onTab} />

        <div className="px-7 pb-10 pt-8 sm:px-9">
          {tab === 'login' ? <LoginForm onTab={onTab} /> : <RegisterForm onTab={onTab} />}
        </div>
      </div>
    </div>
  );
}

/**
 * The two ways in, as a pair of underlined tabs.
 *
 * The underline sits on the container's bottom border rather than on the
 * button, so the inactive tab still shows the rule running past it and the
 * active one reads as the sheet in front.
 */
function Tabs({ tab, onTab }: { tab: AuthTab; onTab: (tab: AuthTab) => void }) {
  return (
    <div className="grid shrink-0 grid-cols-2 border-b border-white/10">
      {(['register', 'login'] as const).map((value) => {
        const active = tab === value;
        return (
          <button
            key={value}
            type="button"
            onClick={() => onTab(value)}
            aria-selected={active}
            className={clsx(
              'relative py-5 text-lg font-medium transition',
              active ? 'text-white' : 'text-white/45 hover:text-white/70',
            )}
          >
            {value === 'register' ? 'Registration' : 'Login'}
            {active ? <span className="absolute inset-x-0 -bottom-px h-0.5 bg-white" /> : null}
          </button>
        );
      })}
    </div>
  );
}

/* ---------------------------------------------------------------- forms -- */

function LoginForm({ onTab }: { onTab: (tab: AuthTab) => void }) {
  const navigate = useNavigate();
  const login = useAuthStore((s) => s.login);

  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const parsed = loginSchema.safeParse({ email, password });
    if (!parsed.success) return setError(firstIssue(parsed.error));

    setSubmitting(true);
    setError(null);
    try {
      await login(parsed.data.email, parsed.data.password);
      navigate('/home', { replace: true });
    } catch (err) {
      setError(err instanceof ApiRequestError ? err.message : 'Could not sign in');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form onSubmit={onSubmit} noValidate className="space-y-4">
      <Field type="email" placeholder="Email" autoComplete="email" value={email} onChange={setEmail} />
      <Field
        type="password"
        placeholder="Password"
        autoComplete="current-password"
        value={password}
        onChange={setPassword}
      />

      <FormError message={error} />

      <SubmitButton submitting={submitting}>Log in</SubmitButton>

      <Switcher prompt="No account?" action="Register" onClick={() => onTab('register')} />
    </form>
  );
}

function RegisterForm({ onTab }: { onTab: (tab: AuthTab) => void }) {
  const navigate = useNavigate();
  const register = useAuthStore((s) => s.register);

  const [form, setForm] = useState({ email: '', username: '', fullName: '', password: '' });
  const [accepted, setAccepted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const update = (field: keyof typeof form) => (value: string) =>
    setForm((previous) => ({ ...previous, [field]: value }));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (!accepted) return setError('Please accept the terms to continue');

    const parsed = registerSchema.safeParse(form);
    if (!parsed.success) return setError(firstIssue(parsed.error));

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
    <form onSubmit={onSubmit} noValidate className="space-y-4">
      <Field
        type="email"
        placeholder="Email"
        autoComplete="email"
        value={form.email}
        onChange={update('email')}
      />
      <Field placeholder="Full name" autoComplete="name" value={form.fullName} onChange={update('fullName')} />
      <Field
        placeholder="Username"
        autoComplete="username"
        value={form.username}
        onChange={update('username')}
        hint="3-24 characters. Letters, digits and underscores."
      />
      <Field
        type="password"
        placeholder="Password"
        autoComplete="new-password"
        value={form.password}
        onChange={update('password')}
        hint="At least 8 characters, with an uppercase letter, a lowercase letter and a digit. Do not reuse a password from anywhere else."
      />

      <Consent accepted={accepted} onChange={setAccepted} />

      <FormError message={error} />

      <SubmitButton submitting={submitting}>Register</SubmitButton>

      <Switcher prompt="Have an account?" action="Log in" onClick={() => onTab('login')} />
    </form>
  );
}

/* ----------------------------------------------------------------- bits -- */

/**
 * One field.
 *
 * The label is the placeholder rather than a caption above the box: with five
 * of these in a panel, captions cost a line each and say nothing the
 * placeholder does not. The input keeps its `type`, so a password manager and
 * a phone keyboard still know what they are looking at.
 */
function Field({
  type = 'text',
  placeholder,
  autoComplete,
  value,
  onChange,
  hint,
}: {
  type?: string;
  placeholder: string;
  autoComplete?: string;
  value: string;
  onChange: (value: string) => void;
  hint?: string;
}) {
  const [revealed, setRevealed] = useState(false);
  const isPassword = type === 'password';

  return (
    <div>
      <div className="relative">
        <input
          type={isPassword && revealed ? 'text' : type}
          placeholder={placeholder}
          aria-label={placeholder}
          autoComplete={autoComplete}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          className={clsx(
            'w-full rounded-xl border border-white/15 bg-white/[0.06] px-4 py-4 text-[15px] text-white',
            'outline-none transition placeholder:text-white/40 focus:border-brand/70',
            isPassword && 'pr-12',
          )}
        />
        {isPassword ? (
          <button
            type="button"
            onClick={() => setRevealed((r) => !r)}
            aria-label={revealed ? 'Hide password' : 'Show password'}
            className="absolute right-3 top-1/2 -translate-y-1/2 rounded-md p-1.5 text-white/40 transition hover:text-white"
          >
            {revealed ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
          </button>
        ) : null}
      </div>
      {hint ? <p className="mt-2 text-[13px] leading-snug text-white/40">{hint}</p> : null}
    </div>
  );
}

/**
 * The terms checkbox.
 *
 * A real gate rather than decoration: the button below refuses while it is
 * clear, and the refusal says so. The two documents it points at are routes on
 * this site, so a visitor can read them before agreeing instead of agreeing to
 * a link that goes nowhere.
 */
function Consent({ accepted, onChange }: { accepted: boolean; onChange: (value: boolean) => void }) {
  return (
    <label className="flex cursor-pointer items-start gap-3 pt-1">
      <span className="relative mt-0.5 shrink-0">
        <input
          type="checkbox"
          checked={accepted}
          onChange={(event) => onChange(event.target.checked)}
          className="peer sr-only"
        />
        <span
          aria-hidden
          className={clsx(
            'grid h-6 w-6 place-items-center rounded-md border-2 transition',
            'peer-focus-visible:ring-2 peer-focus-visible:ring-brand/60 peer-focus-visible:ring-offset-2',
            'peer-focus-visible:ring-offset-[#26282c]',
            accepted ? 'border-brand bg-brand' : 'border-brand/70',
          )}
        >
          {accepted ? (
            <svg viewBox="0 0 16 16" className="h-4 w-4" fill="none" stroke="#26282c" strokeWidth="2.5">
              <path d="M3 8.5 6.5 12 13 4.5" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
          ) : null}
        </span>
      </span>
      <span className="text-[15px] leading-snug text-white/80">
        I accept the terms of the{' '}
        <NavLink to="/terms" target="_blank" className="underline hover:text-white">
          Client Agreement
        </NavLink>{' '}
        and{' '}
        <NavLink to="/privacy" target="_blank" className="underline hover:text-white">
          Privacy Policy
        </NavLink>{' '}
        and confirm being adult
      </span>
    </label>
  );
}

function SubmitButton({ submitting, children }: { submitting: boolean; children: ReactNode }) {
  return (
    <button
      type="submit"
      disabled={submitting}
      className={clsx(
        'mt-1 inline-flex w-full select-none items-center justify-center gap-2 rounded-xl bg-brand py-4',
        'text-[17px] font-bold text-[#26282c] transition hover:brightness-105',
        'disabled:cursor-not-allowed disabled:opacity-50',
      )}
    >
      {submitting ? <Spinner /> : null}
      {children}
    </button>
  );
}

function Switcher({ prompt, action, onClick }: { prompt: string; action: string; onClick: () => void }) {
  return (
    <div className="flex items-center justify-center gap-3 pt-4">
      <span className="text-[15px] text-white/45">{prompt}</span>
      <button
        type="button"
        onClick={onClick}
        className="rounded-lg bg-white/10 px-4 py-2.5 text-[15px] font-bold text-white transition hover:bg-white/15"
      >
        {action}
      </button>
    </div>
  );
}

function FormError({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <p role="alert" className="rounded-xl bg-down/15 px-4 py-3 text-sm text-down ring-1 ring-inset ring-down/30">
      {message}
    </p>
  );
}
