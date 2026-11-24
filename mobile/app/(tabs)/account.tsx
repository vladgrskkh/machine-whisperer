import { useEffect, useState } from 'react';
import { ActivityIndicator, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { errorMessage } from '../../src/api/errors';
import type { User } from '../../src/api/types';
import { useSession } from '../../src/auth/session-provider';
import { Button } from '../../src/ui/button';
import { colors, typography } from '../../src/ui/theme';

type Profile = { state: 'loading' } | { state: 'ready'; user: User } | { state: 'error'; message: string };

export default function Account() {
  const { api, session } = useSession();
  const [profile, setProfile] = useState<Profile>({ state: 'loading' });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let active = true;
    void api.me().then((user) => {
      if (active) setProfile({ state: 'ready', user });
    }).catch((error: unknown) => {
      if (active) setProfile({ state: 'error', message: errorMessage(error, 'Could not load your account.') });
    });
    return () => { active = false; };
  }, [api, attempt]);

  return (
    <SafeAreaView style={styles.screen} edges={['top', 'left', 'right']}>
      <ScrollView contentContainerStyle={styles.content}>
        <Text accessibilityRole="header" style={typography.heading}>Account</Text>
        <View style={styles.profileCard}>
          {profile.state === 'loading' && <ActivityIndicator color={colors.accent} accessibilityLabel="Loading account" />}
          {profile.state === 'ready' && (
            <>
              <View accessible={false} style={styles.avatar}>
                <Text style={styles.avatarText}>{Array.from(profile.user.email)[0]?.toUpperCase()}</Text>
              </View>
              <Text selectable style={styles.email}>{profile.user.email}</Text>
              <Text style={typography.body}>Member since {new Date(profile.user.created_at).toLocaleDateString()}</Text>
            </>
          )}
          {profile.state === 'error' && (
            <>
              <Text accessibilityRole="alert" style={typography.error}>{profile.message}</Text>
              <Button label="Try again" outline onPress={() => {
                setProfile({ state: 'loading' });
                setAttempt((current) => current + 1);
              }} />
            </>
          )}
        </View>
        <Text style={typography.eyebrow}>Preferences</Text>
        <View style={styles.preferencesCard}>
          <View style={styles.preference}>
            <Text style={typography.label}>Units</Text>
            <Text style={typography.body}>Vibration measurements</Text>
          </View>
          <Text style={styles.unit}>g RMS</Text>
        </View>
        {/* The root route displays cleanup progress and any retry action. */}
        <Button label="Sign out" outline onPress={() => { void session.logout().catch(() => undefined); }} />
      </ScrollView>
    </SafeAreaView>
  );
}

const card = { backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.outline, borderRadius: 15, padding: 16 };

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: colors.background },
  content: { width: '100%', maxWidth: 460, alignSelf: 'center', padding: 20, gap: 14 },
  profileCard: { ...card, minHeight: 132, justifyContent: 'center', alignItems: 'center', gap: 8, marginBottom: 8 },
  preferencesCard: { ...card, flexDirection: 'row', alignItems: 'center', gap: 12 },
  preference: { flex: 1, gap: 2 },
  avatar: { width: 52, height: 52, borderRadius: 26, backgroundColor: colors.accentTint, alignItems: 'center', justifyContent: 'center' },
  avatarText: { color: colors.accentInk, fontWeight: '800', fontSize: 18 },
  email: { color: colors.ink, fontWeight: '800', fontSize: 16, textAlign: 'center' },
  unit: { color: colors.muted, backgroundColor: '#e7eeee', fontSize: 12, fontWeight: '700', paddingHorizontal: 10, paddingVertical: 5, borderRadius: 20 },
});
