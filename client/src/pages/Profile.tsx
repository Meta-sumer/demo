import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import {
  AlertTriangle,
  ArrowDownToLine,
  ArrowUpFromLine,
  BadgeCheck,
  ChevronRight,
  FileText,
  Globe,
  Headset,
  Lock,
  LogOut,
  ShieldOff,
  SlidersHorizontal,
  User as UserIcon,
} from 'lucide-react';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { useAccountStore } from '../stores/accountStore';
import { useSettingsStore } from '../stores/settingsStore';
import { useThemeStore } from '../stores/themeStore';
import { useWorkspaceStore } from '../stores/workspaceStore';
import { toast } from '../hooks/useToast';
import { formatDateTime, formatMoney } from '../utils/format';
import { TIME_ZONES, zoneLabel } from '../utils/time';
import { Badge, EmptyState, Panel, Skeleton, Spinner } from '../components/ui';
import { ThemeToggle } from '../components/ThemeToggle';
import { AccountMarker } from '../components/AccountSwitch';
import { OneTapSwitch, TradeModeControl } from '../components/TradingPreferences';
import { UnreadDot, useSupportUnread } from '../components/BottomNav';
import { openFunds } from '../features/account/funds';
import { UsdtWalletLink } from '../features/usdt/parts';
import { COUNTRIES } from '../utils/countries';
import type { AccountType } from '../types/api';

/**
 * Account (brief UX-19).
 *
 * One page, in the brief's order: Profile, Verification, Funds, Transactions,
 * Security, Preferences, Notifications, Support, Legal & risk. A strip of
 * links at the top jumps to each, so a phone does not have to scroll past the
 * profile form to reach the support chat. On a wide screen the same links
 * stand in a column on the left that stays in view and marks the section
 * being read, and the sections take the rest of the width. Trading controls (stake, expiry,
 * chart) are not here - they belong to the trade screen and its settings.
 *
 * Everything on this page either saves to the server or says plainly that it
 * cannot. The platform has no KYC, no email delivery and no second factor - so
 * rather than showing controls for them, this page states their absence. A
 * toggle labelled "two-step verification" that changes nothing would leave a
 * customer believing their account is protected when it is not, which is worse
 * than not offering it at all.
 */
const SECTIONS = [
  { id: 'profile', label: 'Profile' },
  { id: 'verification', label: 'Verification' },
  { id: 'funds', label: 'Funds' },
  { id: 'transactions', label: 'Transactions' },
  { id: 'security', label: 'Security' },
  { id: 'preferences', label: 'Preferences' },
  { id: 'notifications', label: 'Notifications' },
  { id: 'support', label: 'Support' },
  { id: 'legal', label: 'Legal & risk' },
] as const;

const SECTION_IDS: readonly string[] = SECTIONS.map((section) => section.id);

/**
 * The section being read: the first one with any part in the top 40% of the
 * screen, or the last one once the page is scrolled to its end (the short
 * sections at the bottom can never reach the top). A section chosen from the
 * links holds while the page scrolls to it. Without IntersectionObserver it
 * stays on the first.
 */
function useActiveSection(ids: readonly string[]): [string, (id: string) => void] {
  const [active, setActive] = useState(ids[0] ?? '');
  const heldUntil = useRef(0);

  useEffect(() => {
    if (typeof IntersectionObserver === 'undefined') return;
    const visible = new Set<string>();
    const scroller = scrollParent(document.getElementById(ids[0] ?? ''));

    const pick = () => {
      if (Date.now() < heldUntil.current) return;
      if (scroller && scroller.scrollTop > 0 && scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 2) {
        setActive(ids[ids.length - 1] ?? '');
        return;
      }
      const first = ids.find((id) => visible.has(id));
      if (first) setActive(first);
    };

    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) visible.add(entry.target.id);
          else visible.delete(entry.target.id);
        }
        pick();
      },
      { rootMargin: '0px 0px -60% 0px' },
    );
    for (const id of ids) {
      const element = document.getElementById(id);
      if (element) observer.observe(element);
    }
    scroller?.addEventListener('scroll', pick, { passive: true });
    return () => {
      observer.disconnect();
      scroller?.removeEventListener('scroll', pick);
    };
  }, [ids]);

  const choose = useCallback((id: string) => {
    heldUntil.current = Date.now() + 1000;
    setActive(id);
  }, []);
  return [active, choose];
}

