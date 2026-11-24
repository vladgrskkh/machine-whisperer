import { Redirect } from 'expo-router';

import { useSessionStatus } from '../src/auth/session-provider';

export default function Index() {
  return <Redirect href={useSessionStatus() === 'signedIn' ? '/account' : '/sign-in'} />;
}
