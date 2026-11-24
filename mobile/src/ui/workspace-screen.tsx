import type { ReactNode } from 'react';
import { KeyboardAvoidingView, Platform, ScrollView, StyleSheet } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { colors } from './theme';

export function WorkspaceScreen({ children, tab = false }: { children: ReactNode; tab?: boolean }) {
  return (
    <SafeAreaView style={styles.screen} edges={tab ? ['top', 'left', 'right'] : undefined}>
      <KeyboardAvoidingView style={styles.screen} behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
        <ScrollView contentContainerStyle={styles.content} keyboardShouldPersistTaps="handled">
          {children}
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: colors.background },
  content: { width: '100%', maxWidth: 500, alignSelf: 'center', paddingHorizontal: 20, paddingVertical: 16, gap: 16 },
});
