import { useEffect, useState, type ReactNode } from 'react';
import { Platform, Pressable, View, type TextStyle } from 'react-native';
import { router } from 'expo-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  AlertTriangle,
  ArrowDownToLine,
  ArrowUpFromLine,
  BadgeCheck,
  ChevronDown,
  ChevronRight,
  FileText,
  Globe,
  Headset,
  Lock,
  LogOut,
  ShieldOff,
  SlidersHorizontal,
  User as UserIcon,
} from 'lucide-react-native';
import { api } from '../../services/api';
import { ApiRequestError } from '../../services/http';
import { useAuthStore } from '../../stores/authStore';
import { useAccountStore } from '../../stores/accountStore';
import { useSettingsStore } from '../../stores/settingsStore';
import { useThemeStore } from '../../stores/themeStore';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { toast } from '../../hooks/useToast';
import { formatDateTime, formatMoney } from '../../utils/format';
import { TIME_ZONES, zoneLabel } from '../../utils/time';
import { COUNTRIES } from '../../utils/countries';
import { cn } from '../../utils/cn';
import { Badge, Button, EmptyState, Field, Icon, Panel, Select, Skeleton, Text } from '../../components/ui';
import { ThemeToggle } from '../../components/ThemeToggle';
import { AccountMarker } from '../../components/AccountSwitch';
import { OneTapSwitch, TradeModeControl } from '../../components/TradingPreferences';
import { UnreadDot, useSupportUnread } from '../../components/BottomNav';
import { UsdtWalletLink } from '../usdt/parts';
import { openFunds } from './funds';
import type { AccountType } from '../../types/api';

/**
 * The nine sections of the Account tab, in the brief's order. The screen that
 * stacks them, and jumps between them, is `AccountScreen`.
 */

/** IDs, user ids and the like: the platform's own monospace face. */
const mono: TextStyle = { fontFamily: Platform.select({ ios: 'Menlo', default: 'monospace' }) };

export function Section({ id, title, action, children }: { id: string; title: string; action?: ReactNode; children: ReactNode }) {
  return (
    <View nativeID={id}>
      <View className="mb-3 flex-row items-center justify-between gap-3">
        <Text accessibilityRole="header" className="text-base font-bold text-content-100">
          {title}
        </Text>
        {action}
      </View>
      {children}
    </View>
  );
}

/** A small accent link with a chevron, for the section headers and the Funds panel. */
function InlineLink({ label, onPress, className }: { label: string; onPress: () => void; className?: string }) {
  return (
    <Pressable
      onPress={onPress}
      accessibilityRole="link"
      hitSlop={6}
      className={cn('flex-row items-center gap-0.5 self-start active:opacity-70', className)}
    >
      <Text className="text-xs font-semibold text-accent">{label}</Text>
      <Icon as={ChevronRight} size={14} className="text-accent" />
    </Pressable>
  );
}

/* ------------------------------------------------------------- Profile */

/** YYYY-MM-DD as it is typed: digits only, the dashes put in for the customer. */
function maskDate(text: string): string {
  const digits = text.replace(/\D/g, '').slice(0, 8);
  return digits.slice(0, 4) + (digits.length > 4 ? '-' + digits.slice(4, 6) : '') + (digits.length > 6 ? '-' + digits.slice(6, 8) : '');
}

/**
 * Whether a typed date of birth can be sent.
 *
 * The web uses the browser's date input, which only ever produces a real
 * YYYY-MM-DD; a phone has no built-in picker here, so the typed text is
 * checked the same way before it goes to the server. Empty clears the date.
 */
function dateOfBirthError(value: string): string | null {
  if (value === '') return null;
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return 'Use the format YYYY-MM-DD';
  const [year, month, day] = [Number(match[1]), Number(match[2]), Number(match[3])];
  const date = new Date(Date.UTC(year, month - 1, day));
  if (date.getUTCFullYear() !== year || date.getUTCMonth() !== month - 1 || date.getUTCDate() !== day) {
    return 'That date does not exist';
  }
  if (date.getTime() > Date.now()) return 'The date of birth cannot be in the future';
  return null;
}

const COUNTRY_OPTIONS = [
  { value: '', label: 'Not set' },
  ...COUNTRIES.map((country) => ({ value: country.code, label: country.name })),
];

