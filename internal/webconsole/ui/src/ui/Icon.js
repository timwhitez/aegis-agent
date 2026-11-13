// Curated inline SVG icons (original 24×24 stroke glyphs, see vendor notices).
// Each entry is a list of path data; entries prefixed "F:" are filled.
import { html } from '../lib/html.js';

const circle = (cx, cy, r) => `M${cx} ${cy - r}a${r} ${r} 0 1 1 0 ${2 * r}a${r} ${r} 0 0 1 0-${2 * r}z`;

export const ICONS = {
  plus: ['M12 5v14', 'M5 12h14'],
  search: [circle(11, 11, 6.5), 'M20 20l-4.4-4.4'],
  'message-square': ['M4.5 6.5a2 2 0 0 1 2-2h11a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H9l-4.5 3.5z'],
  history: ['M4 12a8 8 0 1 0 2.4-5.7', 'M4 4.5v4h4', 'M12 8v4.5l3 1.8'],
  box: ['M4 7.5l8-4 8 4v9l-8 4-8-4z', 'M4 7.5l8 4 8-4', 'M12 11.5v9'],
  folder: ['M3.5 7a2 2 0 0 1 2-2h4l2 2h7a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z'],
  'folder-open': ['M3.5 17V7a2 2 0 0 1 2-2h4l2 2h6.5a2 2 0 0 1 2 2v1', 'M3.5 17l2.3-6.2A1.5 1.5 0 0 1 7.2 10h13.3l-2.6 7a1.5 1.5 0 0 1-1.4 1H4.5a1 1 0 0 1-1-1z'],
  settings: ['M4 7h9', 'M17 7h3', 'M4 17h3', 'M11 17h9', 'M15 4.5v5', 'M9 14.5v5'],
  'panel-left': ['M5 4.5h14a1.5 1.5 0 0 1 1.5 1.5v12a1.5 1.5 0 0 1-1.5 1.5H5A1.5 1.5 0 0 1 3.5 18V6A1.5 1.5 0 0 1 5 4.5z', 'M9.5 4.5v15'],
  'panel-right': ['M5 4.5h14a1.5 1.5 0 0 1 1.5 1.5v12a1.5 1.5 0 0 1-1.5 1.5H5A1.5 1.5 0 0 1 3.5 18V6A1.5 1.5 0 0 1 5 4.5z', 'M14.5 4.5v15'],
  square: ['F:M8 6.5h8A1.5 1.5 0 0 1 17.5 8v8a1.5 1.5 0 0 1-1.5 1.5H8A1.5 1.5 0 0 1 6.5 16V8A1.5 1.5 0 0 1 8 6.5z'],
  hand: ['M8 13V6.5a1.5 1.5 0 0 1 3 0V12', 'M11 11V5a1.5 1.5 0 0 1 3 0v6', 'M14 11V6.5a1.5 1.5 0 0 1 3 0V14a6 6 0 0 1-6 6h-.5a5.5 5.5 0 0 1-4.6-2.5L4.6 14a1.5 1.5 0 0 1 2.4-1.8L8 13.5'],
  'arrow-up': ['M12 19V5', 'M6 11l6-6 6 6'],
  'arrow-down': ['M12 5v14', 'M6 13l6 6 6-6'],
  'arrow-right': ['M4.5 12h14.5', 'M13 6l6 6-6 6'],
  'corner-down-right': ['M5 4.5V11a4 4 0 0 0 4 4h10', 'M15 10.5l4.5 4.5-4.5 4.5'],
  bell: ['M6.5 10.5a5.5 5.5 0 0 1 11 0c0 4.2 1.2 5.8 2 6.5h-15c.8-.7 2-2.3 2-6.5z', 'M10 20a2.2 2.2 0 0 0 4 0'],
  target: [circle(12, 12, 8), circle(12, 12, 4.5), 'F:' + circle(12, 12, 1.2)],
  'clipboard-list': ['M9 3.5h6v3H9z', 'M9 5H7A1.5 1.5 0 0 0 5.5 6.5v12A1.5 1.5 0 0 0 7 20h10a1.5 1.5 0 0 0 1.5-1.5v-12A1.5 1.5 0 0 0 17 5h-2', 'M9 11h6', 'M9 14.5h6', 'M9 18h3.5'],
  check: ['M5 12.5l4.5 4.5L19 7.5'],
  x: ['M6 6l12 12', 'M18 6L6 18'],
  'chevron-down': ['M6 9l6 6 6-6'],
  'chevron-up': ['M6 15l6-6 6 6'],
  'chevron-left': ['M15 6l-6 6 6 6'],
  'chevron-right': ['M9 6l6 6-6 6'],
  copy: ['M10 8.5h8.5a1.5 1.5 0 0 1 1.5 1.5v8.5a1.5 1.5 0 0 1-1.5 1.5H10a1.5 1.5 0 0 1-1.5-1.5V10A1.5 1.5 0 0 1 10 8.5z', 'M15.5 8.5v-3A1.5 1.5 0 0 0 14 4H5.5A1.5 1.5 0 0 0 4 5.5V14a1.5 1.5 0 0 0 1.5 1.5h3'],
  'more-horizontal': ['F:' + circle(6, 12, 1.4), 'F:' + circle(12, 12, 1.4), 'F:' + circle(18, 12, 1.4)],
  loader: ['M12 3.5a8.5 8.5 0 1 0 8.5 8.5'],
  'alert-triangle': ['M10.3 4.9a2 2 0 0 1 3.4 0l7 12.1a2 2 0 0 1-1.7 3H5a2 2 0 0 1-1.7-3z', 'M12 10v3.5', 'M12 16.8h.01'],
  'alert-circle': [circle(12, 12, 8.5), 'M12 8v4.5', 'M12 15.8h.01'],
  info: [circle(12, 12, 8.5), 'M12 11v5', 'M12 8h.01'],
  file: ['M13.5 3.5H7.5A1.5 1.5 0 0 0 6 5v14a1.5 1.5 0 0 0 1.5 1.5h9A1.5 1.5 0 0 0 18 19V8z', 'M13.5 3.5V8H18'],
  'file-text': ['M13.5 3.5H7.5A1.5 1.5 0 0 0 6 5v14a1.5 1.5 0 0 0 1.5 1.5h9A1.5 1.5 0 0 0 18 19V8z', 'M13.5 3.5V8H18', 'M9 12.5h6', 'M9 16h6'],
  upload: ['M12 15V4.5', 'M7.5 9L12 4.5 16.5 9', 'M4.5 15.5V18a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2v-2.5'],
  download: ['M12 4.5V15', 'M7.5 10.5L12 15l4.5-4.5', 'M4.5 15.5V18a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2v-2.5'],
  pencil: ['M15.5 5.5l3 3L8.5 18.5l-4 1 1-4z', 'M13.5 7.5l3 3'],
  trash: ['M4.5 7h15', 'M9.5 7V5a1 1 0 0 1 1-1h3a1 1 0 0 1 1 1v2', 'M6.5 7l.8 11.6a1.5 1.5 0 0 0 1.5 1.4h6.4a1.5 1.5 0 0 0 1.5-1.4L17.5 7', 'M10 11v5.5', 'M14 11v5.5'],
  refresh: ['M19.5 12a7.5 7.5 0 0 1-13 5.1', 'M4.5 12a7.5 7.5 0 0 1 13-5.1', 'M17.5 3.5v3.4h-3.4', 'M6.5 20.5v-3.4h3.4'],
  command: ['M9 9V6.5A2.5 2.5 0 1 0 6.5 9H9z', 'M15 9V6.5A2.5 2.5 0 1 1 17.5 9H15z', 'M9 15v2.5A2.5 2.5 0 1 1 6.5 15H9z', 'M15 15v2.5a2.5 2.5 0 1 0 2.5-2.5H15z', 'M9 9h6v6H9z'],
  keyboard: ['M4.5 6.5h15A1.5 1.5 0 0 1 21 8v8a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 16V8a1.5 1.5 0 0 1 1.5-1.5z', 'M7 10h.01', 'M10.5 10h.01', 'M14 10h.01', 'M17 10h.01', 'M8 14h8'],
  globe: [circle(12, 12, 8.5), 'M3.5 12h17', 'M12 3.5c2.3 2.4 3.5 5.2 3.5 8.5s-1.2 6.1-3.5 8.5c-2.3-2.4-3.5-5.2-3.5-8.5S9.7 5.9 12 3.5z'],
  terminal: ['M4.5 5h15A1.5 1.5 0 0 1 21 6.5v11a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5v-11A1.5 1.5 0 0 1 4.5 5z', 'M7 9.5l3 2.5-3 2.5', 'M12.5 15H17'],
  wrench: ['M14.5 5.5a4 4 0 0 0-4.9 5.2l-5.1 5.1a1.8 1.8 0 0 0 2.5 2.5l5.1-5.1a4 4 0 0 0 5.2-4.9l-2.6 2.6-2.4-.6-.6-2.4z'],
  brain: ['M9.5 4.5A2.5 2.5 0 0 0 7 7a2.5 2.5 0 0 0-2 4 2.5 2.5 0 0 0 .5 4.5 2.5 2.5 0 0 0 4 2.5V4.5z', 'M14.5 4.5A2.5 2.5 0 0 1 17 7a2.5 2.5 0 0 1 2 4 2.5 2.5 0 0 1-.5 4.5 2.5 2.5 0 0 1-4 2.5V4.5z'],
  'git-branch': ['M7 4.5v11', circle(7, 18, 2.5), circle(17, 6.5, 2.5), 'M17 9c0 4-4.5 4.5-9.5 6.8'],
  users: [circle(9, 7.5, 3.5), 'M3 20c.6-3.4 3-5.5 6-5.5s5.4 2.1 6 5.5', 'M16 4.3a3.5 3.5 0 0 1 0 6.4', 'M17.5 14.7c2 .8 3.2 2.6 3.5 5.3'],
  clock: [circle(12, 12, 8.5), 'M12 7.5V12l3 2'],
  'external-link': ['M13.5 4.5h6v6', 'M19.5 4.5L11 13', 'M17.5 13.5V18a1.5 1.5 0 0 1-1.5 1.5H6A1.5 1.5 0 0 1 4.5 18V8A1.5 1.5 0 0 1 6 6.5h4.5'],
  eye: ['M2.5 12s3.5-6.5 9.5-6.5 9.5 6.5 9.5 6.5-3.5 6.5-9.5 6.5S2.5 12 2.5 12z', circle(12, 12, 2.5)],
  'list-checks': ['M11 6.5h9', 'M11 12h9', 'M11 17.5h9', 'M4 6.5l1.5 1.5 3-3', 'M4 12l1.5 1.5 3-3', 'M4 17.5l1.5 1.5 3-3'],
  sparkles: ['M12 3.5l1.6 4.4 4.4 1.6-4.4 1.6-1.6 4.4-1.6-4.4L6 9.5l4.4-1.6z', 'M18.5 15l.7 1.8 1.8.7-1.8.7-.7 1.8-.7-1.8-1.8-.7 1.8-.7z'],
  'circle-dot': [circle(12, 12, 8.5), 'F:' + circle(12, 12, 2.5)],
  pause: ['M8.5 5.5v13', 'M15.5 5.5v13'],
  play: ['M7.5 5.5v13l11-6.5z'],
  'log-in': ['M14 4.5h4a1.5 1.5 0 0 1 1.5 1.5v12a1.5 1.5 0 0 1-1.5 1.5h-4', 'M10 16l4-4-4-4', 'M14 12H4.5'],
};

/**
 * <Icon name="plus" size={16} label?="..." />. Decorative unless label is set.
 * Unknown names render an empty box so layout stays stable.
 */
export function Icon({ name, size = 16, label = '', class: className = '', strokeWidth = 1.75 }) {
  const paths = ICONS[name] || [];
  const a11y = label ? { role: 'img', 'aria-label': label } : { 'aria-hidden': 'true' };
  return html`<svg
    class=${`icon icon-${name} ${className}`.trim()}
    width=${size}
    height=${size}
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width=${strokeWidth}
    stroke-linecap="round"
    stroke-linejoin="round"
    focusable="false"
    ...${a11y}
  >
    ${paths.map((d) => (d.startsWith('F:')
      ? html`<path d=${d.slice(2)} fill="currentColor" stroke="none" />`
      : html`<path d=${d} />`))}
  </svg>`;
}
