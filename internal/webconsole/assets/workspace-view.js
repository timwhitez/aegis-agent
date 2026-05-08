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
    nodes.fileTree.innerHTML = '<div class="empty-panel">Failed to load workspace.</div>';
    nodes.editorFilename.innerText = 'Workspace';
    nodes.editorContent.innerText = 'Failed to load workspace.';
    showToast('Failed to load workspace.', 'error');
  }
}

function updateWorkspaceMeta() {
  if (nodes.workspaceSubtitle) {
    if (state.meta?.workspace_switch_supported) {
      nodes.workspaceSubtitle.textContent = 'Browse the active workspace and switch roots when needed.';
    } else {
      nodes.workspaceSubtitle.textContent = 'Browsing the selected workspace path. Use .. to move to the parent directory.';
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
    const icon = node.navigation === 'parent' ? 'corner-up-left' : node.type === 'directory' ? 'folder' : 'file-code';
    button.innerHTML = `<i data-lucide="${icon}" class="icon-small"></i><span>${escapeHTML(node.name)}</span>`;
    const childrenContainer = document.createElement('div');
    childrenContainer.className = 'tree-node-children';
    if (node.type === 'directory') {
      childrenContainer.classList.add('is-collapsed');
    }

    button.addEventListener('click', async () => {
      if (node.navigation === 'parent') {
        button.disabled = true;
        button.classList.add('is-loading');
        try {
          await loadWorkspaceDirectory(node.path || '');
        } catch (err) {
          showToast('Failed to load parent directory.', 'error');
        } finally {
          button.disabled = false;
          button.classList.remove('is-loading');
        }
        return;
      }
      if (node.type === 'file') {
        await loadFile(node.path);
        document.querySelectorAll('.tree-node').forEach((treeNode) => treeNode.classList.remove('active'));
        button.classList.add('active');
        return;
      }
      if (node.type === 'directory') {
        button.disabled = true;
        button.classList.add('is-loading');
        try {
          await loadWorkspaceDirectory(node.path);
        } catch (err) {
          nodes.editorFilename.innerText = node.path;
          nodes.editorContent.innerText = 'Error loading directory.';
          showToast(`Failed to load directory: ${node.path}`, 'error');
        } finally {
          button.disabled = false;
          button.classList.remove('is-loading');
        }
      }
    });

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

async function loadFile(path) {
  nodes.editorFilename.innerText = path;
  nodes.editorContent.innerText = 'Loading...';
  try {
    const data = await requestJSON(`/api/file/read?path=${encodeURIComponent(path)}`);
    nodes.editorContent.innerText = data.content;
  } catch (err) {
    nodes.editorContent.innerText = 'Error loading file.';
    showToast(`Failed to load file: ${path}`, 'error');
  }
}
