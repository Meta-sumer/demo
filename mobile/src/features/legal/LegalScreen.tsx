import { View } from 'react-native';
import { AlertTriangle } from 'lucide-react-native';
import { useAuthStore } from '../../stores/authStore';
import { Screen, ScreenHeader } from '../../components/Screen';
import { Icon, Text } from '../../components/ui';

/**
 * The Client Agreement and the Privacy Policy.
 *
 * Registration asks a visitor to accept both, so both have to exist and be
 * readable before they tick the box - a consent checkbox pointing at a missing
 * screen is worse than no checkbox at all.
 *
 * What is here is a skeleton, not a document: the headings each agreement
 * needs, and a line under each saying what belongs there. The operator writes
 * the text and has it reviewed; nothing on this screen is drafted to be relied
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

export function TermsScreen() {
  return <LegalDocument title="Client Agreement" sections={CLIENT_AGREEMENT} />;
}

export function PrivacyScreen() {
  return <LegalDocument title="Privacy Policy" sections={PRIVACY_POLICY} />;
}

function LegalDocument({ title, sections }: { title: string; sections: Section[] }) {
  // Readable signed in or out: with nothing to go back to (opened from a
  // link), back leads to the start of whichever half of the app is reachable.
  const authenticated = useAuthStore((s) => s.status === 'authenticated');

  return (
    <View className="flex-1 bg-surface-950">
      <ScreenHeader title={title} fallbackHref={authenticated ? '/home' : '/welcome'} />
      <Screen bottomInset contentClassName="px-4 pt-6">
        <Text accessibilityRole="header" className="text-3xl font-black tracking-tight text-content-100">
          {title}
        </Text>

        <View className="mt-6 flex-row items-start gap-3 rounded-xl border border-warn/30 bg-warn/10 p-4">
          <View className="mt-0.5">
            <Icon as={AlertTriangle} size={20} className="text-warn" />
          </View>
          <Text className="min-w-0 flex-1 text-sm leading-6 text-content-200">
            <Text className="text-sm font-bold leading-6 text-content-200">This document has not been written yet.</Text>{' '}
            What follows is an outline of the sections it needs, not terms you can rely on and not terms this platform
            is operating under. It must be drafted and reviewed by a qualified adviser before the platform takes a real
            client.
          </Text>
        </View>

        <View className="mt-10 gap-8">
          {sections.map((section, index) => (
            <View key={section.heading}>
              <Text accessibilityRole="header" className="text-lg font-bold text-content-100">
                <Text className="text-lg font-bold text-content-400 tnum">{index + 1 + '.  '}</Text>
                {section.heading}
              </Text>
              <Text className="mt-2 text-sm leading-6 text-content-300">{section.needs}</Text>
            </View>
          ))}
        </View>
      </Screen>
    </View>
  );
}
