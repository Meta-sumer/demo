import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Pressable, View } from 'react-native';
import { router } from 'expo-router';
import { Image } from 'expo-image';
import * as ImagePicker from 'expo-image-picker';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowDownToLine, ArrowUpFromLine, Check, Copy, Paperclip, X } from 'lucide-react-native';
import { api, type UploadFile } from '../../services/api';
import { ApiRequestError } from '../../services/http';
import { useAccountStore } from '../../stores/accountStore';
import { formatMoney, fromMinor, toMinor } from '../../utils/format';
import { cn } from '../../utils/cn';
import { toast } from '../../hooks/useToast';
import { Screen, ScreenHeader } from '../../components/Screen';
import { Button, Field, Icon, Label, Panel, Text } from '../../components/ui';
import { monoStyle, useCopied } from '../usdt/parts';
import { MethodPicker } from './MethodPicker';
import type { PaymentMethod, PayoutDestination } from '../../types/api';

const QUICK = [1_000_00, 5_000_00, 10_000_00, 25_000_00];
const MAX_PROOF_BYTES = 5 * 1024 * 1024;
/** What the server accepts as a screenshot, by its own bytes (it also takes a PDF, which a photo picker cannot offer). */
const PROOF_TYPES = new Set(['image/png', 'image/jpeg', 'image/jpg', 'image/webp']);

/**
 * Manual deposit and withdrawal.
 *
 * Money moves outside the platform. A deposit is therefore a *claim*: the
 * customer pays to the account shown here, then submits the UTR and/or a
 * screenshot, and an operator credits the wallet only after checking it against
 * the bank. The copy on this screen says so plainly, because a balance that
 * appears instantly and then disappears on rejection is worse than a wait.
 */
export function TransferPage({ kind }: { kind: 'DEPOSIT' | 'WITHDRAWAL' }) {
  // The request is listed on the Transactions screen; the form is not
  // somewhere to come back to.
  const onDone = () => router.replace('/wallet');

  return kind === 'DEPOSIT' ? (
    <PageShell
      title="Deposit to Live account"
      subtitle="Pay to the account below, then submit the reference so we can verify it."
    >
      <View className="mt-5">
        <DepositForm onDone={onDone} />
      </View>
    </PageShell>
  ) : (
    <PageShell
      title="Withdraw from Live account"
      subtitle="The amount is reserved immediately and paid out once an operator approves it."
    >
      <View className="mt-5">
        <WithdrawForm onDone={onDone} />
      </View>
    </PageShell>
  );
}

function PageShell({ title, subtitle, children }: { title: string; subtitle: string; children: ReactNode }) {
  return (
    <View className="flex-1 bg-surface-950">
      <ScreenHeader title={title} fallbackHref="/account?section=funds" />
      <Screen bottomInset>
        <Panel className="p-5">
          <Text className="text-sm text-content-400">{subtitle}</Text>
          {children}
        </Panel>
      </Screen>
    </View>
  );
}

function AmountField({ amountMinor, onChange }: { amountMinor: number; onChange: (minor: number) => void }) {
  // The text is kept as typed, so "12." can become "12.5"; the amount itself
  // is still only ever the parsed minor units.
  const [text, setText] = useState(() => String(fromMinor(amountMinor)));

  useEffect(() => {
    // A quick amount (or a reset) from outside replaces what was typed.
    setText((current) => {
      const parsed = Number(current.replace(/[^0-9.]/g, ''));
      return Number.isFinite(parsed) && toMinor(parsed) === amountMinor ? current : String(fromMinor(amountMinor));
    });
  }, [amountMinor]);

  return (
    <View>
      <Field
        label="Amount"
        accessibilityLabel="Amount"
        keyboardType="decimal-pad"
        value={text}
        onChangeText={(value) => {
          const cleaned = value.replace(/[^0-9.]/g, '');
          const parsed = Number(cleaned);
          if (Number.isFinite(parsed)) {
            setText(cleaned);
            onChange(toMinor(parsed));
          }
        }}
      />
      <View className="mt-2 flex-row flex-wrap gap-1">
        {QUICK.map((value) => (
          <Pressable
            key={value}
            onPress={() => onChange(value)}
            accessibilityRole="button"
            accessibilityState={{ selected: amountMinor === value }}
            className={cn(
              'rounded-md px-2 py-1',
              amountMinor === value ? 'bg-accent' : 'bg-surface-800 active:bg-surface-700',
            )}
          >
            <Text
              className={cn(
                'text-[11px] font-medium tnum',
                amountMinor === value ? 'text-surface-950' : 'text-content-300',
              )}
            >
              {formatMoney(value, 'INR', false)}
            </Text>
          </Pressable>
        ))}
      </View>
    </View>
  );
}

