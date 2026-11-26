import { useFocusEffect } from 'expo-router';
import { useCallback, useRef, useState } from 'react';

import type { ApiClient } from '../../api/client';
import { ApiError, errorMessage } from '../../api/errors';
import { useSession } from '../../auth/session-provider';

type Query<T> =
  | { state: 'loading' }
  | { state: 'ready'; data: T; revision: number; refresh?: 'refreshing' | { message: string } }
  | { state: 'error'; message: string };

export function useMachineQuery<T>(
  load: (api: ApiClient, signal: AbortSignal) => Promise<T>,
  fallback: string,
  { keepPreviousData = false }: { keepPreviousData?: boolean } = {},
) {
  const { api } = useSession();
  const [query, setQuery] = useState<Query<T>>({ state: 'loading' });
  const reload = useRef<(() => void) | null>(null);

  useFocusEffect(useCallback(() => {
    const controller = new AbortController();
    const { signal } = controller;
    let pending = false;

    function runQuery() {
      if (pending) return;
      pending = true;
      setQuery((previous) => keepPreviousData && previous.state === 'ready'
        ? { ...previous, refresh: 'refreshing' }
        : { state: 'loading' });
      void load(api, signal).then(
        (data) => {
          if (signal.aborted) return;
          setQuery((previous) => ({
            state: 'ready', data, revision: previous.state === 'ready' ? previous.revision + 1 : 0,
          }));
        },
        (error: unknown) => {
          if (signal.aborted) return;
          const message = errorMessage(error, fallback);
          const missingMachine = error instanceof ApiError && error.status === 404 && error.code === 'machine_not_found';
          setQuery((previous) => !missingMachine && previous.state === 'ready'
            ? { ...previous, refresh: { message } }
            : { state: 'error', message });
        },
      ).finally(() => { pending = false; });
    }

    reload.current = runQuery;
    runQuery();
    return () => {
      controller.abort();
      reload.current = null;
    };
  }, [api, fallback, keepPreviousData, load]));

  return {
    query,
    refresh: query.state === 'ready' ? query.refresh : undefined,
    retry: () => reload.current?.(),
  };
}
