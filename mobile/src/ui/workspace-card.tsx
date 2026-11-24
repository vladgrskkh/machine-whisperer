import type { ReactNode } from 'react';
import { StyleSheet, View } from 'react-native';

import { colors } from './theme';

export function WorkspaceCard({ children }: { children: ReactNode }) {
  return <View style={styles.card}>{children}</View>;
}

const styles = StyleSheet.create({
  card: {
    backgroundColor: colors.surface,
    borderColor: colors.outline,
    borderWidth: 1,
    borderRadius: 14,
    padding: 14,
    gap: 10,
  },
});
