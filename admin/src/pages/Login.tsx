import { useState, type FormEvent, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAuthStore } from '../stores/authStore';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { toast } from '../hooks/useToast';
import { Spinner } from '../components/ui';
import { CodeInput, EnrolmentSetup, RecoveryCodes } from '../components/twoFactor';
import type { AuthResult, MfaChallenge, MfaEnrolment } from '../types/api';

/**
 * Where the sign-in is. An account with two-factor authentication goes from
 * the password to the code; an admin without it (when the server requires it)
 * enrols first and is then shown recovery codes once.
 */
type Step =
  | { kind: 'password' }
  | { kind: 'verify'; challenge: MfaChallenge }
  | { kind: 'enrol'; challenge: MfaChallenge; enrolment: MfaEnrolment | null }
  | { kind: 'codes'; codes: string[]; result: AuthResult };

function errorText(err: unknown, fallback: string): string {
  return err instanceof ApiRequestError ? err.message : fallback;
}

/** Admin sign-in. The server refuses a non-administrative account here. */
export function AdminLoginPage() {
  const navigate = useNavigate();
  const login = useAuthStore((s) => s.login);
  const completeLogin = useAuthStore((s) => s.completeLogin);

  const [step, setStep] = useState<Step>({ kind: 'password' });
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [recoveryCode, setRecoveryCode] = useState('');
  const [useRecovery, setUseRecovery] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  function finish(result: AuthResult) {
    completeLogin(result);
    navigate('/admin/dashboard', { replace: true });
  }

  /** An expired or used challenge cannot be retried: start again. */
  function handleSecondStepError(err: unknown) {
    if (err instanceof ApiRequestError && err.code === 'MFA_CHALLENGE_INVALID') {
      setStep({ kind: 'password' });
      setPassword('');
    }
    setCode('');
    setError(errorText(err, 'Could not verify the code'));
  }

  async function startEnrolment(challenge: MfaChallenge) {
    setStep({ kind: 'enrol', challenge, enrolment: null });
    try {
      const enrolment = await api.auth.startLoginEnrolment(challenge.mfaToken);
      setStep({ kind: 'enrol', challenge, enrolment });
    } catch (err) {
      handleSecondStepError(err);
    }
  }

  async function onPassword(event: FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      const challenge = await login(email.trim(), password);
      if (!challenge) {
        navigate('/admin/dashboard', { replace: true });
        return;
      }
      setCode('');
      setRecoveryCode('');
      setUseRecovery(false);
      if (challenge.enrolmentRequired) await startEnrolment(challenge);
      else setStep({ kind: 'verify', challenge });
    } catch (err) {
      setError(errorText(err, 'Could not sign in'));
    } finally {
      setSubmitting(false);
    }
  }

  async function onVerify(event: FormEvent) {
    event.preventDefault();
    if (step.kind !== 'verify') return;
    setSubmitting(true);
    setError(null);
    try {
      const input = useRecovery ? { recoveryCode: recoveryCode.trim() } : { code };
      const result = await api.auth.loginSecondFactor(step.challenge.mfaToken, input);
      if (useRecovery) {
        toast(
          'info',
          'Recovery code used',
          result.recoveryCodesRemaining + ' left. Create new ones under Security if you are running low.',
        );
      }
      finish(result);
    } catch (err) {
      handleSecondStepError(err);
    } finally {
      setSubmitting(false);
    }
  }

  async function onConfirmEnrolment(event: FormEvent) {
    event.preventDefault();
    if (step.kind !== 'enrol') return;
    setSubmitting(true);
    setError(null);
    try {
      const { recoveryCodes, ...result } = await api.auth.confirmLoginEnrolment(step.challenge.mfaToken, code);
      setStep({ kind: 'codes', codes: recoveryCodes, result });
    } catch (err) {
      handleSecondStepError(err);
    } finally {
      setSubmitting(false);
    }
  }

  function backToPassword() {
    setStep({ kind: 'password' });
    setPassword('');
    setCode('');
    setError(null);
  }

  const errorBox = error ? (
    <p className="rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-sm text-down">{error}</p>
  ) : null;

  const backLink = (
    <button type="button" className="w-full text-center text-[11px] text-content-400 hover:text-content-100" onClick={backToPassword}>
      Sign in with a different account
    </button>
  );

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

        {step.kind === 'password' ? (
          <form onSubmit={onPassword} className="panel space-y-4 p-5">
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

            {errorBox}

            <button type="submit" className="btn-primary w-full" disabled={submitting}>
              {submitting ? <Spinner /> : null}
              Sign in
            </button>

            <p className="text-center text-[11px] text-content-400">
              Every action in this console is written to the append-only audit log.
            </p>
          </form>
        ) : null}

        {step.kind === 'verify' ? (
          <form onSubmit={onVerify} className="panel space-y-4 p-5">
            <StepTitle title="Two-factor authentication" subtitle={step.challenge.user.email} />
            {useRecovery ? (
              <div>
                <label className="label" htmlFor="recovery">
                  Recovery code
                </label>
                <input
                  id="recovery"
                  className="field font-mono"
                  autoComplete="off"
                  autoFocus
                  value={recoveryCode}
                  onChange={(event) => setRecoveryCode(event.target.value)}
                  placeholder="XXXX-XXXX-XXXX-XXXX"
                />
              </div>
            ) : (
              <div>
                <label className="label" htmlFor="code">
                  Code from your authenticator app
                </label>
                <CodeInput id="code" value={code} onChange={setCode} autoFocus />
              </div>
            )}

            {errorBox}

            <button
              type="submit"
              className="btn-primary w-full"
              disabled={submitting || (useRecovery ? recoveryCode.trim().length < 16 : code.length !== 6)}
            >
              {submitting ? <Spinner /> : null}
              Verify
            </button>

            <button
              type="button"
              className="w-full text-center text-[11px] text-content-400 hover:text-content-100"
              onClick={() => {
                setUseRecovery(!useRecovery);
                setError(null);
              }}
            >
              {useRecovery ? 'Use the authenticator code instead' : 'Lost your phone? Use a recovery code'}
            </button>
            {backLink}
          </form>
        ) : null}

        {step.kind === 'enrol' ? (
          <form onSubmit={onConfirmEnrolment} className="panel space-y-4 p-5">
            <StepTitle
              title="Set up two-factor authentication"
              subtitle="Admin accounts need a second step at sign-in. This takes a minute and is done once."
            />
            {step.enrolment ? (
              <>
                <EnrolmentSetup enrolment={step.enrolment} />
                <div>
                  <label className="label" htmlFor="code">
                    6-digit code
                  </label>
                  <CodeInput id="code" value={code} onChange={setCode} />
                </div>
              </>
            ) : error ? null : (
              <div className="flex justify-center py-6">
                <Spinner />
              </div>
            )}

            {errorBox}

            {step.enrolment ? (
              <button type="submit" className="btn-primary w-full" disabled={submitting || code.length !== 6}>
                {submitting ? <Spinner /> : null}
                Turn on and sign in
              </button>
            ) : null}
            {backLink}
          </form>
        ) : null}

        {step.kind === 'codes' ? (
          <div className="panel space-y-4 p-5">
            <StepTitle title="Two-factor authentication is on" subtitle="One last step" />
            <RecoveryCodes codes={step.codes} continueLabel="Continue to the console" onContinue={() => finish(step.result)} />
          </div>
        ) : null}
      </div>
    </div>
  );
}

function StepTitle({ title, subtitle }: { title: string; subtitle?: ReactNode }) {
  return (
    <div>
      <h2 className="text-sm font-semibold text-content-100">{title}</h2>
      {subtitle ? <p className="text-[11px] text-content-400">{subtitle}</p> : null}
    </div>
  );
}
