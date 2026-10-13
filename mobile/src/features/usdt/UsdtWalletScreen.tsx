import { useEffect, useState, type ReactNode } from 'react';
import { View } from 'react-native';
import { router, useLocalSearchParams } from 'expo-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowDownToLine, ArrowRightLeft, ArrowUpFromLine, Clock3 } from 'lucide-react-native';
import { Screen, ScreenHeader } from '../../components/Screen';
import { Button, Icon, Modal, Panel, Skeleton, Text } from '../../components/ui';
import { cn } from '../../utils/cn';
import { formatUsdt } from '../../utils/usdt';
import type { UsdtNetworkView, UsdtOverview } from '../../types/api';
import { errorCode, usdtErrorMessage } from './model';
import { USDT_KEY, useUsdtOverview } from './queries';
import { NetworkChip, UsdtFigure, UsdtMark } from './parts';
import { DepositFlow } from './DepositFlow';
import { WithdrawFlow } from './WithdrawFlow';
import { TransferFlow } from './TransferFlow';
import { UsdtHistory, type UsdtDetail } from './History';
import { TxDetails } from './TxDetails';

type Flow = 'deposit' | 'withdraw' | 'transfer';
const FLOWS: Flow[] = ['deposit', 'withdraw', 'transfer'];

const TRANSFER_OFF_REASON = 'Trading with USDT is not available yet';

const ACCOUNT_FUNDS = { pathname: '/account', params: { section: 'funds' } } as const;

/**
 * USDT wallet.
 *
 * A balance of its own, apart from the INR Live and Demo accounts in the top
 * bar: USDT comes in from a crypto wallet over TRC20 or BSC, goes back out
 * the same way, and can be moved to the trading side. Deposits, withdrawals
 * and the transfer open over the screen in a sheet, so the balances and
 * history stay where they were.
 *
 * `?flow=deposit|withdraw|transfer` opens one directly, for the links from
 * Account and the wallet screen.
 */
export function UsdtWalletScreen() {
  const overview = useUsdtOverview();
  const queryClient = useQueryClient();
  const params = useLocalSearchParams<{ flow?: string }>();
  const [flow, setFlow] = useState<Flow | null>(null);
  const [detail, setDetail] = useState<UsdtDetail | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);
  const [refreshing, setRefreshing] = useState(false);

  const data = overview.data;
  // A server that answers USDT_DISABLED is saying the same as `enabled: false`.
  const disabled = data ? !data.enabled : errorCode(overview.error) === 'USDT_DISABLED';

  // Honour a deep link once the wallet is known to be usable, then drop the
  // parameter so coming back to the screen does not reopen the flow.
  const requested = (typeof params.flow === 'string' ? params.flow : null) as Flow | null;
  useEffect(() => {
    if (!requested || !data) return;
    const allowed =
      data.enabled && FLOWS.includes(requested) && (requested !== 'transfer' || data.transferToTradingEnabled);
    if (allowed) setFlow(requested);
    router.setParams({ flow: undefined });
  }, [requested, data]);

  async function onRefresh() {
    setRefreshing(true);
    try {
      await queryClient.invalidateQueries({ queryKey: USDT_KEY });
    } finally {
      setRefreshing(false);
    }
  }

  return (
    <View className="flex-1 bg-surface-950">
      <ScreenHeader
        title="USDT wallet"
        subtitle="Separate from your INR Live and Demo balances"
        fallbackHref="/account?section=funds"
      />
      <Screen bottomInset onRefresh={onRefresh} refreshing={refreshing} contentClassName="gap-4">
        {overview.isLoading ? (
          <BalancesSkeleton />
        ) : disabled ? (
          <Unavailable data={data} />
        ) : !data ? (
          <Panel className="items-center gap-3 px-6 py-10">
            <Text className="text-center text-sm font-medium text-content-200">Could not load your USDT wallet</Text>
            <Text className="max-w-sm text-center text-xs text-content-400">{usdtErrorMessage(overview.error)}</Text>
            <Button variant="ghost" onPress={() => void overview.refetch()}>
              Try again
            </Button>
          </Panel>
        ) : (
          <>
            <Balances data={data} onFlow={setFlow} />
            <UsdtHistory
              networks={data.networks}
              onOpen={(next) => {
                setDetail(next);
                setDetailOpen(true);
              }}
            />
            <NetworksPanel networks={data.networks} />
          </>
        )}
      </Screen>

      {data && data.enabled ? (
        <>
          <Modal
            open={flow === 'deposit'}
            onClose={() => setFlow(null)}
            title="Deposit USDT"
            subtitle="Pay from any crypto wallet or exchange. Credited once the network confirms it."
          >
            <DepositFlow networks={data.networks} onDone={() => setFlow(null)} />
          </Modal>
          <Modal
            open={flow === 'withdraw'}
            onClose={() => setFlow(null)}
            title="Withdraw USDT"
            subtitle="Sent to a USDT address on the network you choose."
          >
            <WithdrawFlow networks={data.networks} wallet={data.wallet} onDone={() => setFlow(null)} />
          </Modal>
          <Modal
            open={flow === 'transfer'}
            onClose={() => setFlow(null)}
            title="Transfer to Trading"
            subtitle="Moves USDT from this wallet to your trading balance."
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
        >
          {detail ? (
            <TxDetails detail={detail} networks={data.networks} onLeave={() => setDetailOpen(false)} />
          ) : null}
        </Modal>
      ) : null}
    </View>
  );
}

