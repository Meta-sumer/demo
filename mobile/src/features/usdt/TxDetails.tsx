import { View } from 'react-native';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ExternalLink, RefreshCw } from 'lucide-react-native';
import { api } from '../../services/api';
import { Badge, Button, Text } from '../../components/ui';
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
import { useOpenCheckout } from './checkout';
import { CopyValue, DetailList, ErrorNote, NetworkWarning, StatusBadge, UsdtMark } from './parts';
import { WithdrawalSummary } from './WithdrawFlow';
import type { UsdtDetail } from './History';

/**
 * The full record behind a history row, kept current while it is still moving.
 *
 * `onLeave` closes whatever sheet this is drawn in before the deposit status
 * screen is pushed (after the payment page hands back).
 */
export function TxDetails({
  detail,
  networks,
  onLeave,
}: {
  detail: UsdtDetail;
  networks: UsdtNetworkView[];
  onLeave?: () => void;
}) {
  if (detail.kind === 'deposit') return <DepositDetails initial={detail.item} networks={networks} onLeave={onLeave} />;
  if (detail.kind === 'withdrawal') return <WithdrawalDetails initial={detail.item} networks={networks} />;
  return <LedgerDetails row={detail.item} networks={networks} />;
}

/** Sandbox / test invoices are marked so nobody mistakes one for real money. */
function isTestMode(mode: string | null | undefined): boolean {
  return !!mode && !['live', 'production', 'prod', 'real'].includes(mode.toLowerCase());
}

export function DepositDetails({
  initial,
  networks,
  onLeave,
}: {
  initial: UsdtDepositView;
  networks: UsdtNetworkView[];
  onLeave?: () => void;
}) {
  const queryClient = useQueryClient();
  const query = useUsdtDeposit(initial.id, initial);
  const deposit = query.data ?? initial;
  const label = networkLabel(deposit.network, networks);
  const paymentUrl = canStillPay(deposit.status) ? safeExternalUrl(deposit.paymentUrl) : null;
  const openCheckout = useOpenCheckout();

  const refresh = useMutation({
    mutationFn: () => api.usdt.refreshDeposit(deposit.id),
    onSuccess: (fresh) => queryClient.setQueryData(usdtKeys.deposit(fresh.id), fresh),
  });

  return (
    <View className="gap-4">
      <View className="flex-row items-center gap-3">
        <UsdtMark />
        <View className="min-w-0 flex-1">
          <View className="flex-row items-center gap-2">
            <Text accessibilityRole="header" className="text-sm font-bold text-content-100">
              USDT deposit
            </Text>
            {isTestMode(deposit.mode) ? <Badge tone="warn">Test</Badge> : null}
          </View>
          <Text numberOfLines={1} className="text-xs text-content-400">
            Reference {deposit.reference}
          </Text>
        </View>
        <StatusBadge tone={depositStatusTone(deposit.status)}>{depositStatusWord(deposit.status)}</StatusBadge>
      </View>

      <Text className="text-sm text-content-300">{depositStatusExplanation(deposit, label)}</Text>

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

      {paymentUrl || depositNeedsPolling(deposit) ? (
        <View className="gap-2">
          {paymentUrl ? (
            <Button iconRight={ExternalLink} onPress={() => void openCheckout(deposit.id, paymentUrl, { onLeave })}>
              Open payment page
            </Button>
          ) : null}
          {depositNeedsPolling(deposit) ? (
            <Button
              variant="ghost"
              icon={RefreshCw}
              loading={refresh.isPending}
              disabled={refresh.isPending}
              onPress={() => refresh.mutate()}
            >
              Refresh status
            </Button>
          ) : null}
        </View>
      ) : null}
    </View>
  );
}

function WithdrawalDetails({ initial, networks }: { initial: UsdtWithdrawalView; networks: UsdtNetworkView[] }) {
  const query = useUsdtWithdrawal(initial.id, initial);
  const withdrawal = query.data ?? initial;
  return (
    <View className="gap-4">
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
    </View>
  );
}

function LedgerDetails({ row, networks }: { row: UsdtTransaction; networks: UsdtNetworkView[] }) {
  const sign = ledgerSign(row);
  const amount = Math.abs(row.amount);
  return (
    <View className="gap-4">
      <View className="flex-row items-center gap-3">
        <UsdtMark />
        <View className="min-w-0 flex-1">
          <Text accessibilityRole="header" className="text-sm font-bold text-content-100">
            {ledgerLabel(row.type)}
          </Text>
          <Text className="text-xs text-content-400">{formatDateTime(row.createdAt)}</Text>
        </View>
        <StatusBadge tone={sign > 0 ? 'up' : 'neutral'}>{ledgerMovementWord(row.movement)}</StatusBadge>
      </View>
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
    </View>
  );
}
