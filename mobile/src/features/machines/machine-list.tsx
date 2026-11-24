import { ActivityIndicator, Pressable, StyleSheet, Text, View } from 'react-native';

import type { ApiClient } from '../../api/client';
import { Button } from '../../ui/button';
import { colors, typography } from '../../ui/theme';
import { WorkspaceScreen } from '../../ui/workspace-screen';
import { useMachineQuery } from './use-machine-query';

const listMachines = (api: ApiClient) => api.listMachines();

export function MachineList({ onAdd, onSelect }: { onAdd: () => void; onSelect: (machineId: string) => void }) {
  const { query, retry } = useMachineQuery(listMachines, 'Could not load your machines.');

  return (
    <WorkspaceScreen tab>
      <Text accessibilityLabel="Machine Whisperer" style={typography.brand}>
        <Text style={styles.mark}>◈  </Text>Machine Whisperer
      </Text>
      <Text accessibilityRole="header" style={typography.heading}>Your machines</Text>
      {query.state === 'loading' && <ActivityIndicator accessibilityLabel="Loading machines" color={colors.accent} />}
      {query.state === 'error' && (
        <View style={styles.card}>
          <Text accessibilityRole="alert" style={typography.error}>{query.message}</Text>
          <Button label="Try again" onPress={retry} outline />
        </View>
      )}
      {query.state === 'ready' && (
        query.data.length === 0 ? <Text style={typography.body}>No machines yet.</Text> : (
          query.data.map((machine) => (
            <Pressable
              key={machine.id}
              accessibilityRole="button"
              accessibilityLabel={machine.name}
              accessibilityHint="Opens this machine"
              onPress={() => onSelect(machine.id)}
              style={({ pressed }) => [styles.card, styles.row, pressed && styles.pressed]}>
              <Text accessible={false} style={styles.icon}>◉</Text>
              <Text style={styles.name}>{machine.name}</Text>
              <Text accessible={false} style={typography.link}>›</Text>
            </Pressable>
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
  row: { flexDirection: 'row', alignItems: 'center' },
  pressed: { backgroundColor: colors.accentTint },
  icon: {
    width: 38, height: 38, lineHeight: 38, textAlign: 'center', borderRadius: 10, overflow: 'hidden',
    color: colors.accentInk, backgroundColor: colors.accentTint, fontSize: 17,
  },
  name: { flex: 1, color: colors.ink, fontSize: 16, fontWeight: '700' },
});
