import { useRef, useState } from 'react';
import { TextInput, View } from 'react-native';
import { router } from 'expo-router';
import { Button, Field, Text } from '../../components/ui';
import { useAuthStore } from '../../stores/authStore';
import { ApiRequestError } from '../../services/http';
import { AuthShell } from './LoginScreen';
import { firstIssue, registerSchema } from './schemas';
import { continueAfterRegistration } from './afterRegister';

/**
 * Creating an account on its own screen - what a deep link, or the landing
 * page's footer, lands on. Same store action and schema as the landing
 * page's panel.
 */
export function RegisterScreen() {
  const register = useAuthStore((s) => s.register);

  const [form, setForm] = useState({ email: '', username: '', fullName: '', password: '' });
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const usernameRef = useRef<TextInput>(null);
  const emailRef = useRef<TextInput>(null);
  const passwordRef = useRef<TextInput>(null);

  function update(field: keyof typeof form, value: string) {
    setForm((previous) => ({ ...previous, [field]: value }));
  }

  async function onSubmit() {
    if (submitting) return;
    const parsed = registerSchema.safeParse(form);
    if (!parsed.success) {
      setError(firstIssue(parsed.error));
      return;
    }

    setSubmitting(true);
    setError(null);
    try {
      await register(parsed.data);
      // A new account gets the five-step introduction once, ending in demo.
      continueAfterRegistration();
    } catch (err) {
      setError(err instanceof ApiRequestError ? err.message : 'Could not create the account');
      setSubmitting(false);
    }
  }

  return (
    <AuthShell
      title="Create account"
      subtitle="Start trading in a minute"
      footer={
        <Text className="text-sm text-content-400">
          Already registered?{' '}
          <Text
            accessibilityRole="link"
            className="text-sm font-medium text-accent"
            onPress={() => router.replace('/login')}
          >
            Sign in
          </Text>
        </Text>
      }
    >
      <View className="gap-4">
        <Field
          label="Full name"
          value={form.fullName}
          onChangeText={(value) => update('fullName', value)}
          autoCapitalize="words"
          autoComplete="name"
          textContentType="name"
          returnKeyType="next"
          submitBehavior="submit"
          onSubmitEditing={() => usernameRef.current?.focus()}
        />

        <Field
          ref={usernameRef}
          label="Username"
          value={form.username}
          onChangeText={(value) => update('username', value)}
          autoCapitalize="none"
          autoCorrect={false}
          autoComplete="nickname"
          textContentType="nickname"
          returnKeyType="next"
          submitBehavior="submit"
          onSubmitEditing={() => emailRef.current?.focus()}
        />

        <Field
          ref={emailRef}
          label="Email"
          value={form.email}
          onChangeText={(value) => update('email', value)}
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
          value={form.password}
          onChangeText={(value) => update('password', value)}
          secureTextEntry
          autoCapitalize="none"
          autoCorrect={false}
          autoComplete="new-password"
          textContentType="newPassword"
          passwordRules="minlength: 8; required: lower; required: upper; required: digit;"
          returnKeyType="go"
          onSubmitEditing={() => void onSubmit()}
          hint={
            <Text className="mt-1 text-[11px] text-content-400">
              At least 8 characters with upper case, lower case and a digit.
            </Text>
          }
        />

        {error ? (
          <View accessibilityRole="alert" className="rounded-lg border border-down/30 bg-down/10 px-3 py-2">
            <Text className="text-sm text-down">{error}</Text>
          </View>
        ) : null}

        <Button className="w-full" loading={submitting} onPress={() => void onSubmit()}>
          Create account
        </Button>
      </View>
    </AuthShell>
  );
}
