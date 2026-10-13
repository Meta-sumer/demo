import { useMemo, useState } from 'react';
import clsx from 'clsx';
import { Building2, ChevronRight, Smartphone } from 'lucide-react';
import { formatMoney } from '../../utils/format';
import type { PaymentMethod } from '../../types/api';

/**
 * Deposit method picker.
 *
 * Categories on the left, the methods themselves on the right. Every entry here
 * is a method an operator has actually configured in the admin console - there
 * are no placeholder rails, because a customer who picks one that does not
 * exist has paid into nothing.
 *
 * The minimum is shown on the card rather than discovered on submit: it is the
 * one constraint that decides whether a method is usable at all.
 */
const CATEGORIES = [
  {
    type: 'UPI' as const,
    label: 'UPI',
    hint: 'Instant, from any UPI app',
    icon: Smartphone,
    tone: 'bg-up-solid',
  },
  {
    type: 'BANK' as const,
    label: 'Bank transfer',
    hint: 'NEFT, IMPS or RTGS',
    icon: Building2,
    tone: 'bg-accent',
  },
];

interface Props {
  methods: PaymentMethod[];
  selectedKey: string | null;
  onSelect: (key: string) => void;
  loading?: boolean;
}

export function MethodPicker({ methods, selectedKey, onSelect, loading }: Props) {
  const available = useMemo(
    () => CATEGORIES.filter((category) => methods.some((method) => method.type === category.type)),
    [methods],
  );

  // Follow the selection, so opening the picker lands on the chosen method's
  // group rather than resetting to the first one.
  const selectedType = methods.find((method) => method.key === selectedKey)?.type ?? null;
  const [activeType, setActiveType] = useState<PaymentMethod['type'] | null>(null);
  const active = activeType ?? selectedType ?? available[0]?.type ?? null;

  if (loading) {
    return (
      <div className="rounded-xl border border-surface-700 bg-surface-850 px-4 py-8 text-center text-xs text-content-400">
        Loading payment methods…
      </div>
    );
  }

  if (methods.length === 0) {
    return (
      <p className="rounded-xl border border-warn/30 bg-warn/10 px-4 py-3 text-xs text-warn">
        No deposit method is configured yet. Please contact support before sending any money.
      </p>
    );
  }

  return (
    <div className="grid gap-3 lg:grid-cols-[220px_1fr]">
      {/* Categories. A horizontal strip on narrow screens, a column on wide. */}
      <div className="flex gap-2 overflow-x-auto lg:flex-col lg:overflow-visible">
        {available.map((category) => {
          const count = methods.filter((method) => method.type === category.type).length;
          const isActive = active === category.type;

          return (
            <button
              key={category.type}
              type="button"
              onClick={() => setActiveType(category.type)}
              aria-pressed={isActive}
              className={clsx(
                'flex min-w-[180px] shrink-0 flex-col gap-2 rounded-xl border p-3 text-left transition lg:min-w-0',
                isActive
                  ? 'border-transparent bg-accent text-surface-950 shadow-raised'
                  : 'border-surface-700 bg-surface-850 hover:border-surface-600',
              )}
            >
              <span
                className={clsx(
                  'grid h-8 w-8 place-items-center rounded-lg',
                  isActive ? 'bg-white/20 text-white' : category.tone + ' text-white',
                )}
              >
                <category.icon className="h-4 w-4" />
              </span>
              <span>
                <span className="block text-sm font-bold uppercase tracking-wide">{category.label}</span>
                <span className={clsx('block text-[11px]', isActive ? 'text-white/80' : 'text-content-400')}>
                  {count} {count === 1 ? 'method' : 'methods'}
                </span>
              </span>
            </button>
          );
        })}
      </div>

      {/* Methods in the active category. */}
      <div className="min-w-0">
        <h3 className="mb-2 text-sm font-bold text-content-100">
          {CATEGORIES.find((category) => category.type === active)?.label}{' '}
          <span className="font-normal text-content-400">
            ({methods.filter((method) => method.type === active).length})
          </span>
        </h3>

        <div className="grid gap-2 sm:grid-cols-2">
          {methods
            .filter((method) => method.type === active)
            .map((method) => {
              const isSelected = method.key === selectedKey;

              return (
                <button
                  key={method.key}
                  type="button"
                  onClick={() => onSelect(method.key)}
                  aria-pressed={isSelected}
                  className={clsx(
                    'flex items-center gap-3 rounded-xl border px-3 py-2.5 text-left transition',
                    isSelected
                      ? 'border-accent bg-accent/10'
                      : 'border-surface-700 bg-surface-850 hover:border-surface-600',
                  )}
                >
                  <span
                    className={clsx(
                      'grid h-9 w-9 shrink-0 place-items-center rounded-lg text-[11px] font-black uppercase text-white',
                      method.type === 'UPI' ? 'bg-up-solid' : 'bg-accent',
                    )}
                  >
                    {method.label.slice(0, 2)}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-semibold text-content-100">{method.label}</span>
                    <span className="block text-[11px] tnum text-content-400">
                      Min. {formatMoney(method.minAmount)}
                    </span>
                  </span>
                  <ChevronRight
                    className={clsx('h-4 w-4 shrink-0', isSelected ? 'text-accent' : 'text-content-400')}
                  />
                </button>
              );
            })}
        </div>
      </div>
    </div>
  );
}
