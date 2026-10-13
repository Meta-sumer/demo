import { LoginScreen } from '../features/auth/LoginScreen';

/** Signing in on its own screen (web `/login`). The root gate moves a signed-in session into the app. */
export default function LoginRoute() {
  return <LoginScreen />;
}