/** Available first and largest; pending, locked and total under it. */
function Balances({ data, onFlow }: { data: UsdtOverview; onFlow: (flow: Flow) => void }) {
  const wallet = data.wallet;
  const canDeposit = data.networks.some((n) => n.depositEnabled);
  const canWithdraw = data.networks.some((n) => n.withdrawEnabled);

  return (
    <Panel className="p-4">
      <View className="gap-4">
        <View className="min-w-0">
          <View className="flex-row items-center gap-2">
            <UsdtMark size="sm" />
            <Text className="text-[11px] font-medium uppercase tracking-wider text-content-400">Available</Text>
          </View>
          <View className="mt-1.5">
            <UsdtFigure micro={wallet?.availableBalance ?? 0} size={28} />
          </View>
        </View>

        <Figure
          label="Pending"
          hint="Deposits being confirmed"
          value={wallet?.pendingDeposits ?? 0}
          icon={wallet && wallet.pendingDeposits > 0 ? <Icon as={Clock3} size={14} className="text-warn" /> : null}
        />
        <Figure label="Locked" hint="Reserved by withdrawals" value={wallet?.lockedBalance ?? 0} />
        <Figure label="Total" hint="Available + locked" value={wallet?.balance ?? 0} />
      </View>

      {data.trading ? (
        <Text className="mt-3 text-xs text-content-400">
          Trading balance:{' '}
          <Text className="text-xs font-semibold text-content-200 tnum">{formatUsdt(data.trading.balance)}</Text>
        </Text>
      ) : null}

      <View className="mt-4 flex-row flex-wrap gap-2 border-t border-surface-800 pt-4">
        <Button
          icon={ArrowDownToLine}
          className="min-w-[45%] flex-1 px-2.5"
          onPress={() => onFlow('deposit')}
          disabled={!canDeposit}
        >
          Deposit USDT
        </Button>
        <Button
          variant="ghost"
          icon={ArrowUpFromLine}
          className="min-w-[45%] flex-1 px-2.5"
          onPress={() => onFlow('withdraw')}
          disabled={!canWithdraw}
        >
          Withdraw USDT
        </Button>
        <Button
          variant="ghost"
          icon={ArrowRightLeft}
          className="w-full px-2.5"
          onPress={() => onFlow('transfer')}
          disabled={!data.transferToTradingEnabled}
          accessibilityHint={data.transferToTradingEnabled ? undefined : TRANSFER_OFF_REASON}
        >
          Transfer to Trading
        </Button>
      </View>
      {!data.transferToTradingEnabled || !canDeposit || !canWithdraw ? (
        <View className="mt-2 gap-0.5">
          {!canDeposit ? <Text className="text-[11px] text-content-400">USDT deposits are paused right now.</Text> : null}
          {!canWithdraw ? (
            <Text className="text-[11px] text-content-400">USDT withdrawals are paused right now.</Text>
          ) : null}
          {!data.transferToTradingEnabled ? (
            <Text className="text-[11px] text-content-400">{TRANSFER_OFF_REASON}.</Text>
          ) : null}
        </View>
      ) : null}
    </Panel>
  );
}