export function PersonalData() {
  const queryClient = useQueryClient();
  const setUser = useAuthStore((s) => s.setUser);
  const logout = useAuthStore((s) => s.logout);

  const profileQuery = useQuery({ queryKey: ['profile'], queryFn: api.profile.get });
  const [form, setForm] = useState({
    username: '',
    fullName: '',
    dateOfBirth: '',
    phone: '',
    country: '',
    address: '',
  });
  const [dobError, setDobError] = useState<string | null>(null);

  useEffect(() => {
    const user = profileQuery.data;
    if (!user) return;
    setForm({
      username: user.username,
      fullName: user.fullName ?? '',
      dateOfBirth: user.dateOfBirth ?? '',
      phone: user.phone ?? '',
      country: user.country ?? '',
      address: user.address ?? '',
    });
  }, [profileQuery.data]);

  const mutation = useMutation({
    mutationFn: () =>
      api.profile.update({
        username: form.username.trim() || undefined,
        fullName: form.fullName.trim() || undefined,
        // An empty string clears the stored date; undefined would leave it.
        dateOfBirth: form.dateOfBirth,
        phone: form.phone.trim() || undefined,
        country: form.country || undefined,
        address: form.address.trim(),
      }),
    onSuccess: (user) => {
      setUser(user);
      void queryClient.invalidateQueries({ queryKey: ['profile'] });
      toast('success', 'Profile saved');
    },
    onError: (err) => toast('error', 'Could not save', err instanceof ApiRequestError ? err.message : undefined),
  });

  function onSubmit() {
    const error = dateOfBirthError(form.dateOfBirth);
    setDobError(error);
    if (error) return;
    mutation.mutate();
  }

  const user = profileQuery.data;

  return (
    <Section
      id="profile"
      title="Profile"
      action={
        // Sign-out lives here on a phone, where the top bar has no room for it.
        <Pressable
          onPress={() => void logout()}
          accessibilityRole="button"
          className="flex-row items-center gap-1.5 rounded-lg px-2 py-1 active:opacity-70"
        >
          <Icon as={LogOut} size={14} className="text-content-400" />
          <Text className="text-xs font-semibold text-content-400">Sign out</Text>
        </Pressable>
      }
    >
      <Panel className="p-5">
        <View className="mb-5 flex-row items-center gap-4">
          <View className="h-14 w-14 items-center justify-center rounded-full bg-surface-800">
            <Icon as={UserIcon} size={24} className="text-content-400" />
          </View>
          <View className="min-w-0 flex-1">
            {user ? (
              <>
                <Text numberOfLines={1} className="text-sm font-semibold text-content-100">
                  {user.email}
                </Text>
                <Text numberOfLines={1} selectable className="mt-0.5 text-[11px] text-content-400" style={mono}>
                  ID: {user.id}
                </Text>
                <Text className="mt-0.5 text-[11px] text-content-400">Member since {formatDateTime(user.createdAt)}</Text>
              </>
            ) : (
              <Skeleton className="h-14 w-48" />
            )}
          </View>
        </View>

        <View className="gap-3">
          <Field
            label="Nickname"
            hint="Letters, numbers and . _ - # — must be unique."
            autoCapitalize="none"
            autoCorrect={false}
            value={form.username}
            onChangeText={(text) => setForm((f) => ({ ...f, username: text }))}
          />

          <Field
            label="Full name"
            placeholder="Empty"
            autoComplete="name"
            value={form.fullName}
            onChangeText={(text) => setForm((f) => ({ ...f, fullName: text }))}
          />

          <Field
            label="Date of birth"
            placeholder="YYYY-MM-DD"
            keyboardType="number-pad"
            maxLength={10}
            inputClassName="tnum"
            error={dobError}
            value={form.dateOfBirth}
            onChangeText={(text) => {
              setDobError(null);
              setForm((f) => ({ ...f, dateOfBirth: maskDate(text) }));
            }}
          />

          <Field
            label="Phone"
            keyboardType="phone-pad"
            autoComplete="tel"
            placeholder="Empty"
            value={form.phone}
            onChangeText={(text) => setForm((f) => ({ ...f, phone: text }))}
          />

          <Field
            label="Email"
            hint="Changing the sign-in address is not self-service — contact support."
            value={user?.email ?? ''}
            editable={false}
          />

          <Select
            label="Country"
            title="Country"
            placeholder="Not set"
            value={form.country}
            options={COUNTRY_OPTIONS}
            onChange={(country) => setForm((f) => ({ ...f, country }))}
          />

          <Field
            label="Address"
            placeholder="Empty"
            autoComplete="street-address"
            value={form.address}
            onChangeText={(text) => setForm((f) => ({ ...f, address: text }))}
          />

          <Button className="w-full" loading={mutation.isPending} disabled={!user} onPress={onSubmit}>
            Save
          </Button>
        </View>
      </Panel>
    </Section>
  );
}

