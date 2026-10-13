import { useEffect, useState, type ReactNode } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import clsx from 'clsx';
import { ArrowDownToLine, ArrowLeft, ArrowRightLeft, ArrowUpFromLine, Clock3 } from 'lucide-react';
import { Modal } from '../components/Modal';
import { Panel, Skeleton } from '../components/ui';
import { formatUsdt } from '../utils/usdt';
import type { UsdtNetworkView, UsdtOverview } from '../types/api';
import { errorCode, usdtErrorMessage } from '../features/usdt/model';
import { useUsdtOverview } from '../features/usdt/queries';
import { NetworkChip, UsdtFigure, UsdtMark } from '../features/usdt/parts';
import { DepositFlow } from '../features/usdt/DepositFlow';
import { WithdrawFlow } from '../features/usdt/WithdrawFlow';
import { TransferFlow } from '../features/usdt/TransferFlow';
import { UsdtHistory, type UsdtDetail } from '../features/usdt/History';
import { TxDetails } from '../features/usdt/TxDetails';

type Flow = 'deposit' | 'withdraw' | 'transfer';
const FLOWS: Flow[] = ['deposit', 'withdraw', 'transfer'];

const TRANSFER_OFF_REASON = 'Trading with USDT is not available yet';

/**
 * USDT wallet.
 *
 * A balance of its own, apart from the INR Live and Demo accounts in the top
 * bar: USDT comes in from a crypto wallet over TRC20 or BSC, goes back out
 * the same way, and can be moved to the trading side. Deposits, withdrawals
 * and the transfer open over the page (a bottom sheet on a phone), so the
 * balances and history stay where they were.
 *
 * `?flow=deposit|withdraw|transfer` opens one directly, for the links from
 * Account and the wallet page.
 */
export function UsdtWalletPage() {
  const overview = useUsdtOverview();
  const [params, setParams] = useSearchParams();
  const [flow, setFlow] = useState<Flow | null>(null);
  const [detail, setDetail] = useState<UsdtDetail | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);

  const data = overview.data;
  // A server that answers USDT_DISABLED is saying the same as `enabled: false`.
  const disabled = data ? !data.enabled : errorCode(overview.error) === 'USDT_DISABLED';

  // Honour a deep link once the wallet is known to be usable, then drop the
  // parameter so a reload does not reopen the flow.
  const requested = params.get('flow') as Flow | null;
  useEffect(() => {
    if (!requested || !data) return;
    const allowed =
      data.enabled && FLOWS.includes(requested) && (requested !== 'transfer' || data.transferToTradingEnabled);
    if (allowed) setFlow(requested);
    const next = new URLSearchParams(params);
    next.delete('flow');
    setParams(next, { replace: true });
  }, [requested, data, params, setParams]);

  return (
    <div className="mx-auto max-w-5xl space-y-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Link to="/account#funds" className="inline-flex items-center gap-1.5 text-sm text-content-400 hover:text-content-100">
          <ArrowLeft className="h-4 w-4" />
          Account
        </Link>
        <h1 className="text-lg font-bold text-content-100">USDT wallet</h1>
        <span className="text-xs text-content-400">Separate from your INR Live and Demo balances</span>
      </div>

      {overview.isLoading ? (
        <BalancesSkeleton />
      ) : disabled ? (
        <Unavailable data={data} />
      ) : !data ? (
        <Panel className="flex flex-col items-center gap-3 px-6 py-10 text-center">
          <p className="text-sm font-medium text-content-200">Could not load your USDT wallet</p>
          <p className="max-w-sm text-xs text-content-400">{usdtErrorMessage(overview.error)}</p>
          <button type="button" className="btn-ghost" onClick={() => void overview.refetch()}>
            Try again
          </button>
        </Panel>
      ) : (
        <>
          <Balances data={data} onFlow={setFlow} />

          <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_300px]">
            <UsdtHistory
              networks={data.networks}
              onOpen={(next) => {
                setDetail(next);
                setDetailOpen(true);
              }}
            />
            <NetworksPanel networks={data.networks} />
          </div>
        </>
      )}

      {data && data.enabled ? (
        <>
          <Modal
            open={flow === 'deposit'}
            onClose={() => setFlow(null)}
            title="Deposit USDT"
            subtitle="Pay from any crypto wallet or exchange. Credited once the network confirms it."
            width="sm:w-[min(500px,94vw)]"
          >
            <DepositFlow networks={data.networks} onDone={() => setFlow(null)} />
          </Modal>
          <Modal
            open={flow === 'withdraw'}
            onClose={() => setFlow(null)}
            title="Withdraw USDT"
            subtitle="Sent to a USDT address on the network you choose."
            width="sm:w-[min(500px,94vw)]"
          >
            <WithdrawFlow networks={data.networks} wallet={data.wallet} onDone={() => setFlow(null)} />
          </Modal>
          <Modal
            open={flow === 'transfer'}
            onClose={() => setFlow(null)}
            title="Transfer to Trading"
            subtitle="Moves USDT from this wallet to your trading balance."
            width="sm:w-[min(460px,94vw)]"
          >
            <TransferFlow wallet={data.wallet} trading={data.trading} onDone={() => setFlow(null)} />
          </Modal>
        </>
      ) : null}

      {data ? (
        <Modal
          open={detailOpen}
          onClose={() => setDetailOpen(false)}
          title={
            detail?.kind === 'deposit'
              ? 'Deposit details'
              : detail?.kind === 'withdrawal'
                ? 'Withdrawal details'
                : 'Transaction details'
          }
          width="sm:w-[min(500px,94vw)]"
        >
          {detail ? <TxDetails detail={detail} networks={data.networks} /> : null}
        </Modal>
      ) : null}
    </div>
  );
}

