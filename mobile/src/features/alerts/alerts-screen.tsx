import { useCallback, useEffect, useRef, useState } from 'react';
import { ActivityIndicator, StyleSheet, Text, View } from 'react-native';

import type { ApiClient } from '../../api/client';
import { errorMessage } from '../../api/errors';
import { useSession } from '../../auth/session-provider';
import { Button } from '../../ui/button';
import { QueryRefreshStatus } from '../../ui/query-refresh-status';
import { colors, typography } from '../../ui/theme';
import { WorkspaceCard } from '../../ui/workspace-card';
import { WorkspaceScreen } from '../../ui/workspace-screen';
import { useMachineDeletions } from '../machines/machine-deletion-provider';
import { useMachineQuery } from '../machines/use-machine-query';
import { loadMachineAlerts, type NamedAlert } from './load-machine-alerts';

const dateFormat = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
const scoreFormat = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 2 });

type Acknowledgement = { state: 'pending' | 'seen' } | { state: 'error'; message: string };

export function AlertsScreen() {
  const { api } = useSession();
  const { deletedIds } = useMachineDeletions();
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const [updates, setUpdates] = useState<Record<string, Acknowledgement>>({});
  const pending = useRef(new Set<string>());
  const loading = useRef<ReturnType<typeof loadMachineAlerts> | null>(null);
  const load = useCallback((api: ApiClient) => {
    // Returning to the tab while it is loading shares the existing request pool.
    if (!controller.current || controller.current.signal.aborted) controller.current = new AbortController();
    loading.current ??= loadMachineAlerts(api, controller.current.signal).finally(() => { loading.current = null; });
    return loading.current;
  }, []);
  const { query, refresh, retry } = useMachineQuery(load, 'Could not load machine alerts.', { keepPreviousData: true });
  const alerts = query.state === 'ready' ? query.data.alerts.filter(({ machine }) => !deletedIds.has(machine.id)) : [];

  async function acknowledge(id: string) {
    if (pending.current.has(id)) return;
    pending.current.add(id);
    setUpdates((previous) => ({ ...previous, [id]: { state: 'pending' } }));
    try {
      await api.acknowledgeAlert(id);
      setUpdates((previous) => ({ ...previous, [id]: { state: 'seen' } }));
    } catch (error) {
      const message = errorMessage(error, 'Could not acknowledge the alert.');
      setUpdates((previous) => ({ ...previous, [id]: { state: 'error', message } }));
    } finally {
      pending.current.delete(id);
    }
  }

  return (
    <WorkspaceScreen tab>
      <Text accessibilityRole="header" style={typography.heading}>Machine alerts</Text>
      <QueryRefreshStatus refresh={refresh} resource="alerts" retry={retry} />
      {query.state === 'loading' && <ActivityIndicator color={colors.accent} accessibilityLabel="Loading alerts" />}
      {query.state === 'error' && (
        <>
          <Text accessibilityRole="alert" style={typography.error}>{query.message}</Text>
          <Button label="Try again" onPress={retry} outline />
        </>
      )}
      {query.state === 'ready' && (
        <>
          {query.data.failures > 0 && (
            <>
              <Text accessibilityRole="alert" style={typography.error}>
                Could not load alerts for {query.data.failures} {query.data.failures === 1 ? 'machine' : 'machines'}.
              </Text>
              {!refresh && <Button label="Try again" onPress={retry} outline />}
            </>
          )}
          <AlertGroup
            title="Active alerts"
            entries={alerts.filter(({ alert }) => alert.recovered_at === null)}
            complete={query.data.failures === 0}
            updates={updates}
            onAcknowledge={acknowledge}
          />
          <AlertGroup
            title="Recovered alerts"
            entries={alerts.filter(({ alert }) => alert.recovered_at !== null)}
            complete={query.data.failures === 0}
            updates={updates}
            onAcknowledge={acknowledge}
          />
        </>
      )}
    </WorkspaceScreen>
  );
}

function AlertGroup({ title, entries, complete, updates, onAcknowledge }: {
  title: string;
  entries: NamedAlert[];
  complete: boolean;
  updates: Record<string, Acknowledgement>;
  onAcknowledge: (id: string) => void;
}) {
  return (
    <View accessibilityLabel={title} style={styles.group}>
      <Text accessibilityRole="header" style={typography.brand}>{title} · {entries.length}</Text>
      {entries.map((entry) => (
        <AlertCard key={entry.alert.id} {...entry} update={updates[entry.alert.id]} onAcknowledge={onAcknowledge} />
      ))}
      {entries.length === 0 && complete && <Text style={typography.body}>No {title.toLowerCase()}.</Text>}
    </View>
  );
}

function AlertCard({ machine, alert, update, onAcknowledge }: NamedAlert & {
  update?: Acknowledgement;
  onAcknowledge: (id: string) => void;
}) {
  const seen = alert.acknowledged_at !== null || update?.state === 'seen';
  const busy = update?.state === 'pending';
  const recovered = alert.recovered_at !== null;

  return (
    <WorkspaceCard>
      <View style={styles.statusRow}>
        <Text style={[typography.label, styles.machineName]}>{machine.name}</Text>
        <Text style={[styles.badge, recovered ? styles.recovered : styles.active]}>{recovered ? 'Recovered' : 'Active'}</Text>
      </View>
      {!recovered && <Text style={styles.deviation}>Sustained deviation</Text>}
      <Text style={typography.body}>Opened {dateFormat.format(new Date(alert.triggered_at))}</Text>
      {alert.recovered_at !== null && <Text style={typography.body}>Recovered {dateFormat.format(new Date(alert.recovered_at))}</Text>}
      {!recovered && (
        <View style={styles.statusRow}>
          <Text style={typography.body}>Current deviation</Text>
          <Text style={typography.brand}>{scoreFormat.format(alert.last_z_score)} σ</Text>
        </View>
      )}
      <Text style={typography.body}>
        Baseline {alert.trigger_mean_rms_g} ± {alert.trigger_stddev_rms_g} g · Peak {scoreFormat.format(alert.peak_z_score)} σ
      </Text>
      {!seen && update?.state === 'error' && (
        <Text accessibilityRole="alert" style={typography.error}>{update.message}</Text>
      )}
      {seen
        ? <Text accessibilityLiveRegion="polite" style={typography.label}>Seen</Text>
        : <Button label={busy ? 'Acknowledging…' : 'Acknowledge alert'}
            accessibilityLabel={`Acknowledge alert for ${machine.name}`} onPress={() => onAcknowledge(alert.id)} busy={busy} outline />}
    </WorkspaceCard>
  );
}

const styles = StyleSheet.create({
  group: { gap: 10 },
  statusRow: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: 6 },
  machineName: { flex: 1, fontSize: 16 },
  badge: { borderRadius: 12, paddingVertical: 4, paddingHorizontal: 9, fontWeight: '700' },
  active: { backgroundColor: '#fbe9e3', color: colors.error },
  recovered: { backgroundColor: '#e6f2eb', color: '#286246' },
  deviation: { color: colors.error, fontWeight: '700' },
});
