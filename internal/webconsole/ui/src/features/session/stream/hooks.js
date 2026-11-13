// Small hooks for the stream components.
import { useEffect, useState } from '../../../lib/html.js';

/** Re-renders every intervalMs while active and returns Date.now(); idle when inactive. */
export function useTicker(active, intervalMs = 1000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return undefined;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(timer);
  }, [active, intervalMs]);
  return active ? now : Date.now();
}
