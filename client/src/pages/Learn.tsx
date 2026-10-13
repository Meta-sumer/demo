import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { ChevronDown, PlayCircle, Search, X } from 'lucide-react';
import { Panel } from '../components/ui';
import { matchesQuery, nodeText } from '../utils/textSearch';
import { useDrawPolicy } from '../features/trade/drawRule';
import type { DrawPolicy } from '../types/api';

/**
 * Learn (brief UX-18).
 *
 * Short guides and answers, written to match how the platform actually
 * behaves - the server sets every price, payouts differ by market and
 * duration, a tie settles by the configured draw policy (refunded by default),
 * selling early is priced live with a fee, and
 * deposits are checked by hand. If any of those change, this page has to
 * change with them; a guide that describes a different product is worse than
 * none.
 *
 * It supports trading and stays out of the way of it: nothing here is linked
 * from inside the trade ticket except by the customer's choice - the ticket's
 * "How a trade works" panel links here for the full guide.
 *
 * A search box at the top narrows the guides and the answers to the ones that
 * mention every word typed (reference record 19, Olymptrade's "Find the help
 * you need"); a matching answer opens, so the answer is what the search finds.
 */
interface Guide {
  id: string;
  title: string;
  body: ReactNode;
}

/** The guides, stating the tie rule the platform settles by. */
function buildGuides(drawPolicy: DrawPolicy): Guide[] {
  return [
    {
      id: 'how-it-works',
      title: 'What is fixed-time trading?',
      body: (
        <>
          <p>
            You choose a market, a duration and an amount, then predict whether the price will be higher (
            <b className="text-up">UP</b>) or lower (<b className="text-down">DOWN</b>) than your entry price when the
            duration ends.
          </p>
          <p>
            If you are right you receive your investment back plus the payout. If you are wrong you lose the amount
            you invested. You do not own the asset, and there is no leverage: a trade can never cost more than you
            put into it.
          </p>
        </>
      ),
    },
    {
      id: 'duration-expiry',
      title: 'Duration and expiry',
      body: (
        <>
          <p>
            The duration is how long the trade runs - for example 1 minute or 5 minutes. Each market offers its own
            set of durations.
          </p>
          <p>
            The expiry is the clock time the trade ends: the moment it was accepted plus the duration. It is shown
            before you confirm and on the running trade. The platform's own clock decides expiry, not your device.
          </p>
        </>
      ),
    },
    {
      id: 'payout-return',
      title: 'Payout and return',
      body: (
        <>
          <p>
            The payout is a percentage of your investment, set per market and per duration, and shown before you
            trade. It is fixed when your trade is accepted, even if the displayed payout changes afterwards.
          </p>
          <p>
            Return = investment + payout. ₹100 at an 82% payout returns ₹182 if you are right: a net result of +₹82.
          </p>
          <p>
            Your entry price is the platform's price at the moment the trade is accepted. If the price moves too far
            while your order is on its way, the trade is not placed and you are shown the new price instead.
          </p>
        </>
      ),
    },
    {
      id: 'max-loss',
      title: 'Maximum loss',
      body: (
        <>
          <p>
            The most you can lose on a trade is the amount you invest in it. If the price ends on the wrong side of
            your entry price, that amount is lost in full.
          </p>
          <p>
            If the closing price is exactly equal to your entry price,{' '}
            {drawPolicy === 'LOSS'
              ? 'the trade counts as a loss and that amount is lost too.'
              : drawPolicy === 'WIN'
                ? 'the trade counts as a win.'
                : 'the trade is a draw and your investment is refunded.'}
          </p>
        </>
      ),
    },
    {
      id: 'selling-early',
      title: 'Selling early',
      body: (
        <>
          <p>
            While a trade is running you can sell it before expiry. The value offered is calculated live from the
            current price and the time left, minus a small fee, and it can be more or less than you invested.
          </p>
          <p>Selling is not available in the final seconds before expiry. After selling, the result is final.</p>
        </>
      ),
    },
    {
      id: 'demo-live',
      title: 'Demo and Live accounts',
      body: (
        <>
          <p>
            You have two accounts. <b className="text-warn">Demo</b> holds virtual money for practice; it uses the same
            markets, prices and rules, and you can reset its balance. <b className="text-content-100">Live</b> holds
            real money.
          </p>
          <p>
            The balance at the top of the screen always shows which account you are using. Switch from its menu or
            from Home. Every trade is placed on the account that is active when you confirm it.
          </p>
        </>
      ),
    },
    {
      id: 'deposits-withdrawals',
      title: 'Deposits and withdrawals',
      body: (
        <>
          <p>
            Deposits and withdrawals are for your Live account only. To deposit, pay by UPI or bank transfer to the
            details shown, then submit the UTR / reference number (or a screenshot). Our team checks it against the
            bank record and credits your balance once it matches.
          </p>
          <p>
            A withdrawal reserves the amount straight away and is paid manually to the UPI ID or bank account you
            enter. If it is rejected, the amount is returned to your balance. Each method has a minimum and a maximum.
          </p>
        </>
      ),
    },
    {
      id: 'risk',
      title: 'Risk',
      body: (
        <>
          <p>
            Fixed-time trading is high risk. Short-term price movements are close to unpredictable, and because a win
            pays back less than double, trading without an edge loses money over time.
          </p>
          <p>
            Only trade with money you can afford to lose. Set yourself a limit, take breaks, and never trade more to
            win back a loss. Practise on Demo first.
          </p>
        </>
      ),
    },
  ];
}

