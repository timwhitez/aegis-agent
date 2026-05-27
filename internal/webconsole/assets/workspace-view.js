async function fetchWorkspace() {
  try {
    if (!state.meta) {
      await refreshMeta().catch(() => {});
    }
    updateWorkspaceMeta();
    nodes.fileTree.innerHTML = '<div class="view-loading">Loading workspace…</div>';
    nodes.editorFilename.innerText = workspaceDisplayName();
    nodes.editorContent.innerText = 'Choose a file or directory to inspect inside the current server workspace.';
    await loadWorkspaceDirectory(state.workspacePath || '');
  } catch (err) {
    console.error('workspace error', err);
    const message = workspaceErrorMessage(err);
    nodes.fileTree.innerHTML = '<div class="empty-panel">Failed to load workspace.</div>';
    nodes.editorFilename.innerText = 'Workspace';
    nodes.editorContent.innerText = message;
    showToast(message, 'error');
  }
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
  nodes.fileTree.innerHTML = '<div class="view-loading">Loading workspace…</div>';
  const tree = await requestJSON(`/api/files?path=${encodeURIComponent(queryPath)}`);
  state.workspacePath = normalized;
  state.fileTree = tree;
  state.selectedTreePath = '';
  renderFileTree(tree);
  updateWorkspaceMeta();
  nodes.editorFilename.innerText = workspaceDisplayName();
  nodes.editorContent.innerText = 'Choose a file or directory to inspect inside the current server workspace.';
}

function normalizeWorkspacePath(path = '') {
  const normalized = String(path || '')
    .replace(/\\/g, '/')
    .replace(/^\.\//, '')
    .replace(/^\/+|\/+$/g, '');
  return normalized === '.' ? '' : normalized;
}

function workspaceDisplayName() {
  return state.workspacePath ? `Workspace / ${state.workspacePath}` : 'Workspace';
}

function selectedWorkspaceWorkdir() {
  const root = String(state.meta?.workspace_root || '').trim();
  if (!root) {
    return '';
  }
  const rel = normalizeWorkspacePath(state.workspacePath);
  return rel ? `${root.replace(/\/+$/g, '')}/${rel}` : root;
}

function renderFileTree(tree, container = nodes.fileTree, level = 0) {
  if (level === 0) {
    container.innerHTML = '';
    ensureFileTreeDelegation(container);
  }
  if (!Array.isArray(tree)) {
    return;
  }
  if (level === 0 && tree.length === 0) {
    container.innerHTML = state.workspacePath
      ? '<div class="empty-panel">This directory is empty.</div>'
      : '<div class="empty-panel">This workspace is empty.</div>';
    return;
  }

  tree.forEach((node) => {
    const itemWrapper = document.createElement('div');
    const button = document.createElement('button');
    button.className = 'tree-node';
    button.style.paddingLeft = `${16 + level * 16}px`;
    button.dataset.path = node.path || '';
    button.dataset.type = node.type || '';
    button.dataset.navigation = node.navigation || '';
    if (state.selectedTreePath && node.type === 'file' && node.path === state.selectedTreePath) {
      button.classList.add('active');
    }
    const icon = node.navigation === 'parent' ? 'corner-up-left' : node.type === 'directory' ? 'folder' : 'file-code';
    button.innerHTML = `<i data-lucide="${icon}" class="icon-small"></i><span>${escapeHTML(node.name)}</span>`;
    const childrenContainer = document.createElement('div');
    childrenContainer.className = 'tree-node-children';
    if (node.type === 'directory') {
      childrenContainer.classList.add('is-collapsed');
    }
    itemWrapper.appendChild(button);
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
}

async function handleFileTreeClick(event) {
  const button = event.target.closest('.tree-node');
  if (!button || !nodes.fileTree.contains(button) || button.disabled) {
    return;
  }
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
    await loadFile(path);
    setActiveTreeNode(button, path);
    return;
  }

  if (type === 'directory') {
    button.disabled = true;
    button.classList.add('is-loading');
    try {
      await loadWorkspaceDirectory(path);
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

function setActiveTreeNode(button, path) {
  const previous = state.selectedTreePath;
  if (previous && previous !== path) {
    const old = nodes.fileTree.querySelector(`.tree-node.active[data-path="${cssEscape(previous)}"]`);
    if (old) {
      old.classList.remove('active');
    }
  }
  state.selectedTreePath = path;
  button.classList.add('active');
}

function cssEscape(value) {
  if (typeof CSS !== 'undefined' && typeof CSS.escape === 'function') {
    return CSS.escape(value);
  }
  return String(value || '').replace(/(["\\\]\[\(\)\.\s])/g, '\\$1');
}

async function loadFile(path) {
  nodes.editorFilename.innerText = path;
  nodes.editorContent.innerText = 'Loading...';
  try {
    const data = await requestJSON(`/api/file/read?path=${encodeURIComponent(path)}`);
    nodes.editorContent.innerText = data.content;
  } catch (err) {
    const message = workspaceErrorMessage(err, `Failed to load file: ${path}`);
    nodes.editorContent.innerText = message;
    showToast(message, 'error');
  }
}
