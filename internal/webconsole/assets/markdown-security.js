(function installMarkdownImagePolicy() {
  if (typeof safeMarkdown !== 'function') {
    throw new Error('markdown image policy requires utils.js');
  }

  // utils.js escapes every generated attribute before this policy sees the
  // rendered string. Inspect every generated Markdown image, not only absolute
  // HTTP URLs: browsers normalize backslashes in special URLs, so a value such
  // as /\attacker.example/pixel can become a cross-origin request even though
  // it initially looks root-relative.
  const renderMarkdown = safeMarkdown;
  const markdownImagePattern = /<img\s+class="md-img"\s+src="([^"]*)"\s+alt="([^"]*)"\s+loading="lazy"\s*\/>/gi;

  function isExplicitSameOriginImageSource(src) {
    const value = String(src || '').trim();
    if (!value || value.includes('\\')) {
      return false;
    }
    if (/^[a-z][a-z0-9+.-]*:/i.test(value) || value.startsWith('//')) {
      return false;
    }
    return value.startsWith('/') || value.startsWith('./') || value.startsWith('../');
  }

  safeMarkdown = function safeMarkdownWithInertRemoteImages(text) {
    return renderMarkdown(text).replace(markdownImagePattern, (imageHTML, src, alt) => {
      if (isExplicitSameOriginImageSource(src)) {
        return imageHTML;
      }
      const label = alt || 'image';
      if (/^https?:/i.test(src)) {
        return `<a class="md-img-link" href="${src}" target="_blank" rel="noopener noreferrer">Open image: ${label}</a>`;
      }
      return `<span class="md-img-blocked">Blocked image: ${label}</span>`;
    });
  };
})();
