import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft } from 'lucide-react';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatDateTime, formatMoney, formatSignedMoney, fromMinor, toMinor } from '../utils/format';
import { Badge, Panel, PanelHeader, Skeleton, Stat, StatusBadge } from '../components/ui';

/**
 * One customer: balances, exposure, trading record, and the two privileged
 * actions - status change and a manual wallet adjustment. Both are written to
 * the audit log before the response is returned.
 */
export function UserDetailPage() {
  const { id = '' } = useParams();
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);

  const query = useQuery({ queryKey: ['users', id], queryFn: () => api.users.detail(id) });

  const [adjustment, setAdjustment] = useState({
    amountMinor: 1_000_00,
    direction: 'CREDIT' as 'CREDIT' | 'DEBIT',
    reason: '',
  });

  const statusMutation = useMutation({
    mutationFn: (status: string) => api.users.update(id, { status }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['users', id] });
      toast('success', 'User status updated');
    },
    onError: (err) => toast('error', 'Update failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  const limitsMutation = useMutation({
    mutationFn: (input: { maxTradeAmount: number | null; maxUserExposure: number | null }) =>
      api.users.update(id, input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['users', id] });
      toast('success', 'Limits updated');
    },
    onError: (err) => toast('error', 'Update failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  const adjustMutation = useMutation({
    mutationFn: () =>
      api.users.adjustWallet(id, {
        amount: adjustment.amountMinor,
        direction: adjustment.direction,
        reason: adjustment.reason,
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['users', id] });
      setAdjustment((a) => ({ ...a, reason: '' }));
      toast('success', 'Wallet adjusted', 'The change is recorded in the audit log');
    },
    onError: (err) => toast('error', 'Adjustment failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  if (query.isLoading || !query.data) {
    return <Skeleton className="h-96 w-full" />;
  }

  const { user, wallet, stats, openExposure } = query.data;
  const demoWallet = query.data.wallets?.find((w) => w.accountType === 'DEMO');

  return (
    <div className="space-y-4">
      <Link to="/admin/users" className="inline-flex items-center gap-1.5 text-sm text-content-400 hover:text-content-100">
        <ArrowLeft className="h-4 w-4" />
        Back to users
      </Link>

      <Panel className="flex flex-wrap items-center gap-4 p-4">
        <span className="grid h-12 w-12 place-items-center rounded-xl bg-surface-800 text-lg font-bold text-content-100">
          {user.username.slice(0, 2).toUpperCase()}
        </span>
        <div className="min-w-0">
          <h1 className="truncate text-lg font-semibold text-content-100">{user.fullName ?? user.username}</h1>
          <p className="truncate text-sm text-content-400">{user.email}</p>
        </div>
        <div className="flex items-center gap-2">
          <Badge tone={user.role === 'USER' ? 'neutral' : 'accent'}>{user.role}</Badge>
          <StatusBadge status={user.status} />
        </div>

        {can('users:suspend') ? (
          <div className="ml-auto flex gap-2">
            {user.status === 'ACTIVE' ? (
              <button
                className="btn-danger"
                disabled={statusMutation.isPending}
                onClick={() => statusMutation.mutate('SUSPENDED')}
              >
                Suspend
              </button>
            ) : (
              <button
                className="btn-primary"
                disabled={statusMutation.isPending}
                onClick={() => statusMutation.mutate('ACTIVE')}
              >
                Reactivate
              </button>
            )}
          </div>
        ) : null}
      </Panel>

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <Stat label="Live balance" value={formatMoney(wallet.balance, wallet.currency)} />
        <Stat label="Available" value={formatMoney(wallet.availableBalance, wallet.currency)} tone="accent" />
        <Stat
          label="Locked"
          value={formatMoney(wallet.lockedBalance, wallet.currency)}
          hint={'Open exposure ' + formatMoney(openExposure)}
        />
        <Stat
          label="Customer net result"
          value={formatSignedMoney(stats.netCustomerResult)}
          tone={stats.netCustomerResult >= 0 ? 'up' : 'down'}
          hint={stats.totalTrades + ' trades, ' + stats.openTrades + ' open'}
        />
        <Stat
          label="Demo balance"
          value={demoWallet ? formatMoney(demoWallet.balance, demoWallet.currency) : '--'}
          hint={demoWallet ? 'Practice money, locked ' + formatMoney(demoWallet.lockedBalance, demoWallet.currency) : undefined}
        />
      </div>

      <div className="grid gap-3 lg:grid-cols-2">
        <Panel>
          <PanelHeader title="Trading record" />
          <dl className="grid grid-cols-2 gap-px bg-surface-800/60">
            <Field label="Total trades" value={String(stats.totalTrades)} />
            <Field label="Open trades" value={String(stats.openTrades)} />
            <Field label="Won" value={String(stats.wonTrades)} />
            <Field label="Lost" value={String(stats.lostTrades)} />
            <Field label="Total staked" value={formatMoney(stats.totalStaked)} />
            <Field label="Joined" value={formatDateTime(user.createdAt)} />
          </dl>
        </Panel>

        <Panel>
          <PanelHeader
            title="Per-user limits"
            description="Overrides the global risk configuration for this account"
          />
          <div className="space-y-3 p-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <div>
                <label className="label" htmlFor="maxTrade">
                  Max trade amount
                </label>
                <input
                  id="maxTrade"
                  className="field tnum"
                  defaultValue={user.maxTradeAmount !== null ? fromMinor(user.maxTradeAmount) : ''}
                  placeholder="platform default"
                  onBlur={(event) => {
                    const raw = event.target.value.trim();
                    const value = raw === '' ? null : toMinor(Number(raw));
                    if (value !== user.maxTradeAmount) {
                      limitsMutation.mutate({ maxTradeAmount: value, maxUserExposure: user.maxUserExposure });
                    }
                  }}
                />
              </div>
              <div>
                <label className="label" htmlFor="maxExposure">
                  Max open exposure
                </label>
                <input
                  id="maxExposure"
                  className="field tnum"
                  defaultValue={user.maxUserExposure !== null ? fromMinor(user.maxUserExposure) : ''}
                  placeholder="platform default"
                  onBlur={(event) => {
                    const raw = event.target.value.trim();
                    const value = raw === '' ? null : toMinor(Number(raw));
                    if (value !== user.maxUserExposure) {
                      limitsMutation.mutate({ maxTradeAmount: user.maxTradeAmount, maxUserExposure: value });
                    }
                  }}
                />
              </div>
            </div>
            <p className="text-[11px] text-content-400">Leave blank to fall back to the platform limits.</p>
          </div>
        </Panel>
      </div>

      {can('wallets:adjust') ? (
        <Panel>
          <PanelHeader
            title="Manual wallet adjustment"
            description="Recorded in the append-only audit log with your account and the stated reason"
          />
          <form
            className="grid gap-3 p-4 sm:grid-cols-[140px_180px_minmax(0,1fr)_auto]"
            onSubmit={(event) => {
              event.preventDefault();
              if (adjustment.reason.trim().length < 3) {
                toast('error', 'A reason is required');
                return;
              }
              adjustMutation.mutate();
            }}
          >
            <div>
              <label className="label" htmlFor="direction">
                Direction
              </label>
              <select
                id="direction"
                className="field"
                value={adjustment.direction}
                onChange={(event) =>
                  setAdjustment((a) => ({ ...a, direction: event.target.value as 'CREDIT' | 'DEBIT' }))
                }
              >
                <option value="CREDIT">Credit</option>
                <option value="DEBIT">Debit</option>
              </select>
            </div>
            <div>
              <label className="label" htmlFor="amount">
                Amount
              </label>
              <input
                id="amount"
                className="field tnum"
                value={fromMinor(adjustment.amountMinor)}
                onChange={(event) => {
                  const parsed = Number(event.target.value.replace(/[^0-9.]/g, ''));
                  if (Number.isFinite(parsed)) setAdjustment((a) => ({ ...a, amountMinor: toMinor(parsed) }));
                }}
              />
            </div>
            <div>
              <label className="label" htmlFor="reason">
                Reason
              </label>
              <input
                id="reason"
                className="field"
                value={adjustment.reason}
                onChange={(event) => setAdjustment((a) => ({ ...a, reason: event.target.value }))}
                placeholder="Goodwill credit, ticket 1234"
              />
            </div>
            <div className="flex items-end">
              <button type="submit" className="btn-primary" disabled={adjustMutation.isPending}>
                Apply
              </button>
            </div>
          </form>
        </Panel>
      ) : null}
    </div>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="bg-surface-900 px-4 py-3">
      <dt className="text-[10px] uppercase tracking-wider text-content-400">{label}</dt>
      <dd className="mt-0.5 text-sm font-medium text-content-100 tnum">{value}</dd>
    </div>
  );
}
