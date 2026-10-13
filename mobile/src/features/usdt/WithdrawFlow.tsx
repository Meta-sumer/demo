import { useState } from 'react';
import { Pressable, TextInput, View } from 'react-native';
import * as WebBrowser from 'expo-web-browser';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { CheckCircle2, Clock3, ExternalLink, Loader2, XCircle } from 'lucide-react-native';
import { api } from '../../services/api';
import { Button, Icon, Label, Text } from '../../components/ui';
import { InfoTip } from '../../components/InfoTip';
import { useDebouncedValue } from '../../hooks/useDebouncedValue';
import { cn } from '../../utils/cn';
import { useTokenColor } from '../../utils/theme-colors';
import { formatUsdt, parseUsdt } from '../../utils/usdt';
import type { UsdtNetworkView, UsdtWalletView, UsdtWithdrawalQuote, UsdtWithdrawalView } from '../../types/api';
import {
  addressHint,
  amountProblem,
  explorerTxUrl,
  isWithdrawalFinal,
  isWithdrawalReturned,
  networkLabel,
  usdtErrorMessage,
  withdrawalLimitProblem,
  withdrawalStatusExplanation,
  withdrawalStatusTone,
  withdrawalStatusWord,
} from './model';
import { USDT_KEY, usdtKeys, useSendOnce, useUsdtWithdrawal } from './queries';
import { createDeviceAttemptKey } from './checkout';
import {
  AmountInput,
  CopyValue,
  DetailList,
  ErrorNote,
  NetworkPicker,
  NetworkWarning,
  Spin,
  StatusBadge,
  monoStyle,
} from './parts';

const QUOTE_DEBOUNCE_MS = 400;

/**
 * USDT withdrawal: network, address, amount, a live quote, a confirmation
 * that repeats all of it, then the request and its progress.
 *
 * The amount is reserved the moment the server accepts the request. The fee
 * on the quote is the provider's estimate; the withdrawal record carries the
 * final one, and once it exists that is what is shown.
 */
