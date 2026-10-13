import { useState } from 'react';
import { View } from 'react-native';
import * as Linking from 'expo-linking';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ExternalLink } from 'lucide-react-native';
import { api } from '../../services/api';
import { Button, Label, Text } from '../../components/ui';
import { formatDateTime } from '../../utils/format';
import { formatUsdt, parseUsdt } from '../../utils/usdt';
import type { UsdtDepositView, UsdtNetworkView } from '../../types/api';
import {
  amountProblem,
  canStillPay,
  depositStatusExplanation,
  depositStatusTone,
  depositStatusWord,
  networkLabel,
  safeExternalUrl,
  usdtErrorMessage,
} from './model';
import { USDT_KEY, usdtKeys, useSendOnce, useUsdtDeposit } from './queries';
import { createDeviceAttemptKey, useOpenCheckout } from './checkout';
import {
  AmountInput,
  CopyValue,
  DetailList,
  ErrorNote,
  NetworkPicker,
  NetworkWarning,
  StatusBadge,
  UsdtMark,
} from './parts';

/**
 * USDT deposit: network, amount, then the provider's checkout.
 *
 * Continue creates the deposit on the server, which opens an invoice with the
 * payment provider and hands back its checkout page and the address to pay.
 * Nothing is credited here: the wallet moves only when the server has been
 * told by the provider that the money arrived. The API key the provider
 * issues stays on the server - the app only ever sees the checkout link.
 */
export function DepositFlow({ networks, onDone }: { networks: UsdtNetworkView[]; onDone: () => void }) {
  const queryClient = useQueryClient();
  const open = networks.filter((network) => network.depositEnabled);
  const [code, setCode] = useState<string>(open[0]?.code ?? '');
  const [amountText, setAmountText] = useState('');
  const [attempt] = useState(() => createDeviceAttemptKey());
  const [created, setCreated] = useState<UsdtDepositView | null>(null);

  const network = open.find((n) => n.code === code) ?? null;
  const amount = parseUsdt(amountText);

  const mutation = useMutation({
    // Same body, same key: a double tap or a retry after a timeout is one
    // deposit, not two invoices.
    mutationFn: (body: { network: string; amount: number }) => api.usdt.createDeposit(body, attempt.for(body)),
    onSuccess: (deposit) => {
      attempt.reset();
      queryClient.setQueryData(usdtKeys.deposit(deposit.id), deposit);
      void queryClient.invalidateQueries({ queryKey: USDT_KEY });
      setCreated(deposit);
    },
  });
  const send = useSendOnce(mutation.mutate);

  if (created) return <DepositCreated initial={created} networks={networks} onDone={onDone} />;

  if (open.length === 0) {
    return (
      <View className="rounded-lg border border-surface-700 bg-surface-850 px-4 py-3">
        <Text className="text-sm text-content-300">
          USDT deposits are paused on every network right now. Please try again later.
        </Text>
      </View>
    );
  }

  const problem =
    amountProblem(amountText, amount) ??
    (network && amount !== null && network.minDeposit > 0 && amount > 0 && amount < network.minDeposit
      ? 'The minimum deposit on ' + network.label + ' is ' + formatUsdt(network.minDeposit, { trim: true }) + '.'
      : null);
  const ready = !!network && amount !== null && amount > 0 && !problem;

  function onSubmit() {
    if (!ready || !network || amount === null || mutation.isPending) return;
    send({ network: network.code, amount });
  }

  return (
    <View className="gap-4">
      <View>
        <Label>Currency</Label>
        <View className="flex-row items-center gap-2.5 rounded-lg border border-surface-700 bg-surface-850 px-3 py-2">
          <UsdtMark size="sm" />
          <Text className="text-sm font-bold text-content-100">USDT</Text>
          <Text className="text-xs text-content-400">Tether USD</Text>
        </View>
      </View>

      <NetworkPicker
        purpose="deposit"
        networks={open}
        value={code}
        onChange={(next) => {
          setCode(next);
          mutation.reset();
        }}
      />

      <AmountInput
        id="usdt-deposit-amount"
        value={amountText}
        onChange={(text) => {
          setAmountText(text);
          mutation.reset();
        }}
        error={problem}
        hint={
          network && network.minDeposit > 0 && !problem ? (
            <Text className="text-[11px] text-content-400">
              Minimum on {network.label}:{' '}
              <Text className="text-[11px] font-semibold text-content-200 tnum">
                {formatUsdt(network.minDeposit, { trim: true })}
              </Text>
            </Text>
          ) : undefined
        }
      />

      <NetworkWarning />

      <ErrorNote message={mutation.isError ? usdtErrorMessage(mutation.error) : null} />

      <Button onPress={onSubmit} disabled={!ready || mutation.isPending} loading={mutation.isPending}>
        Continue
      </Button>
      <Text className="text-center text-[11px] leading-4 text-content-400">
        Continue opens a payment request. Your USDT wallet is credited once the payment is confirmed on the network.
      </Text>
    </View>
  );
}

/** The deposit just created: what to pay, where, and the way to the checkout. */
function DepositCreated({
  initial,
  networks,
  onDone,
}: {
  initial: UsdtDepositView;
  networks: UsdtNetworkView[];
  onDone: () => void;
}) {
  const query = useUsdtDeposit(initial.id, initial);
  const deposit = query.data ?? initial;
  const label = networkLabel(deposit.network, networks);
  const paymentUrl = canStillPay(deposit.status) ? safeExternalUrl(deposit.paymentUrl) : null;
  const openCheckout = useOpenCheckout();

  return (
    <View className="gap-4">
      <View className="flex-row items-center gap-3">
        <UsdtMark />
        <View className="min-w-0 flex-1">
          <Text className="text-sm font-bold text-content-100">Deposit created</Text>
          <Text className="text-xs text-content-400">Reference {deposit.reference}</Text>
        </View>
        <StatusBadge tone={depositStatusTone(deposit.status)}>{depositStatusWord(deposit.status)}</StatusBadge>
      </View>

      <Text className="text-sm text-content-300">{depositStatusExplanation(deposit, label)}</Text>

      <DetailList
        rows={[
          { label: 'Amount', value: formatUsdt(deposit.amountExpected), strong: true },
          { label: 'Network', value: label },
          { label: 'Status', value: depositStatusWord(deposit.status) },
          deposit.expiresAt ? { label: 'Pay before', value: formatDateTime(deposit.expiresAt) } : null,
        ]}
      />

      {deposit.payAddress && canStillPay(deposit.status) ? (
        <CopyValue label="Pay address" value={deposit.payAddress} />
      ) : null}

      <NetworkWarning />

      {paymentUrl ? (
        <View className="gap-1.5">
          {/* The checkout opens in the in-app browser; when it hands back,
              the deposit's status screen takes over. */}
          <Button iconRight={ExternalLink} onPress={() => void openCheckout(deposit.id, paymentUrl, { onLeave: onDone })}>
            Open payment page
          </Button>
          <Text className="text-center text-[11px] text-content-400">
            Page did not open?{' '}
            <Text
              accessibilityRole="link"
              onPress={() => void Linking.openURL(paymentUrl).catch(() => undefined)}
              className="text-[11px] font-semibold text-accent underline"
            >
              Open it in your browser
            </Text>
          </Text>
        </View>
      ) : null}

      <Button variant="ghost" onPress={onDone}>
        Done
      </Button>
    </View>
  );
}
