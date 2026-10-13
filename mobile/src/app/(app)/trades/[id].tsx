import { useLocalSearchParams } from 'expo-router';
import { TradeDetailScreen } from '../../../features/trades/TradeDetailScreen';

export default function TradeDetailRoute() {
  const { id } = useLocalSearchParams<{ id: string }>();
  return <TradeDetailScreen id={typeof id === 'string' ? id : ''} />;
}