export function WithdrawFlow({
  networks,
  wallet,
  onDone,
}: {
  networks: UsdtNetworkView[];
  wallet: UsdtWalletView | null;
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const open = networks.filter((network) => network.withdrawEnabled);
  const [code, setCode] = useState<string>(open[0]?.code ?? '');
  const [address, setAddress] = useState('');
  const [amountText, setAmountText] = useState('');
  const [step, setStep] = useState<'form' | 'review'>('form');
  const [attempt] = useState(() => createDeviceAttemptKey());
  const [result, setResult] = useState<UsdtWithdrawalView | null>(null);
  const [addressFocused, setAddressFocused] = useState(false);
  const placeholderColor = useTokenColor('content-400');
  const selectionColor = useTokenColor('accent');

  const network = open.find((n) => n.code === code) ?? null;
  const amount = parseUsdt(amountText);
  const settledAmount = useDebouncedValue(amount, QUOTE_DEBOUNCE_MS);
  const available = wallet?.availableBalance ?? null;

  const quoteQuery = useQuery({
    queryKey: usdtKeys.quote(code, settledAmount ?? 0),
    queryFn: () => api.usdt.withdrawalQuote({ network: code, amount: settledAmount! }),
    enabled: !!network && settledAmount !== null && settledAmount > 0 && step === 'form',
    placeholderData: (previous) => previous,
    staleTime: 15_000,
    retry: false,
  });
  // A quote counts only for exactly what is on screen now: not while the
  // amount is still settling, and not the previous amount's placeholder.
  const quote: UsdtWithdrawalQuote | null =
    quoteQuery.data && !quoteQuery.isPlaceholderData && settledAmount === amount && amount !== null
      ? quoteQuery.data
      : null;

  const mutation = useMutation({
    mutationFn: (body: { network: string; address: string; amount: number }) =>
      api.usdt.withdraw(body, attempt.for(body)),
    onSuccess: (withdrawal) => {
      attempt.reset();
      queryClient.setQueryData(usdtKeys.withdrawal(withdrawal.id), withdrawal);
      void queryClient.invalidateQueries({ queryKey: USDT_KEY });
      setResult(withdrawal);
    },
  });
  const send = useSendOnce(mutation.mutate);

  if (result) return <WithdrawalProgress initial={result} networks={networks} onDone={onDone} />;

  if (open.length === 0) {
    return (
      <View className="rounded-lg border border-surface-700 bg-surface-850 px-4 py-3">
        <Text className="text-sm text-content-300">
          USDT withdrawals are paused on every network right now. Please try again later.
        </Text>
      </View>
    );
  }

  const label = network?.label ?? '';
  const hint = network ? addressHint(network.code, address) : null;
  const typed = amountProblem(amountText, amount);
  const limit = amount !== null && amount > 0 ? withdrawalLimitProblem(amount, available, quote) : null;
  const quoteError = quoteQuery.isError && settledAmount === amount ? usdtErrorMessage(quoteQuery.error) : null;
  const ready =
    !!network && address.trim().length > 0 && amount !== null && amount > 0 && !typed && !limit && !!quote;

  function edit() {
    mutation.reset();
  }

  if (step === 'review' && network && amount !== null && quote) {
    const body = { network: network.code, address: address.trim(), amount };
    return (
      <View className="gap-4">
        <Text accessibilityRole="header" className="text-sm font-bold text-content-100">
          Check your withdrawal
        </Text>
        <DetailList
          rows={[
            { label: 'Network', value: label + (network.name ? ' · ' + network.name : '') },
            { label: 'To address', value: body.address, mono: true },
            { label: 'Amount', value: formatUsdt(quote.amount) },
            { label: quote.feeIsEstimate ? 'Fee (estimate)' : 'Fee', value: formatUsdt(quote.feeAmount) },
            { label: 'You receive', value: formatUsdt(quote.netAmount), strong: true },
            available !== null
              ? { label: 'Available after', value: formatUsdt(Math.max(0, available - quote.amount)) }
              : null,
          ]}
        />
        {quote.needsReview ? (
          <Text className="text-xs text-content-300">
            This withdrawal is checked by our team before it is sent. The amount stays reserved until then.
          </Text>
        ) : null}
        <NetworkWarning>
          Withdrawals cannot be reversed once sent. Make sure the address is a USDT address on the {label} network.
        </NetworkWarning>

        <ErrorNote message={mutation.isError ? usdtErrorMessage(mutation.error) : null} />

        <View className="flex-row gap-2">
          <Button
            variant="ghost"
            disabled={mutation.isPending}
            onPress={() => {
              edit();
              setStep('form');
            }}
          >
            Edit
          </Button>
          <Button
            className="flex-1"
            disabled={mutation.isPending}
            loading={mutation.isPending}
            onPress={() => {
              send(body);
            }}
          >
            Confirm withdrawal
          </Button>
        </View>
      </View>
    );
  }

  function onSubmit() {
    if (ready) setStep('review');
  }

  return (
    <View className="gap-4">
      <NetworkPicker
        purpose="withdraw"
        networks={open}
        value={code}
        onChange={(next) => {
          setCode(next);
          edit();
        }}
      />

      <View>
        <Label>Destination address</Label>
        <View
          className={cn(
            'min-h-[44px] flex-row items-center rounded-lg border bg-surface-850 px-3',
            hint ? 'border-warn' : addressFocused ? 'border-accent' : 'border-surface-700',
          )}
        >
          <TextInput
            testID="usdt-withdraw-address"
            accessibilityLabel="Destination address"
            autoComplete="off"
            autoCapitalize="none"
            autoCorrect={false}
            spellCheck={false}
            placeholder={network?.addressExample || (label === 'BSC' ? '0x…' : 'T…')}
            placeholderTextColor={placeholderColor}
            selectionColor={selectionColor}
            className="flex-1 py-2.5 text-[13px] text-content-100"
            style={monoStyle}
            value={address}
            onFocus={() => setAddressFocused(true)}
            onBlur={() => setAddressFocused(false)}
            onChangeText={(text) => {
              setAddress(text);
              edit();
            }}
          />
        </View>
        <Text className={cn('mt-1 text-[11px]', hint ? 'text-warn' : 'text-content-400')}>
          {hint ?? 'A USDT address on the ' + label + ' network. Paste it rather than typing it.'}
        </Text>
      </View>

      <AmountInput
        id="usdt-withdraw-amount"
        value={amountText}
        onChange={(text) => {
          setAmountText(text);
          edit();
        }}
        max={available ?? undefined}
        error={typed ?? limit ?? quoteError}
        hint={
          available !== null ? (
            <Text className="text-[11px] text-content-400">
              Available:{' '}
              <Text className="text-[11px] font-semibold text-content-200 tnum">{formatUsdt(available)}</Text>
            </Text>
          ) : undefined
        }
      />

      {amount !== null && amount > 0 && !typed && !quoteError ? (
        // The last quote stays up, dimmed, while the next one is fetched.
        <QuotePanel quote={quote ?? quoteQuery.data ?? null} loading={!quote} />
      ) : null}

      <Button onPress={onSubmit} disabled={!ready}>
        Review withdrawal
      </Button>
    </View>
  );
}

/** Amount, fee and what arrives, with the limits that apply - updated as the amount changes. */
function QuotePanel({ quote, loading }: { quote: UsdtWithdrawalQuote | null; loading: boolean }) {
  if (!quote) {
    return (
      <View className="flex-row items-center gap-2 rounded-lg border border-surface-700 bg-surface-850 px-3.5 py-3">
        {loading ? (
          <Spin>
            <Icon as={Loader2} size={14} className="text-content-400" />
          </Spin>
        ) : null}
        <Text className="text-xs text-content-400">
          {loading ? 'Calculating the fee…' : 'Enter an amount to see the fee.'}
        </Text>
      </View>
    );
  }
  const limits = [
    quote.minWithdrawal > 0 ? 'Min ' + formatUsdt(quote.minWithdrawal, { trim: true }) : null,
    quote.maxWithdrawal > 0 ? 'Max ' + formatUsdt(quote.maxWithdrawal, { trim: true }) : null,
    quote.dailyCap > 0
      ? 'Daily limit ' +
        formatUsdt(quote.dailyCap, { trim: true }) +
        ' (' +
        formatUsdt(quote.dailyUsed, { trim: true, unit: false }) +
        ' used)'
      : null,
  ].filter(Boolean);

  return (
    <View
      className={cn('gap-2', loading && 'opacity-60')}
      accessibilityLiveRegion="polite"
      accessibilityState={{ busy: loading }}
    >
      <DetailList
        rows={[
          { label: 'Amount', value: formatUsdt(quote.amount) },
          {
            label: (
              <View className="flex-row items-center gap-0.5">
                <Text className="text-xs text-content-400">{quote.feeIsEstimate ? 'Fee (estimate)' : 'Fee'}</Text>
                {quote.feeIsEstimate ? (
                  <InfoTip label="the fee estimate">
                    The network fee can change a little until the withdrawal is sent. The final fee is shown on the
                    withdrawal once it is created.
                  </InfoTip>
                ) : null}
              </View>
            ),
            value: formatUsdt(quote.feeAmount),
          },
          { label: 'You receive', value: formatUsdt(quote.netAmount), strong: true },
        ]}
      />
      {limits.length > 0 ? <Text className="text-[11px] text-content-400 tnum">{limits.join(' · ')}</Text> : null}
      {quote.needsReview ? (
        <Text className="text-[11px] text-content-300">
          {quote.reviewedAbove > 0
            ? 'Withdrawals above ' + formatUsdt(quote.reviewedAbove, { trim: true }) + ' are checked by our team first.'
            : 'This withdrawal is checked by our team first.'}
        </Text>
      ) : null}
    </View>
  );
}

/**
 * The withdrawal after it was accepted, kept current until it is completed,
 * failed or rejected. Amounts come from the record - the final fee, not the
 * estimate that was quoted.
 */
export function WithdrawalProgress({
  initial,
  networks,
  onDone,
}: {
  initial: UsdtWithdrawalView;
  networks: UsdtNetworkView[];
  onDone?: () => void;
}) {
  const query = useUsdtWithdrawal(initial.id, initial);
  const withdrawal = query.data ?? initial;
  return (
    <View className="gap-4">
      <WithdrawalSummary withdrawal={withdrawal} networks={networks} />
      {onDone ? (
        <Button variant="ghost" onPress={onDone}>
          Done
        </Button>
      ) : null}
    </View>
  );
}

export function WithdrawalSummary({
  withdrawal,
  networks,
}: {
  withdrawal: UsdtWithdrawalView;
  networks: UsdtNetworkView[];
}) {
  const label = networkLabel(withdrawal.network, networks);
  const final = isWithdrawalFinal(withdrawal.status);
  const returned = isWithdrawalReturned(withdrawal.status);
  const reason = withdrawal.failureReason || withdrawal.reviewNote;
  const explorer = withdrawal.txHash ? explorerTxUrl(withdrawal.network, withdrawal.txHash) : null;
  // A review can take hours, so it waits on a clock; a payout on its way spins.
  const reviewing = withdrawal.status === 'pending_review';
  const glyph = withdrawal.status === 'completed' ? CheckCircle2 : returned ? XCircle : reviewing ? Clock3 : Loader2;
  const tone = withdrawal.status === 'completed' ? 'text-up' : returned ? 'text-down' : 'text-warn';
  const statusIcon = <Icon as={glyph} size={20} className={tone} />;

  return (
    <View className="gap-4">
      <View className="flex-row items-center gap-3">
        <View
          className={cn(
            'h-9 w-9 shrink-0 items-center justify-center rounded-full',
            withdrawal.status === 'completed' ? 'bg-up/15' : returned ? 'bg-down/15' : 'bg-warn/15',
          )}
        >
          {!final && !reviewing ? <Spin>{statusIcon}</Spin> : statusIcon}
        </View>
        <View className="min-w-0 flex-1">
          <Text accessibilityRole="header" className="text-sm font-bold text-content-100">
            {withdrawal.status === 'completed'
              ? 'Withdrawal completed'
              : returned
                ? withdrawal.status === 'rejected'
                  ? 'Withdrawal rejected'
                  : 'Withdrawal failed'
                : 'Withdrawal requested'}
          </Text>
          <Text className="text-xs text-content-400">Reference {withdrawal.reference}</Text>
        </View>
        <StatusBadge tone={withdrawalStatusTone(withdrawal.status)}>
          {withdrawalStatusWord(withdrawal.status)}
        </StatusBadge>
      </View>

      <Text className="text-sm text-content-300">{withdrawalStatusExplanation(withdrawal, label)}</Text>

      {returned ? (
        <View className="rounded-lg border border-down/30 bg-down/10 px-3 py-2.5">
          {reason ? <Text className="text-sm text-content-100">{reason}</Text> : null}
          <Text className={cn('text-sm text-content-300', reason && 'mt-1 text-xs')}>
            The funds were returned to your wallet.
          </Text>
        </View>
      ) : null}

      <DetailList
        rows={[
          { label: 'Network', value: label },
          { label: 'To address', value: withdrawal.address, mono: true },
          { label: 'Amount', value: formatUsdt(withdrawal.amount) },
          { label: 'Fee', value: formatUsdt(withdrawal.feeAmount) },
          {
            label: returned ? 'Would have received' : 'You receive',
            value: formatUsdt(withdrawal.netAmount),
            strong: true,
          },
        ]}
      />

      {withdrawal.txHash ? (
        <View className="gap-1.5">
          <CopyValue label="TX hash" value={withdrawal.txHash} />
          {explorer ? (
            <Pressable
              accessibilityRole="link"
              onPress={() => void WebBrowser.openBrowserAsync(explorer).catch(() => undefined)}
              className="flex-row items-center gap-1 self-start py-1 active:opacity-70"
            >
              <Text className="text-xs font-semibold text-accent">View on the {label} explorer</Text>
              <Icon as={ExternalLink} size={14} className="text-accent" />
            </Pressable>
          ) : null}
        </View>
      ) : null}
    </View>
  );
}