/** The element that scrolls the page: the app's main area, not the window. */
function scrollParent(element: HTMLElement | null): HTMLElement | null {
  for (let node = element?.parentElement ?? null; node; node = node.parentElement) {
    const { overflowY } = getComputedStyle(node);
    if (overflowY === 'auto' || overflowY === 'scroll') return node;
  }
  return null;
}

export function AccountPage() {
  const { hash } = useLocation();
  const supportUnread = useSupportUnread();
  const [activeSection, setActiveSection] = useActiveSection(SECTION_IDS);

  // The bell in the top bar links to #notifications; honour any section hash.
  useEffect(() => {
    if (!hash) return;
    const target = document.getElementById(hash.slice(1));
    // After the first paint, so the sections above have their height.
    const frame = window.requestAnimationFrame(() => target?.scrollIntoView({ block: 'start' }));
    return () => window.cancelAnimationFrame(frame);
  }, [hash]);

  return (
    <div className="mx-auto max-w-4xl space-y-6 lg:grid lg:max-w-6xl lg:grid-cols-[12rem_minmax(0,1fr)] lg:gap-x-8 lg:space-y-0 xl:grid-cols-[14rem_minmax(0,1fr)]">
      <header className="lg:col-span-2 lg:mb-6">
        <h1 className="text-lg font-bold text-content-100">Account</h1>
      </header>

      <nav
        aria-label="Account sections"
        className="-mx-1 flex gap-1.5 overflow-x-auto px-1 pb-1 lg:sticky lg:top-0 lg:mx-0 lg:flex-col lg:gap-0.5 lg:self-start lg:overflow-visible lg:p-0"
      >
        {SECTIONS.map((section) => (
          <a
            key={section.id}
            href={'#' + section.id}
            aria-current={activeSection === section.id ? 'location' : undefined}
            onClick={(event) => {
              event.preventDefault();
              setActiveSection(section.id);
              document.getElementById(section.id)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
            }}
            className={clsx(
              'relative shrink-0 rounded-full border border-surface-700 bg-surface-850 px-3 py-1 text-xs font-semibold text-content-300 hover:text-content-100',
              'lg:rounded-lg lg:border-transparent lg:px-3 lg:py-2 lg:text-sm',
              activeSection === section.id
                ? 'lg:bg-surface-850 lg:text-content-100'
                : 'lg:bg-transparent lg:text-content-400 lg:hover:bg-surface-850/60',
            )}
          >
            {section.label}
            {section.id === 'support' ? <UnreadDot count={supportUnread} className="-right-1.5 -top-1.5" /> : null}
          </a>
        ))}
      </nav>

      <div className="min-w-0 space-y-6">
        <PersonalData />
        <Verification />
        <Funds />
        <Transactions />
        <Security />
        <Preferences />
        <Notifications />
        <Support />
        <LegalRisk />
      </div>
    </div>
  );
}

function Section({
  id,
  title,
  action,
  children,
}: {
  id: string;
  title: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section id={id} aria-labelledby={id + '-title'} className="scroll-mt-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <h2 id={id + '-title'} className="text-base font-bold text-content-100">
          {title}
        </h2>
        {action}
      </div>
      {children}
    </section>
  );
}