/**
 * Says which way the money is going, first thing in the form.
 *
 * Deposit and withdrawal share every building block (brief UX-20), so this is
 * what keeps them from being mistaken for each other: its own icon, its own
 * tone and the words "into" / "out of". Both name the Live account - demo
 * balance is virtual and never moves through here.
 */
function KindBanner({ kind }: { kind: 'DEPOSIT' | 'WITHDRAWAL' }) {
  const deposit = kind === 'DEPOSIT';
  return (
    <View
      className={cn(
        'flex-row items-center gap-3 rounded-lg border px-3 py-2.5',
        deposit ? 'border-up/30 bg-up/10' : 'border-accent/30 bg-accent/10',
      )}
    >
      <View
        className={cn('h-8 w-8 shrink-0 items-center justify-center rounded-lg', deposit ? 'bg-up/15' : 'bg-accent/15')}
      >
        <Icon
          as={deposit ? ArrowDownToLine : ArrowUpFromLine}
          size={16}
          className={deposit ? 'text-up' : 'text-accent'}
        />
      </View>
      <View className="min-w-0 flex-1">
        <Text className={cn('text-sm font-bold', deposit ? 'text-up' : 'text-content-100')}>
          {deposit ? 'Deposit into your Live account' : 'Withdraw from your Live account'}
        </Text>
        <Text className="text-xs leading-4 text-content-400">Real money. Your demo balance is not affected.</Text>
      </View>
    </View>
  );
}

/** Limits line under the amount, for either direction. */
function LimitsHint({ limits }: { limits: { min: number; max: number } | null }) {
  if (!limits) return null;
  return (
    <Text className="mt-1 text-[11px] text-content-400">
      Minimum <Text className="text-[11px] font-semibold text-content-200 tnum">{formatMoney(limits.min)}</Text> ·
      maximum <Text className="text-[11px] font-semibold text-content-200 tnum">{formatMoney(limits.max)}</Text>
    </Text>
  );
}

function FormError({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <View
      accessibilityRole="alert"
      accessibilityLiveRegion="polite"
      className="rounded-lg border border-down/30 bg-down/10 px-3 py-2"
    >
      <Text className="text-sm text-down">{message}</Text>
    </View>
  );
}

/**
 * The review step before a request is sent.
 *
 * Money requests are checked by hand and cannot be recalled once a payment is
 * made against them, so the customer sees exactly what will be submitted -
 * and can go back to change it - before anything reaches the server. The
 * buttons say what they do ("Confirm deposit", "Edit"), never "Continue".
 */
function ReviewStep({
  kind,
  rows,
  error,
  pending,
  onConfirm,
  onEdit,
  footnote,
}: {
  kind: 'DEPOSIT' | 'WITHDRAWAL';
  rows: Array<{ label: string; value: string; strong?: boolean }>;
  error: string | null;
  pending: boolean;
  onConfirm: () => void;
  onEdit: () => void;
  footnote: string;
}) {
  const deposit = kind === 'DEPOSIT';
  return (
    <View className="gap-4">
      <KindBanner kind={kind} />
      <View>
        <Text accessibilityRole="header" className="text-sm font-bold text-content-100">
          {deposit ? 'Check your deposit' : 'Check your withdrawal'}
        </Text>
        <View className="mt-2 rounded-lg border border-surface-700 bg-surface-850">
          {rows.map((row, index) => (
            <View
              key={row.label}
              className={cn(
                'flex-row items-start justify-between gap-4 px-4 py-2.5',
                index > 0 && 'border-t border-surface-800',
              )}
            >
              <Text className="shrink-0 text-xs text-content-400">{row.label}</Text>
              <Text
                className={cn(
                  'min-w-0 flex-1 text-right tnum',
                  row.strong ? 'text-base font-bold text-content-100' : 'text-sm font-medium text-content-200',
                )}
              >
                {row.value}
              </Text>
            </View>
          ))}
        </View>
      </View>

      <FormError message={error} />

      <View className="flex-row gap-2">
        <Button variant="ghost" onPress={onEdit} disabled={pending}>
          Edit
        </Button>
        <Button className="flex-1" onPress={onConfirm} disabled={pending} loading={pending}>
          {deposit ? 'Confirm deposit' : 'Confirm withdrawal'}
        </Button>
      </View>

      <Text className="text-center text-[11px] leading-4 text-content-400">{footnote}</Text>
    </View>
  );
}

