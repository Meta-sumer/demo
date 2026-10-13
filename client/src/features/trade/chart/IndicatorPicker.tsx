import { useEffect, useMemo, useState } from 'react';
import clsx from 'clsx';
import { Check, Plus, Search, X } from 'lucide-react';
import { Modal } from '../../../components/Modal';
import { useSettingsStore, type Indicator } from '../../../stores/settingsStore';
import {
  INDICATOR_CATALOG,
  INDICATOR_CATEGORIES,
  INDICATOR_ORDER,
  searchIndicators,
  type IndicatorCategory,
  type IndicatorInfo,
} from './indicatorCatalog';

/**
 * The indicator picker (record 09), modelled on Deriv's: sections down the
 * side - what is on the chart now, then Trend, Momentum and Volatility - with
 * a search across all of them.
 *
 * "Active" comes first and is where the picker opens when anything is on: the
 * commonest reason to open it again is to take something off. Every row says
 * what the indicator measures in a sentence, because a name like "Stochastic"
 * tells a trader who has not met it nothing.
 *
 * A dialog rather than a popover: six rows with a description each do not fit
 * above a toolbar on a phone, and the shared dialog is already a bottom sheet
 * there.
 */
type Section = 'active' | IndicatorCategory;

export function IndicatorPicker({ open, onClose }: { open: boolean; onClose: () => void }) {
  const active = useSettingsStore((s) => s.indicators);
  const toggle = useSettingsStore((s) => s.toggleIndicator);
  const remove = useSettingsStore((s) => s.removeIndicator);
  const clear = useSettingsStore((s) => s.clearIndicators);

  const [section, setSection] = useState<Section>('trend');
  const [query, setQuery] = useState('');

  // Each opening starts where it is most useful, with the search cleared.
  useEffect(() => {
    if (!open) return;
    setQuery('');
    setSection(useSettingsStore.getState().indicators.length > 0 ? 'active' : 'trend');
  }, [open]);

  const activeInOrder = useMemo(() => INDICATOR_ORDER.filter((id) => active.includes(id)), [active]);
  const results = useMemo(() => searchIndicators(query), [query]);
  const searching = query.trim() !== '';

  const categoryItems =
    section === 'active'
      ? []
      : INDICATOR_ORDER.map((id) => INDICATOR_CATALOG[id]).filter((info) => info.category === section);

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Indicators"
      subtitle="Drawn from the candles on screen. Display only - they never change an order."
      width="sm:w-[min(720px,94vw)]"
    >
      <div className="flex flex-col gap-4 sm:flex-row sm:gap-6">
        {/* A column on a wide screen, a row of chips on a phone. */}
        <nav
          aria-label="Indicator sections"
          className="-mx-1 flex shrink-0 gap-1 overflow-x-auto px-1 pb-1 sm:mx-0 sm:w-40 sm:flex-col sm:overflow-visible sm:p-0"
        >
          <SectionButton
            label="Active"
            count={active.length}
            selected={!searching && section === 'active'}
            onClick={() => {
              setQuery('');
              setSection('active');
            }}
          />
          <div className="mx-2 my-1.5 hidden border-t border-surface-800 sm:block" />
          {INDICATOR_CATEGORIES.map((category) => (
            <SectionButton
              key={category.id}
              label={category.label}
              selected={!searching && section === category.id}
              onClick={() => {
                setQuery('');
                setSection(category.id);
              }}
            />
          ))}
        </nav>

        <div className="min-w-0 flex-1">
          <label className="relative block">
            <span className="sr-only">Search indicators</span>
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-content-400" />
            <input
              type="search"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search, e.g. RSI or moving average"
              className="field pl-9"
              autoComplete="off"
            />
          </label>

          {/* A fixed height on a wide screen, so the dialog does not jump
              as the sections change length. */}
          <div className="mt-3 min-h-[220px] sm:h-[400px] sm:overflow-y-auto sm:pr-1">
            {searching ? (
              results.length > 0 ? (
                <IndicatorList items={results} active={active} onToggle={toggle} />
              ) : (
                <p className="px-1 py-8 text-center text-sm text-content-400">No indicator matches "{query.trim()}".</p>
              )
            ) : section === 'active' ? (
              activeInOrder.length > 0 ? (
                <>
                  <ul className="space-y-1.5">
                    {activeInOrder.map((id) => (
                      <ActiveRow key={id} info={INDICATOR_CATALOG[id]} onRemove={() => remove(id)} />
                    ))}
                  </ul>
                  <button
                    type="button"
                    onClick={clear}
                    className="mt-3 inline-flex h-9 items-center rounded-lg px-2 text-xs font-semibold text-content-400 transition hover:bg-surface-850 hover:text-down"
                  >
                    Remove all
                  </button>
                </>
              ) : (
                <div className="flex flex-col items-center gap-1 px-4 py-10 text-center">
                  <p className="text-sm font-medium text-content-200">Nothing on the chart yet</p>
                  <p className="max-w-xs text-xs text-content-400">
                    Add one from Trend, Momentum or Volatility, or search for it by name.
                  </p>
                </div>
              )
            ) : (
              <>
                <p className="mb-2 px-1 text-[11px] text-content-400">
                  {INDICATOR_CATEGORIES.find((category) => category.id === section)?.hint}
                </p>
                <IndicatorList items={categoryItems} active={active} onToggle={toggle} />
              </>
            )}
          </div>
        </div>
      </div>
    </Modal>
  );
}

