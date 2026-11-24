import { Link } from 'expo-router';
import { KeyboardAvoidingView, Platform, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import type { Credentials } from '../api/types';
import { CredentialsForm } from './credentials-form';
import { colors, typography } from './theme';

export function AuthScreen({ mode, onSubmit, accountCreated = false }: {
  mode: 'sign-up' | 'sign-in';
  onSubmit: (credentials: Credentials) => Promise<void>;
  accountCreated?: boolean;
}) {
  const signup = mode === 'sign-up';
  return (
    <SafeAreaView style={styles.screen}>
      <KeyboardAvoidingView style={styles.screen} behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
        <ScrollView keyboardShouldPersistTaps="handled" contentContainerStyle={styles.scroll}>
          <View style={styles.header}>
            {signup && <Link replace href="/sign-in" style={typography.link}>‹  Sign in</Link>}
            <Text accessibilityLabel="Machine Whisperer" style={typography.brand}>
              <Text style={styles.mark}>◈  </Text>{signup ? 'MW' : 'Machine Whisperer'}
            </Text>
          </View>
          <View style={[styles.content, !signup && styles.center]}>
            <Text style={typography.eyebrow}>{signup ? 'Get started' : 'Welcome back'}</Text>
            <Text accessibilityRole="header" style={typography.heading}>
              {signup ? 'Create your account' : 'Sign in'}
            </Text>
            <Text style={typography.body}>
              {signup ? 'Set up your workspace to add a machine.' : 'Sign in to see your machines and alerts.'}
            </Text>
            {accountCreated && (
              <Text accessibilityRole="alert" style={[typography.body, styles.notice]}>
                Account created. Sign in to continue.
              </Text>
            )}
            <CredentialsForm mode={mode} onSubmit={onSubmit} />
            <Text style={[typography.body, styles.switchMode]}>
              {signup ? 'Already have an account? ' : 'New here? '}
              <Link replace={signup} href={signup ? '/sign-in' : '/sign-up'} style={typography.link}>
                {signup ? 'Sign in' : 'Create account'}
              </Link>
            </Text>
            {signup && (
              <Text style={[typography.body, styles.notice]}>
                Your account only sees machines you create or claim.
              </Text>
            )}
          </View>
          {!signup && (
            <Text style={[typography.body, styles.footer]}>
              Your equipment data stays private to your account.
            </Text>
          )}
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: colors.background },
  scroll: { flexGrow: 1, paddingHorizontal: 20, width: '100%', maxWidth: 460, alignSelf: 'center' },
  header: { minHeight: 58, flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: 12 },
  mark: { color: colors.accent },
  content: { flex: 1, paddingVertical: 20, gap: 5 },
  center: { justifyContent: 'center' },
  switchMode: { marginTop: 12, textAlign: 'center' },
  notice: { marginTop: 10, backgroundColor: colors.accentTint, borderRadius: 11, padding: 12 },
  footer: { paddingVertical: 16, textAlign: 'center', fontSize: 12 },
});
