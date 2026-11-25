import { ActivityIndicator, StyleSheet, Text, View } from 'react-native';

import type { ApiClient } from '../../api/client';
import { Button } from '../../ui/button';
import { QueryRefreshStatus } from '../../ui/query-refresh-status';
import { colors, typography } from '../../ui/theme';
import { WorkspaceScreen } from '../../ui/workspace-screen';
import { MachineCard } from './machine-card';
import { useMachineQuery } from './use-machine-query';

async function listMachines(api: ApiClient, signal: AbortSignal) {
  return { machines: await api.listMachines(), signal };
}

export function MachineList({ onAdd, onSelect }: { onAdd: () => void; onSelect: (machineId: string) => void }) {
  const { query, refresh, retry } = useMachineQuery(listMachines, 'Could not load your machines.', { keepPreviousData: true });

  return (
    <WorkspaceScreen tab>
      <Text accessibilityLabel="Machine Whisperer" style={typography.brand}>
        <Text style={styles.mark}>◈  </Text>Machine Whisperer
      </Text>
      <Text accessibilityRole="header" style={typography.heading}>Your machines</Text>
      <QueryRefreshStatus refresh={refresh} resource="machines" retry={retry} />
      {query.state === 'loading' && <ActivityIndicator accessibilityLabel="Loading machines" color={colors.accent} />}
      {query.state === 'error' && (
        <View style={styles.card}>
          <Text accessibilityRole="alert" style={typography.error}>{query.message}</Text>
          <Button label="Try again" onPress={retry} outline />
        </View>
      )}
      {query.state === 'ready' && (
        query.data.machines.length === 0 ? <Text style={typography.body}>No machines yet.</Text> : (
          query.data.machines.map((machine) => (
            <MachineCard key={machine.id} machine={machine} onSelect={onSelect} signal={query.data.signal} />
          ))
        )
      )}
      <View style={styles.card}>
        <Text style={typography.body}>Create a machine, then attach its sensor.</Text>
        <Button label="Add machine" onPress={onAdd} outline />
      </View>
    </WorkspaceScreen>
  );
}

const styles = StyleSheet.create({
  mark: { color: colors.accent },
  card: {
    backgroundColor: colors.surface, borderColor: colors.outline, borderWidth: 1, borderRadius: 14, padding: 14, gap: 10,
  },
});
