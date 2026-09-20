// Phase 97: the per-mode SLO default table, mirroring
// internal/domain/loadmode's SuggestedThresholds number-for-number. The
// suggestion is derived client-side on purpose — no server round-trip is
// needed to OFFER defaults (the Run tab already holds the mode entry from
// its config read); the server's own table stays the authority for any
// future server-side use, and the two are pinned together by their tests.
// Changing one number here without the Go table (or vice versa) is a
// contract break: keep them in lockstep.
import type { LoadMode } from './modeConfig';
import type { ThresholdInput } from '../api/scenarios';

/**
 * The mode's default health contract, in the threshold wire grammar the
 * editor and saveThresholds already speak. The soak contract adds a
 * throughput floor at 90% of targetQps; a non-positive targetQps
 * (unlimited rate) states no floor — there is no rate to undershoot.
 * An unknown mode yields no contract (empty array).
 */
export function suggestedThresholds(mode: LoadMode, targetQps: number): ThresholdInput[] {
  switch (mode) {
    case 'burst':
      return [
        { metric: 'error_rate', comparison: 'lt', value: 0.01 },
        { metric: 'http_p95_ms', comparison: 'lt', value: 500 },
      ];
    case 'ramp':
      return [
        { metric: 'error_rate', comparison: 'lt', value: 0.01 },
        { metric: 'http_p95_ms', comparison: 'lt', value: 800 },
      ];
    case 'soak': {
      const rows: ThresholdInput[] = [
        { metric: 'error_rate', comparison: 'lt', value: 0.005 },
        { metric: 'http_p95_ms', comparison: 'lt', value: 600 },
      ];
      if (targetQps > 0) {
        rows.push({ metric: 'throughput_qps', comparison: 'gt', value: targetQps * 0.9 });
      }
      return rows;
    }
    case 'staircase':
      // The widest latency ceiling in the table: the point is finding the
      // breaking plateau -- alerts should flag breakage, not proximity.
      return [
        { metric: 'error_rate', comparison: 'lt', value: 0.01 },
        { metric: 'http_p95_ms', comparison: 'lt', value: 1000 },
      ];
    default:
      return [];
  }
}
