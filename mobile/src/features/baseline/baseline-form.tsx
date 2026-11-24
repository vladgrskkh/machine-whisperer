import { useRef, useState } from 'react';
import { Text } from 'react-native';

import { errorMessage } from '../../api/errors';
import type { MachineBaseline } from '../../api/types';
import { useSession } from '../../auth/session-provider';
import { Button } from '../../ui/button';
import { FormField } from '../../ui/form-field';
import { typography } from '../../ui/theme';
import { WorkspaceCard } from '../../ui/workspace-card';

export function BaselineForm({ id, initialBaseline, onSaved }: {
  id: string;
  initialBaseline: MachineBaseline | null;
  onSaved: () => void;
}) {
  const { api } = useSession();
  const [mean, setMean] = useState(initialBaseline?.mean_rms_g.toString() ?? '');
  const [stddev, setStddev] = useState(initialBaseline?.stddev_rms_g.toString() ?? '');
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState('');
  const pending = useRef(false);

  async function save() {
    if (pending.current) return;
    const meanValue = parseMeasurement(mean);
    const stddevValue = parseMeasurement(stddev);
    if (!Number.isFinite(meanValue) || meanValue < 0) {
      setMessage('Enter a finite baseline mean of 0 g or higher.');
      return;
    }
    if (!Number.isFinite(stddevValue) || stddevValue <= 0) {
      setMessage('Enter a finite standard deviation greater than 0 g.');
      return;
    }

    pending.current = true;
    setSaving(true);
    setMessage('');
    try {
      await api.setBaseline(id, { mean_rms_g: meanValue, stddev_rms_g: stddevValue });
      onSaved();
    } catch (error) {
      setMessage(errorMessage(error, 'Could not save the baseline.'));
    } finally {
      pending.current = false;
      setSaving(false);
    }
  }

  return (
    <WorkspaceCard>
      <FormField label="Baseline mean RMS (g)" keyboardType="decimal-pad" value={mean} onChangeText={setMean} editable={!saving} />
      <Text style={typography.body}>Typical vibration while the machine runs steadily.</Text>
      <FormField label="Baseline standard deviation (g)" keyboardType="decimal-pad" value={stddev} onChangeText={setStddev} editable={!saving} />
      <Text style={typography.body}>How much vibration usually varies around the mean.</Text>
      {!!message && <Text accessibilityRole="alert" style={typography.error}>{message}</Text>}
      <Button label={saving ? 'Saving baseline…' : 'Save baseline'} onPress={save} busy={saving} />
    </WorkspaceCard>
  );
}

function parseMeasurement(input: string) {
  const decimal = input.trim().replace(',', '.');
  return /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i.test(decimal) ? Number(decimal) : NaN;
}
