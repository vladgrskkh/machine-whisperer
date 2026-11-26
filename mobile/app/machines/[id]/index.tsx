import { Link, router, useLocalSearchParams, useNavigation } from 'expo-router';
import { useCallback } from 'react';
import { ActivityIndicator, Text } from 'react-native';

import type { ApiClient } from '../../../src/api/client';
import { parseUUID } from '../../../src/api/uuid';
import { MachineHistory } from '../../../src/features/history/machine-history';
import { DeleteMachineAction } from '../../../src/features/machines/delete-machine-action';
import { useMachineDeletions } from '../../../src/features/machines/machine-deletion-provider';
import { useMachineQuery } from '../../../src/features/machines/use-machine-query';
import { Button } from '../../../src/ui/button';
import { QueryRefreshStatus } from '../../../src/ui/query-refresh-status';
import { colors, typography } from '../../../src/ui/theme';
import { WorkspaceScreen } from '../../../src/ui/workspace-screen';

export default function MachineDetailRoute() {
  const { id: parameter } = useLocalSearchParams<{ id: string }>();
  const id = parseUUID(parameter);
  const { deletedIds } = useMachineDeletions();

  return (
    <WorkspaceScreen>
      <Link href="/machines" dismissTo style={typography.link}>‹  Machines</Link>
      {!id && <Text accessibilityRole="alert" style={typography.error}>Invalid machine ID.</Text>}
      {id && (deletedIds.has(id)
        ? <Text accessibilityRole="alert" style={typography.error}>This machine was deleted.</Text>
        : <SelectedMachine key={id} id={id} />)}
    </WorkspaceScreen>
  );
}

function SelectedMachine({ id }: { id: string }) {
  const navigation = useNavigation();
  const load = useCallback((api: ApiClient) => api.getMachine(id), [id]);
  const { query, refresh, retry } = useMachineQuery(load, 'Could not load the machine.', { keepPreviousData: true });

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
      <QueryRefreshStatus refresh={refresh} resource="machine details" retry={retry} />
      <MachineHistory
        machine={query.data}
        onEditBaseline={() => router.push({ pathname: '/machines/[id]/baseline', params: { id } })}
        onClaim={() => router.push({ pathname: '/machines/[id]/claim-device', params: { id } })}
      />
      <DeleteMachineAction machine={query.data} onDeleted={() => { if (navigation.isFocused()) router.dismissTo('/machines'); }} />
    </>
  );
}
