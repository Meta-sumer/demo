import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ExternalLink, RefreshCw } from 'lucide-react';
import { api } from '../../services/api';
import { Badge, Spinner } from '../../components/ui';
import { formatDateTime } from '../../utils/format';
import { formatUsdt } from '../../utils/usdt';
import type { UsdtDepositView, UsdtNetworkView, UsdtTransaction, UsdtWithdrawalView } from '../../types/api';
import {
  canStillPay,
  depositNeedsPolling,
  depositStatusExplanation,
  depositStatusTone,
  depositStatusWord,
  ledgerLabel,
  ledgerMovementWord,
  ledgerSign,
  networkLabel,
  safeExternalUrl,
  usdtErrorMessage,
} from './model';
import { usdtKeys, useUsdtDeposit, useUsdtWithdrawal } from './queries';
import { CopyValue, DetailList, ErrorNote, NetworkWarning, StatusBadge, UsdtMark } from './parts';
import { WithdrawalSummary } from './WithdrawFlow';
import type { UsdtDetail } from './History';

/** The full record behind a history row, kept current while it is still moving. */
export function TxDetails({ detail, networks }: { detail: UsdtDetail; networks: UsdtNetworkView[] }) {
  if (detail.kind === 'deposit') return <DepositDetails initial={detail.item} networks={networks} />;
  if (detail.kind === 'withdrawal') return <WithdrawalDetails initial={detail.item} networks={networks} />;
  return <LedgerDetails row={detail.item} networks={networks} />;
}

/** Sandbox / test invoices are marked so nobody mistakes one for real money. */
function isTestMode(mode: string | null | undefined): boolean {
  return !!mode && !['live', 'production', 'prod', 'real'].includes(mode.toLowerCase());
}

export function DepositDetails({ initial, networks }: { initial: UsdtDepositView; networks: UsdtNetworkView[] }) {
  const queryClient = useQueryClient();
  const query = useUsdtDeposit(initial.id, initial);
  const deposit = query.data ?? initial;
  const label = networkLabel(deposit.network, networks);
  const paymentUrl = canStillPay(deposit.status) ? safeExternalUrl(deposit.paymentUrl) : null;

  const refresh = useMutation({
    mutationFn: () => api.usdt.refreshDeposit(deposit.id),
    onSuccess: (fresh) => queryClient.setQueryData(usdtKeys.deposit(fresh.id), fresh),
  });

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <UsdtMark />
        <div className="min-w-0 flex-1">
          <h3 className="flex items-center gap-2 text-sm font-bold text-content-100">
            USDT deposit
            {isTestMode(deposit.mode) ? <Badge tone="warn">Test</Badge> : null}
          </h3>
          <p className="truncate text-xs text-content-400">Reference {deposit.reference}</p>
        </div>
        <StatusBadge tone={depositStatusTone(deposit.status)}>{depositStatusWord(deposit.status)}</StatusBadge>
      </div>

      <p className="text-sm text-content-300">{depositStatusExplanation(deposit, label)}</p>

      <DetailList
        rows={[
          { label: 'Network', value: label },
          { label: 'Amount requested', value: formatUsdt(deposit.amountExpected) },
          { label: 'Received', value: formatUsdt(deposit.amountReceived) },
          deposit.feeAmount > 0 ? { label: 'Fee', value: formatUsdt(deposit.feeAmount) } : null,
          deposit.credited
            ? { label: 'Credited', value: formatUsdt(deposit.amountCredited), strong: true }
            : { label: 'Credited', value: 'Not yet' },
          deposit.creditedAt ? { label: 'Credited at', value: formatDateTime(deposit.creditedAt) } : null,
          deposit.expiresAt && canStillPay(deposit.status)
            ? { label: 'Pay before', value: formatDateTime(deposit.expiresAt) }
            : null,
          { label: 'Created', value: formatDateTime(deposit.createdAt) },
          { label: 'Order ID', value: deposit.orderId, mono: true },
        ]}
      />

      {deposit.payAddress && canStillPay(deposit.status) ? (
        <>
          <CopyValue label="Pay address" value={deposit.payAddress} />
          <NetworkWarning />
        </>
      ) : null}

      <ErrorNote message={refresh.isError ? usdtErrorMessage(refresh.error) : null} />

      <div className="grid gap-2 sm:grid-cols-2">
        {paymentUrl ? (
          <button type="button" className="btn-primary" onClick={() => window.location.assign(paymentUrl)}>
            Open payment page
            <ExternalLink className="h-4 w-4" />
          </button>
        ) : null}
        {depositNeedsPolling(deposit) ? (
          <button
            type="button"
            className="btn-ghost"
            onClick={() => refresh.mutate()}
            disabled={refresh.isPending}
          >
            {refresh.isPending ? <Spinner /> : <RefreshCw className="h-4 w-4" />}
            Refresh status
          </button>
        ) : null}
      </div>
    </div>
  );
}

function WithdrawalDetails({ initial, networks }: { initial: UsdtWithdrawalView; networks: UsdtNetworkView[] }) {
  const query = useUsdtWithdrawal(initial.id, initial);
  const withdrawal = query.data ?? initial;
  return (
    <div className="space-y-4">
      <WithdrawalSummary withdrawal={withdrawal} networks={networks} />
      <DetailList
        rows={[
          { label: 'Requested', value: formatDateTime(withdrawal.createdAt) },
          withdrawal.completedAt ? { label: 'Completed', value: formatDateTime(withdrawal.completedAt) } : null,
          { label: 'Last update', value: formatDateTime(withdrawal.updatedAt) },
          withdrawal.providerReference
            ? { label: 'Provider reference', value: withdrawal.providerReference, mono: true }
            : null,
        ]}
      />
    </div>
  );
}

function LedgerDetails({ row, networks }: { row: UsdtTransaction; networks: UsdtNetworkView[] }) {
  const sign = ledgerSign(row);
  const amount = Math.abs(row.amount);
  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <UsdtMark />
        <div className="min-w-0 flex-1">
          <h3 className="text-sm font-bold text-content-100">{ledgerLabel(row.type)}</h3>
          <p className="text-xs text-content-400">{formatDateTime(row.createdAt)}</p>
        </div>
        <StatusBadge tone={sign > 0 ? 'up' : 'neutral'}>{ledgerMovementWord(row.movement)}</StatusBadge>
      </div>
      <DetailList
        rows={[
          {
            label: 'Amount',
            value: formatUsdt(sign < 0 ? -amount : amount, { signed: sign > 0 }),
            strong: true,
          },
          row.fee ? { label: 'Fee', value: formatUsdt(row.fee) } : null,
          row.network ? { label: 'Network', value: networkLabel(row.network, networks) } : null,
          { label: 'Balance before', value: formatUsdt(row.balanceBefore) },
          { label: 'Balance after', value: formatUsdt(row.balanceAfter) },
          row.description ? { label: 'Description', value: row.description } : null,
          row.referenceId ? { label: 'Reference', value: row.referenceId, mono: true } : null,
        ]}
      />
    </div>
  );
}