/* -------------------------------------------------------- Verification */

/**
 * Identity verification.
 *
 * The platform runs no KYC. Saying so is the whole content of this panel -
 * an upload box that led nowhere would imply a check that never happens.
 */
export function Verification() {
  const user = useAuthStore((s) => s.user);
  return (
    <Section id="verification" title="Verification">
      <Panel className="gap-3 p-5">
        <View className="flex-row items-center justify-between gap-3">
          <Text className="text-sm text-content-300">Email address</Text>
          {user?.emailVerified ? (
            <Badge tone="up">
              <Icon as={BadgeCheck} size={14} className="text-up" />
              <Text className="text-[11px] font-semibold text-up">Verified</Text>
            </Badge>
          ) : (
            <Badge tone="warn">
              <Icon as={AlertTriangle} size={14} className="text-warn" />
              <Text className="text-[11px] font-semibold text-warn">Not verified</Text>
            </Badge>
          )}
        </View>
        <View className="flex-row gap-3 rounded-xl border border-warn/30 bg-warn/10 p-4">
          <Icon as={AlertTriangle} size={20} className="text-warn" />
          <View className="min-w-0 flex-1">
            <Text className="text-sm font-semibold text-warn">Identity verification is not available</Text>
            <Text className="mt-1 text-[13px] leading-5 text-content-300">
              This platform performs no KYC, sanctions screening or document checks. Deposits and withdrawals are
              reviewed by an operator against the bank record instead. A regulated deployment would need real
              verification before this page could offer it.
            </Text>
          </View>
        </View>
      </Panel>
    </Section>
  );
}

/* --------------------------------------------------------------- Funds */

