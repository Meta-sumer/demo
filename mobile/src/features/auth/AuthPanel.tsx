import { forwardRef, useCallback, useEffect, useRef, useState, type ReactNode, type RefObject } from 'react';
import {
  ActivityIndicator,
  BackHandler,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  TextInput,
  View,
  type TextInputProps,
} from 'react-native';
import Animated, { Easing, FadeInRight, FadeOut, useReducedMotion } from 'react-native-reanimated';
import { router, useFocusEffect } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import Svg, { Path } from 'react-native-svg';
import { Eye, EyeOff, X } from 'lucide-react-native';
import { Icon, Text } from '../../components/ui';
import { useAuthStore } from '../../stores/authStore';
import { ApiRequestError } from '../../services/http';
import { cn } from '../../utils/cn';
import { useTokenColor } from '../../utils/theme-colors';
import { firstIssue, loginSchema, registerSchema } from './schemas';
import { continueAfterRegistration } from './afterRegister';

export type AuthTab = 'login' | 'register';

/** The web's `panel-in`: a short slide from the right as it fades in. */
const PANEL_EASING = Easing.bezier(0.22, 0.9, 0.3, 1);
const PANEL_BACKGROUND = '#26282c';

/**
 * Sign in and sign up, as a panel over the landing page.
 *
 * A visitor deciding whether to open an account is reading the page; sending
 * them to a separate screen to type an email throws that away and makes coming
 * back their problem. The panel keeps the page behind it, so closing it
 * returns them to the paragraph they were on.
 *
 * It is drawn inside the landing screen rather than as a system modal, so the
 * Client Agreement and the Privacy Policy can be opened over it and the
 * visitor comes back to the form exactly as they left it.
 *
 * The standalone /login and /register screens still exist and are what a deep
 * link lands on. Both paths call the same store actions and validate with the
 * same schemas, so there is one way in with two doors rather than two
 * implementations.
 *
 * What is deliberately not here: buttons for Google or Facebook, a currency
 * picker, and a "forgot my password" link. The server has no OAuth, creates
 * every wallet in INR, and has no password-reset endpoint or mailer, so each
 * of those would be a control that looks live and does nothing.
 */
export function AuthPanel({
  tab,
  onTab,
  onClose,
}: {
  tab: AuthTab | null;
  onTab: (tab: AuthTab) => void;
  onClose: () => void;
}) {
  const open = tab !== null;
  const insets = useSafeAreaInsets();
  const reduced = useReducedMotion();

  // The system back gesture closes it, as Escape does on the web - but only
  // while this screen is in front, not while a document is open over it.
  useFocusEffect(
    useCallback(() => {
      if (!open) return;
      const subscription = BackHandler.addEventListener('hardwareBackPress', () => {
        onClose();
        return true;
      });
      return () => subscription.remove();
    }, [open, onClose]),
  );

  if (!open) return null;

  return (
    <Animated.View
      entering={reduced ? undefined : FadeInRight.duration(260).easing(PANEL_EASING)}
      exiting={reduced ? undefined : FadeOut.duration(150)}
      accessibilityViewIsModal
      accessibilityLabel={tab === 'login' ? 'Log in' : 'Create an account'}
      style={[StyleSheet.absoluteFill, { backgroundColor: PANEL_BACKGROUND }]}
    >
      <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined} style={{ flex: 1 }}>
        <View style={{ paddingTop: insets.top }}>
          <View className="relative">
            <Tabs tab={tab} onTab={onTab} />
            <Pressable
              onPress={onClose}
              accessibilityRole="button"
              accessibilityLabel="Close"
              hitSlop={6}
              className="absolute right-3 top-3 rounded-lg p-2 active:bg-white/10"
            >
              <Icon as={X} size={20} color="rgba(255,255,255,0.4)" />
            </Pressable>
          </View>
        </View>

        <ScrollView
          className="flex-1"
          keyboardShouldPersistTaps="handled"
          contentContainerClassName="px-7 pt-8"
          contentContainerStyle={{ paddingBottom: insets.bottom + 40 }}
        >
          {tab === 'login' ? <LoginForm key="login" onTab={onTab} /> : <RegisterForm key="register" onTab={onTab} />}
        </ScrollView>
      </KeyboardAvoidingView>
    </Animated.View>
  );
}

