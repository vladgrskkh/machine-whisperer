import { createContext, useContext, useState, type ReactNode } from 'react';

import { sessionRequired } from '../../api/errors';
import { useSession, useSessionStatus } from '../../auth/session-provider';

type MachineDeletions = {
  deletedIds: ReadonlySet<string>;
  deleteMachine: (id: string) => Promise<void>;
};

const MachineDeletionContext = createContext<MachineDeletions | null>(null);

function createScope(generation: number | undefined) {
  return { generation, requests: new Map<string, Promise<void>>(), deletedIds: new Set<string>() };
}

export function MachineDeletionProvider({ children }: { children: ReactNode }) {
  const { api, session } = useSession();
  const status = useSessionStatus();
  const generation = status === 'signedIn' ? session.getAccess()?.generation : undefined;
  const [scope, setScope] = useState(() => createScope(generation));
  const current = scope.generation === generation ? scope : createScope(generation);
  if (current !== scope) setScope(current);

  async function deleteMachine(id: string) {
    if (generation === undefined || session.getAccess()?.generation !== generation) throw sessionRequired();
    const existing = current.requests.get(id);
    if (existing) return existing;

    // Keep fulfilled deletions for this session so old snapshots cannot start another write.
    const request = api.deleteMachine(id).then(() => {
      setScope((latest) => latest.requests === current.requests
        ? { ...latest, deletedIds: new Set(latest.deletedIds).add(id) }
        : latest);
    }, (error: unknown) => {
      current.requests.delete(id);
      throw error;
    });
    current.requests.set(id, request);
    return request;
  }

  return (
    <MachineDeletionContext.Provider value={{ deletedIds: current.deletedIds, deleteMachine }}>
      {children}
    </MachineDeletionContext.Provider>
  );
}

export function useMachineDeletions() {
  const value = useContext(MachineDeletionContext);
  if (!value) throw new Error('MachineDeletionProvider is missing.');
  return value;
}