function SectionButton({
  label,
  count,
  selected,
  onClick,
}: {
  label: string;
  count?: number;
  selected: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-current={selected ? 'true' : undefined}
      className={clsx(
        'flex h-9 shrink-0 items-center justify-between gap-2 whitespace-nowrap rounded-lg px-3 text-sm font-semibold transition',
        selected
          ? 'bg-surface-800 text-content-100 sm:shadow-[inset_3px_0_0_0_rgb(var(--accent))]'
          : 'text-content-400 hover:bg-surface-850 hover:text-content-200',
      )}
    >
      {label}
      {count !== undefined ? (
        <span
          className={clsx(
            'grid h-5 min-w-5 place-items-center rounded-full px-1.5 text-[10px] font-bold tnum',
            count > 0 ? 'bg-accent text-surface-950' : 'bg-surface-800 text-content-400',
          )}
        >
          {count}
        </span>
      ) : null}
    </button>
  );
}

function IndicatorList({
  items,
  active,
  onToggle,
}: {
  items: IndicatorInfo[];
  active: Indicator[];
  onToggle: (id: Indicator) => void;
}) {
  return (
    <ul className="space-y-1.5">
      {items.map((info) => {
        const on = active.includes(info.id);
        return (
          <li key={info.id}>
            <button
              type="button"
              onClick={() => onToggle(info.id)}
              aria-pressed={on}
              className={clsx(
                'group flex w-full items-center gap-3 rounded-xl border px-3 py-2.5 text-left transition',
                on ? 'border-accent/50 bg-accent/10' : 'border-surface-800 hover:border-surface-700 hover:bg-surface-850',
              )}
            >
              <Abbreviation info={info} />
              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-baseline gap-x-1.5 text-sm font-semibold text-content-100">
                  {info.name}
                  <span className="text-[11px] font-medium tnum text-content-400">{info.params}</span>
                </span>
                <span className="mt-0.5 block text-[11px] leading-snug text-content-400">{info.description}</span>
              </span>
              <span
                className={clsx(
                  'flex h-8 shrink-0 items-center gap-1 rounded-lg px-2.5 text-[11px] font-bold transition',
                  on ? 'text-accent' : 'bg-surface-800 text-content-200 group-hover:bg-surface-700',
                )}
              >
                {on ? <Check className="h-3.5 w-3.5" strokeWidth={3} /> : <Plus className="h-3.5 w-3.5" strokeWidth={3} />}
                {on ? 'Added' : 'Add'}
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

function ActiveRow({ info, onRemove }: { info: IndicatorInfo; onRemove: () => void }) {
  return (
    <li className="flex items-center gap-3 rounded-xl border border-surface-800 bg-surface-850/50 px-3 py-2">
      <Abbreviation info={info} />
      <span className="min-w-0 flex-1">
        <span className="flex flex-wrap items-baseline gap-x-1.5 text-sm font-semibold text-content-100">
          {info.name}
          <span className="text-[11px] font-medium tnum text-content-400">{info.params}</span>
        </span>
        <span className="block text-[11px] text-content-400">
          {info.placement === 'overlay' ? 'On the price' : 'In its own band under the price'}
        </span>
      </span>
      <button
        type="button"
        onClick={onRemove}
        aria-label={'Remove ' + info.name}
        title={'Remove ' + info.name}
        className="grid h-9 w-9 shrink-0 place-items-center rounded-lg text-content-400 transition hover:bg-surface-800 hover:text-down"
      >
        <X className="h-4 w-4" />
      </button>
    </li>
  );
}

function Abbreviation({ info }: { info: IndicatorInfo }) {
  return (
    <span className="grid h-9 w-12 shrink-0 place-items-center rounded-lg bg-surface-800 text-[10px] font-black uppercase tracking-wide text-content-200">
      {info.short}
    </span>
  );
}
