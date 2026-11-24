import { Link, router, useLocalSearchParams, useNavigation } from 'expo-router';
import { useCallback } from 'react';
import { ActivityIndicator, Text } from 'react-native';

import type { ApiClient } from '../../../src/api/client';
import { parseUUID } from '../../../src/api/uuid';
import { BaselineForm } from '../../../src/features/baseline/baseline-form';
import { useMachineQuery } from '../../../src/features/machines/use-machine-query';
import { Button } from '../../../src/ui/button';
import { colors, typography } from '../../../src/ui/theme';
import { WorkspaceScreen } from '../../../src/ui/workspace-screen';

export default function BaselineRoute() {
  const { id: parameter } = useLocalSearchParams<{ id: string }>();
  const id = parseUUID(parameter);

  return (
    <WorkspaceScreen>
      {id
        ? <SelectedBaseline key={id} id={id} />
        : <Text accessibilityRole="alert" style={typography.error}>Invalid machine ID.</Text>}
    </WorkspaceScreen>
  );
}

function SelectedBaseline({ id }: { id: string }) {
  const navigation = useNavigation();
  const detail = { pathname: '/machines/[id]' as const, params: { id } };
  const load = useCallback(async (api: ApiClient, signal: AbortSignal) => {
    const machine = await api.getMachine(id);
    const baseline = await api.getBaseline(id, signal);
    return { machine, baseline };
  }, [id]);
  const { query, retry } = useMachineQuery(load, 'Could not load the baseline.');

  return (
    <>
      <Link href={detail} dismissTo style={typography.link}>‹  {query.state === 'ready' ? query.data.machine.name : 'Machine'}</Link>
      <Text accessibilityRole="header" style={typography.heading}>Set baseline</Text>
      {query.state === 'loading' && <ActivityIndicator color={colors.accent} accessibilityLabel="Loading baseline" />}
      {query.state === 'error' && (
        <>
          <Text accessibilityRole="alert" style={typography.error}>{query.message}</Text>
          <Button label="Try again" onPress={retry} outline />
        </>
      )}
      {query.state === 'ready' && (
        <BaselineForm id={id} initialBaseline={query.data.baseline} onSaved={() => { if (navigation.isFocused()) router.dismissTo(detail); }} />
      )}
    </>
  );
}
