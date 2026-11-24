import { CameraView, useCameraPermissions } from 'expo-camera';
import { useIsFocused } from 'expo-router';
import { useRef, useState } from 'react';
import { StyleSheet, Text, View } from 'react-native';

import { ApiError } from '../../api/errors';
import { parseUUID } from '../../api/uuid';
import { useSession } from '../../auth/session-provider';
import { Button } from '../../ui/button';
import { FormField } from '../../ui/form-field';
import { colors, typography } from '../../ui/theme';
import { parseClaimCode, parseClaimQR } from './claim-input';

type Stage = 'choose' | 'scan' | 'manual' | 'claimed';

export function ClaimDeviceForm({ machineId, onDone }: { machineId: string; onDone: () => void }) {
  const { api } = useSession();
  const focused = useIsFocused();
  const [permission, requestPermission] = useCameraPermissions();
  const [stage, setStage] = useState<Stage>('choose');
  const [deviceId, setDeviceId] = useState('');
  const [claimCode, setClaimCode] = useState('');
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const scanner = useRef<'idle' | 'opening' | 'active'>('idle');
  const submitting = useRef(false);

  async function startScanning() {
    if (scanner.current !== 'idle') return;
    scanner.current = 'opening';
    setBusy(true);
    setMessage('');
    try {
      const access = permission?.granted || permission?.canAskAgain === false
        ? permission
        : await requestPermission();
      if (access.granted) {
        scanner.current = 'active';
        setStage('scan');
        return;
      }
      setMessage('Camera access is denied. Enter the claim code manually.');
    } catch {
      setMessage('Could not open the camera. Enter the claim code manually.');
    } finally {
      if (scanner.current === 'opening') scanner.current = 'idle';
      setBusy(false);
    }
  }

  function stopScanning() {
    scanner.current = 'idle';
    setStage('choose');
  }

  function openManual() {
    scanner.current = 'idle';
    setMessage('');
    setStage('manual');
  }

  function readBarcode(data: string) {
    if (scanner.current !== 'active') return;
    const qr = parseClaimQR(data);
    if (!qr) {
      stopScanning();
      setMessage('Invalid device QR. Scan the QR on the sensor device label.');
      return;
    }
    setDeviceId(qr.device_id);
    setClaimCode(qr.claim_code);
    openManual();
  }

  async function submit() {
    if (submitting.current) return;
    const id = parseUUID(deviceId.trim());
    if (!id) {
      setMessage('Enter a valid device ID.');
      return;
    }
    const code = parseClaimCode(claimCode.trim());
    if (!code) {
      setMessage('Enter a valid claim code.');
      return;
    }

    submitting.current = true;
    setBusy(true);
    setMessage('');
    try {
      await api.claimDevice({ device_id: id, claim_code: code, machine_id: machineId });
      setDeviceId('');
      setClaimCode('');
      setStage('claimed');
    } catch (error) {
      setMessage(error instanceof ApiError && error.code === 'claim_unavailable'
        ? 'Device claim unavailable. Check the label and try again.'
        : 'Could not claim the sensor device. Try again.');
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  if (stage === 'claimed') {
    return (
      <>
        <Text accessibilityLiveRegion="polite" style={typography.body}>Sensor device connected.</Text>
        <Button label="Back to machines" onPress={onDone} />
      </>
    );
  }

  return (
    <>
      {stage === 'scan' && focused && (
        <View style={styles.cameraCard}>
          <View style={styles.frame}>
            <CameraView
              style={StyleSheet.absoluteFill}
              barcodeScannerSettings={{ barcodeTypes: ['qr'] }}
              onBarcodeScanned={({ data }) => readBarcode(data)}
              onMountError={() => {
                if (scanner.current !== 'active') return;
                stopScanning();
                setMessage('Could not open the camera. Enter the claim code manually.');
              }}
            />
            <View pointerEvents="none" style={styles.target} />
          </View>
          <Text style={typography.body}>Place the QR label inside the frame.</Text>
        </View>
      )}
      {stage === 'manual' && (
        <>
          <FormField label="Device ID" value={deviceId} onChangeText={setDeviceId}
            autoCapitalize="none" autoCorrect={false} editable={!busy} />
          <FormField label="Claim code" value={claimCode} onChangeText={setClaimCode}
            autoCapitalize="none" autoCorrect={false} autoComplete="off" secureTextEntry editable={!busy} />
        </>
      )}
      {message && <Text accessibilityRole="alert" style={typography.error}>{message}</Text>}
      {stage === 'choose' && (
        <Button label={message.startsWith('Invalid device QR') ? 'Scan again' : 'Scan device QR'}
          onPress={() => { void startScanning(); }} busy={busy} />
      )}
      {stage !== 'manual' ? (
        <Button label="Enter claim code" onPress={openManual} outline busy={busy} />
      ) : (
        <Button label={busy ? 'Claiming device…' : 'Claim device'} onPress={() => { void submit(); }} busy={busy} />
      )}
      <Text style={[typography.body, styles.notice]}>Claim codes expire and can only be used once.</Text>
    </>
  );
}

const styles = StyleSheet.create({
  cameraCard: { backgroundColor: colors.surface, borderColor: colors.outline, borderWidth: 1, borderRadius: 14, padding: 12, gap: 8 },
  frame: { height: 250, borderRadius: 11, backgroundColor: colors.ink, overflow: 'hidden', padding: 24 },
  target: { flex: 1, borderWidth: 2, borderColor: '#80d7c6', borderRadius: 16 },
  notice: { backgroundColor: colors.accentTint, borderRadius: 11, padding: 12 },
});