function PersonalData() {
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

  const user = profileQuery.data;

  return (
    <Section
      id="profile"
      title="Profile"
      action={
        // Sign-out lives here on a phone, where the top bar has no room for it.
        <button
          type="button"
          onClick={() => void logout()}
          className="inline-flex items-center gap-1.5 rounded-lg px-2 py-1 text-xs font-semibold text-content-400 hover:text-down"
        >
          <LogOut className="h-3.5 w-3.5" />
          Sign out
        </button>
      }
    >
      <Panel className="p-5">
        <div className="mb-5 flex items-center gap-4">
          <span className="grid h-14 w-14 shrink-0 place-items-center rounded-full bg-surface-800 text-content-400">
            <UserIcon className="h-6 w-6" />
          </span>
          <div className="min-w-0">
            {user ? (
              <>
                <div className="truncate text-sm font-semibold text-content-100">{user.email}</div>
                <div className="mt-0.5 truncate font-mono text-[11px] text-content-400">ID: {user.id}</div>
                <div className="mt-0.5 text-[11px] text-content-400">Member since {formatDateTime(user.createdAt)}</div>
              </>
            ) : (
              <Skeleton className="h-14 w-48" />
            )}
          </div>
        </div>

        <form
          className="grid gap-3 sm:grid-cols-2"
          onSubmit={(event: FormEvent) => {
            event.preventDefault();
            mutation.mutate();
          }}
        >
          <Field label="Nickname" htmlFor="username" hint="Letters, numbers and . _ - # — must be unique.">
            <input
              id="username"
              className="field"
              value={form.username}
              onChange={(event) => setForm((f) => ({ ...f, username: event.target.value }))}
            />
          </Field>

          <Field label="Full name" htmlFor="fullName">
            <input
              id="fullName"
              className="field"
              placeholder="Empty"
              value={form.fullName}
              onChange={(event) => setForm((f) => ({ ...f, fullName: event.target.value }))}
            />
          </Field>

          <Field label="Date of birth" htmlFor="dateOfBirth">
            <input
              id="dateOfBirth"
              type="date"
              className="field"
              value={form.dateOfBirth}
              onChange={(event) => setForm((f) => ({ ...f, dateOfBirth: event.target.value }))}
            />
          </Field>

          <Field label="Phone" htmlFor="phone">
            <input
              id="phone"
              className="field"
              inputMode="tel"
              placeholder="Empty"
              value={form.phone}
              onChange={(event) => setForm((f) => ({ ...f, phone: event.target.value }))}
            />
          </Field>

          <Field
            label="Email"
            htmlFor="email"
            hint="Changing the sign-in address is not self-service — contact support."
          >
            <input id="email" className="field opacity-60" value={user?.email ?? ''} readOnly disabled />
          </Field>

          <Field label="Country" htmlFor="country">
            <select
              id="country"
              className="field"
              value={form.country}
              onChange={(event) => setForm((f) => ({ ...f, country: event.target.value }))}
            >
              <option value="">Not set</option>
              {COUNTRIES.map((country) => (
                <option key={country.code} value={country.code}>
                  {country.name}
                </option>
              ))}
            </select>
          </Field>

          <div className="sm:col-span-2">
            <Field label="Address" htmlFor="address">
              <input
                id="address"
                className="field"
                placeholder="Empty"
                value={form.address}
                onChange={(event) => setForm((f) => ({ ...f, address: event.target.value }))}
              />
            </Field>
          </div>

          <button type="submit" className="btn-primary w-full sm:col-span-2" disabled={mutation.isPending || !user}>
            {mutation.isPending ? <Spinner /> : null}
            Save
          </button>
        </form>
      </Panel>
    </Section>
  );
}

/**
 * Identity verification.
 *
 * The platform runs no KYC. Saying so is the whole content of this panel -
 * an upload box that led nowhere would imply a check that never happens.
 */
function Verification() {
  const user = useAuthStore((s) => s.user);
  return (
    <Section id="verification" title="Verification">
      <Panel className="space-y-3 p-5">
        <div className="flex items-center justify-between gap-3">
          <span className="text-sm text-content-300">Email address</span>
          {user?.emailVerified ? (
            <Badge tone="up">
              <BadgeCheck className="h-3.5 w-3.5" />
              Verified
            </Badge>
          ) : (
            <Badge tone="warn">
              <AlertTriangle className="h-3.5 w-3.5" />
              Not verified
            </Badge>
          )}
        </div>
        <div className="flex gap-3 rounded-xl border border-warn/30 bg-warn/10 p-4">
          <AlertTriangle className="h-5 w-5 shrink-0 text-warn" />
          <div className="min-w-0 text-sm">
            <p className="font-semibold text-warn">Identity verification is not available</p>
            <p className="mt-1 text-[13px] leading-relaxed text-content-300">
              This platform performs no KYC, sanctions screening or document checks. Deposits and withdrawals are
              reviewed by an operator against the bank record instead. A regulated deployment would need real
              verification before this page could offer it.
            </p>
          </div>
        </div>
      </Panel>
    </Section>
  );
}

