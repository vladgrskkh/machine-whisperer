import { Tabs } from 'expo-router';
import { Text } from 'react-native';

import { colors } from '../../src/ui/theme';

export default function TabLayout() {
  return (
    <Tabs screenOptions={{
      headerShown: false,
      tabBarActiveTintColor: colors.accent,
      tabBarInactiveTintColor: colors.muted,
      tabBarStyle: { backgroundColor: colors.surface, borderTopColor: colors.outline },
    }}>
      <Tabs.Screen name="machines/index" options={{
        title: 'Machines',
        tabBarIcon: ({ color }) => <Text accessible={false} style={{ color, fontSize: 21 }}>⌂</Text>,
      }} />
      <Tabs.Screen name="account" options={{
        title: 'Account',
        tabBarIcon: ({ color }) => <Text accessible={false} style={{ color, fontSize: 21 }}>○</Text>,
      }} />
    </Tabs>
  );
}