/** Available first and largest; pending, locked and total beside it (under it on a phone). */
function Balances({ data, onFlow }: { data: UsdtOverview; onFlow: (flow: Flow) => void }) {
  const wallet = data.wallet;
  const canDeposit = data.networks.some((n) => n.depositEnabled);
  const canWithdraw = data.networks.some((n) => n.withdrawEnabled);

  return (
    <Panel className="p-4 sm:p-5">
      <div className="grid gap-4 sm:grid-cols-[minmax(0,1.6fr)_repeat(3,minmax(0,1fr))] sm:items-end">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <UsdtMark className="h-6 w-6 text-xs" />
            <span className="text-[11px] font-medium uppercase tracking-wider text-content-400">Available</span>
          </div>
          <div className="mt-1.5 break-words text-[28px] font-bold leading-tight text-content-100 sm:text-3xl">
            <UsdtFigure micro={wallet?.availableBalance ?? 0} />
          </div>
        </div>

        <dl className="contents">
          <Figure
            label="Pending"
            hint="Deposits being confirmed"
            value={wallet?.pendingDeposits ?? 0}
            icon={wallet && wallet.pendingDeposits > 0 ? <Clock3 className="h-3.5 w-3.5 text-warn" aria-hidden /> : null}
          />
          <Figure label="Locked" hint="Reserved by withdrawals" value={wallet?.lockedBalance ?? 0} />
          <Figure label="Total" hint="Available + locked" value={wallet?.balance ?? 0} />
        </dl>
      </div>

      {data.trading ? (
        <p className="mt-3 text-xs text-content-400">
          Trading balance:{' '}
          <span className="font-semibold text-content-200 tnum">{formatUsdt(data.trading.balance)}</span>
        </p>
      ) : null}

      {/* Tighter padding on a phone so "Withdraw USDT" stays on one line. */}
      <div className="mt-4 grid grid-cols-2 gap-2 border-t border-surface-800 pt-4 sm:grid-cols-3 [&>button]:whitespace-nowrap [&>button]:px-2.5 sm:[&>button]:px-4">
        <button type="button" className="btn-primary" onClick={() => onFlow('deposit')} disabled={!canDeposit}>
          <ArrowDownToLine className="h-4 w-4" />
          Deposit USDT
        </button>
        <button type="button" className="btn-ghost" onClick={() => onFlow('withdraw')} disabled={!canWithdraw}>
          <ArrowUpFromLine className="h-4 w-4" />
          Withdraw USDT
        </button>
        <button
          type="button"
          className="btn-ghost col-span-2 sm:col-span-1"
          onClick={() => onFlow('transfer')}
          disabled={!data.transferToTradingEnabled}
          aria-describedby={data.transferToTradingEnabled ? undefined : 'usdt-transfer-off'}
          title={data.transferToTradingEnabled ? undefined : TRANSFER_OFF_REASON}
        >
          <ArrowRightLeft className="h-4 w-4" />
          Transfer to Trading
        </button>
      </div>
      {!data.transferToTradingEnabled || !canDeposit || !canWithdraw ? (
        <ul className="mt-2 space-y-0.5 text-[11px] text-content-400">
          {!canDeposit ? <li>USDT deposits are paused right now.</li> : null}
          {!canWithdraw ? <li>USDT withdrawals are paused right now.</li> : null}
          {!data.transferToTradingEnabled ? <li id="usdt-transfer-off">{TRANSFER_OFF_REASON}.</li> : null}
        </ul>
      ) : null}
    </Panel>
  );
}