/**
 * The two ways in, as a pair of underlined tabs.
 *
 * The underline sits on the container's bottom border rather than on the
 * button, so the inactive tab still shows the rule running past it and the
 * active one reads as the sheet in front.
 */
function Tabs({ tab, onTab }: { tab: AuthTab; onTab: (tab: AuthTab) => void }) {
  return (
    <View accessibilityRole="tablist" className="flex-row border-b border-white/10">
      {(['register', 'login'] as const).map((value) => {
        const active = tab === value;
        return (
          <Pressable
            key={value}
            onPress={() => onTab(value)}
            accessibilityRole="tab"
            accessibilityState={{ selected: active }}
            className="relative flex-1 items-center py-5"
          >
            <Text className={cn('text-lg font-medium', active ? 'text-white' : 'text-white/45')}>
              {value === 'register' ? 'Registration' : 'Login'}
            </Text>
            {active ? <View className="absolute inset-x-0 -bottom-px h-0.5 bg-white" /> : null}
          </Pressable>
        );
      })}
    </View>
  );
}

/* ---------------------------------------------------------------- forms -- */

/** The first field is where the visitor is going next, so put them there. */
function useFocusFirst(ref: RefObject<TextInput | null>) {
  useEffect(() => {
    const timer = setTimeout(() => ref.current?.focus(), 220);
    return () => clearTimeout(timer);
  }, [ref]);
}

function LoginForm({ onTab }: { onTab: (tab: AuthTab) => void }) {
  const login = useAuthStore((s) => s.login);

  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const emailRef = useRef<TextInput>(null);
  const passwordRef = useRef<TextInput>(null);
  useFocusFirst(emailRef);

  async function onSubmit() {
    if (submitting) return;
    const parsed = loginSchema.safeParse({ email, password });
    if (!parsed.success) return setError(firstIssue(parsed.error));

    setSubmitting(true);
    setError(null);
    try {
      await login(parsed.data.email, parsed.data.password);
      // Nothing to navigate: the root gate moves a signed-in session into the
      // app, and this panel goes with the landing page.
    } catch (err) {
      setError(err instanceof ApiRequestError ? err.message : 'Could not sign in');
      setSubmitting(false);
    }
  }

  return (
    <View className="gap-4">
      <PanelField
        ref={emailRef}
        placeholder="Email"
        value={email}
        onChangeText={setEmail}
        keyboardType="email-address"
        autoCapitalize="none"
        autoCorrect={false}
        autoComplete="email"
        textContentType="username"
        returnKeyType="next"
        submitBehavior="submit"
        onSubmitEditing={() => passwordRef.current?.focus()}
      />
      <PanelField
        ref={passwordRef}
        secure
        placeholder="Password"
        value={password}
        onChangeText={setPassword}
        autoCapitalize="none"
        autoCorrect={false}
        autoComplete="current-password"
        textContentType="password"
        returnKeyType="go"
        onSubmitEditing={() => void onSubmit()}
      />

      <FormError message={error} />

      <SubmitButton submitting={submitting} onPress={() => void onSubmit()}>
        Log in
      </SubmitButton>

      <Switcher prompt="No account?" action="Register" onPress={() => onTab('register')} />
    </View>
  );
}

