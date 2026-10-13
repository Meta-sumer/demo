import { Pressable, View } from 'react-native';
import { useAccountStore } from '../stores/accountStore';
import { formatMoney } from '../utils/format';
import { cn } from '../utils/cn';
import { Text } from './ui';
import type { AccountType } from '../types/api';

/**
 * Demo / Live switch.
 *
 * Both options carry their own balance, so the choice is made looking at the
 * money it affects. Demo is marked in the warning tone everywhere it appears -
 * the one thing a customer must never be unsure of is whether a trade is
 * spending real money (brief UX-16).
 *
 * Used in the balance menu and on Home; the account store is the single
 * source, so both move together.
 */
const OPTIONS: Array<{ value: AccountType; label: string; hint: string }> = [
  { value: 'DEMO', label: 'Demo', hint: 'Virtual money' },
  { value: 'LIVE', label: 'Live', hint: 'Real money' },
];

export function AccountSwitch({ className }: { className?: string }) {
  const active = useAccountStore((s) => s.active);
  const wallets = useAccountStore((s) => s.wallets);
  const setActive = useAccountStore((s) => s.setActive);

  return (
    <View accessibilityRole="radiogroup" accessibilityLabel="Trading account" className={cn('flex-row gap-1.5', className)}>
      {OPTIONS.map((option) => {
        const wallet = wallets[option.value];
        const selected = active === option.value;
        // A server without demo accounts returns only the live wallet.
        const available = !!wallet || option.value === active;
        return (
          <Pressable
            key={option.value}
            accessibilityRole="radio"
            accessibilityState={{ checked: selected, disabled: !available }}
            disabled={!available}
            onPress={() => setActive(option.value)}
            className={cn(
              'flex-1 rounded-lg border px-3 py-2',
              !available && 'opacity-45',
              selected
                ? option.value === 'DEMO'
                  ? 'border-warn/60 bg-warn/10'
                  : 'border-accent/60 bg-accent/10'
                : 'border-surface-700 bg-surface-850 active:border-surface-600',
            )}
          >
            <View className="flex-row items-center gap-1.5">
              <View
                className={cn(
                  'h-2 w-2 rounded-full',
                  option.value === 'DEMO' ? 'bg-warn' : 'bg-up',
                  !selected && 'opacity-50',
                )}
              />
              <Text
                className={cn(
                  'text-[11px] font-bold uppercase tracking-wider',
                  selected ? (option.value === 'DEMO' ? 'text-warn' : 'text-content-100') : 'text-content-300',
                )}
              >
                {option.label}
              </Text>
            </View>
            <Text numberOfLines={1} className="mt-0.5 text-sm font-bold tnum text-content-100">
              {wallet ? formatMoney(wallet.balance, wallet.currency) : available ? '--' : 'Unavailable'}
            </Text>
            <Text numberOfLines={1} className="text-[10px] text-content-400">
              {option.hint}
            </Text>
          </Pressable>
        );
      })}
    </View>
  );
}

/** "DEMO · virtual" / "LIVE" - the marker shown wherever a balance is. */
export function AccountMarker({ account, className }: { account: AccountType; className?: string }) {
  const demo = account === 'DEMO';
  return (
    <View
      className={cn(
        'self-start rounded border px-1.5 py-px',
        demo ? 'border-warn/30 bg-warn/15' : 'border-up/30 bg-up/15',
        className,
      )}
    >
      <Text className={cn('text-[9px] font-bold uppercase tracking-wider', demo ? 'text-warn' : 'text-up')}>
        {demo ? 'Demo · virtual' : 'Live'}
      </Text>
    </View>
  );
}
