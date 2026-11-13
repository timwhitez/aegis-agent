// Command palette registry and fuzzy ranking (pure, DOM-free).
//
// A command is { id, title, group?, keywords?: string[], subtitle?, shortcut?,
// enabled?: boolean | () => boolean, run: () => void }. Only title/keywords/
// subtitle take part in matching.

export function normalizeText(value) {
  return String(value ?? '')
    .normalize('NFKD')
    .replace(/[\u0300-\u036f]/g, '')
    .toLowerCase()
    .trim();
}

const BOUNDARY = /[\s\-_/.:·]/;

/**
 * Scores how well query matches text. Returns null when the query characters
 * are not a subsequence of text. Higher is better; contiguous, prefix and
 * word-boundary matches are rewarded and gaps are penalised.
 */
export function fuzzyScore(query, text) {
  const q = normalizeText(query).replace(/\s+/g, ' ');
  const s = normalizeText(text);
  if (!q) return 0;
  if (!s) return null;
  const direct = s.indexOf(q);
  if (direct >= 0) {
    let score = 1000 - direct * 2 - (s.length - q.length) * 0.5;
    if (direct === 0) score += 200;
    else if (BOUNDARY.test(s[direct - 1])) score += 100;
    return score;
  }
  const needle = q.replace(/ /g, '');
  let score = 0;
  let last = -1;
  let streak = 0;
  for (const ch of needle) {
    const index = s.indexOf(ch, last + 1);
    if (index < 0) return null;
    if (index === last + 1) {
      streak += 1;
      score += 5 + streak * 3;
    } else {
      streak = 0;
      score += 1 - Math.min(index - last - 1, 10) * 0.5;
    }
    if (index === 0 || BOUNDARY.test(s[index - 1])) score += 8;
    last = index;
  }
  return score - s.length * 0.05;
}

export function isCommandEnabled(command) {
  if (typeof command?.enabled === 'function') return Boolean(command.enabled());
  return command?.enabled !== false;
}

export function scoreCommand(command, query) {
  if (!normalizeText(query)) return 0;
  const candidates = [
    [command?.title, 1],
    ...(Array.isArray(command?.keywords) ? command.keywords.map((keyword) => [keyword, 0.85]) : []),
    [command?.subtitle, 0.6],
  ];
  let best = null;
  for (const [text, weight] of candidates) {
    if (!text) continue;
    const score = fuzzyScore(query, text);
    if (score === null) continue;
    const weighted = score * weight;
    if (best === null || weighted > best) best = weighted;
  }
  return best;
}

/** Filters and orders commands for a query; ties keep registration order. */
export function rankCommands(commands, query, { limit = 50 } = {}) {
  const ranked = [];
  (commands || []).forEach((command, index) => {
    const score = scoreCommand(command, query);
    if (score === null) return;
    ranked.push({ command, score, index });
  });
  if (normalizeText(query)) {
    ranked.sort((a, b) => (b.score - a.score) || (a.index - b.index));
  }
  return ranked.slice(0, limit).map(({ command, score }) => ({ command, score }));
}

export function createCommandRegistry() {
  const commands = new Map();
  return {
    register(input) {
      const list = Array.isArray(input) ? input : [input];
      for (const command of list) {
        if (!command?.id) throw new Error('command id is required');
        commands.set(command.id, command);
      }
      return () => {
        for (const command of list) {
          if (commands.get(command.id) === command) commands.delete(command.id);
        }
      };
    },
    list() {
      return Array.from(commands.values());
    },
    get(id) {
      return commands.get(id);
    },
    clear() {
      commands.clear();
    },
  };
}
