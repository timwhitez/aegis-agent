(() => {
  const primaryNewSession = document.getElementById('v2-new-session-btn');
  const canonicalNewSession = document.getElementById('new-session-btn');
  primaryNewSession?.addEventListener('click', () => canonicalNewSession?.click());
  document.documentElement.dataset.uiReady = 'true';
})();
