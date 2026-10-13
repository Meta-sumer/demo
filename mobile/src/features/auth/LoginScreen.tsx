import { useRef, useState, type ReactNode } from 'react';
import { KeyboardAvoidingView, Platform, ScrollView, TextInput, View } from 'react-native';
import { router } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { ChevronLeft } from 'lucide-react-native';
import { Button, Field, IconButton, Panel, Text } from '../../components/ui';
import { useAuthStore } from '../../stores/authStore';
import { ApiRequestError } from '../../services/http';
import { firstIssue, loginSchema } from './schemas';

/**
 * The frame the standalone sign-in and sign-up screens share: the mark, a
 * title, and the form in a panel, centred on the theme's background.
 *
 * Unlike the landing page these follow the theme - they are part of the app,
 * reached from a link or the landing page's footer.
 *
 * A browser has its own back button; a phone screen with no header gets a
 * small one here, shown only when there is somewhere to go back to.
 */
export function AuthShell({
  title,
  subtitle,
  children,
  footer,
}: {
  title: string;
  subtitle: string;
  children: ReactNode;
  footer?: ReactNode;
}) {
  const insets = useSafeAreaInsets();
  const canGoBack = router.canGoBack();

  return (
    <View className="flex-1 bg-surface-950">
      <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined} style={{ flex: 1 }}>
        <ScrollView
          keyboardShouldPersistTaps="handled"
          contentContainerClassName="flex-grow items-center justify-center px-4"
          contentContainerStyle={{ paddingTop: insets.top + 40, paddingBottom: insets.bottom + 40 }}
        >
          <View className="w-full max-w-sm">
            <View className="mb-6 flex-row items-center gap-3">
              <View className="h-10 w-10 items-center justify-center rounded-xl bg-accent">
                <Text className="text-sm font-black text-surface-950">BT</Text>
              </View>
              <View className="min-w-0 flex-1">
                <Text accessibilityRole="header" className="text-lg font-semibold tracking-tight">
                  {title}
                </Text>
                <Text className="text-sm text-content-400">{subtitle}</Text>
              </View>
            </View>

            <Panel className="p-5">{children}</Panel>

            {footer ? <View className="mt-4 items-center">{footer}</View> : null}
          </View>
        </ScrollView>
      </KeyboardAvoidingView>

      {canGoBack ? (
        <View className="absolute left-1.5" style={{ top: insets.top + 6 }}>
          <IconButton
            icon={ChevronLeft}
            size={22}
            accessibilityLabel="Back"
            iconClassName="text-content-100"
            onPress={() => router.back()}
          />
        </View>
      ) : null}
    </View>
  );
}

/**
 * Signing in on its own screen - what a deep link lands on. The landing page
 * signs in through its own panel; both use the same store action and schema.
 *
 * Success is not a navigation here: the root layout's auth gate moves a
 * signed-in session into the app on its own.
 */
export function LoginScreen() {
  const login = useAuthStore((s) => s.login);

  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const passwordRef = useRef<TextInput>(null);

  async function onSubmit() {
    if (submitting) return;
    const parsed = loginSchema.safeParse({ email, password });
    if (!parsed.success) {
      setError(firstIssue(parsed.error));
      return;
    }

    setSubmitting(true);
    setError(null);
    try {
      await login(parsed.data.email, parsed.data.password);
    } catch (err) {
      setError(err instanceof ApiRequestError ? err.message : 'Could not sign in');
      setSubmitting(false);
    }
  }

  return (
    <AuthShell
      title="Sign in"
      subtitle="Access your trading account"
      footer={
        <Text className="text-sm text-content-400">
          No account yet?{' '}
          <Text
            accessibilityRole="link"
            className="text-sm font-medium text-accent"
            onPress={() => router.replace('/register')}
          >
            Create one
          </Text>
        </Text>
      }
    >
      <View className="gap-4">
        <Field
          label="Email"
          value={email}
          onChangeText={setEmail}
          placeholder="trader@binary.local"
          keyboardType="email-address"
          autoCapitalize="none"
          autoCorrect={false}
          autoComplete="email"
          textContentType="username"
          returnKeyType="next"
          submitBehavior="submit"
          onSubmitEditing={() => passwordRef.current?.focus()}
        />

        <Field
          ref={passwordRef}
          label="Password"
          value={password}
          onChangeText={setPassword}
          placeholder="Your password"
          secureTextEntry
          autoCapitalize="none"
          autoCorrect={false}
          autoComplete="current-password"
          textContentType="password"
          returnKeyType="go"
          onSubmitEditing={() => void onSubmit()}
        />

        {error ? (
          <View accessibilityRole="alert" className="rounded-lg border border-down/30 bg-down/10 px-3 py-2">
            <Text className="text-sm text-down">{error}</Text>
          </View>
        ) : null}

        <Button className="w-full" loading={submitting} onPress={() => void onSubmit()}>
          Sign in
        </Button>
      </View>
    </AuthShell>
  );
}
