/// <reference types="vite/client" />

// The one node surface a src-side test needs (globals.css.test.ts reads
// the stylesheet from disk to pin the reduced-motion contract; jsdom
// computes no styles, and Tailwind's vite plugin empties CSS ?raw
// imports). Runtime is vitest/bun, which provide the real module; this
// only tells tsc its shape. Deliberately NOT a devDependency -- @types/node
// would drag a hundred ambient globals into a DOM-only app config.
declare module 'node:fs' {
  export function readFileSync(path: string, encoding: 'utf8'): string;
}
