import { useLocalSearchParams } from 'expo-router';

import { useSession } from '../../src/auth/session-provider';
import { AuthScreen } from '../../src/ui/auth-screen';

export default function SignIn() {
  const { session } = useSession();
  const { registered } = useLocalSearchParams<{ registered?: string }>();
  return (
    <AuthScreen mode="sign-in" accountCreated={registered === '1'}
      onSubmit={(credentials) => session.login(credentials)} />
  );
}
