(function installMarkdownImagePolicy() {
  if (typeof safeMarkdown !== 'function') {
    throw new Error('markdown image policy requires utils.js');
  }

  // utils.js escapes every generated attribute before this policy sees the
  // rendered string. Convert remote images into inert links only after the
  // complete Markdown fragment has been rendered, so no remote resource is
  // ever inserted into the DOM. Relative/same-origin images remain inline.
  const renderMarkdown = safeMarkdown;
  const remoteImagePattern = /<img\s+class="md-img"\s+src="((?:https?):[^"]*)"\s+alt="([^"]*)"\s+loading="lazy"\s*\/>/gi;

  safeMarkdown = function safeMarkdownWithInertRemoteImages(text) {
    return renderMarkdown(text).replace(remoteImagePattern, (imageHTML, src, alt) => {
      const label = alt || 'image';
      return `<a class="md-img-link" href="${src}" target="_blank" rel="noopener noreferrer">Open image: ${label}</a>`;
    });
  };
})();