/** Both balances side by side, and the only doors money moves through. */
function Funds() {
  const wallets = useAccountStore((s) => s.wallets);
  const active = useAccountStore((s) => s.active);
  const transfersQuery = useQuery({ queryKey: ['transfers'], queryFn: () => api.transfers.list({ limit: 10 }) });
  const transfers = (transfersQuery.data?.items ?? []).slice(0, 3);

  return (
    <Section id="funds" title="Funds">
      <div className="grid gap-3 sm:grid-cols-2">
        {(['LIVE', 'DEMO'] as AccountType[]).map((type) => {
          const wallet = wallets[type];
          return (
            <Panel key={type} className={clsx('p-4', type === 'DEMO' && 'border-warn/30')}>
              <div className="flex items-center justify-between gap-2">
                <AccountMarker account={type} />
                {active === type ? <span className="text-[11px] font-semibold text-content-400">In use</span> : null}
              </div>
              <div className="mt-2 text-2xl font-bold tnum text-content-100">
                {wallet ? formatMoney(wallet.balance, wallet.currency) : '--'}
              </div>
              <div className="mt-1 flex flex-wrap gap-x-4 text-xs text-content-400">
                <span>
                  Available{' '}
                  <b className="font-semibold tnum text-content-200">
                    {wallet ? formatMoney(wallet.availableBalance, wallet.currency) : '--'}
                  </b>
                </span>
                <span>
                  In trades{' '}
                  <b className="font-semibold tnum text-content-200">
                    {wallet ? formatMoney(wallet.lockedBalance, wallet.currency) : '--'}
                  </b>
                </span>
              </div>
              {type === 'DEMO' ? (
                <p className="mt-2 text-[11px] text-content-400">Virtual money for practice. It cannot be withdrawn.</p>
              ) : null}
            </Panel>
          );
        })}
      </div>

      {/* A third place money is kept, apart from the two INR accounts. */}
      <UsdtWalletLink className="mt-3" />

      <Panel className="mt-3 p-4">
        <div className="grid grid-cols-2 gap-2">
          <button type="button" onClick={() => openFunds('DEPOSIT')} className="btn-primary">
            <ArrowDownToLine className="h-4 w-4" />
            Deposit
          </button>
          <button type="button" onClick={() => openFunds('WITHDRAWAL')} className="btn-ghost">
            <ArrowUpFromLine className="h-4 w-4" />
            Withdraw
          </button>
        </div>
        <p className="mt-2 text-[11px] text-content-400">
          Deposits and withdrawals use your Live account only. Each request is checked by our team before money is
          credited or paid out.
        </p>

        {transfers.length > 0 ? (
          <ul className="mt-3 divide-y divide-surface-800 border-t border-surface-800">
            {transfers.map((transfer) => (
              <li key={transfer.id} className="flex items-center gap-3 py-2.5">
                <Badge tone={transfer.kind === 'DEPOSIT' ? 'up' : 'neutral'}>
                  {transfer.kind === 'DEPOSIT' ? 'Deposit' : 'Withdrawal'}
                </Badge>
                <span className="min-w-0 flex-1">
                  <span className="block text-sm font-semibold tnum text-content-100">
                    {formatMoney(transfer.amount, transfer.currency)}
                  </span>
                  <span className="block text-[11px] text-content-400">{formatDateTime(transfer.createdAt)}</span>
                </span>
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
              </li>
            ))}
          </ul>
        ) : null}

        <Link
          to="/wallet"
          className="mt-2 inline-flex items-center gap-0.5 text-xs font-semibold text-accent hover:underline"
        >
          All deposits, withdrawals and transactions
          <ChevronRight className="h-3.5 w-3.5" />
        </Link>
      </Panel>
    </Section>
  );
}

