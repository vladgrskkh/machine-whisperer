import { Redirect } from 'expo-router';
import { ActivityIndicator, StyleSheet, Text } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { useSession, useSessionStatus } from '../src/auth/session-provider';
import { Button } from '../src/ui/button';
import { colors, typography } from '../src/ui/theme';

export default function Index() {
  const { session } = useSession();
  const status = useSessionStatus();

  if (status === 'clearing' || status === 'cleanupFailed') {
    return (
      <SafeAreaView style={styles.cleanup}>
        <Text accessibilityRole="header" style={typography.heading}>Clear session</Text>
        {status === 'clearing' ? (
          <ActivityIndicator accessibilityLabel="Clearing session" color={colors.accent} />
        ) : (
          <>
            <Text accessibilityRole="alert" style={typography.error}>
              Could not remove the session from this device.
            </Text>
            <Button label="Try again" onPress={() => { void session.logout().catch(() => undefined); }} />
          </>
        )}
      </SafeAreaView>
    );
  }

  return <Redirect href={status === 'signedIn' ? '/machines' : '/sign-in'} />;
}

const styles = StyleSheet.create({
  cleanup: {
    flex: 1,
    width: '100%',
    maxWidth: 460,
    alignSelf: 'center',
    justifyContent: 'center',
    padding: 20,
    gap: 14,
  },
});
