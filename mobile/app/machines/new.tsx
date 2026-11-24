import { Link, router, useFocusEffect } from 'expo-router';
import { useCallback, useRef } from 'react';
import { Text } from 'react-native';

import { CreateMachineForm } from '../../src/features/machines/create-machine-form';
import { typography } from '../../src/ui/theme';
import { WorkspaceScreen } from '../../src/ui/workspace-screen';

export default function NewMachineRoute() {
  const focused = useRef(false);
  useFocusEffect(useCallback(() => {
    focused.current = true;
    return () => { focused.current = false; };
  }, []));

  return (
    <WorkspaceScreen>
      <Link href="/machines" dismissTo style={typography.link}>‹  Machines</Link>
      <Text accessibilityRole="header" style={typography.heading}>Add a machine</Text>
      <Text style={typography.body}>Give your machine a name you’ll recognize.</Text>
      <CreateMachineForm onCreated={({ id }) => {
        if (focused.current) router.replace({ pathname: '/machines/[id]/claim-device', params: { id } });
      }} />
    </WorkspaceScreen>
  );
}
