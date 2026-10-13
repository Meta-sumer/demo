import { TradeScreen } from '../../../features/trade/TradeScreen';

/**
 * The Trade tab. The workspace manages its own regions rather than sitting in
 * a `Screen`: a scroll view around the chart would fight its pan gesture.
 */
export default function TradeRoute() {
  return <TradeScreen />;
}
