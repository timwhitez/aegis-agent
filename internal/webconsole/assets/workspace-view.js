const WORKSPACE_FILE_PREVIEW_CHUNK_SIZE = 256 * 1024;

const workspaceViewState = {
  requestSeq: 0,
  path: '',
  tree: [],
  selectedTreePath: '',
  selectedPaths: new Set(),
  filePreview: null,
  syncedSessionWorkdir: '',
  actionPending: ''
};

async function fetchWorkspace() {
  try {
    ensureWorkspaceActionBindings();
    if (!state.meta) {
      await refreshMeta().catch(() => {});
    }
    const sessionPath = currentSessionWorkspacePath();
    if (sessionPath !== null && sessionPath !== currentWorkspacePath()) {
      setCurrentWorkspacePath(sessionPath);
      setSelectedWorkspaceTreePath('');
      clearSelectedWorkspacePaths();
      setWorkspaceFilePreview(null);
    }
    workspaceViewState.syncedSessionWorkdir = currentSessionWorkdir();
    updateWorkspaceMeta();
    nodes.fileTree.innerHTML = '<div class="view-loading">Loading workspace…</div>';
    nodes.editorFilename.innerText = workspaceDisplayName();
    nodes.editorContent.innerText = 'Choose a file or directory to inspect inside the current server workspace.';
    renderWorkspaceActions();
    await loadWorkspaceDirectory(currentWorkspacePath());
  } catch (err) {
    console.error('workspace error', err);
    const message = workspaceErrorMessage(err);
    nodes.fileTree.innerHTML = '<div class="empty-panel">Failed to load workspace.</div>';
    nodes.editorFilename.innerText = 'Workspace';
    nodes.editorContent.innerText = message;
    renderWorkspaceActions();
    showToast(message, 'error');
  }
}

function syncWorkspaceToCurrentSession(options = {}) {
  const sessionWorkdir = currentSessionWorkdir();
  if (workspaceViewState.syncedSessionWorkdir === sessionWorkdir && !options.force) {
    return;
  }
  const sessionPath = currentSessionWorkspacePath();
  if (sessionPath === null) {
    return;
  }
  workspaceViewState.syncedSessionWorkdir = sessionWorkdir;
  if (sessionPath === currentWorkspacePath()) {
    updateWorkspaceMeta();
    return;
  }
  setCurrentWorkspacePath(sessionPath);
  setSelectedWorkspaceTreePath('');
  clearSelectedWorkspacePaths();
  setWorkspaceFilePreview(null);
  updateWorkspaceMeta();
  if (currentViewName() === 'workspace') {
    fetchWorkspace();
  }
}

function currentSessionWorkdir() {
  return String(state.sessionDetail?.metadata?.workdir || '').trim();
}

function currentSessionWorkspacePath() {
  const root = String(state.meta?.workspace_root || '').trim();
  const workdir = currentSessionWorkdir();
  if (!root || !workdir) {
    return '';
  }
  const rootPath = normalizeAbsoluteWorkspacePath(root);
  const workdirPath = normalizeAbsoluteWorkspacePath(workdir);
  if (workdirPath === rootPath) {
    return '';
  }
  const prefix = rootPath.endsWith('/') ? rootPath : `${rootPath}/`;
  if (!workdirPath.startsWith(prefix)) {
    return null;
  }
  return normalizeWorkspacePath(workdirPath.slice(prefix.length));
}

function normalizeAbsoluteWorkspacePath(value) {
  return String(value || '')
    .replace(/\\/g, '/')
    .replace(/\/+$/g, '');
}

function workspaceErrorMessage(err, fallback = 'Failed to load workspace.') {
  return err?.message || fallback;
}

