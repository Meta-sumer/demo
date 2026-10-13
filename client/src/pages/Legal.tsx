import { NavLink } from 'react-router-dom';
import { ArrowLeft, AlertTriangle } from 'lucide-react';

/**
 * The Client Agreement and the Privacy Policy.
 *
 * Registration asks a visitor to accept both, so both have to exist and be
 * readable before they tick the box - a consent checkbox pointing at a 404 is
 * worse than no checkbox at all.
 *
 * What is here is a skeleton, not a document: the headings each agreement
 * needs, and a line under each saying what belongs there. The operator writes
 * the text and has it reviewed; nothing on this page is drafted to be relied
 * on, and the banner at the top says so plainly. Replace the sections below
 * with the reviewed text and delete the banner.
 */
interface Section {
  heading: string;
  needs: string;
}

const CLIENT_AGREEMENT: Section[] = [
  {
    heading: 'Who this agreement is between',
    needs: 'The operating entity, its registration and the jurisdiction whose law governs the agreement.',
  },
  {
    heading: 'Eligibility',
    needs:
      'Minimum age, the countries you will and will not accept clients from, and what happens if someone registers from a restricted one.',
  },
  {
    heading: 'What a fixed-return trade is',
    needs:
      'That the client buys no asset, that a losing position costs the full stake, and how the payout is calculated.',
  },
  {
    heading: 'How a price is decided',
    needs:
      'Where quotes come from, that the entry is the price at the moment the server accepts the order, and how settlement is determined.',
  },
  {
    heading: 'Stakes, limits and payouts',
    needs:
      'The minimum and maximum stake, the expiries offered, and that payout rates can change for trades not yet placed.',
  },
  {
    heading: 'Deposits and withdrawals',
    needs:
      'Accepted methods, processing times, fees, and what identification is required before a withdrawal is paid.',
  },
  {
    heading: 'Closing a position early',
    needs: 'That the sell-now quote is indicative until accepted and may be below the stake.',
  },
  {
    heading: 'Suspension and closure',
    needs:
      'The grounds on which an account may be suspended or closed, and what happens to an open position and a balance if it is.',
  },
  {
    heading: 'Complaints',
    needs:
      'How a client raises a dispute, the timescales you commit to, and the external body they may escalate to.',
  },
  {
    heading: 'Risk warning',
    needs: 'That the client can lose the full amount staked, and that past results do not indicate future ones.',
  },
];

const PRIVACY_POLICY: Section[] = [
  {
    heading: 'Who controls your data',
    needs: 'The entity responsible, and how to reach its data protection contact.',
  },
  {
    heading: 'What is collected',
    needs: 'Registration details, trading activity, payment records, device and connection data.',
  },
  {
    heading: 'Why it is collected',
    needs:
      'The lawful basis for each purpose - performing the contract, meeting anti-money-laundering obligations, preventing fraud.',
  },
  {
    heading: 'Who it is shared with',
    needs:
      'Payment providers, identity verification services, hosting providers, and any regulator entitled to it.',
  },
  {
    heading: 'Where it is stored',
    needs: 'The countries data is processed in, and the safeguards used if it leaves its origin.',
  },
  {
    heading: 'How long it is kept',
    needs: 'Retention periods, including the ones set by financial record-keeping rules rather than by choice.',
  },
  {
    heading: 'Your rights',
    needs: 'Access, correction, erasure, objection and portability, and how to exercise each.',
  },
  { heading: 'Cookies and tracking', needs: 'What is set, what it is for, and how it can be refused.' },
  {
    heading: 'Changes to this policy',
    needs: 'How clients are told when it changes and when a change takes effect.',
  },
];

export function TermsPage() {
  return <LegalDocument title="Client Agreement" sections={CLIENT_AGREEMENT} />;
}

export function PrivacyPage() {
  return <LegalDocument title="Privacy Policy" sections={PRIVACY_POLICY} />;
}

function LegalDocument({ title, sections }: { title: string; sections: Section[] }) {
  return (
    <div className="min-h-screen bg-surface-950 px-4 py-12 text-content-100 sm:px-6">
      <div className="mx-auto max-w-3xl">
        <NavLink
          to="/"
          className="inline-flex items-center gap-2 text-sm font-medium text-content-400 transition hover:text-content-100"
        >
          <ArrowLeft className="h-4 w-4" />
          Back
        </NavLink>

        <h1 className="mt-8 text-3xl font-black tracking-tight sm:text-4xl">{title}</h1>

        <div className="mt-6 flex items-start gap-3 rounded-xl border border-warn/30 bg-warn/10 p-4">
          <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-warn" />
          <p className="text-sm leading-relaxed text-content-200">
            <span className="font-bold">This document has not been written yet.</span> What follows is an outline
            of the sections it needs, not terms you can rely on and not terms this platform is operating under. It
            must be drafted and reviewed by a qualified adviser before the platform takes a real client.
          </p>
        </div>

        <ol className="mt-10 space-y-8">
          {sections.map((section, index) => (
            <li key={section.heading}>
              <h2 className="text-lg font-bold">
                <span className="mr-2 text-content-400 tnum">{index + 1}.</span>
                {section.heading}
              </h2>
              <p className="mt-2 text-sm leading-relaxed text-content-300">{section.needs}</p>
            </li>
          ))}
        </ol>
      </div>
    </div>
  );
}
