import { useRef, useState } from 'react';
import { StyleSheet, Text, TextInput, View } from 'react-native';

import { errorMessage } from '../api/errors';
import type { Credentials } from '../api/types';
import { Button } from './button';
import { colors, typography } from './theme';

export function CredentialsForm({ mode, onSubmit }: {
  mode: 'sign-up' | 'sign-in';
  onSubmit: (credentials: Credentials) => Promise<void>;
}) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const pending = useRef(false);
  const passwordInput = useRef<TextInput>(null);
  const signup = mode === 'sign-up';
  const submitLabel = signup ? 'Create account' : 'Sign in';

  async function submit() {
    if (pending.current) return;

    const address = email.trim();
    if (!address || !password) {
      setError('Enter your email and password.');
      return;
    }
    const passwordLength = Array.from(password).length;
    if (signup && (passwordLength < 15 || passwordLength > 128)) {
      setError('Use 15–128 characters for your password.');
      return;
    }

    pending.current = true;
    setBusy(true);
    setError('');
    try {
      await onSubmit({ email: address, password });
    } catch (failure) {
      setError(errorMessage(failure, 'Could not complete the request. Try again.'));
    } finally {
      pending.current = false;
      setBusy(false);
    }
  }

  return (
    <View style={styles.form}>
      <View style={styles.field}>
        <Text style={typography.label}>Email</Text>
        <TextInput
          accessibilityLabel="Email"
          autoCapitalize="none"
          autoComplete="email"
          autoCorrect={false}
          editable={!busy}
          keyboardType="email-address"
          onChangeText={setEmail}
          onSubmitEditing={() => passwordInput.current?.focus()}
          placeholder="you@example.com"
          placeholderTextColor={colors.muted}
          returnKeyType="next"
          style={styles.input}
          value={email}
        />
      </View>
      <View style={styles.field}>
        <Text style={typography.label}>Password</Text>
        <TextInput
          ref={passwordInput}
          accessibilityLabel="Password"
          autoCapitalize="none"
          autoComplete={signup ? 'new-password' : 'current-password'}
          autoCorrect={false}
          editable={!busy}
          onChangeText={setPassword}
          onSubmitEditing={() => { void submit(); }}
          placeholder={signup ? '15 characters minimum' : 'Enter your password'}
          placeholderTextColor={colors.muted}
          returnKeyType="go"
          secureTextEntry
          style={styles.input}
          value={password}
        />
      </View>
      {error ? <Text accessibilityRole="alert" style={typography.error}>{error}</Text> : null}
      <Button label={busy ? (signup ? 'Creating account…' : 'Signing in…') : submitLabel}
        busy={busy} onPress={() => { void submit(); }} />
    </View>
  );
}

const styles = StyleSheet.create({
  form: { gap: 15, marginTop: 20 },
  field: { gap: 6 },
  input: {
    minHeight: 48,
    paddingHorizontal: 12,
    paddingVertical: 10,
    borderWidth: 1,
    borderRadius: 10,
    borderColor: colors.outline,
    backgroundColor: colors.surface,
    color: colors.ink,
    fontSize: 16,
  },
});