function updateWorkspaceMeta() {
  if (nodes.workspaceSubtitle) {
    if (state.meta?.workspace_switch_supported) {
      nodes.workspaceSubtitle.textContent = 'Browse the active workspace and switch roots when needed.';
    } else {
      nodes.workspaceSubtitle.textContent = 'Browsing the current server workspace only. Switching roots is not available in this view.';
    }
  }
  if (nodes.workspaceRootChip) {
    const root = String(state.meta?.workspace_root || '').trim();
    const selected = selectedWorkspaceWorkdir();
    nodes.workspaceRootChip.textContent = selected || root || 'current cwd';
    nodes.workspaceRootChip.title = selected || root || 'current cwd';
  }
}

async function loadWorkspaceDirectory(path = '') {
  const normalized = normalizeWorkspacePath(path);
  const queryPath = normalized || '.';
  const requestSeq = nextWorkspaceRequestSeq();
  nodes.fileTree.innerHTML = '<div class="view-loading">Loading workspace…</div>';
  let tree;
  try {
    tree = await listWorkspaceFiles(queryPath);
  } catch (err) {
    if (workspaceViewState.requestSeq !== requestSeq) {
      return;
    }
    throw err;
  }
  if (workspaceViewState.requestSeq !== requestSeq) {
    return;
  }
  setCurrentWorkspacePath(normalized);
  setCurrentWorkspaceTree(tree);
  setSelectedWorkspaceTreePath('');
  clearSelectedWorkspacePaths();
  setWorkspaceFilePreview(null);
  renderFileTree(tree);
  updateWorkspaceMeta();
  renderWorkspaceActions();
  nodes.editorFilename.innerText = workspaceDisplayName();
  nodes.editorContent.innerText = 'Choose a file or directory to inspect inside the current server workspace.';
}

function nextWorkspaceRequestSeq() {
  return ++workspaceViewState.requestSeq;
}

function currentWorkspacePath() {
  return workspaceViewState.path || '';
}

function setCurrentWorkspacePath(path) {
  workspaceViewState.path = normalizeWorkspacePath(path);
}

function currentWorkspaceTree() {
  return Array.isArray(workspaceViewState.tree) ? workspaceViewState.tree : [];
}

function setCurrentWorkspaceTree(tree) {
  workspaceViewState.tree = Array.isArray(tree) ? tree : [];
}

function selectedWorkspaceTreePath() {
  return workspaceViewState.selectedTreePath || '';
}

function setSelectedWorkspaceTreePath(path) {
  workspaceViewState.selectedTreePath = String(path || '');
}

function selectedWorkspacePaths() {
  const selected = workspaceViewState.selectedPaths;
  if (!(selected instanceof Set)) {
    workspaceViewState.selectedPaths = new Set();
    return [];
  }
  return Array.from(selected).sort((left, right) => left.localeCompare(right));
}

function selectedWorkspacePathCount() {
  return selectedWorkspacePaths().length;
}

function isWorkspacePathSelected(path) {
  const selected = workspaceViewState.selectedPaths;
  return selected instanceof Set && selected.has(normalizeWorkspacePath(path));
}

function clearSelectedWorkspacePaths() {
  workspaceViewState.selectedPaths = new Set();
}

function toggleWorkspacePathSelection(path, checked) {
  const normalized = normalizeWorkspacePath(path);
  if (!normalized) {
    return;
  }
  const selected = workspaceViewState.selectedPaths instanceof Set ? workspaceViewState.selectedPaths : new Set();
  if (checked) {
    selected.add(normalized);
  } else {
    selected.delete(normalized);
  }
  workspaceViewState.selectedPaths = selected;
  syncWorkspaceSelectionNode(normalized);
  renderWorkspaceActions();
}

function syncWorkspaceSelectionNode(path) {
  if (!nodes.fileTree) {
    return;
  }
  const selector = `.workspace-select-checkbox[data-path="${cssEscape(path)}"]`;
  const input = nodes.fileTree.querySelector(selector);
  if (!input) {
    return;
  }
  const selected = isWorkspacePathSelected(path);
  input.checked = selected;
  input.setAttribute('aria-checked', selected ? 'true' : 'false');
  const row = input.closest('.tree-node-row');
  if (row) {
    row.classList.toggle('is-selected', selected);
  }
}