function RegisterForm({ onTab }: { onTab: (tab: AuthTab) => void }) {
  const register = useAuthStore((s) => s.register);

  const [form, setForm] = useState({ email: '', username: '', fullName: '', password: '' });
  const [accepted, setAccepted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const emailRef = useRef<TextInput>(null);
  const fullNameRef = useRef<TextInput>(null);
  const usernameRef = useRef<TextInput>(null);
  const passwordRef = useRef<TextInput>(null);
  useFocusFirst(emailRef);

  const update = (field: keyof typeof form) => (value: string) =>
    setForm((previous) => ({ ...previous, [field]: value }));

  async function onSubmit() {
    if (submitting) return;
    if (!accepted) return setError('Please accept the terms to continue');

    const parsed = registerSchema.safeParse(form);
    if (!parsed.success) return setError(firstIssue(parsed.error));

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
    <View className="gap-4">
      <PanelField
        ref={emailRef}
        placeholder="Email"
        value={form.email}
        onChangeText={update('email')}
        keyboardType="email-address"
        autoCapitalize="none"
        autoCorrect={false}
        autoComplete="email"
        textContentType="username"
        returnKeyType="next"
        submitBehavior="submit"
        onSubmitEditing={() => fullNameRef.current?.focus()}
      />
      <PanelField
        ref={fullNameRef}
        placeholder="Full name"
        value={form.fullName}
        onChangeText={update('fullName')}
        autoCapitalize="words"
        autoComplete="name"
        textContentType="name"
        returnKeyType="next"
        submitBehavior="submit"
        onSubmitEditing={() => usernameRef.current?.focus()}
      />
      <PanelField
        ref={usernameRef}
        placeholder="Username"
        value={form.username}
        onChangeText={update('username')}
        autoCapitalize="none"
        autoCorrect={false}
        autoComplete="nickname"
        textContentType="nickname"
        returnKeyType="next"
        submitBehavior="submit"
        onSubmitEditing={() => passwordRef.current?.focus()}
        hint="3-24 characters. Letters, digits and underscores."
      />
      <PanelField
        ref={passwordRef}
        secure
        placeholder="Password"
        value={form.password}
        onChangeText={update('password')}
        autoCapitalize="none"
        autoCorrect={false}
        autoComplete="new-password"
        textContentType="newPassword"
        passwordRules="minlength: 8; required: lower; required: upper; required: digit;"
        returnKeyType="done"
        hint="At least 8 characters, with an uppercase letter, a lowercase letter and a digit. Do not reuse a password from anywhere else."
      />

      <Consent accepted={accepted} onChange={setAccepted} />

      <FormError message={error} />

      <SubmitButton submitting={submitting} onPress={() => void onSubmit()}>
        Register
      </SubmitButton>

      <Switcher prompt="Have an account?" action="Log in" onPress={() => onTab('login')} />
    </View>
  );
}

/* ----------------------------------------------------------------- bits -- */

interface PanelFieldProps extends Omit<TextInputProps, 'secureTextEntry'> {
  placeholder: string;
  hint?: string;
  /** A password: hidden, with a control to show it. */
  secure?: boolean;
}

/**
 * One field.
 *
 * The label is the placeholder rather than a caption above the box: with five
 * of these in a panel, captions cost a line each and say nothing the
 * placeholder does not. The input still says what it holds - its keyboard,
 * autofill and content type - so a password manager and the keyboard know
 * what they are looking at.
 */
const PanelField = forwardRef<TextInput, PanelFieldProps>(function PanelField(
  { placeholder, hint, secure = false, onFocus, onBlur, ...props },
  ref,
) {
  const [revealed, setRevealed] = useState(false);
  const [focused, setFocused] = useState(false);
  const selection = useTokenColor('brand');

  return (
    <View>
      <View
        className={cn(
          'flex-row items-center rounded-xl border bg-white/[0.06]',
          focused ? 'border-brand/70' : 'border-white/15',
        )}
      >
        <TextInput
          ref={ref}
          placeholder={placeholder}
          accessibilityLabel={placeholder}
          placeholderTextColor="rgba(255,255,255,0.4)"
          selectionColor={selection}
          secureTextEntry={secure && !revealed}
          className={cn('flex-1 px-4 py-4 text-[15px] text-white', secure && 'pr-1')}
          onFocus={(event) => {
            setFocused(true);
            onFocus?.(event);
          }}
          onBlur={(event) => {
            setFocused(false);
            onBlur?.(event);
          }}
          {...props}
        />
        {secure ? (
          <Pressable
            onPress={() => setRevealed((r) => !r)}
            accessibilityRole="button"
            accessibilityLabel={revealed ? 'Hide password' : 'Show password'}
            hitSlop={8}
            className="mr-2 rounded-md p-1.5"
          >
            <Icon as={revealed ? EyeOff : Eye} size={16} color="rgba(255,255,255,0.4)" />
          </Pressable>
        ) : null}
      </View>
      {hint ? <Text className="mt-2 text-[13px] leading-[18px] text-white/40">{hint}</Text> : null}
    </View>
  );
});

/**
 * The terms checkbox.
 *
 * A real gate rather than decoration: the button below refuses while it is
 * clear, and the refusal says so. The two documents it points at are screens
 * in this app, opened over the panel, so a visitor can read them before
 * agreeing and come back to the form as they left it.
 */
function Consent({ accepted, onChange }: { accepted: boolean; onChange: (value: boolean) => void }) {
  const sentence = 'text-[15px] leading-[21px] text-white/80';
  return (
    <View className="flex-row items-start gap-3 pt-1">
      <Pressable
        onPress={() => onChange(!accepted)}
        accessibilityRole="checkbox"
        accessibilityState={{ checked: accepted }}
        accessibilityLabel="I accept the terms of the Client Agreement and Privacy Policy and confirm being adult"
        hitSlop={8}
        className={cn(
          'mt-0.5 h-6 w-6 shrink-0 items-center justify-center rounded-md border-2',
          accepted ? 'border-brand bg-brand' : 'border-brand/70',
        )}
      >
        {accepted ? (
          <Svg viewBox="0 0 16 16" width={16} height={16} fill="none" stroke={PANEL_BACKGROUND} strokeWidth={2.5}>
            <Path d="M3 8.5 6.5 12 13 4.5" strokeLinecap="round" strokeLinejoin="round" />
          </Svg>
        ) : null}
      </Pressable>
      <Text className={cn('flex-1', sentence)} onPress={() => onChange(!accepted)}>
        I accept the terms of the{' '}
        <Text accessibilityRole="link" className={cn(sentence, 'underline')} onPress={() => router.push('/terms')}>
          Client Agreement
        </Text>{' '}
        and{' '}
        <Text accessibilityRole="link" className={cn(sentence, 'underline')} onPress={() => router.push('/privacy')}>
          Privacy Policy
        </Text>{' '}
        and confirm being adult
      </Text>
    </View>
  );
}

function SubmitButton({
  submitting,
  onPress,
  children,
}: {
  submitting: boolean;
  onPress: () => void;
  children: ReactNode;
}) {
  return (
    <Pressable
      onPress={onPress}
      disabled={submitting}
      accessibilityRole="button"
      accessibilityState={{ disabled: submitting, busy: submitting }}
      className={cn(
        'mt-1 w-full flex-row items-center justify-center gap-2 rounded-xl bg-brand py-4 active:opacity-90',
        submitting && 'opacity-50',
      )}
    >
      {submitting ? <ActivityIndicator size="small" color={PANEL_BACKGROUND} /> : null}
      <Text className="text-[17px] font-bold text-[#26282c]">{children}</Text>
    </Pressable>
  );
}

function Switcher({ prompt, action, onPress }: { prompt: string; action: string; onPress: () => void }) {
  return (
    <View className="flex-row items-center justify-center gap-3 pt-4">
      <Text className="text-[15px] text-white/45">{prompt}</Text>
      <Pressable
        onPress={onPress}
        accessibilityRole="button"
        className="rounded-lg bg-white/10 px-4 py-2.5 active:bg-white/15"
      >
        <Text className="text-[15px] font-bold text-white">{action}</Text>
      </Pressable>
    </View>
  );
}

function FormError({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <View
      accessibilityRole="alert"
      accessibilityLiveRegion="polite"
      className="rounded-xl border border-down/30 bg-down/15 px-4 py-3"
    >
      <Text className="text-sm text-down">{message}</Text>
    </View>
  );
}