/** Both balances, and the only doors money moves through. */
export function Funds() {
  const wallets = useAccountStore((s) => s.wallets);
  const active = useAccountStore((s) => s.active);
  const transfersQuery = useQuery({ queryKey: ['transfers'], queryFn: () => api.transfers.list({ limit: 10 }) });
  const transfers = (transfersQuery.data?.items ?? []).slice(0, 3);

  return (
    <Section id="funds" title="Funds">
      <View className="gap-3">
        {(['LIVE', 'DEMO'] as AccountType[]).map((type) => {
          const wallet = wallets[type];
          return (
            <Panel key={type} className={cn('p-4', type === 'DEMO' && 'border-warn/30')}>
              <View className="flex-row items-center justify-between gap-2">
                <AccountMarker account={type} className="self-center" />
                {active === type ? <Text className="text-[11px] font-semibold text-content-400">In use</Text> : null}
              </View>
              <Text className="mt-2 text-2xl font-bold tnum text-content-100">
                {wallet ? formatMoney(wallet.balance, wallet.currency) : '--'}
              </Text>
              <View className="mt-1 flex-row flex-wrap gap-x-4">
                <Text className="text-xs text-content-400">
                  Available{' '}
                  <Text className="text-xs font-semibold tnum text-content-200">
                    {wallet ? formatMoney(wallet.availableBalance, wallet.currency) : '--'}
                  </Text>
                </Text>
                <Text className="text-xs text-content-400">
                  In trades{' '}
                  <Text className="text-xs font-semibold tnum text-content-200">
                    {wallet ? formatMoney(wallet.lockedBalance, wallet.currency) : '--'}
                  </Text>
                </Text>
              </View>
              {type === 'DEMO' ? (
                <Text className="mt-2 text-[11px] text-content-400">Virtual money for practice. It cannot be withdrawn.</Text>
              ) : null}
            </Panel>
          );
        })}
      </View>

      {/* A third place money is kept, apart from the two INR accounts. */}
      <UsdtWalletLink className="mt-3" />

      <Panel className="mt-3 p-4">
        <View className="flex-row gap-2">
          <Button className="flex-1" icon={ArrowDownToLine} onPress={() => openFunds('DEPOSIT')}>
            Deposit
          </Button>
          <Button className="flex-1" variant="ghost" icon={ArrowUpFromLine} onPress={() => openFunds('WITHDRAWAL')}>
            Withdraw
          </Button>
        </View>
        <Text className="mt-2 text-[11px] leading-4 text-content-400">
          Deposits and withdrawals use your Live account only. Each request is checked by our team before money is
          credited or paid out.
        </Text>

        {transfers.length > 0 ? (
          <View className="mt-3 border-t border-surface-800">
            {transfers.map((transfer, index) => (
              <View
                key={transfer.id}
                className={cn('flex-row items-center gap-3 py-2.5', index > 0 && 'border-t border-surface-800')}
              >
                <Badge tone={transfer.kind === 'DEPOSIT' ? 'up' : 'neutral'}>
                  {transfer.kind === 'DEPOSIT' ? 'Deposit' : 'Withdrawal'}
                </Badge>
                <View className="min-w-0 flex-1">
                  <Text className="text-sm font-semibold tnum text-content-100">
                    {formatMoney(transfer.amount, transfer.currency)}
                  </Text>
                  <Text className="text-[11px] text-content-400">{formatDateTime(transfer.createdAt)}</Text>
                </View>
                <Badge
                  tone={
                    transfer.status === 'COMPLETED' || transfer.status === 'APPROVED'
                      ? 'up'
                      : transfer.status === 'REJECTED' || transfer.status === 'FAILED'
                        ? 'down'
                        : 'warn'
                  }
                >
                  {transfer.status === 'PENDING'
                    ? 'Under review'
                    : transfer.status.charAt(0) + transfer.status.slice(1).toLowerCase()}
                </Badge>
              </View>
            ))}
          </View>
        ) : null}

        <InlineLink
          className="mt-2"
          label="All deposits, withdrawals and transactions"
          onPress={() => router.push('/wallet')}
        />
      </Panel>
    </Section>
  );
}

/* -------------------------------------------------------- Transactions */

const CREDIT_TYPES = new Set(['DEPOSIT', 'TRADE_WIN', 'ADJUSTMENT_CREDIT', 'TRADE_REFUND', 'TRADE_CANCEL']);

/** The latest ledger rows of the account in use; the full ledger is one tap away. */
export function Transactions() {
  const active = useAccountStore((s) => s.active);
  const ledgerQuery = useQuery({
    queryKey: ['wallet', 'transactions', { accountType: active, page: 1, limit: 5 }],
    queryFn: () => api.wallet.transactions({ page: 1, limit: 5, accountType: active }),
  });
  const rows = ledgerQuery.data?.items ?? [];

  return (
    <Section
      id="transactions"
      title="Transactions"
      action={<InlineLink label="View all" onPress={() => router.push('/wallet')} />}
    >
      <Panel className="overflow-hidden">
        <View className="flex-row items-center gap-2 border-b border-surface-800 px-4 py-2.5">
          <Text className="text-[11px] text-content-400">Latest on your</Text>
          <AccountMarker account={active} className="self-center" />
          <Text className="text-[11px] text-content-400">account</Text>
        </View>
        {ledgerQuery.isLoading ? (
          <View className="gap-2 p-3">
            {Array.from({ length: 3 }).map((_, index) => (
              <Skeleton key={index} className="h-10 w-full" />
            ))}
          </View>
        ) : rows.length === 0 ? (
          <EmptyState title="No transactions yet" />
        ) : (
          <View>
            {rows.map((row, index) => {
              const credit = CREDIT_TYPES.has(row.type);
              const neutral = row.movement === 'LOCK' || row.movement === 'UNLOCK';
              return (
                <View
                  key={row._id}
                  className={cn('flex-row items-center gap-3 px-4 py-2.5', index > 0 && 'border-t border-surface-800/70')}
                >
                  <View className="min-w-0 flex-1">
                    <Text className="text-sm font-medium capitalize text-content-100">
                      {row.type.replace(/_/g, ' ').toLowerCase()}
                    </Text>
                    <Text numberOfLines={1} className="text-[11px] text-content-400">
                      {formatDateTime(row.createdAt)}
                    </Text>
                  </View>
                  <Text
                    className={cn(
                      'text-sm font-semibold tnum',
                      neutral ? 'text-content-300' : credit ? 'text-up' : 'text-down',
                    )}
                  >
                    {neutral ? '' : credit ? '+' : '-'}
                    {formatMoney(row.amount)}
                  </Text>
                </View>
              );
            })}
          </View>
        )}
      </Panel>
    </Section>
  );
}