const FAQ: Array<{ q: string; a: ReactNode }> = [
  {
    q: 'Why was my trade refused?',
    a: 'A trade is refused when the market is closed or paused (its price feed is delayed), the amount is outside the market’s limits, your available balance is too low, or the price moved too far while the order was on its way. The trade screen says which.',
  },
  {
    q: 'Why is the payout different on another duration?',
    a: 'Payouts are set per market and per duration. The trade screen always shows the payout for the duration you have selected.',
  },
  {
    q: 'What does “Paused” mean on a market?',
    a: 'The market is open but its price feed is delayed or offline. New trades are refused until prices are flowing again, so nobody trades on an old price.',
  },
  {
    q: 'Can I lose more than I invest?',
    a: 'No. The most any trade can cost is the amount you invested in it.',
  },
  {
    q: 'How long does a deposit take?',
    a: 'It is credited once our team has matched your UTR or screenshot against the bank record. You can see its status under Account → Funds.',
  },
  {
    q: 'Can I withdraw my demo balance?',
    a: 'No. Demo money is virtual and exists only for practice. Only your Live balance can be withdrawn.',
  },
  {
    q: 'What is “confirmation” and one-tap trading?',
    a: 'By default you review each trade before it is placed. If you turn on one-tap trading (Account → Preferences), pressing UP or DOWN places the trade immediately.',
  },
];

/** Each answer with the words it says, read once from its JSX. The guides are read when built. */
const FAQ_TEXT = FAQ.map((item) => item.q + ' ' + nodeText(item.a));
const INTRO_TEXT = 'Replay the introduction onboarding. Five short steps, ending in your demo account.';