function destinationLine(method: PaymentMethod | null): string {
  if (!method) return '--';
  if (method.type === 'UPI') return method.label + ' · ' + (method.upiId ?? '--');
  return method.label + ' · ' + [method.accountNumber, method.ifsc].filter(Boolean).join(' · ');
}

/** Account details are copied, not retyped - a mistyped UPI id loses real money. */
function CopyRow({ label, value, first = false }: { label: string; value: string; first?: boolean }) {
  const { copied, copy } = useCopied();

  return (
    <View className={cn('flex-row items-center gap-2 py-2', !first && 'border-t border-surface-800')}>
      <Text className="w-28 shrink-0 text-[11px] uppercase tracking-wider text-content-400">{label}</Text>
      <Text selectable numberOfLines={1} className="min-w-0 flex-1 text-sm text-content-100" style={monoStyle}>
        {value}
      </Text>
      <Pressable
        accessibilityRole="button"
        accessibilityLabel={'Copy ' + label}
        hitSlop={6}
        className="rounded-md p-1.5 active:bg-surface-800"
        onPress={() => void copy(value)}
      >
        {copied ? (
          <Icon as={Check} size={16} className="text-up" />
        ) : (
          <Icon as={Copy} size={16} className="text-content-400" />
        )}
      </Pressable>
    </View>
  );
}

/** The picked screenshot, with its size when the picker reports one. */
interface PickedProof {
  file: UploadFile;
  size: number | null;
}

const EXTENSION: Record<string, string> = { 'image/png': 'png', 'image/webp': 'webp' };

/**
 * Deposit claim.
 *
 * Rendered both as a screen and inside the sheet, so it owns no navigation of
 * its own - the host decides where "done" goes.
 */
