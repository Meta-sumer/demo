import { useState, type FormEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatDateTime } from '../utils/format';
import { Badge, EmptyState, Panel, PanelHeader, Skeleton, Spinner } from '../components/ui';
import { ConfirmDialog } from '../components/overlay';
import { CodeInput, EnrolmentSetup, RecoveryCodes } from '../components/twoFactor';
import type { AdminMfaRow, MfaEnrolment } from '../types/api';

function errorText(err: unknown, fallback: string): string {
  return err instanceof ApiRequestError ? err.message : fallback;
}

/**
 * The signed-in admin's own two-factor authentication, and for a SUPER_ADMIN
 * the 2FA state of every administrative account with the reset for a lost
 * phone. Every change here is written to the audit log by the server.
 */
export function SecurityPage() {
  const role = useAuthStore((s) => s.user?.role);
  return (
    <div className="space-y-4">
      <OwnTwoFactor />
      {role === 'SUPER_ADMIN' ? <AdminAccounts /> : null}
    </div>
  );
}

function OwnTwoFactor() {
  const queryClient = useQueryClient();
  const status = useQuery({ queryKey: ['security', '2fa'], queryFn: api.security.status });

  const [enrolment, setEnrolment] = useState<MfaEnrolment | null>(null);
  const [regenerating, setRegenerating] = useState(false);
  const [code, setCode] = useState('');
  const [codes, setCodes] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const start = useMutation({
    mutationFn: api.security.startEnrolment,
    onSuccess: (data) => {
      setEnrolment(data);
      setCode('');
      setError(null);
    },
    onError: (err) => toast('error', 'Could not start the setup', errorText(err, '')),
  });

  const confirm = useMutation({
    mutationFn: (value: string) => api.security.confirmEnrolment(value),
    onSuccess: (data) => {
      setEnrolment(null);
      setCodes(data.recoveryCodes);
      void queryClient.invalidateQueries({ queryKey: ['security'] });
      toast('success', 'Two-factor authentication is on');
    },
    onError: (err) => {
      setCode('');
      setError(errorText(err, 'The code is not valid'));
    },
  });

  const regenerate = useMutation({
    mutationFn: (value: string) => api.security.regenerateRecoveryCodes(value),
    onSuccess: (data) => {
      setRegenerating(false);
      setCodes(data.recoveryCodes);
      void queryClient.invalidateQueries({ queryKey: ['security'] });
      toast('success', 'New recovery codes created', 'The old ones no longer work');
    },
    onError: (err) => {
      setCode('');
      setError(errorText(err, 'The code is not valid'));
    },
  });

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError(null);
    if (enrolment) confirm.mutate(code);
    else if (regenerating) regenerate.mutate(code);
  }

  if (status.isLoading || !status.data) return <Skeleton className="h-48 w-full" />;
  const s = status.data;
  const pending = confirm.isPending || regenerate.isPending;

  return (
    <Panel>
      <PanelHeader
        title="Two-factor authentication"
        description="A code from your phone at every sign-in, on top of your password"
        actions={s.enabled ? <Badge tone="up">On</Badge> : <Badge tone={s.required ? 'down' : 'warn'}>Off</Badge>}
      />
      <div className="space-y-4 p-4 text-sm">
        {codes ? (
          <div className="max-w-md">
            <RecoveryCodes codes={codes} continueLabel="Done" onContinue={() => setCodes(null)} />
          </div>
        ) : enrolment || regenerating ? (
          <form onSubmit={onSubmit} className="max-w-md space-y-4">
            {enrolment ? <EnrolmentSetup enrolment={enrolment} /> : (
              <p className="text-content-300">
                Enter a code from your authenticator app. Your current recovery codes stop working and new ones are
                shown once.
              </p>
            )}
            <div>
              <label className="label" htmlFor="own-code">
                6-digit code
              </label>
              <CodeInput id="own-code" value={code} onChange={setCode} autoFocus={!enrolment} />
            </div>
            {error ? (
              <p className="rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-sm text-down">{error}</p>
            ) : null}
            <div className="flex gap-2">
              <button type="submit" className="btn-primary" disabled={pending || code.length !== 6}>
                {pending ? <Spinner /> : null}
                {enrolment ? 'Turn on' : 'Create new codes'}
              </button>
              <button
                type="button"
                className="btn-ghost"
                disabled={pending}
                onClick={() => {
                  setEnrolment(null);
                  setRegenerating(false);
                  setError(null);
                }}
              >
                Cancel
              </button>
            </div>
          </form>
        ) : s.enabled ? (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="text-content-300">
              <div>Turned on {s.enrolledAt ? formatDateTime(s.enrolledAt) : ''}</div>
              <div className="text-[11px] text-content-400">
                {s.recoveryCodesRemaining} recovery {s.recoveryCodesRemaining === 1 ? 'code' : 'codes'} left
              </div>
            </div>
            <button
              type="button"
              className="btn-ghost"
              onClick={() => {
                setRegenerating(true);
                setCode('');
                setError(null);
              }}
            >
              New recovery codes
            </button>
          </div>
        ) : (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-content-300">
              {s.required
                ? 'Required for admin accounts. You will be asked to set it up at your next sign-in.'
                : 'Not turned on. We strongly recommend it for every admin account.'}
            </p>
            <button type="button" className="btn-primary" disabled={start.isPending} onClick={() => start.mutate()}>
              {start.isPending ? <Spinner /> : null}
              Set up
            </button>
          </div>
        )}
      </div>
    </Panel>
  );
}