/**
 * One secondary figure: label on the left, amount on the right. The hint the
 * web shows beside it on a wide screen is read out instead.
 */
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
    <View
      accessible
      accessibilityLabel={label + (hint ? ', ' + hint : '') + ': ' + formatUsdt(value)}
      className="flex-row items-center justify-between gap-3 border-t border-surface-800 pt-2.5"
    >
      <View className="flex-row items-center gap-1">
        <Text className="text-[11px] font-medium uppercase tracking-wider text-content-400">{label}</Text>
        {icon}
      </View>
      <Text className="min-w-0 flex-1 text-right text-sm font-semibold text-content-100 tnum">{formatUsdt(value)}</Text>
    </View>
  );
}

function NetworksPanel({ networks }: { networks: UsdtNetworkView[] }) {
  return (
    <View className="gap-3">
      <Panel className="overflow-hidden">
        <View className="border-b border-surface-800 px-4 py-2.5">
          <Text
            accessibilityRole="header"
            className="text-[11px] font-semibold uppercase tracking-wider text-content-400"
          >
            Networks
          </Text>
        </View>
        {networks.map((network, index) => (
          <View key={network.code} className={cn('px-4 py-2.5', index > 0 && 'border-t border-surface-800/70')}>
            <View className="flex-row items-center gap-2">
              <NetworkChip label={network.label} />
              <Text numberOfLines={1} className="min-w-0 shrink text-sm font-medium text-content-100">
                {network.name || network.chain}
              </Text>
            </View>
            <View className="mt-1 flex-row flex-wrap gap-x-3 gap-y-0.5">
              <Text className={cn('text-[11px]', network.depositEnabled ? 'text-content-400' : 'text-down')}>
                Deposits {network.depositEnabled ? 'open' : 'paused'}
              </Text>
              <Text className={cn('text-[11px]', network.withdrawEnabled ? 'text-content-400' : 'text-down')}>
                Withdrawals {network.withdrawEnabled ? 'open' : 'paused'}
              </Text>
              {network.minDeposit > 0 ? (
                <Text className="text-[11px] text-content-400 tnum">
                  Min deposit {formatUsdt(network.minDeposit, { trim: true })}
                </Text>
              ) : null}
            </View>
          </View>
        ))}
      </Panel>
      <Panel className="gap-2 p-4">
        <Text className="text-xs leading-5 text-content-400">
          <Text className="text-xs font-semibold text-content-200">Same network both ends.</Text> USDT sent on a
          different network, or a different token sent to your deposit address, cannot be recovered.
        </Text>
        <Text className="text-xs leading-5 text-content-400">
          <Text className="text-xs font-semibold text-content-200">Credited on confirmation.</Text> A deposit shows as
          Pending until the network confirms it; returning from the payment page alone does not credit anything.
        </Text>
      </Panel>
    </View>
  );
}

function BalancesSkeleton() {
  return (
    <View className="gap-3">
      <Panel className="gap-3 p-5">
        <Skeleton className="h-4 w-24" />
        <Skeleton className="h-9 w-56" />
        <View className="flex-row flex-wrap gap-2">
          <Skeleton className="h-9 min-w-[45%] flex-1" />
          <Skeleton className="h-9 min-w-[45%] flex-1" />
          <Skeleton className="h-9 w-full" />
        </View>
      </Panel>
      <Panel className="gap-2 p-3">
        {Array.from({ length: 4 }).map((_, index) => (
          <Skeleton key={index} className="h-12 w-full" />
        ))}
      </Panel>
    </View>
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
    <View className="gap-3">
      <Panel className="items-center gap-3 px-6 py-10">
        <UsdtMark size="lg" />
        <Text className="text-center text-base font-semibold text-content-100">USDT wallet is not available yet</Text>
        <Text className="max-w-md text-center text-sm text-content-400">
          USDT deposits, withdrawals and transfers are not open on this platform yet. Your INR Live and Demo balances
          are not affected.
        </Text>
        <Button variant="ghost" className="mt-1" onPress={() => router.navigate(ACCOUNT_FUNDS)}>
          Back to account
        </Button>
      </Panel>
      {held && wallet ? (
        <Panel className="gap-2 p-4">
          <Figure label="Available" hint="" value={wallet.availableBalance} />
          <Figure label="Locked" hint="" value={wallet.lockedBalance} />
          <Figure label="Total" hint="" value={wallet.balance} />
        </Panel>
      ) : null}
    </View>
  );
}