const CREDIT_TYPES = new Set(['DEPOSIT', 'TRADE_WIN', 'ADJUSTMENT_CREDIT', 'TRADE_REFUND', 'TRADE_CANCEL']);

/** The latest ledger rows of the account in use; the full ledger is one tap away. */
function Transactions() {
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
      action={
        <Link
          to="/wallet"
          className="inline-flex items-center gap-0.5 text-xs font-semibold text-accent hover:underline"
        >
          View all
          <ChevronRight className="h-3.5 w-3.5" />
        </Link>
      }
    >
      <Panel className="overflow-hidden">
        <div className="flex items-center gap-2 border-b border-surface-800 px-4 py-2.5 text-[11px] text-content-400">
          Latest on your <AccountMarker account={active} /> account
        </div>
        {ledgerQuery.isLoading ? (
          <div className="space-y-2 p-3">
            {Array.from({ length: 3 }).map((_, index) => (
              <Skeleton key={index} className="h-10 w-full" />
            ))}
          </div>
        ) : rows.length === 0 ? (
          <EmptyState title="No transactions yet" />
        ) : (
          <ul className="divide-y divide-surface-800/70">
            {rows.map((row) => {
              const credit = CREDIT_TYPES.has(row.type);
              const neutral = row.movement === 'LOCK' || row.movement === 'UNLOCK';
              return (
                <li key={row._id} className="flex items-center gap-3 px-4 py-2.5">
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-medium capitalize text-content-100">
                      {row.type.replace(/_/g, ' ').toLowerCase()}
                    </div>
                    <div className="truncate text-[11px] text-content-400">{formatDateTime(row.createdAt)}</div>
                  </div>
                  <div
                    className={clsx(
                      'text-sm font-semibold tnum',
                      neutral ? 'text-content-300' : credit ? 'text-up' : 'text-down',
                    )}
                  >
                    {neutral ? '' : credit ? '+' : '-'}
                    {formatMoney(row.amount)}
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </Panel>
    </Section>
  );
}

/** Password change, plus an honest account of what protection is not offered. */
function Security() {
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

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError(null);
    if (form.next.length < 8) return setError('The new password must be at least 8 characters');
    if (form.next !== form.confirm) return setError('The two new passwords do not match');
    if (form.next === form.current) return setError('The new password must differ from the current one');
    mutation.mutate();
  }

  return (
    <Section id="security" title="Security">
      <div className="grid gap-3 lg:grid-cols-2">
        <Panel className="p-5">
          <div className="mb-4 flex items-center gap-2">
            <Lock className="h-4 w-4 text-content-400" />
            <h3 className="text-sm font-bold text-content-100">Password</h3>
          </div>

          <form className="space-y-3" onSubmit={onSubmit}>
            <Field label="Current password" htmlFor="currentPassword">
              <input
                id="currentPassword"
                type="password"
                autoComplete="current-password"
                className="field"
                value={form.current}
                onChange={(event) => setForm((f) => ({ ...f, current: event.target.value }))}
              />
            </Field>

            <Field label="New password" htmlFor="newPassword" hint="At least 8 characters.">
              <input
                id="newPassword"
                type="password"
                autoComplete="new-password"
                className="field"
                value={form.next}
                onChange={(event) => setForm((f) => ({ ...f, next: event.target.value }))}
              />
            </Field>

            <Field label="Confirm new password" htmlFor="confirmPassword">
              <input
                id="confirmPassword"
                type="password"
                autoComplete="new-password"
                className="field"
                value={form.confirm}
                onChange={(event) => setForm((f) => ({ ...f, confirm: event.target.value }))}
              />
            </Field>

            {error ? (
              <p className="rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-sm text-down">{error}</p>
            ) : null}

            <button type="submit" className="btn-primary w-full" disabled={mutation.isPending}>
              {mutation.isPending ? <Spinner /> : null}
              Change password
            </button>
          </form>
        </Panel>

        <Panel className="p-5">
          <div className="mb-4 flex items-center gap-2">
            <ShieldOff className="h-4 w-4 text-content-400" />
            <h3 className="text-sm font-bold text-content-100">Not available on this platform</h3>
          </div>

          <ul className="space-y-3 text-[13px] leading-relaxed text-content-300">
            <li>
              <span className="font-semibold text-content-100">Two-step verification.</span> No second factor is
              implemented, for sign-in or for withdrawals. A withdrawal is instead held until an operator approves it
              and pays it manually.
            </li>
            <li>
              <span className="font-semibold text-content-100">Email verification and password reset.</span> The
              platform sends no email, so an address cannot be confirmed or a forgotten password recovered without an
              operator.
            </li>
            <li>
              <span className="font-semibold text-content-100">Account deletion.</span> Financial records must be
              retained for reconciliation and audit, so an account is closed by support rather than erased.
            </li>
          </ul>

          <p className="mt-4 border-t border-surface-800 pt-3 text-[11px] leading-relaxed text-content-400">
            These are listed rather than hidden because a control that looks like protection and provides none is worse
            than its absence.
          </p>
        </Panel>
      </div>
    </Section>
  );
}

/**
 * Display and trading preferences, all stored on this device.
 *
 * One-tap trading and the default mode are the same controls the Settings
 * page and the settings drawer show, so they read identically everywhere.
 */
function Preferences() {
  const timezone = useSettingsStore((s) => s.timezone);
  const setTimezone = useSettingsStore((s) => s.setTimezone);
  const themePreference = useThemeStore((s) => s.preference);
  const resolvedTheme = useThemeStore((s) => s.resolved);
  const setSettingsOpen = useWorkspaceStore((s) => s.setSettingsOpen);

  return (
    <Section id="preferences" title="Preferences">
      <Panel className="space-y-4 p-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <div className="text-sm font-medium text-content-100">Theme</div>
            <div className="text-[11px] text-content-400">
              {themePreference === 'system'
                ? 'Following your device, currently ' + resolvedTheme
                : 'Always ' + themePreference}
            </div>
          </div>
          <ThemeToggle size="md" />
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Language" htmlFor="accountLanguage" hint="English is the only bundled locale.">
            <div className="relative">
              <Globe className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-content-400" />
              <select id="accountLanguage" className="field pl-9" defaultValue="en" disabled>
                <option value="en">English</option>
              </select>
            </div>
          </Field>

          <Field
            label="Timezone"
            htmlFor="accountTimezone"
            hint="Changes how times are written. Expiries are always decided on the server."
          >
            <select
              id="accountTimezone"
              className="field"
              value={timezone}
              onChange={(event) => setTimezone(event.target.value)}
            >
              {TIME_ZONES.map((zone) => (
                <option key={zone} value={zone}>
                  {zoneLabel(zone)}
                </option>
              ))}
            </select>
          </Field>
        </div>

        <OneTapSwitch />
        <TradeModeControl />

        <div className="flex flex-wrap gap-2 border-t border-surface-800 pt-3">
          <button type="button" onClick={() => setSettingsOpen(true)} className="btn-ghost px-3 py-1.5 text-xs">
            <SlidersHorizontal className="h-3.5 w-3.5" />
            Chart settings
          </button>
          <Link to="/settings" className="btn-ghost px-3 py-1.5 text-xs">
            All settings
          </Link>
        </div>
      </Panel>
    </Section>
  );
}

function Notifications() {
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
          <button
            type="button"
            onClick={() => markAll.mutate()}
            disabled={markAll.isPending}
            className="text-xs font-semibold text-accent hover:underline disabled:opacity-50"
          >
            Mark all as read
          </button>
        ) : null
      }
    >
      <Panel className="overflow-hidden">
        {query.isLoading ? (
          <div className="space-y-2 p-3">
            {Array.from({ length: 3 }).map((_, index) => (
              <Skeleton key={index} className="h-12 w-full" />
            ))}
          </div>
        ) : items.length === 0 ? (
          <EmptyState title="Nothing new" description="Deposit, withdrawal and account updates appear here." />
        ) : (
          <ul className="divide-y divide-surface-800/70">
            {items.map((item) => (
              <li key={item.id} className={clsx('flex gap-3 px-4 py-3', !item.read && 'bg-accent/5')}>
                <span
                  className={clsx('mt-1.5 h-2 w-2 shrink-0 rounded-full', item.read ? 'bg-transparent' : 'bg-accent')}
                  aria-hidden="true"
                />
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-medium text-content-100">{item.title}</div>
                  <p className="mt-0.5 text-xs text-content-300">{item.body}</p>
                  <p className="mt-1 text-[10px] text-content-400">{formatDateTime(item.createdAt)}</p>
                </div>
                {!item.read ? (
                  <button
                    type="button"
                    onClick={() => markOne.mutate(item.id)}
                    className="shrink-0 self-start rounded-md px-2 py-1 text-[11px] font-semibold text-content-400 hover:bg-surface-850 hover:text-content-100"
                  >
                    Mark read
                  </button>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </Section>
  );
}

function Support() {
  const unread = useSupportUnread();
  return (
    <Section id="support" title="Support">
      <Link
        to="/account/support"
        className="panel flex items-center gap-3 p-4 transition hover:border-surface-600 hover:bg-surface-850"
      >
        <span className="relative grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-surface-800 text-content-200">
          <Headset className="h-5 w-5" />
          <UnreadDot count={unread} className="-right-1 -top-1" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-sm font-semibold text-content-100">Live chat with support</span>
          <span className="block text-xs text-content-400">
            {unread > 0
              ? unread + (unread === 1 ? ' new reply' : ' new replies') + ' waiting for you'
              : 'Questions about a deposit, a withdrawal, a trade or your account.'}
          </span>
        </span>
        <ChevronRight className="h-4 w-4 shrink-0 text-content-400" />
      </Link>
      <p className="mt-2 text-xs text-content-400">
        Looking for how something works?{' '}
        <Link to="/learn" className="font-semibold text-accent hover:underline">
          Read the guides
        </Link>
      </p>
    </Section>
  );
}

function LegalRisk() {
  return (
    <Section id="legal" title="Legal & risk">
      <Panel className="p-5">
        <div className="flex gap-3 rounded-xl border border-warn/30 bg-warn/10 p-4">
          <AlertTriangle className="h-5 w-5 shrink-0 text-warn" />
          <div className="text-[13px] leading-relaxed text-content-300">
            <p className="font-semibold text-warn">Risk warning</p>
            <p className="mt-1">
              Fixed-time trading is high risk. You can lose the whole amount you invest in any trade, and past results
              do not indicate future ones. Only trade with money you can afford to lose.
            </p>
          </div>
        </div>
        <ul className="mt-3 divide-y divide-surface-800">
          {[
            { to: '/terms', label: 'Terms of service' },
            { to: '/privacy', label: 'Privacy policy' },
            { to: '/learn#risk', label: 'Understanding the risk' },
          ].map((link) => (
            <li key={link.to}>
              <Link
                to={link.to}
                className="flex items-center gap-3 py-2.5 text-sm text-content-200 hover:text-content-100"
              >
                <FileText className="h-4 w-4 shrink-0 text-content-400" />
                <span className="flex-1">{link.label}</span>
                <ChevronRight className="h-4 w-4 text-content-400" />
              </Link>
            </li>
          ))}
        </ul>
      </Panel>
    </Section>
  );
}

function Field({
  label,
  htmlFor,
  hint,
  children,
}: {
  label: string;
  htmlFor?: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label htmlFor={htmlFor} className="mb-1 block text-[11px] font-medium text-content-400">
        {label}
      </label>
      {children}
      {hint ? <p className="mt-1 text-[10px] leading-snug text-content-400">{hint}</p> : null}
    </div>
  );
}