export function DepositForm({ onDone }: { onDone: () => void }) {
  const queryClient = useQueryClient();

  const [amountMinor, setAmountMinor] = useState(QUICK[0]!);
  const [methodKey, setMethodKey] = useState<string>('');
  const [utr, setUtr] = useState('');
  const [note, setNote] = useState('');
  const [proof, setProof] = useState<PickedProof | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [reviewing, setReviewing] = useState(false);

  const methodsQuery = useQuery({ queryKey: ['payment-methods'], queryFn: api.transfers.paymentMethods });
  const methods = useMemo(() => methodsQuery.data ?? [], [methodsQuery.data]);
  const selected = methods.find((m) => m.key === methodKey) ?? methods[0] ?? null;

  useEffect(() => {
    if (!methodKey && methods.length > 0) setMethodKey(methods[0]!.key);
  }, [methods, methodKey]);

  const mutation = useMutation({
    mutationFn: () =>
      api.transfers.deposit({
        amount: amountMinor,
        paymentMethodKey: selected?.key,
        utr: utr.trim() || undefined,
        customerNote: note.trim() || undefined,
        proof: proof?.file ?? null,
      }),
    onSuccess: (transfer) => {
      void queryClient.invalidateQueries({ queryKey: ['transfers'] });
      toast(
        'success',
        'Deposit submitted for review',
        transfer.reference + ' — your wallet is credited once an operator confirms the payment',
      );
      onDone();
    },
    onError: (err) => setError(err instanceof ApiRequestError ? err.message : 'Could not submit the deposit'),
  });

  async function pickProof() {
    const result = await ImagePicker.launchImageLibraryAsync({
      mediaTypes: 'images',
      allowsMultipleSelection: false,
      // Hands back a JPEG for a photo stored as HEIC, which the server would
      // not recognise.
      preferredAssetRepresentationMode: ImagePicker.UIImagePickerPreferredAssetRepresentationMode.Compatible,
    }).catch(() => null);
    const asset = result && !result.canceled ? result.assets[0] : null;
    if (!asset) return;

    const type = (asset.mimeType ?? 'image/jpeg').toLowerCase();
    if (!PROOF_TYPES.has(type)) {
      setError('Screenshots must be PNG, JPEG or WebP images');
      return;
    }
    const size = typeof asset.fileSize === 'number' ? asset.fileSize : null;
    if (size !== null && size > MAX_PROOF_BYTES) {
      setError('Screenshots must be under 5 MB');
      return;
    }
    setError(null);
    setProof({
      file: {
        uri: asset.uri,
        name: asset.fileName || 'screenshot.' + (EXTENSION[type] ?? 'jpg'),
        type: type === 'image/jpg' ? 'image/jpeg' : type,
      },
      size,
    });
  }

  function onSubmit() {
    setError(null);

    if (amountMinor <= 0) return setError('Enter an amount greater than zero');
    if (selected && (amountMinor < selected.minAmount || amountMinor > selected.maxAmount)) {
      return setError(
        'Deposits through ' +
          selected.label +
          ' must be between ' +
          formatMoney(selected.minAmount) +
          ' and ' +
          formatMoney(selected.maxAmount),
      );
    }
    // Either is accepted; without one there is nothing for an operator to check.
    if (!utr.trim() && !proof) {
      return setError('Enter the UTR / reference number of your payment, or attach a screenshot');
    }
    setReviewing(true);
  }

  if (reviewing) {
    return (
      <ReviewStep
        kind="DEPOSIT"
        rows={[
          { label: 'Amount', value: formatMoney(amountMinor), strong: true },
          { label: 'Paid to', value: destinationLine(selected) },
          { label: 'UTR / reference', value: utr.trim() ? utr.trim().toUpperCase() : 'Not entered' },
          { label: 'Screenshot', value: proof ? proof.file.name : 'None' },
          ...(note.trim() ? [{ label: 'Note', value: note.trim() }] : []),
          { label: 'Credited to', value: 'Live account, after review' },
        ]}
        error={error}
        pending={mutation.isPending}
        onConfirm={() => {
          setError(null);
          mutation.mutate();
        }}
        onEdit={() => {
          setError(null);
          setReviewing(false);
        }}
        footnote="Your wallet is credited only after an operator matches this against our bank statement."
      />
    );
  }

  return (
    <View className="gap-4">
      <KindBanner kind="DEPOSIT" />

      <MethodPicker
        methods={methods}
        selectedKey={selected?.key ?? null}
        onSelect={setMethodKey}
        loading={methodsQuery.isLoading}
      />

      {selected ? (
        <View className="rounded-lg border border-surface-700 bg-surface-850 px-4 py-3">
          <Text className="mb-1 text-[11px] font-semibold uppercase tracking-wider text-content-400">
            Step 1 — pay to
          </Text>
          {selected.type === 'UPI' ? (
            <CopyRow first label="UPI ID" value={selected.upiId ?? '--'} />
          ) : (
            <>
              <CopyRow first label="Account name" value={selected.accountName ?? '--'} />
              <CopyRow label="Account no." value={selected.accountNumber ?? '--'} />
              <CopyRow label="IFSC" value={selected.ifsc ?? '--'} />
              {selected.bankName ? <CopyRow label="Bank" value={selected.bankName} /> : null}
            </>
          )}
          {selected.instructions ? (
            <View className="mt-2 border-t border-surface-800 pt-2">
              <Text className="text-[11px] leading-4 text-content-400">{selected.instructions}</Text>
            </View>
          ) : null}
        </View>
      ) : null}

      <View>
        <AmountField amountMinor={amountMinor} onChange={setAmountMinor} />
        <LimitsHint limits={selected ? { min: selected.minAmount, max: selected.maxAmount } : null} />
      </View>

      <View className="rounded-lg border border-surface-700 bg-surface-850 px-4 py-3">
        <Text className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-content-400">
          Step 2 — prove the payment
        </Text>

        <Field
          label="UTR / reference number"
          accessibilityLabel="UTR / reference number"
          placeholder="e.g. 412345678901"
          autoCapitalize="characters"
          autoCorrect={false}
          spellCheck={false}
          style={monoStyle}
          value={utr}
          onChangeText={setUtr}
        />
        <Text className="mt-1 text-[11px] text-content-400">
          The reference shown on your payment receipt. Each payment can only be submitted once.
        </Text>

        <View className="mt-3">
          <Label>Screenshot (optional if you entered a UTR)</Label>
          {proof ? (
            <View className="flex-row items-center gap-2 rounded-lg border border-surface-700 bg-surface-900 px-3 py-2">
              <Image
                source={{ uri: proof.file.uri }}
                contentFit="cover"
                style={{ width: 36, height: 36, borderRadius: 6 }}
                accessibilityLabel="Attached screenshot"
              />
              <Icon as={Paperclip} size={16} className="text-content-400" />
              <Text numberOfLines={1} className="min-w-0 flex-1 text-xs text-content-200">
                {proof.file.name}
              </Text>
              {proof.size !== null ? (
                <Text className="shrink-0 text-[11px] text-content-400">{Math.ceil(proof.size / 1024)} KB</Text>
              ) : null}
              <Pressable
                accessibilityRole="button"
                accessibilityLabel="Remove attachment"
                hitSlop={6}
                className="rounded p-1 active:opacity-70"
                onPress={() => setProof(null)}
              >
                <Icon as={X} size={16} className="text-content-400" />
              </Pressable>
            </View>
          ) : (
            <Button variant="subtle" size="sm" icon={Paperclip} className="self-start" onPress={() => void pickProof()}>
              Choose screenshot
            </Button>
          )}
        </View>
      </View>

      <Field
        label="Note (optional)"
        accessibilityLabel="Note (optional)"
        placeholder="Anything the reviewer should know"
        value={note}
        onChangeText={setNote}
      />

      <FormError message={error} />

      <Button onPress={onSubmit}>Review deposit</Button>

      <Text className="text-center text-[11px] leading-4 text-content-400">
        Your wallet is credited only after an operator matches this against our bank statement. Submitting a reference
        you did not pay will simply be rejected.
      </Text>
    </View>
  );
}

