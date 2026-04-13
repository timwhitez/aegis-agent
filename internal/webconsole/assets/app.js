/**
 * Agent Console App Logic
 */

// --- State ---
const state = {
  currentView: 'chat',
  messages: [],
  isGenerating: false,
  isConnected: false,
  ws: null,
  sessionId: '0x' + Math.random().toString(16).slice(2, 8).toUpperCase(),
  skills: [],
  fileTree: []
};

// --- DOM Elements ---
const nodes = {
  chatInput: document.getElementById('chat-input'),
  sendBtn: document.getElementById('send-btn'),
  chatMessages: document.getElementById('chat-messages'),
  chatContainer: document.getElementById('chat-container'),
  connectionDot: document.getElementById('connection-dot'),
  connectionStatus: document.getElementById('connection-status'),
  sessionIdDisplay: document.getElementById('session-id-display'),
  clearChatBtn: document.getElementById('clear-chat-btn'),
  skillsGrid: document.getElementById('skills-grid'),
  fileTree: document.getElementById('file-tree'),
  editorFilename: document.getElementById('editor-filename'),
  editorContent: document.getElementById('editor-content'),
  views: {
    chat: document.getElementById('chat-view'),
    skills: document.getElementById('skills-view'),
    workspace: document.getElementById('workspace-view'),
    history: document.getElementById('history-view'),
    settings: document.getElementById('settings-view')
  },
  navItems: document.querySelectorAll('.nav-item[data-view]')
};

// --- Initialization ---
function init() {
  lucide.createIcons();
  setupWebSocket();
  setupEventListeners();
  resetChatSession({ notifyBackend: false });
}

// --- WebSocket ---
function setupWebSocket() {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const wsUrl = `${protocol}//${window.location.host}/ws`;
  
  const ws = new WebSocket(wsUrl);
  state.ws = ws;

  ws.onopen = () => {
    state.isConnected = true;
    nodes.connectionStatus.innerText = 'Agent Connected';
    updateUI();
  };

  ws.onmessage = (event) => {
    const data = JSON.parse(event.data);
    handleServerEvent(data);
  };

  ws.onclose = () => {
    state.isConnected = false;
    state.isGenerating = false;
    nodes.connectionStatus.innerText = 'Disconnected';
    updateUI();
    // Reconnect after 3s
    setTimeout(setupWebSocket, 3000);
  };
}

function handleServerEvent(data) {
  switch (data.type) {
    case 'session':
      if (data.payload?.sessionId) {
        state.sessionId = data.payload.sessionId;
        updateSessionId();
      }
      break;
    case 'message':
      addMessage(data.payload.role, data.payload.agentName || 'Agent', data.payload.content);
      break;
    case 'status':
      if (data.payload?.sessionId) {
        state.sessionId = data.payload.sessionId;
        updateSessionId();
      }
      if (['awaiting_input', 'completed', 'paused', 'failed'].includes(data.payload?.status)) {
        state.isGenerating = false;
      }
      if (data.payload?.status === 'running') {
        state.isGenerating = true;
      }
      updateUI();
      break;
    case 'engine_event':
      // Progress/Terminal logs could be shown here
      console.log('Engine Event:', data.payload);
      break;
    case 'error':
      addMessage('system', 'System', data.payload.content);
      state.isGenerating = false;
      updateUI();
      break;
  }
}

// --- Logic ---
function setupEventListeners() {
  // Navigation
  nodes.navItems.forEach(item => {
    item.addEventListener('click', () => {
      const view = item.getAttribute('data-view');
      switchView(view);
    });
  });

  // Sending
  const sendMessage = () => {
    const text = nodes.chatInput.value.trim();
    if (!text || state.isGenerating) return;
    if (!state.ws || state.ws.readyState !== WebSocket.OPEN) {
      addMessage('system', 'System', 'The agent connection is offline. Wait for reconnection and try again.');
      updateUI();
      return;
    }

    // Local UI update
    addMessage('user', 'You', text);
    nodes.chatInput.value = '';
    nodes.chatInput.style.height = 'auto';
    
    // Server communication
    state.ws.send(JSON.stringify({
      type: 'chat',
      message: text,
      sessionId: state.sessionId
    }));
    state.isGenerating = true;
    updateUI();
  };

  nodes.sendBtn.addEventListener('click', sendMessage);
  nodes.clearChatBtn?.addEventListener('click', () => {
    resetChatSession({ notifyBackend: true });
  });
  
  nodes.chatInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      sendMessage();
    }
  });

  // Auto-resize textarea
  nodes.chatInput.addEventListener('input', function() {
    this.style.height = 'auto';
    this.style.height = (this.scrollHeight) + 'px';
  });
}

