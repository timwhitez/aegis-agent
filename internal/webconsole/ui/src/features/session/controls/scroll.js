// Scrolls an inline session card into view and moves focus to its heading.
export function revealCard(testId) {
  const card = globalThis.document?.querySelector(`[data-testid="${testId}"]`);
  if (!card) return false;
  const reduce = globalThis.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
  card.scrollIntoView({ block: 'start', behavior: reduce ? 'auto' : 'smooth' });
  const target = card.querySelector('[data-card-focus]') || card;
  target.focus?.({ preventScroll: true });
  return true;
}
