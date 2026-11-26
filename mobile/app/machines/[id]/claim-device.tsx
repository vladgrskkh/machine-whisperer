import { Link, router, useLocalSearchParams } from 'expo-router';
import { useCallback } from 'react';
import { ActivityIndicator, Text } from 'react-native';

import type { ApiClient } from '../../../src/api/client';
import { parseUUID } from '../../../src/api/uuid';
import { ClaimDeviceForm } from '../../../src/features/devices/claim-device-form';
import { useMachineDeletions } from '../../../src/features/machines/machine-deletion-provider';
import { useMachineQuery } from '../../../src/features/machines/use-machine-query';
import { Button } from '../../../src/ui/button';
import { colors, typography } from '../../../src/ui/theme';
import { WorkspaceScreen } from '../../../src/ui/workspace-screen';

function SelectedMachine({ id }: { id: string }) {
  const load = useCallback((api: ApiClient) => api.getMachine(id), [id]);
  const { query, retry } = useMachineQuery(load, 'Could not load the machine.');

  if (query.state === 'loading') {
    return <ActivityIndicator accessibilityLabel="Loading machine" color={colors.accent} />;
  }
  if (query.state === 'error' || query.data?.id !== id || typeof query.data.name !== 'string') {
    return (
      <>
        <Text accessibilityRole="alert" style={typography.error}>
          {query.state === 'error' ? query.message : 'Could not load the selected machine.'}
        </Text>
        <Button label="Try again" onPress={retry} outline />
      </>
    );
  }
  return (
    <>
      <Text style={typography.body}>Pair a sensor device with {query.data.name}.</Text>
      <ClaimDeviceForm machineId={id} onDone={() => router.dismissTo('/machines')} />
    </>
  );
}

export default function ClaimDeviceRoute() {
  const params = useLocalSearchParams<{ id: string }>();
  const id = parseUUID(params.id);
  const { deletedIds } = useMachineDeletions();

  return (
    <WorkspaceScreen>
      <Link href="/machines" dismissTo style={typography.link}>‹  Machines</Link>
      <Text accessibilityRole="header" style={typography.heading}>Scan device QR</Text>
      {!id && <Text accessibilityRole="alert" style={typography.error}>Invalid machine ID.</Text>}
      {id && (deletedIds.has(id)
        ? <Text accessibilityRole="alert" style={typography.error}>This machine was deleted.</Text>
        : <SelectedMachine key={id} id={id} />)}
    </WorkspaceScreen>
  );
}
