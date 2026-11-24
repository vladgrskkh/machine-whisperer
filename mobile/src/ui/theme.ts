import { StyleSheet } from 'react-native';

export const colors = {
  background: '#f7f9f8',
  surface: '#ffffff',
  ink: '#13313b',
  muted: '#536b72',
  outline: '#dce6e4',
  accent: '#137a75',
  accentInk: '#176b67',
  accentTint: '#e5f1ef',
  error: '#9a3726',
};

export const typography = StyleSheet.create({
  brand: { fontSize: 16, fontWeight: '800', color: colors.ink },
  heading: { fontSize: 28, fontWeight: '800', color: colors.ink, letterSpacing: -0.8 },
  eyebrow: { fontSize: 11, fontWeight: '800', color: colors.muted, letterSpacing: 1.15, textTransform: 'uppercase' },
  body: { fontSize: 14, lineHeight: 21, color: colors.muted },
  label: { fontSize: 13, fontWeight: '700', color: colors.ink },
  link: { fontSize: 14, fontWeight: '700', color: colors.accentInk },
  error: { fontSize: 14, lineHeight: 20, color: colors.error },
});