/* ------------------------------------------------------------ Security */

/** Password change, plus an honest account of what protection is not offered. */
export function Security() {
  const [form, setForm] = useState({ current: '', next: '', confirm: '' });
  const [error, setError] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: () => api.auth.changePassword(form.current, form.next),
    onSuccess: () => {
      setForm({ current: '', next: '', confirm: '' });
      toast('success', 'Password changed', 'Other sessions stay signed in until their tokens expire.');
    },
    onError: (err) => setError(err instanceof ApiRequestError ? err.message : 'Could not change the password'),
  });

  function onSubmit() {
    setError(null);
    if (form.next.length < 8) return setError('The new password must be at least 8 characters');
    if (form.next !== form.confirm) return setError('The two new passwords do not match');
    if (form.next === form.current) return setError('The new password must differ from the current one');
    mutation.mutate();
  }

  return (
    <Section id="security" title="Security">
      <View className="gap-3">
        <Panel className="p-5">
          <View className="mb-4 flex-row items-center gap-2">
            <Icon as={Lock} size={16} className="text-content-400" />
            <Text accessibilityRole="header" className="text-sm font-bold text-content-100">
              Password
            </Text>
          </View>

          <View className="gap-3">
            <Field
              label="Current password"
              secureTextEntry
              autoComplete="current-password"
              textContentType="password"
              autoCapitalize="none"
              value={form.current}
              onChangeText={(text) => setForm((f) => ({ ...f, current: text }))}
            />

            <Field
              label="New password"
              hint="At least 8 characters."
              secureTextEntry
              autoComplete="new-password"
              textContentType="newPassword"
              autoCapitalize="none"
              value={form.next}
              onChangeText={(text) => setForm((f) => ({ ...f, next: text }))}
            />

            <Field
              label="Confirm new password"
              secureTextEntry
              autoComplete="new-password"
              textContentType="newPassword"
              autoCapitalize="none"
              value={form.confirm}
              onChangeText={(text) => setForm((f) => ({ ...f, confirm: text }))}
            />

            {error ? (
              <View className="rounded-lg border border-down/30 bg-down/10 px-3 py-2">
                <Text accessibilityRole="alert" className="text-sm text-down">
                  {error}
                </Text>
              </View>
            ) : null}

            <Button className="w-full" loading={mutation.isPending} onPress={onSubmit}>
              Change password
            </Button>
          </View>
        </Panel>

        <Panel className="p-5">
          <View className="mb-4 flex-row items-center gap-2">
            <Icon as={ShieldOff} size={16} className="text-content-400" />
            <Text accessibilityRole="header" className="text-sm font-bold text-content-100">
              Not available on this platform
            </Text>
          </View>

          <View className="gap-3">
            <Text className="text-[13px] leading-5 text-content-300">
              <Text className="text-[13px] font-semibold leading-5 text-content-100">Two-step verification.</Text> No
              second factor is implemented, for sign-in or for withdrawals. A withdrawal is instead held until an
              operator approves it and pays it manually.
            </Text>
            <Text className="text-[13px] leading-5 text-content-300">
              <Text className="text-[13px] font-semibold leading-5 text-content-100">
                Email verification and password reset.
              </Text>{' '}
              The platform sends no email, so an address cannot be confirmed or a forgotten password recovered without
              an operator.
            </Text>
            <Text className="text-[13px] leading-5 text-content-300">
              <Text className="text-[13px] font-semibold leading-5 text-content-100">Account deletion.</Text> Financial
              records must be retained for reconciliation and audit, so an account is closed by support rather than
              erased.
            </Text>
          </View>

          <Text className="mt-4 border-t border-surface-800 pt-3 text-[11px] leading-4 text-content-400">
            These are listed rather than hidden because a control that looks like protection and provides none is worse
            than its absence.
          </Text>
        </Panel>
      </View>
    </Section>
  );
}