function switchView(viewName) {
  if (!nodes.views[viewName]) return;
  
  // Hide all views
  Object.values(nodes.views).forEach(v => v.style.display = 'none');
  nodes.navItems.forEach(i => i.classList.remove('active'));
  
  // Show target
  nodes.views[viewName].style.display = 'flex';
  const activeNav = Array.from(nodes.navItems).find(i => i.getAttribute('data-view') === viewName);
  if (activeNav) activeNav.classList.add('active');
  
  state.currentView = viewName;
  
  // View specific loading
  if (viewName === 'skills') fetchSkills();
  if (viewName === 'workspace') fetchWorkspace();
  if (viewName === 'history') fetchHistory();
  if (viewName === 'settings') renderSettings();
}

function updateUI() {
  nodes.sendBtn.disabled = state.isGenerating || !state.isConnected;
  if (!state.isConnected) {
    nodes.connectionDot.className = 'dot';
    return;
  }
  nodes.connectionDot.className = state.isGenerating ? 'dot busy' : 'dot online';
}

function updateSessionId() {
  nodes.sessionIdDisplay.innerText = `ID: ${state.sessionId}`;
}

function nextSessionId() {
  return '0x' + Math.random().toString(16).slice(2, 8).toUpperCase();
}

function resetChatSession({ notifyBackend }) {
  state.sessionId = nextSessionId();
  state.isGenerating = false;
  updateSessionId();
  nodes.chatMessages.innerHTML = '';
  addMessage('assistant', 'System', 'Hello! I am your agentic assistant. How can I help you today?');
  if (notifyBackend && state.ws && state.ws.readyState === WebSocket.OPEN) {
    state.ws.send(JSON.stringify({
      type: 'reset_session',
      sessionId: state.sessionId
    }));
  }
  updateUI();
}

function addMessage(role, name, content) {
  const msgEl = document.createElement('div');
  msgEl.className = `message ${role}`;
  
  const icon = role === 'user' ? 'user' : (role === 'system' ? 'terminal' : 'zap');
  
  msgEl.innerHTML = `
    <div class="message-header">
      <i data-lucide="${icon}" class="message-header-icon" style="width: 14px; height: 14px;"></i>
      <span class="message-header-name">${name}</span>
    </div>
    <div class="message-bubble prose">
      ${marked.parse(content)}
    </div>
  `;
  
  nodes.chatMessages.appendChild(msgEl);
  lucide.createIcons();
  
  // Scroll to bottom
  nodes.chatContainer.scrollTop = nodes.chatContainer.scrollHeight;
}

// --- Skills ---
async function fetchSkills() {
  try {
    const res = await fetch('/api/skills');
    const skills = await res.json();
    renderSkills(skills);
  } catch (err) {
    console.error('Skills error', err);
  }
}

function renderSkills(skills) {
  nodes.skillsGrid.innerHTML = skills.map(skill => `
    <div class="skill-card">
      <div class="skill-icon">
        <i data-lucide="${skill.icon || 'box'}"></i>
      </div>
      <h3 class="skill-name">${skill.name}</h3>
      <p class="skill-author">by ${skill.author}</p>
      <p class="skill-desc">${skill.description}</p>
      <div class="skill-footer">
        <span style="font-size: 11px; color: var(--text-muted)">${(skill.downloads/1000).toFixed(1)}k downloads</span>
        <button class="skill-btn ${skill.installed ? 'uninstall' : 'install'}" onclick="handleSkillAction('${skill.id}', ${skill.installed}, this)">
          ${skill.installed ? 'Uninstall' : 'Install'}
        </button>
      </div>
    </div>
  `).join('');
  lucide.createIcons();
}

