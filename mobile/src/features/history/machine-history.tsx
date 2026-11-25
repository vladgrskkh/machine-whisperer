import { useCallback } from 'react';
import { ActivityIndicator, StyleSheet, Text, View } from 'react-native';

import type { ApiClient } from '../../api/client';
import type { Machine } from '../../api/types';
import { Button } from '../../ui/button';
import { QueryRefreshStatus } from '../../ui/query-refresh-status';
import { colors, typography } from '../../ui/theme';
import { WorkspaceCard } from '../../ui/workspace-card';
import { useMachineQuery } from '../machines/use-machine-query';
import { HistoryPanel } from './history-panel';

export function MachineHistory({ machine, onEditBaseline, onClaim }: {
  machine: Machine;
  onEditBaseline: () => void;
  onClaim: () => void;
}) {
  const { id } = machine;
  const loadHistory = useCallback((api: ApiClient, signal: AbortSignal) => api.getTelemetry(id, { signal }), [id]);
  const loadBaseline = useCallback((api: ApiClient, signal: AbortSignal) => api.getBaseline(id, signal), [id]);
  const loadAlerts = useCallback((api: ApiClient, signal: AbortSignal) => api.listAlerts(id, signal), [id]);
  const history = useMachineQuery(loadHistory, 'Could not load vibration history.');
  const baseline = useMachineQuery(loadBaseline, 'Could not load the baseline.');
  const alerts = useMachineQuery(loadAlerts, 'Could not load alert markers.');

  const readings = history.query.state === 'ready' ? history.query.data : null;
  const manual = baseline.query.state === 'ready' ? baseline.query.data : null;
  const events = alerts.query.state === 'ready' ? alerts.query.data : [];
  const active = events.some((alert) => alert.recovered_at === null);

  return (
    <>
      <View style={styles.headingRow}>
        <Text accessibilityRole="header" style={[typography.heading, styles.machineName]}>{machine.name}</Text>
        {active && <Text style={styles.badge}>Active alert</Text>}
      </View>
      {history.query.state === 'loading' && (
        <ActivityIndicator color={colors.accent} accessibilityLabel="Loading vibration history" />
      )}
      {history.query.state === 'error' && <QueryError message={history.query.message} retry={history.retry} />}
      <QueryRefreshStatus refresh={history.refresh} resource="vibration history" retry={history.retry} />
      {history.query.state === 'ready' && readings && (
        <HistoryPanel
          key={history.query.revision}
          id={id}
          initialPage={readings}
          baselineMean={manual?.mean_rms_g ?? null}
          alerts={events}
        />
      )}
      {alerts.query.state === 'error' && <QueryError message={alerts.query.message} retry={alerts.retry} />}
      <QueryRefreshStatus refresh={alerts.refresh} resource="alert markers" retry={alerts.retry} />
      <WorkspaceCard>
        <Text accessibilityRole="header" style={typography.brand}>Manual baseline</Text>
        {baseline.query.state === 'loading' && <ActivityIndicator color={colors.accent} accessibilityLabel="Loading baseline" />}
        {baseline.query.state === 'error' && <QueryError message={baseline.query.message} retry={baseline.retry} />}
        <QueryRefreshStatus refresh={baseline.refresh} resource="baseline" retry={baseline.retry} />
        {baseline.query.state === 'ready' && (
          <Text style={typography.body}>{manual ? `Mean ${manual.mean_rms_g} g · Std dev ${manual.stddev_rms_g} g` : 'No baseline set.'}</Text>
        )}
        <Button label={manual ? 'Edit baseline' : 'Set baseline'} onPress={onEditBaseline} outline />
      </WorkspaceCard>
      <WorkspaceCard>
        <Text accessibilityRole="header" style={typography.brand}>Sensor device</Text>
        {readings && (readings.items[0] ? (
          <>
            <Text style={typography.body}>Latest reporting sensor</Text>
            <Text selectable style={typography.body}>{readings.items[0].device_id}</Text>
          </>
        ) : <Text style={typography.body}>No sensor readings yet.</Text>)}
        <Button label="Scan device QR" onPress={onClaim} outline />
      </WorkspaceCard>
    </>
  );
}

function QueryError({ message, retry }: { message: string; retry: () => void }) {
  return (
    <>
      <Text accessibilityRole="alert" style={typography.error}>{message}</Text>
      <Button label="Try again" onPress={retry} outline />
    </>
  );
}

const styles = StyleSheet.create({
  headingRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', gap: 12 },
  machineName: { flex: 1 },
  badge: { backgroundColor: '#fbe9e3', color: colors.error, borderRadius: 12, paddingHorizontal: 9, paddingVertical: 4, fontWeight: '700' },
});
