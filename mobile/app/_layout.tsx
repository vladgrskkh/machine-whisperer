import { Stack } from 'expo-router';
import { StatusBar } from 'expo-status-bar';
import { ActivityIndicator, StyleSheet, View } from 'react-native';

import { SessionProvider, useSessionStatus } from '../src/auth/session-provider';
import { colors } from '../src/ui/theme';

function AppNavigation() {
  const status = useSessionStatus();
  if (status === 'loading') {
    return <View style={styles.loading}><ActivityIndicator color={colors.accent} accessibilityLabel="Opening session" /></View>;
  }

  return (
    <Stack screenOptions={{ headerShown: false, contentStyle: { backgroundColor: colors.background } }}>
      <Stack.Screen name="index" />
      <Stack.Protected guard={status === 'signedOut'}>
        <Stack.Screen name="(auth)" />
      </Stack.Protected>
      <Stack.Protected guard={status === 'signedIn'}>
        <Stack.Screen name="(tabs)" />
        <Stack.Screen name="machines/new" />
        <Stack.Screen name="machines/[id]/index" />
        <Stack.Screen name="machines/[id]/baseline" />
        <Stack.Screen name="machines/[id]/claim-device" />
      </Stack.Protected>
    </Stack>
  );
}

export default function RootLayout() {
  return (
    <SessionProvider>
      <StatusBar style="dark" />
      <AppNavigation />
    </SessionProvider>
  );
}

const styles = StyleSheet.create({
  loading: { flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background },
});
