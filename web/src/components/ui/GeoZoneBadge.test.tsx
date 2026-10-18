import { describe, expect, it } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import GeoZoneBadge from './GeoZoneBadge';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

/** ClusterBadge's mount pattern: render into a real node under act and
 * assert on the DOM, not on JSX equality -- the badge's whole contract is
 * what a browser sees (or doesn't, when unlabeled). */
function mount(props: { geo?: string; zone?: string }): HTMLElement {
  const container = document.createElement('div');
  document.body.appendChild(container);
  let root!: Root;
  act(() => {
    root = createRoot(container);
    root.render(<GeoZoneBadge {...props} />);
  });
  return container;
}

// Phase 109: geo/zone are display-only labels joined by a middle dot; the
// empty string means unlabeled and must render nothing (legacy rows stay
// quiet, same as ClusterBadge's absent-cluster rule).
describe('GeoZoneBadge (phase 109)', () => {
  it('renders geo · zone when both labels are present', () => {
    const container = mount({ geo: 'eu', zone: 'eu-central-1' });
    const badge = container.querySelector('[data-testid="geolocation-badge"]')!;
    expect(badge).not.toBeNull();
    expect(badge.textContent).toBe('eu · eu-central-1');
  });

  it('renders geo alone when only geo is labeled', () => {
    const container = mount({ geo: 'eu' });
    expect(container.querySelector('[data-testid="geolocation-badge"]')!.textContent).toBe('eu');
  });

  it('renders zone alone when only zone is labeled', () => {
    const container = mount({ zone: 'eu-central-1' });
    expect(container.querySelector('[data-testid="geolocation-badge"]')!.textContent).toBe('eu-central-1');
  });

  it('renders nothing when both labels are empty (unlabeled)', () => {
    const container = mount({ geo: '', zone: '' });
    expect(container.querySelector('[data-testid="geolocation-badge"]')).toBeNull();
    expect(container.textContent).toBe('');
  });
});
