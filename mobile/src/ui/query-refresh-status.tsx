import { Text } from 'react-native';

import { Button } from './button';
import { typography } from './theme';

export function QueryRefreshStatus({ refresh, resource, retry }: {
  refresh: 'refreshing' | { message: string } | undefined;
  resource: string;
  retry: () => void;
}) {
  if (!refresh) return null;
  if (refresh === 'refreshing') {
    return <Text role="status" accessibilityLiveRegion="polite" style={typography.body}>Refreshing {resource}…</Text>;
  }
  return (
    <>
      <Text accessibilityRole="alert" accessibilityLiveRegion="polite" style={typography.error}>{refresh.message}</Text>
      <Button label={`Retry ${resource} refresh`} onPress={retry} outline />
    </>
  );
}