/* --------------------------------------------------------- Preferences */

const TIMEZONE_OPTIONS = TIME_ZONES.map((zone) => ({ value: zone as string, label: zoneLabel(zone) }));

/**
 * Display and trading preferences, all stored on this device.
 *
 * One-tap trading and the default mode are the same controls the Settings
 * screen and the settings sheet show, so they read identically everywhere.
 */
export function Preferences() {
  const timezone = useSettingsStore((s) => s.timezone);
  const setTimezone = useSettingsStore((s) => s.setTimezone);
  const themePreference = useThemeStore((s) => s.preference);
  const resolvedTheme = useThemeStore((s) => s.resolved);
  const setSettingsOpen = useWorkspaceStore((s) => s.setSettingsOpen);

  return (
    <Section id="preferences" title="Preferences">
      <Panel className="gap-4 p-5">
        <View className="gap-3">
          <View>
            <Text className="text-sm font-medium text-content-100">Theme</Text>
            <Text className="text-[11px] text-content-400">
              {themePreference === 'system'
                ? 'Following your device, currently ' + resolvedTheme
                : 'Always ' + themePreference}
            </Text>
          </View>
          <ThemeToggle />
        </View>

        <Field
          label="Language"
          hint="English is the only bundled locale."
          value="English"
          editable={false}
          left={<Icon as={Globe} size={16} className="text-content-400" />}
          right={<Icon as={ChevronDown} size={16} className="text-content-400" />}
        />

        <View>
          <Select label="Timezone" title="Timezone" value={timezone} options={TIMEZONE_OPTIONS} onChange={setTimezone} />
          <Text className="mt-1 text-[10px] leading-[14px] text-content-400">
            Changes how times are written. Expiries are always decided on the server.
          </Text>
        </View>

        <OneTapSwitch />
        <TradeModeControl />

        <View className="flex-row flex-wrap gap-2 border-t border-surface-800 pt-3">
          <Button variant="ghost" size="sm" icon={SlidersHorizontal} onPress={() => setSettingsOpen(true)}>
            Chart settings
          </Button>
          <Button variant="ghost" size="sm" onPress={() => router.push('/settings')}>
            All settings
          </Button>
        </View>
      </Panel>
    </Section>
  );
}

/* ------------------------------------------------------- Notifications */

export function Notifications() {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ['notifications'], queryFn: () => api.notifications.list({ limit: 10 }) });
  const items = query.data?.items ?? [];
  const unread = items.filter((n) => !n.read).length;

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ['notifications'] });
  const markOne = useMutation({ mutationFn: api.notifications.markRead, onSuccess: refresh });
  const markAll = useMutation({
    mutationFn: api.notifications.markAllRead,
    onSuccess: refresh,
    onError: () => toast('error', 'Could not mark notifications as read'),
  });

  return (
    <Section
      id="notifications"
      title="Notifications"
      action={
        unread > 0 ? (
          <Pressable
            onPress={() => markAll.mutate()}
            disabled={markAll.isPending}
            accessibilityRole="button"
            hitSlop={6}
            className={cn('active:opacity-70', markAll.isPending && 'opacity-50')}
          >
            <Text className="text-xs font-semibold text-accent">Mark all as read</Text>
          </Pressable>
        ) : null
      }
    >
      <Panel className="overflow-hidden">
        {query.isLoading ? (
          <View className="gap-2 p-3">
            {Array.from({ length: 3 }).map((_, index) => (
              <Skeleton key={index} className="h-12 w-full" />
            ))}
          </View>
        ) : items.length === 0 ? (
          <EmptyState title="Nothing new" description="Deposit, withdrawal and account updates appear here." />
        ) : (
          <View>
            {items.map((item, index) => (
              <View
                key={item.id}
                className={cn(
                  'flex-row gap-3 px-4 py-3',
                  !item.read && 'bg-accent/5',
                  index > 0 && 'border-t border-surface-800/70',
                )}
              >
                <View
                  accessibilityElementsHidden
                  importantForAccessibility="no"
                  className={cn('mt-1.5 h-2 w-2 rounded-full', item.read ? 'bg-transparent' : 'bg-accent')}
                />
                <View className="min-w-0 flex-1">
                  <Text className="text-sm font-medium text-content-100">{item.title}</Text>
                  <Text className="mt-0.5 text-xs text-content-300">{item.body}</Text>
                  <Text className="mt-1 text-[10px] text-content-400">{formatDateTime(item.createdAt)}</Text>
                </View>
                {!item.read ? (
                  <Pressable
                    onPress={() => markOne.mutate(item.id)}
                    accessibilityRole="button"
                    hitSlop={4}
                    className="self-start rounded-md px-2 py-1 active:bg-surface-850"
                  >
                    <Text className="text-[11px] font-semibold text-content-400">Mark read</Text>
                  </Pressable>
                ) : null}
              </View>
            ))}
          </View>
        )}
      </Panel>
    </Section>
  );
}