export function LearnPage() {
  const { hash } = useLocation();
  const [query, setQuery] = useState('');
  const searching = query.trim() !== '';

  const drawPolicy = useDrawPolicy();
  const allGuides = useMemo(
    () => buildGuides(drawPolicy).map((guide) => ({ guide, text: guide.title + ' ' + nodeText(guide.body) })),
    [drawPolicy],
  );
  const guides = useMemo(
    () => allGuides.filter((entry) => matchesQuery(entry.text, query)).map((entry) => entry.guide),
    [allGuides, query],
  );
  const answers = useMemo(() => FAQ.filter((_, index) => matchesQuery(FAQ_TEXT[index]!, query)), [query]);
  const intro = matchesQuery(INTRO_TEXT, query);
  const found = [
    plural(guides.length, 'guide'),
    plural(answers.length, 'answer'),
    searching && intro ? 'the introduction' : '',
  ].filter(Boolean);

  // Home links to single guides; scroll to the one asked for.
  useEffect(() => {
    if (!hash) return;
    document.getElementById(hash.slice(1))?.scrollIntoView({ block: 'start' });
  }, [hash]);

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <header>
        <h1 className="text-lg font-bold text-content-100">Learn</h1>
        <p className="text-xs text-content-400">Short guides to how trading on this platform works.</p>
      </header>

      <div role="search" className="space-y-1.5">
        <div className="relative">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-content-400" />
          <input
            type="search"
            enterKeyHint="search"
            autoComplete="off"
            className="field pl-9 pr-9 [&::-webkit-search-cancel-button]:hidden"
            placeholder="Find the help you need, e.g. payout or draw"
            aria-label="Search guides and answers"
            aria-describedby="learn-search-result"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
          {query ? (
            <button
              type="button"
              onClick={() => setQuery('')}
              className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded-md p-1.5 text-content-400 hover:text-content-100"
              aria-label="Clear search"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          ) : null}
        </div>
        <p id="learn-search-result" role="status" className={searching ? 'text-xs text-content-400' : 'sr-only'}>
          {searching
            ? found.length === 0
              ? 'Nothing matches “' + query.trim() + '”.'
              : 'Found ' + listed(found) + ' for “' + query.trim() + '”.'
            : ''}
        </p>
      </div>

      {searching && found.length === 0 ? (
        <Panel className="p-5 text-center">
          <p className="text-sm text-content-200">Try another word - for example payout, expiry, draw or withdrawal.</p>
          <p className="mt-2 text-xs text-content-400">
            Or{' '}
            <Link to="/account/support" className="font-semibold text-accent hover:underline">
              ask support
            </Link>
            .
          </p>
        </Panel>
      ) : null}

      {intro ? (
        <Link
          to="/onboarding"
          className="panel flex items-center gap-3 p-4 transition hover:border-surface-600 hover:bg-surface-850"
        >
          <PlayCircle className="h-6 w-6 shrink-0 text-accent" />
          <span className="min-w-0">
            <span className="block text-sm font-semibold text-content-100">Replay the introduction</span>
            <span className="block text-xs text-content-400">Five short steps, ending in your demo account.</span>
          </span>
        </Link>
      ) : null}

      {guides.length > 0 ? (
        <nav aria-label="Guides" className="-mx-1 flex gap-1.5 overflow-x-auto px-1 pb-1">
          {guides.map((guide) => (
            <a
              key={guide.id}
              href={'#' + guide.id}
              onClick={(event) => {
                event.preventDefault();
                document.getElementById(guide.id)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
              }}
              className="shrink-0 rounded-full border border-surface-700 bg-surface-850 px-3 py-1 text-xs font-semibold text-content-300 hover:text-content-100"
            >
              {guide.title}
            </a>
          ))}
        </nav>
      ) : null}

      <div className="space-y-2">
        {guides.map((guide) => (
          <Panel key={guide.id} className="scroll-mt-4 p-4 sm:p-5">
            <article id={guide.id} className="scroll-mt-4">
              <h2 className="text-base font-bold text-content-100">{guide.title}</h2>
              <div className="mt-2 space-y-2 text-sm leading-relaxed text-content-300">{guide.body}</div>
            </article>
          </Panel>
        ))}
      </div>

      {answers.length > 0 ? (
        <section aria-labelledby="faq">
          <h2 id="faq" className="mb-2 mt-6 text-base font-bold text-content-100">
            Frequently asked questions
          </h2>
          <Panel className="divide-y divide-surface-800 overflow-hidden">
            {answers.map((item) => (
              // Keyed by the search too: a search opens the answers it finds,
              // and clearing it puts them back the way they were.
              <details key={item.q + (searching ? ':found' : '')} className="group" open={searching || undefined}>
                <summary className="flex cursor-pointer list-none items-center gap-3 px-4 py-3 text-sm font-semibold text-content-100 hover:bg-surface-850 [&::-webkit-details-marker]:hidden">
                  <span className="flex-1">{item.q}</span>
                  <ChevronDown className="h-4 w-4 shrink-0 text-content-400 transition group-open:rotate-180" />
                </summary>
                <p className="px-4 pb-4 text-sm leading-relaxed text-content-300">{item.a}</p>
              </details>
            ))}
          </Panel>
        </section>
      ) : null}

      <p className="pb-2 text-center text-xs text-content-400">
        Still stuck?{' '}
        <Link to="/account/support" className="font-semibold text-accent hover:underline">
          Ask support
        </Link>
      </p>
    </div>
  );
}

/** "2 guides", "1 answer", or nothing for none. */
function plural(count: number, noun: string): string {
  return count === 0 ? '' : count + ' ' + noun + (count === 1 ? '' : 's');
}

/** "a", "a and b", "a, b and c". */
function listed(parts: string[]): string {
  return parts.length <= 1 ? (parts[0] ?? '') : parts.slice(0, -1).join(', ') + ' and ' + parts[parts.length - 1];
}
