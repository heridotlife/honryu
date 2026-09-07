import { describe, expect, it } from 'vitest';
import { clusterCapacity, formatClusterTime, originDescription } from './Clusters';
import type { Cluster } from '../api/clusters';

describe('originDescription', () => {
  // Both wire values need a credential-ownership explanation -- the page
  // renders it as the row's title attribute.
  it('explains both origins', () => {
    expect(originDescription('operator')).toBe('home-cluster Secret managed by the platform operator');
    expect(originDescription('byoc')).toBe('customer-supplied kubeconfig (bring your own cluster)');
  });
});

describe('clusterCapacity', () => {
  const registered: Cluster = {
    name: 'honryu',
    api_url: 'https://kubernetes.default.svc:443',
    ingest_url: 'https://honryu.pve.heri.life/api/ingest',
    sidecar_image: 'registry.pve.heri.life/honryu/honryu-sidecar:phase16',
    namespace: 'honryu',
    secret_ref: 'cluster-honryu-explicit-creds',
    origin: 'operator',
    created_by: 'alice',
    created_time: '2026-09-05T10:29:02.848672Z',
  };

  // Phase 25 flipped the phase-22 honesty pin: capacity numbers now ride
  // the wire (engines_used/engines_ceiling, both required), and their
  // absence -- no quota ledger wired, or a half-wired read -- still maps to
  // the meter's honest "no capacity reported" state.
  it('maps wire capacity numbers onto the meter props', () => {
    expect(clusterCapacity({ ...registered, engines_used: 2, engines_ceiling: 12 })).toEqual({
      used: 2,
      ceiling: 12,
    });
  });
  it('reports no capacity numbers when the fields are absent', () => {
    expect(clusterCapacity(registered)).toEqual({});
  });
  it('reports no capacity numbers when only one field is present (half-wired read)', () => {
    expect(clusterCapacity({ ...registered, engines_ceiling: 12 })).toEqual({});
    expect(clusterCapacity({ ...registered, engines_used: 2 })).toEqual({});
  });
});

describe('formatClusterTime', () => {
  it('formats a valid ISO timestamp via the locale', () => {
    const iso = '2026-08-17T00:00:00Z';
    expect(formatClusterTime(iso)).toBe(new Date(iso).toLocaleString());
  });

  it('passes an unparseable value through unchanged rather than rendering "Invalid Date"', () => {
    expect(formatClusterTime('not-a-date')).toBe('not-a-date');
  });
});
