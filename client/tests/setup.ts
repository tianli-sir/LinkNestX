import "@testing-library/jest-dom/vitest";
// jsdom implements neither matchMedia nor its change events; a static
// no-reduction stub keeps framer-motion and ThemeProvider deterministic.
if (!window.matchMedia) {
  window.matchMedia = ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false })) as unknown as typeof window.matchMedia;
}