async function handleSkillAction(id, isInstalled, btn) {
  if (!isInstalled) return; // not supported locally yet for 'Install' marketplace flow
  btn.disabled = true;
  btn.innerText = 'Uninstalling...';
  try {
    await fetch(`/api/skills/${id}/uninstall`, { method: 'POST' });
    await fetchSkills();
  } catch (err) {
    alert('Failed to uninstall skill.');
    btn.disabled = false;
    btn.innerText = 'Uninstall';
  }
}

// Global upload handler
document.addEventListener('change', async (e) => {
  if (e.target && e.target.id === 'skill-upload') {
    const file = e.target.files[0];
    if (!file) return;
    const formData = new FormData();
    formData.append('file', file);
    
    try {
      await fetch('/api/skills/upload', {
        method: 'POST',
        body: formData
      });
      alert('Skill uploaded and extracted successfully.');
      if (state.currentView === 'skills') {
        await fetchSkills();
      }
    } catch(err) {
      alert('Failed to upload skill zip.');
    }
    e.target.value = ''; // clear
  }
});

// --- History (Tasks) ---
async function fetchHistory() {
  try {
    const res = await fetch('/api/overview');
    const data = await res.json();
    renderHistory(data);
  } catch (err) {
    console.error('History error', err);
  }
}

function renderHistory(data) {
  const container = document.getElementById('history-view');
  if (!container) return;
  
  const feed = data.feed || [];
  
  container.innerHTML = `
    <div class="view-header">
      <h2 class="view-title">Activity History</h2>
      <p class="view-subtitle">Recent sessions and execution logs.</p>
    </div>
    <div class="table-list" style="display: flex; flex-direction: column; gap: 12px;">
      ${feed.map(item => `
        <div class="skill-card" style="padding: 16px; flex-direction: row; align-items: center; gap: 16px;">
          <div class="logo-icon" style="width: 32px; height: 32px; flex-shrink: 0; background: ${item.kind === 'session_summary' ? 'var(--accent)' : 'var(--primary)'}">
            <i data-lucide="${item.kind === 'session_summary' ? 'message-circle' : 'play'}"></i>
          </div>
          <div style="flex: 1">
            <div style="font-weight: 600; font-size: 14px;">${item.text}</div>
            <div style="font-size: 12px; color: var(--text-muted)">${new Date(item.time).toLocaleString()} · ${item.eventType}</div>
          </div>
          <div style="font-family: var(--font-mono); font-size: 11px; background: var(--background); padding: 4px 8px; border-radius: 4px; border: 1px solid var(--border)">
             ${item.data.model || item.data.agent_name || 'N/A'}
          </div>
        </div>
      `).join('')}
      ${feed.length === 0 ? '<p class="text-muted">No activity yet.</p>' : ''}
    </div>
  `;
  lucide.createIcons();
}