/** Withdrawal request. Same arrangement as DepositForm. */
export function WithdrawForm({ onDone }: { onDone: () => void }) {
  const queryClient = useQueryClient();
  // Withdrawals only ever come out of the live account, whichever one is
  // being traded on.
  const wallet = useAccountStore((s) => s.wallets.LIVE ?? null);

  const [amountMinor, setAmountMinor] = useState(QUICK[0]!);
  const [type, setType] = useState<'UPI' | 'BANK'>('UPI');
  const [upiId, setUpiId] = useState('');
  const [bank, setBank] = useState({ accountName: '', accountNumber: '', ifsc: '', bankName: '' });
  const [note, setNote] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [reviewing, setReviewing] = useState(false);

  const insufficient = !!wallet && wallet.availableBalance < amountMinor;

  // The same configured methods the deposit side lists. Payouts go out over
  // the same rails, so their limits are the ones that apply: the widest range
  // any method of the chosen kind accepts.
  const methodsQuery = useQuery({ queryKey: ['payment-methods'], queryFn: api.transfers.paymentMethods });
  const limits = useMemo(() => {
    const all = methodsQuery.data ?? [];
    const ofType = all.filter((method) => method.type === type);
    const pool = ofType.length > 0 ? ofType : all;
    if (pool.length === 0) return null;
    return {
      min: Math.min(...pool.map((method) => method.minAmount)),
      max: Math.max(...pool.map((method) => method.maxAmount)),
    };
  }, [methodsQuery.data, type]);

  const mutation = useMutation({
    mutationFn: () => {
      const destination: PayoutDestination =
        type === 'UPI'
          ? { type: 'UPI', upiId: upiId.trim() }
          : {
              type: 'BANK',
              accountName: bank.accountName.trim(),
              accountNumber: bank.accountNumber.trim(),
              ifsc: bank.ifsc.trim().toUpperCase(),
              bankName: bank.bankName.trim() || undefined,
            };

      return api.transfers.withdraw({
        amount: amountMinor,
        destination,
        customerNote: note.trim() || undefined,
      });
    },
    onSuccess: (transfer) => {
      void queryClient.invalidateQueries({ queryKey: ['wallet'] });
      void queryClient.invalidateQueries({ queryKey: ['transfers'] });
      toast('success', 'Withdrawal requested', transfer.reference + ' — the amount is reserved until it is paid');
      onDone();
    },
    onError: (err) => setError(err instanceof ApiRequestError ? err.message : 'Request failed'),
  });

  function onSubmit() {
    setError(null);
    if (amountMinor <= 0) return setError('Enter an amount greater than zero');
    if (limits && (amountMinor < limits.min || amountMinor > limits.max)) {
      return setError(
        'Withdrawals by ' +
          (type === 'UPI' ? 'UPI' : 'bank transfer') +
          ' must be between ' +
          formatMoney(limits.min) +
          ' and ' +
          formatMoney(limits.max),
      );
    }
    if (insufficient) return setError('Insufficient available balance');
    if (type === 'UPI' && !upiId.trim()) return setError('Enter the UPI ID to pay');
    if (type === 'BANK' && (!bank.accountName.trim() || !bank.accountNumber.trim() || !bank.ifsc.trim())) {
      return setError('Enter the account name, number and IFSC');
    }
    setReviewing(true);
  }

  if (reviewing) {
    const payTo =
      type === 'UPI'
        ? 'UPI · ' + upiId.trim()
        : [bank.accountName.trim(), bank.accountNumber.trim(), bank.ifsc.trim().toUpperCase(), bank.bankName.trim()]
            .filter(Boolean)
            .join(' · ');
    return (
      <ReviewStep
        kind="WITHDRAWAL"
        rows={[
          { label: 'Amount', value: formatMoney(amountMinor), strong: true },
          { label: 'Pay to', value: payTo },
          ...(note.trim() ? [{ label: 'Note', value: note.trim() }] : []),
          {
            label: 'Available after',
            value: wallet ? formatMoney(wallet.availableBalance - amountMinor, wallet.currency) : '--',
          },
        ]}
        error={error}
        pending={mutation.isPending}
        onConfirm={() => {
          setError(null);
          mutation.mutate();
        }}
        onEdit={() => {
          setError(null);
          setReviewing(false);
        }}
        footnote="The amount is reserved as soon as you confirm, and paid manually to exactly these details once approved."
      />
    );
  }

  return (
    <View className="gap-4">
      <KindBanner kind="WITHDRAWAL" />

      <View>
        <AmountField amountMinor={amountMinor} onChange={setAmountMinor} />
        <LimitsHint limits={limits} />
      </View>

      <View>
        <Label>Pay me by</Label>
        <View accessibilityRole="radiogroup" accessibilityLabel="Pay me by" className="flex-row gap-1">
          {(['UPI', 'BANK'] as const).map((option) => (
            <Pressable
              key={option}
              onPress={() => setType(option)}
              accessibilityRole="radio"
              accessibilityState={{ checked: type === option }}
              className={cn(
                'rounded-lg border px-3 py-1.5',
                type === option ? 'border-accent bg-accent/15' : 'border-surface-700 bg-surface-850',
              )}
            >
              <Text
                className={cn('text-sm font-medium', type === option ? 'text-content-100' : 'text-content-300')}
              >
                {option === 'UPI' ? 'UPI' : 'Bank transfer'}
              </Text>
            </Pressable>
          ))}
        </View>
      </View>

      {type === 'UPI' ? (
        <Field
          label="UPI ID"
          accessibilityLabel="UPI ID"
          placeholder="yourname@bank"
          autoCapitalize="none"
          autoCorrect={false}
          spellCheck={false}
          keyboardType="email-address"
          style={monoStyle}
          value={upiId}
          onChangeText={setUpiId}
        />
      ) : (
        <View className="gap-3">
          <Field
            label="Account holder name"
            accessibilityLabel="Account holder name"
            autoCapitalize="words"
            value={bank.accountName}
            onChangeText={(value) => setBank((b) => ({ ...b, accountName: value }))}
          />
          <Field
            label="Account number"
            accessibilityLabel="Account number"
            keyboardType="number-pad"
            style={monoStyle}
            value={bank.accountNumber}
            onChangeText={(value) => setBank((b) => ({ ...b, accountNumber: value }))}
          />
          <Field
            label="IFSC"
            accessibilityLabel="IFSC"
            autoCapitalize="characters"
            autoCorrect={false}
            maxLength={11}
            style={monoStyle}
            value={bank.ifsc}
            onChangeText={(value) => setBank((b) => ({ ...b, ifsc: value }))}
          />
          <Field
            label="Bank name (optional)"
            accessibilityLabel="Bank name (optional)"
            value={bank.bankName}
            onChangeText={(value) => setBank((b) => ({ ...b, bankName: value }))}
          />
        </View>
      )}

      <Field label="Note (optional)" accessibilityLabel="Note (optional)" value={note} onChangeText={setNote} />

      {wallet ? (
        <Text className="text-xs text-content-400">
          Available balance:{' '}
          <Text className="text-xs font-semibold text-content-200 tnum">
            {formatMoney(wallet.availableBalance, wallet.currency)}
          </Text>
        </Text>
      ) : null}

      <FormError message={error} />

      <Button onPress={onSubmit} disabled={insufficient}>
        Review withdrawal
      </Button>

      <Text className="text-center text-[11px] leading-4 text-content-400">
        Double-check these details. Payments are made manually to exactly what you enter here.
      </Text>
    </View>
  );
}
