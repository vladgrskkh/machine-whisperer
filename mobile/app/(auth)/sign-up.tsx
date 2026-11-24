import { useRouter } from 'expo-router';

import { useSession } from '../../src/auth/session-provider';
import { AuthScreen } from '../../src/ui/auth-screen';

export default function SignUp() {
  const { api } = useSession();
  const router = useRouter();
  return (
    <AuthScreen mode="sign-up" onSubmit={async (credentials) => {
      await api.signup(credentials);
      router.replace({ pathname: '/sign-in', params: { registered: '1' } });
    }} />
  );
}