function workspaceFilePreview() {
  return workspaceViewState.filePreview || null;
}

function setWorkspaceFilePreview(preview) {
  workspaceViewState.filePreview = preview || null;
}

function workspaceActionPending() {
  return workspaceViewState.actionPending || '';
}

function setWorkspaceActionPending(action) {
  workspaceViewState.actionPending = String(action || '');
  renderWorkspaceActions();
}

function resetWorkspaceSessionSync() {
  workspaceViewState.syncedSessionWorkdir = '';
}

function normalizeWorkspacePath(path = '') {
  const normalized = String(path || '')
    .replace(/\\/g, '/')
    .replace(/^\.\//, '')
    .replace(/^\/+|\/+$/g, '');
  return normalized === '.' ? '' : normalized;
}

function workspaceDisplayName() {
  const path = currentWorkspacePath();
  return path ? `Workspace / ${path}` : 'Workspace';
}

function selectedWorkspaceWorkdir() {
  const root = String(state.meta?.workspace_root || '').trim();
  if (!root) {
    return '';
  }
  const rel = normalizeWorkspacePath(currentWorkspacePath());
  return rel ? `${root.replace(/\/+$/g, '')}/${rel}` : root;
}

function ensureWorkspaceActionBindings() {
  if (nodes.workspaceNewFolderBtn && nodes.workspaceNewFolderBtn.dataset.bound !== '1') {
    nodes.workspaceNewFolderBtn.dataset.bound = '1';
    nodes.workspaceNewFolderBtn.addEventListener('click', handleCreateWorkspaceFolder);
  }
  if (nodes.workspaceRefreshBtn && nodes.workspaceRefreshBtn.dataset.bound !== '1') {
    nodes.workspaceRefreshBtn.dataset.bound = '1';
    nodes.workspaceRefreshBtn.addEventListener('click', handleRefreshWorkspace);
  }
  if (nodes.workspaceDeleteDirBtn && nodes.workspaceDeleteDirBtn.dataset.bound !== '1') {
    nodes.workspaceDeleteDirBtn.dataset.bound = '1';
    nodes.workspaceDeleteDirBtn.addEventListener('click', handleDeleteCurrentWorkspaceDirectory);
  }
  if (nodes.workspaceDownloadBtn && nodes.workspaceDownloadBtn.dataset.bound !== '1') {
    nodes.workspaceDownloadBtn.dataset.bound = '1';
    nodes.workspaceDownloadBtn.addEventListener('click', handleDownloadSelectedWorkspaceFile);
  }
  if (nodes.workspaceDeleteFileBtn && nodes.workspaceDeleteFileBtn.dataset.bound !== '1') {
    nodes.workspaceDeleteFileBtn.dataset.bound = '1';
    nodes.workspaceDeleteFileBtn.addEventListener('click', handleDeleteSelectedWorkspaceFile);
  }
  if (nodes.workspaceDeleteSelectedBtn && nodes.workspaceDeleteSelectedBtn.dataset.bound !== '1') {
    nodes.workspaceDeleteSelectedBtn.dataset.bound = '1';
    nodes.workspaceDeleteSelectedBtn.addEventListener('click', handleDeleteSelectedWorkspaceItems);
  }
}

function renderWorkspaceActions() {
  const pending = workspaceActionPending();
  const currentPath = currentWorkspacePath();
  const preview = workspaceFilePreview();
  const hasFile = Boolean(preview?.path);
  const selectedCount = selectedWorkspacePathCount();
  const hasSelection = selectedCount > 0;
  setWorkspaceButtonState(nodes.workspaceNewFolderBtn, {
    visible: true,
    disabled: Boolean(pending),
    busy: pending === 'mkdir'
  });
  setWorkspaceButtonState(nodes.workspaceRefreshBtn, {
    visible: true,
    disabled: Boolean(pending),
    busy: pending === 'refresh'
  });
  setWorkspaceButtonState(nodes.workspaceDeleteDirBtn, {
    visible: Boolean(currentPath),
    disabled: Boolean(pending) || !currentPath,
    busy: pending === 'delete-dir'
  });
  if (nodes.workspaceDeleteDirBtn) {
    const label = currentPath ? `Delete folder ${currentPath}` : 'Delete current folder';
    nodes.workspaceDeleteDirBtn.setAttribute('aria-label', label);
    nodes.workspaceDeleteDirBtn.title = label;
  }
  setWorkspaceButtonState(nodes.workspaceDownloadBtn, {
    visible: hasFile,
    disabled: Boolean(pending) || !hasFile,
    busy: false
  });
  if (nodes.workspaceSelectedChip) {
    nodes.workspaceSelectedChip.classList.toggle('is-hidden', !hasSelection);
    nodes.workspaceSelectedChip.textContent = selectedCount === 1 ? '1 selected' : `${selectedCount} selected`;
  }
  setWorkspaceButtonState(nodes.workspaceDeleteSelectedBtn, {
    visible: hasSelection,
    disabled: Boolean(pending) || !hasSelection,
    busy: pending === 'delete-selected'
  });
  if (nodes.workspaceDeleteSelectedBtn && hasSelection) {
    nodes.workspaceDeleteSelectedBtn.title = selectedCount === 1 ? 'Delete selected item' : `Delete ${selectedCount} selected items`;
  }
  setWorkspaceButtonState(nodes.workspaceDeleteFileBtn, {
    visible: hasFile && !hasSelection,
    disabled: Boolean(pending) || !hasFile,
    busy: pending === 'delete-file'
  });
  if (nodes.workspaceDownloadBtn && hasFile) {
    nodes.workspaceDownloadBtn.title = `Download ${preview.path}`;
  }
  if (nodes.workspaceDeleteFileBtn && hasFile) {
    nodes.workspaceDeleteFileBtn.title = `Delete ${preview.path}`;
  }
}

function setWorkspaceButtonState(button, options = {}) {
  if (!button) {
    return;
  }
  button.classList.toggle('is-hidden', options.visible === false);
  button.disabled = Boolean(options.disabled);
  button.classList.toggle('is-loading', Boolean(options.busy));
  button.setAttribute('aria-busy', options.busy ? 'true' : 'false');
}

async function handleCreateWorkspaceFolder() {
  if (workspaceActionPending()) {
    return;
  }
  const parentPath = currentWorkspacePath();
  const parentLabel = parentPath || 'workspace root';
  const name = window.prompt(`New folder in ${parentLabel}`, '');
  if (name === null) {
    return;
  }
  const trimmed = String(name || '').trim();
  if (!trimmed) {
    showToast('Folder name is required.', 'error');
    return;
  }
  if (trimmed === '.' || trimmed === '..' || /[\\/]/.test(trimmed)) {
    showToast('Folder name must not contain path separators.', 'error');
    return;
  }
  setWorkspaceActionPending('mkdir');
  try {
    await createWorkspaceDirectory(parentPath || '.', trimmed);
    showToast(`Created folder ${trimmed}.`, 'success');
    await loadWorkspaceDirectory(parentPath);
  } catch (err) {
    showToast(workspaceErrorMessage(err, 'Failed to create folder.'), 'error');
  } finally {
    setWorkspaceActionPending('');
  }
}

async function handleRefreshWorkspace() {
  if (workspaceActionPending()) {
    return;
  }
  setWorkspaceActionPending('refresh');
  try {
    await loadWorkspaceDirectory(currentWorkspacePath());
    showToast('Workspace refreshed.', 'success');
  } catch (err) {
    showToast(workspaceErrorMessage(err, 'Failed to refresh workspace.'), 'error');
  } finally {
    setWorkspaceActionPending('');
  }
}

async function handleDeleteCurrentWorkspaceDirectory() {
  const path = currentWorkspacePath();
  if (!path || workspaceActionPending()) {
    return;
  }
  const ok = await confirmLocalAction({
    title: 'Delete folder',
    message: `Delete workspace folder ${path} and all of its contents? This cannot be undone from the web console.`,
    confirmLabel: 'Delete',
    tone: 'danger'
  });
  if (!ok) {
    return;
  }
  const parent = parentWorkspacePath(path);
  setWorkspaceActionPending('delete-dir');
  try {
    await deleteWorkspacePath(path);
    showToast(`Deleted folder ${path}.`, 'success');
    await loadWorkspaceDirectory(parent);
  } catch (err) {
    showToast(workspaceErrorMessage(err, 'Failed to delete folder.'), 'error');
  } finally {
    setWorkspaceActionPending('');
  }
}

function handleDownloadSelectedWorkspaceFile() {
  const preview = workspaceFilePreview();
  if (!preview?.path) {
    return;
  }
  const anchor = document.createElement('a');
  anchor.href = workspaceDownloadURL(preview.path);
  anchor.download = preview.path.split('/').pop() || 'download';
  anchor.rel = 'noopener';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  showToast(`Download started for ${preview.path}.`, 'success');
}

async function handleDeleteSelectedWorkspaceFile() {
  const preview = workspaceFilePreview();
  if (!preview?.path || workspaceActionPending()) {
    return;
  }
  const ok = await confirmLocalAction({
    title: 'Delete file',
    message: `Delete workspace file ${preview.path}? This cannot be undone from the web console.`,
    confirmLabel: 'Delete',
    tone: 'danger'
  });
  if (!ok) {
    return;
  }
  setWorkspaceActionPending('delete-file');
  try {
    await deleteWorkspacePath(preview.path);
    showToast(`Deleted file ${preview.path}.`, 'success');
    setSelectedWorkspaceTreePath('');
    setWorkspaceFilePreview(null);
    await loadWorkspaceDirectory(currentWorkspacePath());
  } catch (err) {
    showToast(workspaceErrorMessage(err, 'Failed to delete file.'), 'error');
  } finally {
    setWorkspaceActionPending('');
  }
}

async function handleDeleteSelectedWorkspaceItems() {
  const paths = selectedWorkspacePaths();
  if (!paths.length || workspaceActionPending()) {
    return;
  }
  const ok = await confirmLocalAction({
    title: 'Delete selected items',
    message: `Delete ${paths.length} selected workspace item${paths.length === 1 ? '' : 's'}? Folders will be removed with all of their contents. This cannot be undone from the web console.`,
    confirmLabel: 'Delete selected',
    tone: 'danger'
  });
  if (!ok) {
    return;
  }
  setWorkspaceActionPending('delete-selected');
  try {
    const result = await deleteWorkspacePaths(paths);
    const count = Number(result?.count || paths.length);
    clearSelectedWorkspacePaths();
    setSelectedWorkspaceTreePath('');
    setWorkspaceFilePreview(null);
    showToast(count === 1 ? 'Deleted 1 selected item.' : `Deleted ${count} selected items.`, 'success');
    await loadWorkspaceDirectory(currentWorkspacePath());
  } catch (err) {
    showToast(workspaceErrorMessage(err, 'Failed to delete selected items.'), 'error');
  } finally {
    setWorkspaceActionPending('');
  }
}

function parentWorkspacePath(path) {
  const normalized = normalizeWorkspacePath(path);
  const index = normalized.lastIndexOf('/');
  return index > 0 ? normalized.slice(0, index) : '';
}

function renderFileTree(tree, container = nodes.fileTree, level = 0) {
  if (level === 0) {
    container.innerHTML = '';
    ensureFileTreeDelegation(container);
    container.setAttribute('role', 'tree');
    container.setAttribute('aria-label', 'Workspace files');
  }
  if (!Array.isArray(tree)) {
    return;
  }
  if (level === 0 && tree.length === 0) {
    container.innerHTML = currentWorkspacePath()
      ? '<div class="empty-panel">This directory is empty.</div>'
      : '<div class="empty-panel">This workspace is empty.</div>';
    return;
  }

  tree.forEach((node) => {
    const itemWrapper = document.createElement('div');
    itemWrapper.className = 'tree-entry';
    const row = document.createElement('div');
    row.className = 'tree-node-row';
    row.style.paddingLeft = `${8 + level * 16}px`;
    const canSelect = node.navigation !== 'parent' && (node.type === 'directory' || node.type === 'file') && Boolean(node.path);
    if (canSelect) {
      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox';
      checkbox.className = 'workspace-select-checkbox';
      checkbox.dataset.path = node.path || '';
      checkbox.dataset.type = node.type || '';
      checkbox.checked = isWorkspacePathSelected(node.path);
      checkbox.setAttribute('aria-label', `Select ${node.name}`);
      checkbox.setAttribute('aria-checked', checkbox.checked ? 'true' : 'false');
      row.classList.toggle('is-selected', checkbox.checked);
      row.appendChild(checkbox);
    } else {
      const spacer = document.createElement('span');
      spacer.className = 'workspace-select-spacer';
      spacer.setAttribute('aria-hidden', 'true');
      row.appendChild(spacer);
    }
    const button = document.createElement('button');
    button.className = 'tree-node';
    button.dataset.path = node.path || '';
    button.dataset.type = node.type || '';
    button.dataset.navigation = node.navigation || '';
    button.setAttribute('role', 'treeitem');
    button.setAttribute('aria-level', String(level + 1));
    if (node.type === 'directory') {
      button.setAttribute('aria-expanded', 'false');
    }
    if (selectedWorkspaceTreePath() && node.type === 'file' && node.path === selectedWorkspaceTreePath()) {
      button.classList.add('active');
    }
    const icon = node.navigation === 'parent' ? 'corner-up-left' : node.type === 'directory' ? 'folder' : 'file-code';
    button.innerHTML = `<i data-lucide="${icon}" class="icon-small"></i><span>${escapeHTML(node.name)}</span>`;
    const childrenContainer = document.createElement('div');
    childrenContainer.className = 'tree-node-children';
    if (node.type === 'directory') {
      childrenContainer.classList.add('is-collapsed');
    }
    row.appendChild(button);
    itemWrapper.appendChild(row);
    itemWrapper.appendChild(childrenContainer);
    container.appendChild(itemWrapper);
    if (node.type === 'directory' && node.children) {
      renderFileTree(node.children, childrenContainer, level + 1);
    }
  });

  if (level === 0 && window.lucide && lucide.createIcons) {
    lucide.createIcons({ root: nodes.fileTree });
  }
}

function ensureFileTreeDelegation(container) {
  if (!container || container.dataset.delegationBound === '1') {
    return;
  }
  container.dataset.delegationBound = '1';
  container.addEventListener('click', handleFileTreeClick);
  container.addEventListener('keydown', handleFileTreeKeydown);
  container.addEventListener('change', handleFileTreeSelectionChange);
}

async function handleFileTreeClick(event) {
  const button = event.target.closest('.tree-node');
  if (!button || !nodes.fileTree.contains(button) || button.disabled) {
    return;
  }
  event.preventDefault?.();
  event.stopPropagation?.();
  await activateFileTreeNode(button);
}

function handleFileTreeSelectionChange(event) {
  const input = event.target.closest('.workspace-select-checkbox');
  if (!input || !nodes.fileTree.contains(input) || input.disabled) {
    return;
  }
  event.stopPropagation?.();
  toggleWorkspacePathSelection(input.dataset.path || '', input.checked);
}

async function handleFileTreeKeydown(event) {
  const button = event.target.closest('.tree-node');
  if (!button || !nodes.fileTree.contains(button) || button.disabled) {
    return;
  }
  const key = event.key || '';
  if (key === 'Enter' || key === ' ') {
    event.preventDefault?.();
    event.stopPropagation?.();
    await activateFileTreeNode(button);
    return;
  }
  if (key === 'ArrowDown' || key === 'ArrowUp') {
    event.preventDefault?.();
    event.stopPropagation?.();
    focusAdjacentTreeNode(button, key === 'ArrowDown' ? 1 : -1);
    return;
  }
  if (key === 'ArrowRight' && button.dataset.type === 'directory') {
    event.preventDefault?.();
    event.stopPropagation?.();
    await activateFileTreeNode(button);
    return;
  }
  if (key === 'ArrowLeft') {
    event.preventDefault?.();
    event.stopPropagation?.();
    const parent = findParentTreeNode(button);
    if (parent) {
      parent.focus?.();
    }
  }
}

async function activateFileTreeNode(button) {
  const path = button.dataset.path || '';
  const type = button.dataset.type || '';
  const navigation = button.dataset.navigation || '';

  if (navigation === 'parent') {
    button.disabled = true;
    button.classList.add('is-loading');
    try {
      await loadWorkspaceDirectory(path || '');
    } catch (err) {
      const message = workspaceErrorMessage(err, 'Failed to load parent directory.');
      showToast(message, 'error');
    } finally {
      button.disabled = false;
      button.classList.remove('is-loading');
    }
    return;
  }

  if (type === 'file') {
    if (await loadFile(path)) {
      setActiveTreeNode(button, path);
    }
    return;
  }

  if (type === 'directory') {
    button.disabled = true;
    button.classList.add('is-loading');
    try {
      await loadWorkspaceDirectory(path);
      button.setAttribute('aria-expanded', 'true');
    } catch (err) {
      const message = workspaceErrorMessage(err, `Failed to load directory: ${path}`);
      nodes.editorFilename.innerText = path;
      nodes.editorContent.innerText = message;
      showToast(message, 'error');
    } finally {
      button.disabled = false;
      button.classList.remove('is-loading');
    }
  }
}

function visibleTreeNodes() {
  return Array.from(nodes.fileTree.querySelectorAll('.tree-node'))
    .filter((node) => !node.disabled && node.offsetParent !== null);
}

function focusAdjacentTreeNode(current, direction) {
  const buttons = visibleTreeNodes();
  const currentIndex = buttons.indexOf(current);
  if (currentIndex < 0) {
    return;
  }
  const next = buttons[currentIndex + direction];
  if (next) {
    next.focus?.();
  }
}

function findParentTreeNode(button) {
  const currentLevel = Number(button.getAttribute('aria-level') || '1');
  if (currentLevel <= 1) {
    return null;
  }
  const buttons = visibleTreeNodes();
  const currentIndex = buttons.indexOf(button);
  for (let i = currentIndex - 1; i >= 0; i -= 1) {
    const level = Number(buttons[i].getAttribute('aria-level') || '1');
    if (level < currentLevel) {
      return buttons[i];
    }
  }
  return null;
}

function setActiveTreeNode(button, path) {
  const previous = selectedWorkspaceTreePath();
  if (previous && previous !== path) {
    const old = nodes.fileTree.querySelector(`.tree-node.active[data-path="${cssEscape(previous)}"]`);
    if (old) {
      old.classList.remove('active');
    }
  }
  setSelectedWorkspaceTreePath(path);
  button.classList.add('active');
}

function cssEscape(value) {
  if (typeof CSS !== 'undefined' && typeof CSS.escape === 'function') {
    return CSS.escape(value);
  }
  return String(value || '').replace(/(["\\\]\[\(\)\.\s])/g, '\\$1');
}

async function loadFile(path) {
  const requestSeq = nextWorkspaceRequestSeq();
  nodes.editorFilename.innerText = path;
  nodes.editorContent.innerText = 'Loading...';
  setWorkspaceFilePreview(null);
  renderWorkspaceActions();
  return loadFilePreviewPage(path, requestSeq, 0, false);
}

async function loadFilePreviewPage(path, requestSeq, offset, append) {
  try {
    const data = await readWorkspaceFile(path, offset, WORKSPACE_FILE_PREVIEW_CHUNK_SIZE);
    if (workspaceViewState.requestSeq !== requestSeq) {
      return false;
    }
    const contentChunk = String(data?.content ?? '');
    const pageOffset = normalizePreviewNumber(data?.offset, offset);
    const nextOffsetFallback = pageOffset + contentChunk.length;
    const truncated = Boolean(data?.truncated);
    const nextOffset = truncated ? normalizePreviewNumber(data?.next_offset, nextOffsetFallback) : nextOffsetFallback;
    const size = Math.max(normalizePreviewNumber(data?.size, nextOffset), nextOffset);
    const previousPreview = workspaceFilePreview();
    const previousContent = append && previousPreview?.path === path ? previousPreview.content : '';
    const preview = {
      path,
      content: `${previousContent}${contentChunk}`,
      offset: pageOffset,
      nextOffset,
      size,
      truncated
    };
    setWorkspaceFilePreview(preview);
    renderWorkspaceFilePreview(preview);
    renderWorkspaceActions();
    return true;
  } catch (err) {
    if (workspaceViewState.requestSeq !== requestSeq) {
      return false;
    }
    const message = workspaceErrorMessage(err, `Failed to load file: ${path}`);
    const preview = workspaceFilePreview();
    if (append && preview?.path === path) {
      renderWorkspaceFilePreview(preview, message);
    } else {
      nodes.editorContent.innerText = message;
    }
    renderWorkspaceActions();
    showToast(message, 'error');
    return true;
  }
}

function normalizePreviewNumber(value, fallback) {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback;
}

function renderWorkspaceFilePreview(preview, errorMessage = '') {
  if (typeof nodes.editorContent.replaceChildren === 'function' && document.createTextNode) {
    nodes.editorContent.replaceChildren(document.createTextNode(preview.content));
  } else {
    nodes.editorContent.innerText = preview.content;
    nodes.editorContent.textContent = preview.content;
  }
  if (!preview.truncated && !errorMessage) {
    return;
  }
  const footer = document.createElement('div');
  footer.className = 'workspace-preview-footer';
  const meta = document.createElement('span');
  meta.className = errorMessage ? 'workspace-preview-error' : 'workspace-preview-meta';
  if (errorMessage) {
    meta.innerText = errorMessage;
  } else {
    const shown = Math.min(preview.nextOffset, preview.size);
    meta.innerText = `Showing ${formatWorkspaceBytes(shown)} of ${formatWorkspaceBytes(preview.size)}.`;
  }
  footer.appendChild(meta);
  if (preview.truncated) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'workspace-preview-more';
    button.innerText = 'Load more';
    button.addEventListener('click', async () => {
      if (button.disabled) {
        return;
      }
      button.disabled = true;
      button.innerText = 'Loading...';
      const requestSeq = nextWorkspaceRequestSeq();
      await loadFilePreviewPage(preview.path, requestSeq, preview.nextOffset, true);
    });
    footer.appendChild(button);
  }
  nodes.editorContent.appendChild(footer);
}

function formatWorkspaceBytes(bytes) {
  const value = Number(bytes);
  if (!Number.isFinite(value) || value < 0) {
    return 'unknown size';
  }
  if (value < 1024) {
    return `${value} B`;
  }
  if (value < 1024 * 1024) {
    return `${(value / 1024).toFixed(1)} KiB`;
  }
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
}
