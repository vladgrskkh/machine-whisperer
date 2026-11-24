import { StyleSheet, Text, View } from 'react-native';
import Svg, { Circle, Line, Polyline } from 'react-native-svg';

import type { MachineAlert, TelemetryObservation } from '../../api/types';
import { colors, typography } from '../../ui/theme';
import { buildHistoryChartData } from './history-chart-data';

const baselineColor = '#926213';
const recoveredColor = '#38795b';
const ticks = [0, 0.25, 0.5, 0.75, 1];
const x = (fraction: number) => 8 + fraction * 294;
const y = (fraction: number) => 10 + fraction * 124;
const axisNumber = new Intl.NumberFormat(undefined, { maximumFractionDigits: 3 });
const axisTime = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });

export function HistoryChart({ observations, baselineMean, alerts }: {
  observations: TelemetryObservation[];
  baselineMean: number | null;
  alerts: MachineAlert[];
}) {
  const data = buildHistoryChartData(observations, baselineMean, alerts);
  if (!data) return null;
  const { points, start, end, maximum, baselineY, markers } = data;
  const highest = maximum >= 1e6 || (maximum > 0 && maximum < 0.001)
    ? maximum.toExponential(2)
    : axisNumber.format(maximum);

  return (
    <>
      <View accessible accessibilityRole="image" accessibilityLabel="Vibration history chart in g RMS" style={styles.chart}>
        <View style={[styles.scale, maximum === 0 && styles.zeroScale]}>
          <Text style={styles.axis}>{highest}</Text>
          {maximum > 0 && <Text style={styles.axis}>0</Text>}
        </View>
        <Svg viewBox="0 0 310 144" width="100%" height={144} style={styles.plot} preserveAspectRatio="none">
          {ticks.map((tick) => (
            <Line key={tick} x1={x(0)} x2={x(1)} y1={y(tick)} y2={y(tick)} stroke={colors.outline} />
          ))}
          {baselineY !== null && (
            <Line x1={x(0)} x2={x(1)} y1={y(baselineY)} y2={y(baselineY)} stroke={baselineColor} strokeWidth={2} strokeDasharray="5 4" />
          )}
          {points.length > 1
            ? <Polyline points={points.map((point) => `${x(point.x)},${y(point.y)}`).join(' ')}
                fill="none" stroke={colors.accent} strokeWidth={3} strokeLinejoin="round" strokeLinecap="round" />
            : <Circle cx={x(points[0].x)} cy={y(points[0].y)} r={4} fill={colors.accent} />}
          {markers.map((marker) => {
            const color = marker.kind === 'trigger' ? colors.error : recoveredColor;
            if (marker.y === null) {
              return <Line key={marker.id} x1={x(marker.x)} x2={x(marker.x)} y1={y(0)} y2={y(1)}
                stroke={color} strokeDasharray="3 4" />;
            }
            const recovered = marker.kind === 'recovery';
            return <Circle key={marker.id} cx={x(marker.x)} cy={y(marker.y)} r={recovered ? 7 : 4}
              fill={recovered ? 'none' : color} stroke={recovered ? color : colors.surface} strokeWidth={recovered ? 2 : 1} />;
          })}
        </Svg>
      </View>
      <View style={styles.time}>
        <Text style={styles.axis}>{axisTime.format(start)}</Text>
        {end !== start && <Text style={styles.axis}>{axisTime.format(end)}</Text>}
      </View>
      <View style={styles.legend}>
        <Legend color={colors.accent} label="Measured RMS" />
        {baselineY !== null && <Legend color={baselineColor} label="Baseline mean" dashed />}
        {markers.some(({ kind }) => kind === 'trigger') && <Legend color={colors.error} label="Alert opened" dot />}
        {markers.some(({ kind }) => kind === 'recovery') && <Legend color={recoveredColor} label="Alert recovered" dot />}
      </View>
    </>
  );
}

function Legend({ color, label, dot = false, dashed = false }: {
  color: string; label: string; dot?: boolean; dashed?: boolean;
}) {
  return (
    <View style={styles.key}>
      <View style={[
        dot ? styles.dot : styles.line,
        { borderColor: color },
        dashed && styles.dashed,
      ]} />
      <Text style={typography.body}>{label}</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  chart: { backgroundColor: colors.background, flexDirection: 'row', borderRadius: 8, paddingHorizontal: 6 },
  scale: { justifyContent: 'space-between', paddingVertical: 4, alignItems: 'flex-end' },
  zeroScale: { justifyContent: 'flex-end' },
  axis: { color: colors.muted, fontSize: 11 },
  plot: { flex: 1 },
  time: { flexDirection: 'row', justifyContent: 'space-between', gap: 8 },
  legend: { flexDirection: 'row', flexWrap: 'wrap', columnGap: 14, rowGap: 4 },
  key: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  line: { width: 14, height: 1, borderTopWidth: 2 },
  dashed: { borderStyle: 'dashed' },
  dot: { width: 8, height: 8, borderRadius: 4, borderWidth: 4 },
});
