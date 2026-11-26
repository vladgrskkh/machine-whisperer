import Constants from 'expo-constants';
import { Platform } from 'react-native';
import { createContext, useContext, useEffect, useState, useSyncExternalStore, type ReactNode } from 'react';

import { ApiClient } from '../api/client';
import { createTracing, tracingOrigin } from '../tracing/tracing';
import { secureTokenStore } from './secure-token-store';
import { Session } from './session';

const AuthContext = createContext<{ session: Session; api: ApiClient } | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [value] = useState(() => {
    const baseUrl = process.env.EXPO_PUBLIC_API_URL;
    const session = new Session(secureTokenStore, new ApiClient({ baseUrl }));
    const browserOrigin = Platform.OS === 'web' ? globalThis.location?.origin ?? '' : undefined;
    const origin = tracingOrigin(baseUrl, browserOrigin);
    const tracing = origin && process.env.EXPO_PUBLIC_TRACING !== '0'
      ? createTracing({ origin, getAccess: () => session.getAccess(), version: Constants.expoConfig?.version })
      : undefined;
    return { session, api: new ApiClient({ baseUrl, session, tracing }) };
  });

  useEffect(() => { void value.session.initialize(); }, [value]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useSession() {
  const value = useContext(AuthContext);
  if (!value) throw new Error('SessionProvider is missing.');
  return value;
}

export function useSessionStatus() {
  const { session } = useSession();
  return useSyncExternalStore(session.subscribe, session.getStatus, session.getStatus);
}
