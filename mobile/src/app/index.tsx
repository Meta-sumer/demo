import { Redirect } from 'expo-router';
import { useAuthStore } from '../stores/authStore';

/**
 * The front door: Home for a trader, the welcome screen for a visitor
 * (brief UX-01 - balance, favourites and a way into everything else, rather
 * than dropping straight onto a chart).
 */
export default function Index() {
  const authenticated = useAuthStore((s) => s.status === 'authenticated');
  return <Redirect href={authenticated ? '/home' : '/welcome'} />;
}
