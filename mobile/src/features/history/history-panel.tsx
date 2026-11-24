import { useRef, useState } from 'react';
import { StyleSheet, Text, View } from 'react-native';

import { errorMessage } from '../../api/errors';
import type { MachineAlert, TelemetryObservation, TelemetryPage } from '../../api/types';
import { useSession } from '../../auth/session-provider';
import { Button } from '../../ui/button';
import { typography } from '../../ui/theme';
import { WorkspaceCard } from '../../ui/workspace-card';
import { HistoryChart } from './history-chart';

const timeFormat = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });

export function HistoryPanel({ id, initialPage, baselineMean, alerts }: {
  id: string;
  initialPage: TelemetryPage;
  baselineMean: number | null;
  alerts: MachineAlert[];
}) {
  const { api } = useSession();
  const [history, setHistory] = useState(initialPage);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [loadError, setLoadError] = useState('');
  const pending = useRef(false);
  const latest = history.items[0];

  async function loadOlder() {
    const cursor = history.next_cursor;
    if (pending.current || cursor === null) return;

    pending.current = true;
    setLoadingOlder(true);
    setLoadError('');
    try {
      const older = await api.getTelemetry(id, { cursor });
      setHistory((previous) => ({
        items: [...previous.items, ...older.items],
        next_cursor: older.next_cursor,
      }));
    } catch (error) {
      setLoadError(errorMessage(error, 'Could not load older observations.'));
    } finally {
      pending.current = false;
      setLoadingOlder(false);
    }
  }

  return (
    <WorkspaceCard>
      <Text accessibilityRole="header" style={typography.eyebrow}>Vibration RMS</Text>
      {latest ? (
        <>
          <View style={styles.summary}>
            <Text style={typography.heading}>{latest.vibration_rms_g} g</Text>
            <Text style={typography.body}>Latest · {timeFormat.format(new Date(latest.observed_at))}</Text>
          </View>
          <HistoryChart observations={history.items} baselineMean={baselineMean} alerts={alerts} />
          <LatestMeasurements observation={latest} />
          <Text accessibilityLiveRegion="polite" style={typography.body}>
            {history.items.length} {history.items.length === 1 ? 'observation' : 'observations'} loaded
          </Text>
        </>
      ) : <Text style={typography.body}>No vibration observations yet.</Text>}
      {!!loadError && <Text accessibilityRole="alert" style={typography.error}>{loadError}</Text>}
      {history.next_cursor !== null && (
        <Button label={loadingOlder ? 'Loading older observations…' : 'Load older observations'} onPress={loadOlder} busy={loadingOlder} outline />
      )}
    </WorkspaceCard>
  );
}

function LatestMeasurements({ observation }: { observation: TelemetryObservation }) {
  return (
    <>
      {observation.dominant_frequency_hz != null && (
        <Text style={typography.body}>Dominant frequency {observation.dominant_frequency_hz} Hz</Text>
      )}
      {observation.current_rms_a != null && (
        <Text style={typography.body}>Current RMS {observation.current_rms_a} A</Text>
      )}
    </>
  );
}

const styles = StyleSheet.create({
  summary: { flexDirection: 'row', alignItems: 'baseline', justifyContent: 'space-between', flexWrap: 'wrap', gap: 6 },
});
