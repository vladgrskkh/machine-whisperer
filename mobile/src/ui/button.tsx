import { Pressable, StyleSheet, Text } from 'react-native';

import { colors } from './theme';

export function Button({ label, accessibilityLabel, onPress, busy = false, outline = false }: {
  label: string;
  accessibilityLabel?: string;
  onPress: () => void;
  busy?: boolean;
  outline?: boolean;
}) {
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={accessibilityLabel}
      accessibilityState={{ busy, disabled: busy }}
      disabled={busy}
      onPress={onPress}
      style={({ pressed }) => [styles.button, outline && styles.outline, (pressed || busy) && styles.dimmed]}>
      <Text style={[styles.label, outline && styles.outlineLabel]}>{label}</Text>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  button: {
    minHeight: 48,
    paddingHorizontal: 16,
    paddingVertical: 12,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: colors.accent,
    borderRadius: 11,
  },
  outline: { backgroundColor: colors.surface, borderWidth: 1, borderColor: '#b6d6d2' },
  dimmed: { opacity: 0.72 },
  label: { fontSize: 15, fontWeight: '700', color: colors.surface },
  outlineLabel: { color: colors.accentInk },
});
