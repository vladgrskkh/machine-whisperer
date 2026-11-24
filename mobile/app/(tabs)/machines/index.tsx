import { router } from 'expo-router';

import { MachineList } from '../../../src/features/machines/machine-list';

export default function MachinesRoute() {
  return (
    <MachineList
      onAdd={() => router.push('/machines/new')}
      onSelect={(id) => router.push({ pathname: '/machines/[id]', params: { id } })}
    />
  );
}
