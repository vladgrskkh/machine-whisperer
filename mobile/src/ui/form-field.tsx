import { StyleSheet, Text, TextInput, View, type TextInputProps } from 'react-native';

import { colors, typography } from './theme';

export function FormField({ label, ...props }: TextInputProps & { label: string }) {
  return (
    <View style={styles.field}>
      <Text style={typography.label}>{label}</Text>
      <TextInput accessibilityLabel={label} placeholderTextColor={colors.muted} style={styles.input} {...props} />
    </View>
  );
}

const styles = StyleSheet.create({
  field: { gap: 6 },
  input: {
    backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.outline, borderRadius: 11,
    color: colors.ink, fontSize: 16, paddingHorizontal: 12, minHeight: 48,
  },
});