function AdminAccounts() {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ['security', 'admins'], queryFn: api.security.admins });
  const [target, setTarget] = useState<AdminMfaRow | null>(null);
  const [resetError, setResetError] = useState<string | null>(null);

  const reset = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) => api.security.resetAdmin(id, reason),
    onSuccess: () => {
      toast('success', 'Two-factor reset', (target?.email ?? 'The account') + ' sets it up again at the next sign-in');
      setTarget(null);
      void queryClient.invalidateQueries({ queryKey: ['security'] });
    },
    onError: (err) => setResetError(errorText(err, 'Reset failed')),
  });

  return (
    <Panel>
      <PanelHeader
        title="Admin accounts"
        description={
          query.data?.required
            ? 'Two-factor is required for every admin account'
            : 'Two-factor is optional on this server (ADMIN_2FA_REQUIRED is off)'
        }
      />
      {query.isLoading || !query.data ? (
        <Skeleton className="m-4 h-32" />
      ) : query.data.admins.length === 0 ? (
        <EmptyState title="No admin accounts" />
      ) : (
        <div className="overflow-x-auto">
          <table className="table">
            <thead>
              <tr>
                <th className="px-4">Account</th>
                <th className="px-4">Role</th>
                <th className="px-4">Two-factor</th>
                <th className="px-4">Recovery codes</th>
                <th className="px-4">Last sign-in</th>
                <th className="px-4" />
              </tr>
            </thead>
            <tbody>
              {query.data.admins.map((row) => (
                <tr key={row.id}>
                  <td className="px-4 py-2">
                    <div className="text-content-100">{row.email}</div>
                    <div className="text-[11px] text-content-400">{row.fullName ?? row.username}</div>
                  </td>
                  <td className="px-4 py-2 text-content-300">{row.role}</td>
                  <td className="px-4 py-2">
                    {row.enabled ? <Badge tone="up">On</Badge> : <Badge tone="warn">Off</Badge>}
                  </td>
                  <td className="px-4 py-2 text-content-300">{row.enabled ? row.recoveryCodesRemaining : '-'}</td>
                  <td className="px-4 py-2 text-content-300">
                    {row.lastLoginAt ? formatDateTime(row.lastLoginAt) : '-'}
                  </td>
                  <td className="px-4 py-2 text-right">
                    {row.enabled ? (
                      <button
                        type="button"
                        className="btn-ghost"
                        onClick={() => {
                          setResetError(null);
                          setTarget(row);
                        }}
                      >
                        Reset
                      </button>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {target ? (
        <ConfirmDialog
          title={'Reset two-factor for ' + target.email + '?'}
          confirmLabel="Reset two-factor"
          tone="danger"
          note={{ label: 'Reason', required: true, placeholder: 'e.g. lost phone, confirmed by call' }}
          pending={reset.isPending}
          error={resetError}
          onConfirm={(reason) => reset.mutate({ id: target.id, reason })}
          onCancel={() => setTarget(null)}
        >
          <p>
            Their authenticator and recovery codes stop working and they are signed out everywhere. They set up
            two-factor again at their next sign-in.
          </p>
        </ConfirmDialog>
      ) : null}
    </Panel>
  );
}
