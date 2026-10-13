import { useState } from 'react';
import { View } from 'react-native';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ArrowRight, CheckCircle2 } from 'lucide-react-native';
import { api } from '../../services/api';
import { Button, Icon, Text } from '../../components/ui';
import { formatUsdt, parseUsdt } from '../../utils/usdt';
import type { UsdtTransferResult, UsdtWalletView, Wallet } from '../../types/api';
import { amountProblem, usdtErrorMessage } from './model';
import { USDT_KEY, useSendOnce } from './queries';
import { createDeviceAttemptKey } from './checkout';
import { AmountInput, DetailList, ErrorNote } from './parts';

/**
 * Moves USDT from the wallet to the trading side, where it can be staked.
 *
 * Internal and immediate - nothing goes on chain - but still money, so it
 * is confirmed before it is sent and carries an idempotency key like any
 * other movement.
 */
export function TransferFlow({
  wallet,
  trading,
  onDone,
}: {
  wallet: UsdtWalletView | null;
  trading: Wallet | null;
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const [amountText, setAmountText] = useState('');
  const [reviewing, setReviewing] = useState(false);
  const [attempt] = useState(() => createDeviceAttemptKey());
  const [result, setResult] = useState<UsdtTransferResult | null>(null);

  const available = wallet?.availableBalance ?? 0;
  const amount = parseUsdt(amountText);
  const problem =
    amountProblem(amountText, amount) ??
    (amount !== null && amount > available
      ? 'That is more than your available balance of ' + formatUsdt(available, { trim: true }) + '.'
      : null);
  const ready = amount !== null && amount > 0 && !problem;

  const mutation = useMutation({
    mutationFn: (body: { amount: number }) => api.usdt.transferToTrading(body, attempt.for(body)),
    onSuccess: (transfer) => {
      attempt.reset();
      void queryClient.invalidateQueries({ queryKey: USDT_KEY });
      // The trading side's balances live under `wallet`.
      void queryClient.invalidateQueries({ queryKey: ['wallet'] });
      setResult(transfer);
    },
  });
  const send = useSendOnce(mutation.mutate);

  if (result) {
    return (
      <View className="gap-4">
        <View className="flex-row items-center gap-3">
          <View className="h-9 w-9 shrink-0 items-center justify-center rounded-full bg-up/15">
            <Icon as={CheckCircle2} size={20} className="text-up" />
          </View>
          <View className="min-w-0 flex-1">
            <Text accessibilityRole="header" className="text-sm font-bold text-content-100">
              Transfer complete
            </Text>
            <Text className="text-xs text-content-400">{formatUsdt(result.transfer.amount)} moved to Trading</Text>
          </View>
        </View>
        <DetailList
          rows={[
            { label: 'Moved', value: formatUsdt(result.transfer.amount), strong: true },
            { label: 'USDT wallet, available', value: formatUsdt(result.wallet.availableBalance) },
            {
              label: 'Trading, USDT balance',
              value: result.trading ? formatUsdt(result.trading.balance) : '--',
            },
          ]}
        />
        <Button onPress={onDone}>Done</Button>
      </View>
    );
  }

  if (reviewing && amount !== null) {
    return (
      <View className="gap-4">
        <Text accessibilityRole="header" className="text-sm font-bold text-content-100">
          Check your transfer
        </Text>
        <DetailList
          rows={[
            { label: 'Amount', value: formatUsdt(amount), strong: true },
            { label: 'From', value: 'USDT wallet' },
            { label: 'To', value: 'Trading' },
            { label: 'USDT wallet after', value: formatUsdt(Math.max(0, available - amount)) },
          ]}
        />
        <ErrorNote message={mutation.isError ? usdtErrorMessage(mutation.error) : null} />
        <View className="flex-row gap-2">
          <Button
            variant="ghost"
            disabled={mutation.isPending}
            onPress={() => {
              mutation.reset();
              setReviewing(false);
            }}
          >
            Edit
          </Button>
          <Button
            className="flex-1"
            disabled={mutation.isPending}
            loading={mutation.isPending}
            onPress={() => {
              send({ amount });
            }}
          >
            Confirm transfer
          </Button>
        </View>
      </View>
    );
  }

  function onSubmit() {
    if (ready) setReviewing(true);
  }

  return (
    <View className="gap-4">
      <View className="flex-row items-center gap-2">
        <View className="min-w-0 flex-1 rounded-lg border border-surface-700 bg-surface-850 px-3 py-2">
          <Text className="text-[11px] uppercase tracking-wider text-content-400">From</Text>
          <Text className="text-sm font-semibold text-content-100">USDT wallet</Text>
          <Text numberOfLines={1} className="text-[11px] text-content-400 tnum">
            {formatUsdt(available, { trim: true })}
          </Text>
        </View>
        <Icon as={ArrowRight} size={16} className="text-content-400" />
        <View className="min-w-0 flex-1 rounded-lg border border-surface-700 bg-surface-850 px-3 py-2">
          <Text className="text-[11px] uppercase tracking-wider text-content-400">To</Text>
          <Text className="text-sm font-semibold text-content-100">Trading</Text>
          <Text numberOfLines={1} className="text-[11px] text-content-400 tnum">
            {trading ? formatUsdt(trading.balance, { trim: true }) : 'USDT balance'}
          </Text>
        </View>
      </View>

      <AmountInput
        id="usdt-transfer-amount"
        value={amountText}
        onChange={(text) => {
          setAmountText(text);
          mutation.reset();
        }}
        max={available}
        error={problem}
        hint={
          <Text className="text-[11px] text-content-400">
            Available:{' '}
            <Text className="text-[11px] font-semibold text-content-200 tnum">{formatUsdt(available)}</Text>
          </Text>
        }
      />

      <Button onPress={onSubmit} disabled={!ready}>
        Review transfer
      </Button>
      <Text className="text-center text-[11px] leading-4 text-content-400">
        The transfer happens straight away. Your INR Live and Demo balances are not affected.
      </Text>
    </View>
  );
}
