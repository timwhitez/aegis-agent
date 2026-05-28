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
  const requestSeq = nextWorkspaceRequestSeq();
  nodes.fileTree.innerHTML = '<div class="view-loading">Loading workspace…</div>';
  let tree;
  try {
    tree = await requestJSON(`/api/files?path=${encodeURIComponent(queryPath)}`);
  } catch (err) {
    if (state.workspaceRequestSeq !== requestSeq) {
      return;
    }
    throw err;
  }
  if (state.workspaceRequestSeq !== requestSeq) {
    return;
  }
  state.workspacePath = normalized;
  state.fileTree = tree;
  state.selectedTreePath = '';
  renderFileTree(tree);
  updateWorkspaceMeta();
  nodes.editorFilename.innerText = workspaceDisplayName();
  nodes.editorContent.innerText = 'Choose a file or directory to inspect inside the current server workspace.';
}

function nextWorkspaceRequestSeq() {
  const requestSeq = (state.workspaceRequestSeq || 0) + 1;
  state.workspaceRequestSeq = requestSeq;
  return requestSeq;
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
    container.setAttribute('role', 'tree');
    container.setAttribute('aria-label', 'Workspace files');
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
    button.setAttribute('role', 'treeitem');
    button.setAttribute('aria-level', String(level + 1));
    if (node.type === 'directory') {
      button.setAttribute('aria-expanded', 'false');
    }
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
  container.addEventListener('keydown', handleFileTreeKeydown);
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
  const requestSeq = nextWorkspaceRequestSeq();
  nodes.editorFilename.innerText = path;
  nodes.editorContent.innerText = 'Loading...';
  try {
    const data = await requestJSON(`/api/file/read?path=${encodeURIComponent(path)}`);
    if (state.workspaceRequestSeq !== requestSeq) {
      return false;
    }
    nodes.editorContent.innerText = data.content;
    return true;
  } catch (err) {
    if (state.workspaceRequestSeq !== requestSeq) {
      return false;
    }
    const message = workspaceErrorMessage(err, `Failed to load file: ${path}`);
    nodes.editorContent.innerText = message;
    showToast(message, 'error');
    return true;
  }
}
