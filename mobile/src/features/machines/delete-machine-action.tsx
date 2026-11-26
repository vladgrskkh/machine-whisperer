import { useRef, useState } from 'react';
import { Alert, Platform, Text } from 'react-native';

import { errorMessage } from '../../api/errors';
import type { Machine } from '../../api/types';
import { Button } from '../../ui/button';
import { typography } from '../../ui/theme';
import { useMachineDeletions } from './machine-deletion-provider';

export function DeleteMachineAction({ machine, onDeleted }: { machine: Machine; onDeleted: () => void }) {
  const { deleteMachine } = useMachineDeletions();
  const [deleting, setDeleting] = useState(false);
  const [message, setMessage] = useState('');
  const phase = useRef<'idle' | 'confirming' | 'deleting'>('idle');

  async function confirm() {
    if (phase.current !== 'confirming') return;
    phase.current = 'deleting';
    setDeleting(true);
    setMessage('');
    try {
      await deleteMachine(machine.id);
    } catch (error) {
      phase.current = 'idle';
      setMessage(errorMessage(error, 'Could not delete the machine.'));
      setDeleting(false);
      return;
    }
    onDeleted();
  }

  function requestConfirmation() {
    if (phase.current !== 'idle') return;
    phase.current = 'confirming';
    const title = `Delete ${machine.name}?`;
    const explanation = 'Monitoring will stop and the machine will be hidden. Its history and sensor association will be kept.';
    const cancel = () => {
      if (phase.current === 'confirming') phase.current = 'idle';
    };
    if (Platform.OS === 'web') {
      if (globalThis.confirm(`${title}\n\n${explanation}`)) void confirm();
      else cancel();
      return;
    }
    Alert.alert(title, explanation,
      [
        { text: 'Cancel', style: 'cancel', onPress: cancel },
        { text: 'Delete machine', style: 'destructive', onPress: () => { void confirm(); } },
      ],
      { cancelable: true, onDismiss: cancel },
    );
  }

  return (
    <>
      {!!message && <Text accessibilityRole="alert" style={typography.error}>{message}</Text>}
      <Button label={deleting ? 'Deleting…' : 'Delete machine'} accessibilityLabel="Delete machine"
        onPress={requestConfirmation} busy={deleting} outline />
    </>
  );
}
