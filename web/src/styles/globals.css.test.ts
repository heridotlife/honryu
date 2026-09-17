// Reduced-motion support (phase 77), pinned as a content test: the media
// query does its work in the browser (jsdom computes no styles), so what
// the suite can enforce is that the CSS contract EXISTS -- the reduce
// block, the global animation/transition clamp, and the skeleton-pulse
// shutdown that turns loading bars into static gray blocks. If someone
// deletes the block, this fails before any vestibular operator does.
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

// Read from disk: the ?raw import comes back empty here -- Tailwind's
// vite plugin intercepts CSS modules before the raw loader sees them.
// vitest's root is web/ (the vite.config.ts directory), so the path is
// stable however the suite is invoked.
const globalsCss = readFileSync('src/styles/globals.css', 'utf8');

describe('globals.css reduced-motion contract', () => {
  it('carries the prefers-reduced-motion block with the global clamp', () => {
    const block = globalsCss.match(/@media \(prefers-reduced-motion: reduce\) \{[\s\S]*?\n\}/);
    expect(block).not.toBeNull();
    const body = block![0];
    // Animations complete near-instantly (0.01ms, not 0, so end events
    // still fire) and transitions become instant.
    expect(body).toContain('animation-duration: 0.01ms !important');
    expect(body).toContain('animation-iteration-count: 1 !important');
    expect(body).toContain('transition-duration: 0.01ms !important');
  });

  it('turns the skeleton pulse into a static block under reduced motion', () => {
    const block = globalsCss.match(/@media \(prefers-reduced-motion: reduce\) \{[\s\S]*$/);
    expect(block![0]).toMatch(/\.animate-pulse\s*\{[^}]*animation:\s*none !important/);
  });

  it('is the ONLY animation hook the skeletons need: pulse bars use the animate-pulse class', () => {
    // The class-driven contract the rule above neutralizes: skeleton bars
    // are animate-pulse elements, so the one CSS rule covers them all.
    expect(globalsCss).toContain('.animate-pulse');
  });
});
