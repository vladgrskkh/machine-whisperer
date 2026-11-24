import { useEffect, useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';

import type { Machine } from '../../api/types';
import { useSession } from '../../auth/session-provider';
import { colors, typography } from '../../ui/theme';

const observedTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });

type Summary = { vibration: string; observed: string; baseline: string; alert: string; active: boolean };

export function MachineCard({ machine, onSelect, signal }: {
  machine: Machine;
  onSelect: (machineId: string) => void;
  signal: AbortSignal;
}) {
  const { api } = useSession();
  const [summary, setSummary] = useState<Summary | null>(null);

  useEffect(() => {
    let current = true;
    void Promise.allSettled([
      api.getTelemetry(machine.id, { limit: 1, signal }),
      api.getBaseline(machine.id, signal),
      api.listAlerts(machine.id, signal),
    ]).then(([history, baseline, alerts]) => {
      if (!current || signal.aborted) return;
      const latest = history.status === 'fulfilled' ? history.value.items[0] : undefined;
      const active = alerts.status === 'fulfilled' && alerts.value.some((alert) => alert.recovered_at === null);

      setSummary({
        vibration: latest ? `${latest.vibration_rms_g} g RMS`
          : history.status === 'fulfilled' ? 'No sensor readings yet' : 'Vibration history unavailable',
        observed: latest ? `Observed ${observedTime.format(new Date(latest.observed_at))}` : '',
        baseline: baseline.status === 'fulfilled'
          ? (baseline.value ? 'Baseline set' : 'No baseline set') : 'Baseline status unavailable',
        alert: alerts.status === 'fulfilled'
          ? (active ? 'Active alert' : 'No active alerts') : 'Alert status unavailable',
        active,
      });
    });
    return () => { current = false; };
  }, [api, machine.id, signal]);

  const accessibleSummary = summary
    ? [summary.alert, summary.vibration, summary.observed, summary.baseline].filter(Boolean).join('. ')
    : 'Loading machine status';

  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={machine.name}
      accessibilityValue={{ text: accessibleSummary }}
      accessibilityHint="Opens this machine"
      onPress={() => onSelect(machine.id)}
      style={({ pressed }) => [styles.card, pressed && styles.pressed]}>
      <View style={styles.row}>
        <Text accessible={false} style={styles.icon}>◉</Text>
        <Text style={styles.name}>{machine.name}</Text>
        <Text accessible={false} style={typography.link}>›</Text>
      </View>
      {summary && (
        <>
          <View style={styles.row}>
            <Text style={[typography.label, styles.reading]}>{summary.vibration}</Text>
            <Text style={[styles.badge, summary.active && styles.active]}>{summary.alert}</Text>
          </View>
          {!!summary.observed && <Text style={typography.body}>{summary.observed}</Text>}
          <Text style={typography.body}>{summary.baseline}</Text>
        </>
      )}
    </Pressable>
  );
}

const styles = StyleSheet.create({
  card: { backgroundColor: colors.surface, borderColor: colors.outline, borderWidth: 1, borderRadius: 14, padding: 14, gap: 8 },
  row: { flexDirection: 'row', alignItems: 'center', gap: 8 },
  pressed: { backgroundColor: colors.accentTint },
  icon: {
    width: 38, height: 38, lineHeight: 38, textAlign: 'center', borderRadius: 10, overflow: 'hidden',
    color: colors.accentInk, backgroundColor: colors.accentTint, fontSize: 17,
  },
  name: { flex: 1, color: colors.ink, fontSize: 16, fontWeight: '700' },
  reading: { flex: 1 },
  badge: {
    backgroundColor: colors.accentTint, color: colors.accentInk, borderRadius: 12,
    paddingHorizontal: 9, paddingVertical: 4, fontSize: 12, fontWeight: '700',
  },
  active: { backgroundColor: '#fbe9e3', color: colors.error },
});
