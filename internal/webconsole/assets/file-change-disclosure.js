(function installFileChangeDisclosure() {
  if (typeof renderFileChangesFloat !== 'function') {
    throw new Error('file-change disclosure requires session-view.js');
  }

  const renderFileChanges = renderFileChangesFloat;
  const titleMarker = '<span class="tf-title">Files</span>';
  const disclosureChip = '<span class="tf-sum-chip tf-file-partial" title="Best-effort view: dedicated file tools are accounted directly; shell changes are inferred from recognized redirects and may be incomplete.">partial</span>';
  const bodyMarker = '<div class="tf-body">';
  const disclosureNote = '<div class="tf-file-disclosure" role="note">Best-effort view. Dedicated file tools are accounted directly; shell changes are inferred from recognized redirects and may be incomplete.</div>';

  renderFileChangesFloat = function renderFileChangesFloatWithDisclosure() {
    const html = renderFileChanges();
    if (!html) {
      return html;
    }
    return html
      .replace(titleMarker, titleMarker + disclosureChip)
      .replace(bodyMarker, bodyMarker + disclosureNote);
  };
})();
