import { useRef, useState } from 'react';
import { Text } from 'react-native';

import { errorMessage } from '../../api/errors';
import type { Machine } from '../../api/types';
import { useSession } from '../../auth/session-provider';
import { Button } from '../../ui/button';
import { FormField } from '../../ui/form-field';
import { typography } from '../../ui/theme';
import { parseMachineName } from './machine-name';

export function CreateMachineForm({ onCreated }: { onCreated: (machine: Machine) => void }) {
  const { api } = useSession();
  const [name, setName] = useState('');
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);

  async function submit() {
    if (submitting.current) return;
    const validName = parseMachineName(name);
    if (!validName) {
      setMessage('Enter a machine name with 1–100 characters and no control characters.');
      return;
    }

    submitting.current = true;
    setBusy(true);
    setMessage('');
    try {
      onCreated(await api.createMachine({ name: validName }));
    } catch (error) {
      setMessage(errorMessage(error, 'Could not create the machine. Try again.'));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <>
      <FormField label="Machine name" value={name} onChangeText={setName}
        placeholder="e.g. Workshop fan" editable={!busy} returnKeyType="done"
        onSubmitEditing={() => { void submit(); }} />
      <Text style={typography.body}>After creating the machine, attach its sensor by scanning the QR label.</Text>
      {message && <Text accessibilityRole="alert" style={typography.error}>{message}</Text>}
      <Button label={busy ? 'Creating machine…' : 'Create machine'} onPress={() => { void submit(); }} busy={busy} />
    </>
  );
}