// --- Settings ---
async function renderSettings() {
  const container = document.getElementById('settings-view');
  if (!container) return;
  container.innerHTML = 'Loading backend config...';
  let configData = null;
  try {
    const res = await fetch('/api/config');
    configData = await res.json();
  } catch(e) {
    container.innerHTML = 'Failed to load backend settings.';
    return;
  }
  
  const providers = configData.providers || {};
  const defaultProv = configData.default_provider || '';

  const providerNames = Object.keys(providers);
  const options = providerNames.map(p => `<option value="${p}" ${p === defaultProv ? 'selected' : ''}>${p}</option>`).join('');

  container.innerHTML = `
    <div class="view-header">
      <h2 class="view-title">Settings</h2>
      <p class="view-subtitle">Configure your agent environment and API keys.</p>
    </div>
    <div class="skill-card" style="max-width: 600px;">
      <div style="display: flex; flex-direction: column; gap: 20px;">
        <div class="field">
          <label style="display: block; font-weight: 600; margin-bottom: 8px;">API Provider</label>
          <select id="settings-provider" class="input-container" style="width: 100%; padding: 10px; border: 1px solid var(--border); border-radius: 10px;">
            ${options}
          </select>
        </div>
        <div class="field">
          <label style="display: block; font-weight: 600; margin-bottom: 8px;">Base URL</label>
          <input id="settings-baseurl" type="text" placeholder="https://api..." style="width: 100%; padding: 12px; border: 1px solid var(--border); border-radius: 10px; background: var(--background);">
        </div>
        <div class="field">
          <label style="display: block; font-weight: 600; margin-bottom: 8px;">Model Name</label>
          <input id="settings-model" type="text" placeholder="gpt-4o" style="width: 100%; padding: 12px; border: 1px solid var(--border); border-radius: 10px; background: var(--background);">
        </div>
        <div class="field">
          <label style="display: block; font-weight: 600; margin-bottom: 8px;">API Key</label>
          <input id="settings-apikey" type="password" placeholder="Leave blank to keep existing..." style="width: 100%; padding: 12px; border: 1px solid var(--border); border-radius: 10px; background: var(--background);">
        </div>
        <button id="settings-save-btn" class="skill-btn install" style="padding: 12px;">Save Changes</button>
      </div>
    </div>
  `;
  lucide.createIcons();
  
  const provSelect = document.getElementById('settings-provider');
  const baseInput = document.getElementById('settings-baseurl');
  const modelInput = document.getElementById('settings-model');
  const keyInput = document.getElementById('settings-apikey');
  const saveBtn = document.getElementById('settings-save-btn');
  
  const updateFields = () => {
    const d = providers[provSelect.value];
    if (d) {
      baseInput.value = d.base_url || '';
      modelInput.value = d.model || '';
      keyInput.value = d.has_key ? '••••••••••••••••' : '';
    }
  };
  
  provSelect.addEventListener('change', updateFields);
  updateFields();
  
  saveBtn.addEventListener('click', async () => {
    saveBtn.innerText = 'Saving...';
    try {
      await fetch('/api/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          provider: provSelect.value,
          base_url: baseInput.value,
          model: modelInput.value,
          api_key: keyInput.value
        })
      });
      // Optionally reload config to get the definitive state
      await renderSettings();
    } catch(e) {
      alert('Failed to save configuration');
    }
    saveBtn.innerText = 'Save Changes';
  });
}

// --- Workspace ---
async function fetchWorkspace() {
  try {
    const res = await fetch('/api/files');
    const tree = await res.json();
    state.fileTree = tree;
    renderFileTree(tree);
  } catch (err) {
    console.error('File tree error', err);
  }
}

function renderFileTree(tree, container = nodes.fileTree, level = 0) {
  if (level === 0) container.innerHTML = '';
  if (!Array.isArray(tree)) return;

  tree.forEach(node => {
    const itemWrapper = document.createElement('div');
    
    const btn = document.createElement('button');
    btn.className = 'tree-node';
    btn.style.paddingLeft = `${level * 16 + 8}px`;
    
    // Sort directories to top, but we assume backend returns it as is. Better UI.
    const icon = node.type === 'directory' ? 'folder' : 'file-code';
    btn.innerHTML = `<i data-lucide="${icon}" style="width: 14px; height: 14px;"></i><span>${node.name}</span>`;
    
    const childrenContainer = document.createElement('div');
    if (node.type === 'directory') {
      childrenContainer.style.display = 'none'; // Collapsed by default
    }
    
    btn.onclick = () => {
      if (node.type === 'file') {
        loadFile(node.path);
        document.querySelectorAll('.tree-node').forEach(n => n.classList.remove('active'));
        btn.classList.add('active');
      } else if (node.type === 'directory') {
        const isHidden = childrenContainer.style.display === 'none';
        childrenContainer.style.display = isHidden ? 'block' : 'none';
        const newIcon = isHidden ? 'folder-open' : 'folder';
        btn.innerHTML = `<i data-lucide="${newIcon}" style="width: 14px; height: 14px;"></i><span>${node.name}</span>`;
        lucide.createIcons();
      }
    };
    
    itemWrapper.appendChild(btn);
    itemWrapper.appendChild(childrenContainer);
    container.appendChild(itemWrapper);
    
    if (node.type === 'directory' && node.children) {
      renderFileTree(node.children, childrenContainer, level + 1);
    }
  });
  
  if (level === 0) lucide.createIcons();
}

async function loadFile(path) {
  nodes.editorFilename.innerText = path;
  nodes.editorContent.innerText = 'Loading...';
  try {
    const res = await fetch(`/api/file/read?path=${encodeURIComponent(path)}`);
    const data = await res.json();
    nodes.editorContent.innerText = data.content;
  } catch (err) {
    nodes.editorContent.innerText = 'Error loading file.';
  }
}

// Run
document.addEventListener('DOMContentLoaded', init);
