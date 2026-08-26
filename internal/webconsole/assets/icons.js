(function registerLocalIcons() {
  const glyphs = {
    zap: 'M13 2 4 14h7l-1 8 9-12h-7l1-8z',
    shield: 'M12 2l8 4v6c0 5.5-3.8 10.7-8 12-4.2-1.3-8-6.5-8-12V6l8-4z',
    'layout-dashboard': 'M3 3h8v8H3z M13 3h8v5h-8z M13 10h8v11h-8z M3 13h8v8H3z',
    'message-square': 'M4 5h16v11H8l-4 4z',
    'list-checks': 'M9 6h12 M9 12h12 M9 18h12 M3 6l1 1 2-2 M3 12l1 1 2-2 M3 18l1 1 2-2',
    history: 'M3 12a9 9 0 1 0 3-6.7 M3 4v5h5 M12 7v6l4 2',
    box: 'M4 7l8-4 8 4v10l-8 4-8-4z M4 7l8 4 8-4 M12 11v10',
    folder: 'M3 6h7l2 2h9v11H3z',
    'folder-plus': 'M3 6h7l2 2h9v11H3z M12 12v5 M9.5 14.5h5',
    'file-code': 'M6 3h8l4 4v14H6z M14 3v5h5 M10 12l-2 2 2 2 M14 12l2 2-2 2',
    'corner-up-left': 'M9 14l-4-4 4-4 M5 10h10a4 4 0 0 1 4 4v4',
    'rotate-cw': 'M21 12a9 9 0 1 1-3-6.7 M21 3v6h-6',
    download: 'M12 3v11 M7 9l5 5 5-5 M5 20h14',
    'folder-git-2': 'M3 6h7l2 2h9v11H3z M9 13h6 M12 10v6',
    settings: 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z M4 12h2 M18 12h2 M12 4v2 M12 18v2',
    square: 'M6 6h12v12H6z',
    hand: 'M8 12V6a2 2 0 0 1 4 0v5 M12 11V5a2 2 0 0 1 4 0v7 M16 12V8a2 2 0 0 1 4 0v6a7 7 0 0 1-14 0v-2z',
    plus: 'M12 5v14 M5 12h14',
    siren: 'M7 18h10 M8 14v-3a4 4 0 0 1 8 0v3 M5 10l-2-2 M19 10l2-2 M12 4V2',
    send: 'M3 11l18-8-8 18-2-7z M11 14l10-11',
    'arrow-up': 'M12 19V5 M6 11l6-6 6 6',
    trash: 'M4 7h16 M9 7V5h6v2 M7 7l1 13h8l1-13',
    'trash-2': 'M4 7h16 M9 7V5h6v2 M7 7l1 13h8l1-13',
    'chevron-left': 'M15 6l-6 6 6 6',
    'chevron-right': 'M9 6l6 6-6 6',
    'x-circle': 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z M9 9l6 6 M15 9l-6 6',
    'check-check': 'M3 12l4 4 7-8 M13 15l2 2 6-7',
    user: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8z M4 21a8 8 0 0 1 16 0',
    bot: 'M7 8h10v8H7z M9 12h.01 M15 12h.01 M12 4v4 M8 20h8',
    terminal: 'M4 5h16v14H4z M7 9l3 3-3 3 M12 15h5',
    'panel-right': 'M4 5h16v14H4z M14 5v14',
    'git-branch': 'M6 3v12 M6 15a3 3 0 1 0 0 6 3 3 0 0 0 0-6z M18 6a3 3 0 1 0 0-6 3 3 0 0 0 0 6z M18 6v-1a6 6 0 0 1-6 6H6',
    wrench: 'M14.05 14.05 7.33 7.33a6 6 0 0 1 7.99-7.99l-3.66 3.66.71.71 3.66-3.66a6 6 0 0 1 7.99 7.99l-6.37 6.37a6 6 0 0 1-7.99-7.99l3.73 3.73',
    sparkles: 'M12 3v2 M12 19v2 M3 12h2 M19 12h2 M7.05 7.05l1.41 1.41 M15.54 15.54l1.41 1.41 M7.05 16.95l1.41-1.41 M15.54 8.46l1.41-1.41 M8 12a4 4 0 1 0 8 0 4 4 0 0 0-8 0z',
    radio: 'M4.9 19.1C2.4 16.6 1 13.4 1 10s1.4-6.6 3.9-9.1 M19.1 4.9A13.1 13.1 0 0 1 23 10a13.1 13.1 0 0 1-3.9 9.1 M5 2l1 2M5 20l1-2M19 2l-1 2M19 20l-1 2M12 6v4l3 3',
    'package-search': 'M4 7l8-4 8 4v10l-8 4-8-4z M4 7l8 4 8-4 M12 11v10 M21 21l-2-2 M18 18a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
    'shield-alert': 'M12 2l8 4v6c0 5.5-3.8 10.7-8 12-4.2-1.3-8-6.5-8-12V6l8-4z M12 8v4 M12 16h.01',
    'git-branch-plus': 'M6 3v12 M6 15a3 3 0 1 0 0 6 3 3 0 0 0 0-6z M18 6a3 3 0 1 0 0-6 3 3 0 0 0 0 6z M18 6v-1a6 6 0 0 1-6 6H6 M15 9h6 M18 6v6',
    'list-todo': 'M9 6h12 M9 12h12 M9 18h12 M3 5l1 1 2-2 M3 11l1 1 2-2 M3 17l1 1 2-2',
    inbox: 'M3 4h18v6l-2 6H5L3 10V4z M3 10h6v2h6v-2h6',
    'corner-down-left': 'M15 10l-6 6-6-6 M17 4v6a6 6 0 0 1-6 6H3',
    ban: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z M5.64 5.64l12.72 12.72',
    'pause-circle': 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z M9 9v6 M15 9v6',
    dot: 'M12 12h.01',
    'check-circle-2': 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z M9 12l2 2 4-4'
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