function Figure({
  label,
  hint,
  value,
  icon,
}: {
  label: string;
  hint: string;
  value: number;
  icon?: ReactNode;
}) {
  return (
    <div className="flex items-baseline justify-between gap-3 border-t border-surface-800 pt-2.5 sm:block sm:border-l sm:border-t-0 sm:pl-4 sm:pt-0">
      <dt className="text-[11px] font-medium uppercase tracking-wider text-content-400">
        <span className="inline-flex items-center gap-1">
          {label}
          {icon}
        </span>
        <span className="hidden text-[10px] font-normal normal-case tracking-normal sm:block">{hint}</span>
      </dt>
      <dd className="min-w-0 break-words text-right text-sm font-semibold text-content-100 tnum sm:mt-1 sm:text-left sm:text-base">
        {formatUsdt(value)}
      </dd>
    </div>
  );
}

function NetworksPanel({ networks }: { networks: UsdtNetworkView[] }) {
  return (
    <div className="space-y-3">
      <Panel className="overflow-hidden">
        <h2 className="border-b border-surface-800 px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wider text-content-400">
          Networks
        </h2>
        <ul className="divide-y divide-surface-800/70">
          {networks.map((network) => (
            <li key={network.code} className="px-4 py-2.5">
              <div className="flex items-center gap-2">
                <NetworkChip label={network.label} />
                <span className="min-w-0 truncate text-sm font-medium text-content-100">{network.name || network.chain}</span>
              </div>
              <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-content-400">
                <span className={clsx(!network.depositEnabled && 'text-down')}>
                  Deposits {network.depositEnabled ? 'open' : 'paused'}
                </span>
                <span className={clsx(!network.withdrawEnabled && 'text-down')}>
                  Withdrawals {network.withdrawEnabled ? 'open' : 'paused'}
                </span>
                {network.minDeposit > 0 ? (
                  <span className="tnum">Min deposit {formatUsdt(network.minDeposit, { trim: true })}</span>
                ) : null}
              </div>
            </li>
          ))}
        </ul>
      </Panel>
      <Panel className="space-y-2 p-4 text-xs leading-relaxed text-content-400">
        <p>
          <span className="font-semibold text-content-200">Same network both ends.</span> USDT sent on a different
          network, or a different token sent to your deposit address, cannot be recovered.
        </p>
        <p>
          <span className="font-semibold text-content-200">Credited on confirmation.</span> A deposit shows as Pending
          until the network confirms it; returning from the payment page alone does not credit anything.
        </p>
      </Panel>
    </div>
  );
}

function BalancesSkeleton() {
  return (
    <div className="space-y-3">
      <Panel className="space-y-3 p-5">
        <Skeleton className="h-4 w-24" />
        <Skeleton className="h-9 w-56" />
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
          <Skeleton className="h-9" />
          <Skeleton className="h-9" />
          <Skeleton className="col-span-2 h-9 sm:col-span-1" />
        </div>
      </Panel>
      <Panel className="space-y-2 p-3">
        {Array.from({ length: 4 }).map((_, index) => (
          <Skeleton key={index} className="h-12 w-full" />
        ))}
      </Panel>
    </div>
  );
}

/**
 * USDT switched off. No actions at all; if there is money in the wallet from
 * before, it is still shown, read only, so nobody thinks it has gone.
 */
function Unavailable({ data }: { data?: UsdtOverview }) {
  const wallet = data?.wallet ?? null;
  const held = !!wallet && (wallet.balance > 0 || wallet.pendingDeposits > 0);
  return (
    <div className="space-y-3">
      <Panel className="flex flex-col items-center gap-3 px-6 py-10 text-center">
        <UsdtMark className="h-12 w-12 text-xl" />
        <p className="text-base font-semibold text-content-100">USDT wallet is not available yet</p>
        <p className="max-w-md text-sm text-content-400">
          USDT deposits, withdrawals and transfers are not open on this platform yet. Your INR Live and Demo balances
          are not affected.
        </p>
        <Link to="/account#funds" className="btn-ghost mt-1">
          Back to account
        </Link>
      </Panel>
      {held && wallet ? (
        <Panel className="p-4">
          <dl className="grid gap-2 text-sm sm:grid-cols-3">
            <Figure label="Available" hint="" value={wallet.availableBalance} />
            <Figure label="Locked" hint="" value={wallet.lockedBalance} />
            <Figure label="Total" hint="" value={wallet.balance} />
          </dl>
        </Panel>
      ) : null}
    </div>
  );
}