/* ------------------------------------------------------------- Support */

export function Support() {
  const unread = useSupportUnread();
  return (
    <Section id="support" title="Support">
      <Pressable
        onPress={() => router.push('/support')}
        accessibilityRole="link"
        className="flex-row items-center gap-3 rounded-xl border border-surface-700 bg-surface-900 p-4 active:border-surface-600 active:bg-surface-850"
      >
        <View className="relative h-10 w-10 items-center justify-center rounded-xl bg-surface-800">
          <Icon as={Headset} size={20} className="text-content-200" />
          <UnreadDot count={unread} className="-right-1 -top-1" />
        </View>
        <View className="min-w-0 flex-1">
          <Text className="text-sm font-semibold text-content-100">Live chat with support</Text>
          <Text className="text-xs text-content-400">
            {unread > 0
              ? unread + (unread === 1 ? ' new reply' : ' new replies') + ' waiting for you'
              : 'Questions about a deposit, a withdrawal, a trade or your account.'}
          </Text>
        </View>
        <Icon as={ChevronRight} size={16} className="text-content-400" />
      </Pressable>
      <Text className="mt-2 text-xs text-content-400">
        Looking for how something works?{' '}
        <Text accessibilityRole="link" onPress={() => router.push('/learn')} className="text-xs font-semibold text-accent">
          Read the guides
        </Text>
      </Text>
    </Section>
  );
}

/* -------------------------------------------------------- Legal & risk */

const LEGAL_LINKS = [
  { key: 'terms', label: 'Terms of service', go: () => router.push('/terms') },
  { key: 'privacy', label: 'Privacy policy', go: () => router.push('/privacy') },
  // The web links to /learn#risk; on a phone the Learn screen takes the
  // section as a parameter, like Account does.
  { key: 'risk', label: 'Understanding the risk', go: () => router.push({ pathname: '/learn', params: { section: 'risk' } }) },
];

export function LegalRisk() {
  return (
    <Section id="legal" title="Legal & risk">
      <Panel className="p-5">
        <View className="flex-row gap-3 rounded-xl border border-warn/30 bg-warn/10 p-4">
          <Icon as={AlertTriangle} size={20} className="text-warn" />
          <View className="min-w-0 flex-1">
            <Text className="text-[13px] font-semibold text-warn">Risk warning</Text>
            <Text className="mt-1 text-[13px] leading-5 text-content-300">
              Fixed-time trading is high risk. You can lose the whole amount you invest in any trade, and past results do
              not indicate future ones. Only trade with money you can afford to lose.
            </Text>
          </View>
        </View>
        <View className="mt-3">
          {LEGAL_LINKS.map((link, index) => (
            <Pressable
              key={link.key}
              onPress={link.go}
              accessibilityRole="link"
              className={cn(
                'flex-row items-center gap-3 py-2.5 active:opacity-70',
                index > 0 && 'border-t border-surface-800',
              )}
            >
              <Icon as={FileText} size={16} className="text-content-400" />
              <Text className="flex-1 text-sm text-content-200">{link.label}</Text>
              <Icon as={ChevronRight} size={16} className="text-content-400" />
            </Pressable>
          ))}
        </View>
      </Panel>
    </Section>
  );
}
