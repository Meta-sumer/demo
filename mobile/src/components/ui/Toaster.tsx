import { Pressable, View } from 'react-native';
import Animated, { FadeInDown, FadeOutDown, LinearTransition } from 'react-native-reanimated';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { CheckCircle2, Info, XCircle } from 'lucide-react-native';
import { useToastStore } from '../../hooks/useToast';
import { cn } from '../../utils/cn';
import { Icon } from './Icon';
import { Text } from './Text';

/**
 * Transient confirmations and errors.
 *
 * Bottom of the screen, clear of the navigation bar, where the thumb that
 * caused them already is. A tap dismisses one early.
 */
const TAB_BAR_HEIGHT = 64;

export function Toaster({ bottomOffset = TAB_BAR_HEIGHT }: { bottomOffset?: number }) {
  const toasts = useToastStore((s) => s.toasts);
  const dismiss = useToastStore((s) => s.dismiss);
  const insets = useSafeAreaInsets();

  if (toasts.length === 0) return null;

  return (
    <View
      pointerEvents="box-none"
      style={{ position: 'absolute', left: 12, right: 12, bottom: insets.bottom + bottomOffset + 12 }}
      className="gap-2"
    >
      {toasts.map((item) => {
        const glyph = item.tone === 'success' ? CheckCircle2 : item.tone === 'error' ? XCircle : Info;
        const tone = item.tone === 'success' ? 'text-up' : item.tone === 'error' ? 'text-down' : 'text-accent';
        return (
          <Animated.View key={item.id} entering={FadeInDown.duration(180)} exiting={FadeOutDown.duration(160)} layout={LinearTransition}>
            <Pressable
              onPress={() => dismiss(item.id)}
              accessibilityRole="alert"
              className="flex-row items-start gap-3 rounded-xl border border-surface-700 bg-surface-900 px-4 py-3"
              style={{ elevation: 8, shadowColor: '#000', shadowOpacity: 0.3, shadowRadius: 12, shadowOffset: { width: 0, height: 6 } }}
            >
              <View className="mt-0.5">
                <Icon as={glyph} size={16} className={cn(tone)} />
              </View>
              <View className="min-w-0 flex-1">
                <Text className="text-sm font-medium text-content-100">{item.title}</Text>
                {item.description ? <Text className="mt-0.5 text-xs text-content-400">{item.description}</Text> : null}
              </View>
            </Pressable>
          </Animated.View>
        );
      })}
    </View>
  );
}
