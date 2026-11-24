import { useFocusEffect } from 'expo-router';
import { useCallback, useRef, useState } from 'react';

import type { ApiClient } from '../../api/client';
import { errorMessage } from '../../api/errors';
import { useSession } from '../../auth/session-provider';

type Query<T> = { state: 'loading' } | { state: 'ready'; data: T } | { state: 'error'; message: string };

export function useMachineQuery<T>(load: (api: ApiClient) => Promise<T>, fallback: string) {
  const { api } = useSession();
  const [query, setQuery] = useState<Query<T>>({ state: 'loading' });
  const reload = useRef<(() => void) | null>(null);

  useFocusEffect(useCallback(() => {
    let current = true;
    let pending = false;

    function runQuery() {
      if (pending) return;
      pending = true;
      setQuery({ state: 'loading' });
      void load(api).then(
        (data) => { if (current) setQuery({ state: 'ready', data }); },
        (error: unknown) => { if (current) setQuery({ state: 'error', message: errorMessage(error, fallback) }); },
      ).finally(() => { pending = false; });
    }

    reload.current = runQuery;
    runQuery();
    return () => {
      current = false;
      reload.current = null;
    };
  }, [api, fallback, load]));

  return { query, retry: () => reload.current?.() };
}
