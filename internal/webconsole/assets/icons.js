(function registerLocalIcons() {
  const glyphs = {
    zap: 'M13 2 4 14h7l-1 8 9-12h-7l1-8z',
    'layout-dashboard': 'M3 3h8v8H3z M13 3h8v5h-8z M13 10h8v11h-8z M3 13h8v8H3z',
    'message-square': 'M4 5h16v11H8l-4 4z',
    'list-checks': 'M9 6h12 M9 12h12 M9 18h12 M3 6l1 1 2-2 M3 12l1 1 2-2 M3 18l1 1 2-2',
    history: 'M3 12a9 9 0 1 0 3-6.7 M3 4v5h5 M12 7v6l4 2',
    box: 'M4 7l8-4 8 4v10l-8 4-8-4z M4 7l8 4 8-4 M12 11v10',
    'folder-git-2': 'M3 6h7l2 2h9v11H3z M9 13h6 M12 10v6',
    settings: 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z M4 12h2 M18 12h2 M12 4v2 M12 18v2',
    square: 'M6 6h12v12H6z',
    hand: 'M8 12V6a2 2 0 0 1 4 0v5 M12 11V5a2 2 0 0 1 4 0v7 M16 12V8a2 2 0 0 1 4 0v6a7 7 0 0 1-14 0v-2z',
    plus: 'M12 5v14 M5 12h14',
    siren: 'M7 18h10 M8 14v-3a4 4 0 0 1 8 0v3 M5 10l-2-2 M19 10l2-2 M12 4V2',
    send: 'M3 11l18-8-8 18-2-7z M11 14l10-11',
    trash: 'M4 7h16 M9 7V5h6v2 M7 7l1 13h8l1-13',
    'trash-2': 'M4 7h16 M9 7V5h6v2 M7 7l1 13h8l1-13',
    'chevron-left': 'M15 6l-6 6 6 6',
    'chevron-right': 'M9 6l6 6-6 6',
    'x-circle': 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z M9 9l6 6 M15 9l-6 6',
    'check-check': 'M3 12l4 4 7-8 M13 15l2 2 6-7',
    user: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8z M4 21a8 8 0 0 1 16 0',
    bot: 'M7 8h10v8H7z M9 12h.01 M15 12h.01 M12 4v4 M8 20h8',
    terminal: 'M4 5h16v14H4z M7 9l3 3-3 3 M12 15h5'
  };

  function svgFor(name) {
    const path = glyphs[name] || glyphs.box;
    return `<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="${path}"></path></svg>`;
  }

  window.lucide = window.lucide || {};
  window.lucide.createIcons = function createIcons(options = {}) {
    const root = options.root || document;
    root.querySelectorAll('i[data-lucide]').forEach((node) => {
      const name = node.getAttribute('data-lucide') || 'box';
      node.innerHTML = svgFor(name);
      node.classList.add('local-icon');
    });
  };
})();
