// sprite-agent reference web client.
//
// Speaks the same WebSocket/REST protocol as sprite-mobile v1 (the protocol the
// Go hub broadcasts): stream-json events forwarded per-session. Trimmed to the
// Phase-1 core: session list, token streaming, tool indicators, co-presence.
'use strict';

(() => {
  // ---- markdown (progressive enhancement) ----
  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, c => (
      { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
    ));
  }
  function renderMarkdown(text) {
    if (window.marked && !window.__noMarked) {
      try { return window.marked.parse(text); } catch (e) { /* fall through */ }
    }
    return '<p>' + escapeHtml(text).replace(/\n/g, '<br>') + '</p>';
  }

  // ---- DOM ----
  const $ = id => document.getElementById(id);
  const sessionsList = $('sessions-list');
  const fleetList = $('fleet-list');
  const messagesEl = $('messages');
  const inputEl = $('input');
  const sendBtn = $('send');
  const stopBtn = $('stop-btn');
  const statusEl = $('status');
  const chatTitle = $('chat-title');
  const emptyState = $('empty-state');
  const sidebar = $('sidebar');
  const overlay = $('overlay');

  // ---- state ----
  let ws = null;
  let currentSession = null;
  let currentWsSessionId = null;
  let intentionalDisconnect = false;
  let reconnectAttempts = 0;
  let reconnectTimer = null;
  let sessions = [];
  let currentAssistantEl = null;
  let assistantText = '';
  let currentToolName = null;
  let currentToolInput = '';

  // ---- sessions REST ----
  async function loadSessions() {
    try {
      const res = await fetch('/api/sessions');
      sessions = await res.json() || [];
    } catch (e) { sessions = []; }
    renderSessions();
    return sessions;
  }
  async function createSession() {
    const res = await fetch('/api/sessions', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: 'New chat' }),
    });
    const s = await res.json();
    sessions.unshift(s);
    renderSessions();
    selectSession(s);
  }
  async function deleteSession(id, ev) {
    ev.stopPropagation();
    await fetch('/api/sessions/' + id, { method: 'DELETE' });
    sessions = sessions.filter(s => s.id !== id);
    if (currentSession && currentSession.id === id) {
      currentSession = null;
      disconnectWs();
      messagesEl.innerHTML = '';
      emptyState.style.display = 'flex';
      chatTitle.textContent = 'sprite-agent';
    }
    renderSessions();
  }
  window.deleteSession = deleteSession;

  function renderSessions() {
    sessionsList.innerHTML = sessions.map(s => `
      <div class="session-item ${currentSession && currentSession.id === s.id ? 'active' : ''}" data-id="${s.id}">
        <div class="session-name">${escapeHtml(s.name || 'Chat')}</div>
        <button class="session-delete" onclick="deleteSession('${s.id}', event)">×</button>
        <div class="session-preview">${escapeHtml(s.lastMessage || 'No messages yet')}</div>
      </div>`).join('');
    sessionsList.querySelectorAll('.session-item').forEach(el => {
      el.addEventListener('click', () => {
        const s = sessions.find(x => x.id === el.dataset.id);
        if (s) selectSession(s);
      });
    });
  }

  function selectSession(s) {
    currentSession = s;
    chatTitle.textContent = s.name || 'Chat';
    emptyState.style.display = 'none';
    messagesEl.innerHTML = '';
    currentAssistantEl = null;
    assistantText = '';
    renderSessions();
    connectWs(s.id);
    history.replaceState(null, '', '#session=' + s.id);
    closeSidebar();
  }

  // ---- fleet (M4): glance view, read straight from the roster ----
  async function loadFleet() {
    try {
      const res = await fetch('/api/fleet');
      if (!res.ok) { fleetList.innerHTML = '<div class="fleet-empty">no brain</div>'; return; }
      const roster = await res.json() || [];
      fleetList.innerHTML = roster.map(a => `
        <div class="fleet-item">
          <span class="dot ${a.alive ? 'on' : 'off'}"></span>
          <span class="fleet-id">${escapeHtml(a.id)}</span>
          <span class="fleet-role">${escapeHtml(a.role || '')}</span>
          <span class="fleet-phase">${escapeHtml(a.phase || '')}</span>
        </div>`).join('') || '<div class="fleet-empty">empty</div>';
    } catch (e) { fleetList.innerHTML = '<div class="fleet-empty">—</div>'; }
  }

  // ---- websocket ----
  function connectWs(sessionId) {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    if (ws) { const old = ws; ws = null; old.onclose = null; old.close(); }
    currentWsSessionId = sessionId;
    intentionalDisconnect = false;

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${proto}//${location.host}/ws?session=${sessionId}`);

    ws.onopen = () => {
      statusEl.textContent = 'Connected'; statusEl.className = 'connected';
      sendBtn.disabled = false; reconnectAttempts = 0;
    };
    ws.onclose = () => {
      statusEl.textContent = 'Disconnected'; statusEl.className = 'error';
      sendBtn.disabled = true; scheduleReconnect();
    };
    ws.onerror = () => { statusEl.textContent = 'Error'; statusEl.className = 'error'; };
    ws.onmessage = (ev) => {
      try { handleMessage(JSON.parse(ev.data)); } catch (e) { console.error('bad msg', e); }
    };
  }
  function disconnectWs() {
    intentionalDisconnect = true; currentWsSessionId = null;
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    if (ws) { ws.onclose = null; ws.close(); ws = null; }
  }
  function scheduleReconnect() {
    if (intentionalDisconnect || !currentWsSessionId) return;
    const delay = Math.min(1000 * Math.pow(2, reconnectAttempts), 30000);
    reconnectAttempts++;
    const target = currentWsSessionId;
    reconnectTimer = setTimeout(() => {
      if (currentWsSessionId === target && !intentionalDisconnect) connectWs(target);
    }, delay);
  }

  // ---- message protocol ----
  function handleMessage(msg) {
    switch (msg.type) {
      case 'system':
        if (msg.message && !/Connected/.test(msg.message)) addSystem(msg.message);
        break;
      case 'history':
        messagesEl.innerHTML = '';
        currentAssistantEl = null; assistantText = '';
        (msg.messages || []).forEach(m => {
          if (m.role === 'user') addUser(m.content);
          else if (m.role === 'assistant') addStoredAssistant(m.content);
        });
        if (msg.isGenerating) showThinking();
        break;
      case 'processing':
        if (msg.isProcessing) showThinking();
        break;
      case 'user_message':
        if (msg.message) { addUser(msg.message.content); showThinking(); }
        break;
      case 'assistant':
        // Terminal co-presence path: a complete message from the transcript.
        if (msg.message && msg.message.content) renderAssistantContent(msg.message.content);
        break;
      case 'content_block_start':
        if (msg.content_block?.type === 'text') startAssistant();
        else if (msg.content_block?.type === 'tool_use') addTool(msg.content_block.name);
        break;
      case 'content_block_delta':
        if (msg.delta?.type === 'text_delta') appendAssistant(msg.delta.text);
        else if (msg.delta?.type === 'input_json_delta') currentToolInput += msg.delta.partial_json || '';
        break;
      case 'message_stop':
        finalizeAssistant();
        break;
      case 'result':
        removeTool(); finalizeAssistant(); stopBtn.disabled = true;
        break;
      case 'error':
        addSystem('⚠ ' + (msg.message || 'error'));
        finalizeAssistant(); stopBtn.disabled = true;
        break;
    }
    scrollDown();
  }

  function renderAssistantContent(content) {
    if (Array.isArray(content)) {
      for (const b of content) {
        if (b.type === 'text') { startAssistant(); appendAssistant(b.text); }
        else if (b.type === 'tool_use') addTool(b.name);
      }
    } else if (typeof content === 'string') { startAssistant(); appendAssistant(content); }
    finalizeAssistant();
  }

  // ---- message DOM ----
  function addUser(text) {
    removeThinking();
    const el = document.createElement('div');
    el.className = 'message user';
    el.innerHTML = `<div class="message-content">${escapeHtml(text)}</div>`;
    messagesEl.appendChild(el); scrollDown();
  }
  function addSystem(text) {
    const el = document.createElement('div');
    el.className = 'message system';
    el.textContent = text;
    messagesEl.appendChild(el); scrollDown();
  }
  function addStoredAssistant(text) {
    const el = document.createElement('div');
    el.className = 'message assistant';
    el.innerHTML = `<div class="message-header">Claude</div><div class="message-content">${renderMarkdown(text)}</div>`;
    messagesEl.appendChild(el);
  }
  function startAssistant() {
    if (currentAssistantEl) return;
    removeThinking(); removeTool();
    currentAssistantEl = document.createElement('div');
    currentAssistantEl.className = 'message assistant';
    currentAssistantEl.innerHTML = `<div class="message-header">Claude</div><div class="message-content streaming"></div>`;
    messagesEl.appendChild(currentAssistantEl);
    assistantText = '';
    stopBtn.disabled = false;
  }
  function appendAssistant(text) {
    if (!currentAssistantEl) startAssistant();
    assistantText += text;
    currentAssistantEl.querySelector('.message-content').innerHTML = renderMarkdown(assistantText);
    scrollDown();
  }
  function finalizeAssistant() {
    removeTool();
    if (currentAssistantEl) {
      currentAssistantEl.querySelector('.message-content').classList.remove('streaming');
      currentAssistantEl = null; assistantText = '';
    }
  }

  // ---- indicators ----
  function showThinking() {
    if ($('thinking')) return;
    const el = document.createElement('div');
    el.id = 'thinking'; el.className = 'message assistant thinking';
    el.innerHTML = `<div class="message-content"><span class="dots"><span>·</span><span>·</span><span>·</span></span></div>`;
    messagesEl.appendChild(el); scrollDown();
  }
  function removeThinking() { const t = $('thinking'); if (t) t.remove(); }
  function addTool(name) {
    currentToolName = name; currentToolInput = '';
    removeThinking(); removeTool();
    const el = document.createElement('div');
    el.id = 'tool'; el.className = 'message tool-indicator';
    el.textContent = '🔧 ' + (name || 'tool');
    messagesEl.appendChild(el); scrollDown();
  }
  function removeTool() { const t = $('tool'); if (t) t.remove(); currentToolName = null; }

  function scrollDown() { messagesEl.scrollTop = messagesEl.scrollHeight; }

  // ---- send ----
  function send() {
    const text = inputEl.value.trim();
    if (!text || !ws || ws.readyState !== WebSocket.OPEN) return;
    addUser(text); showThinking();
    ws.send(JSON.stringify({ type: 'user', content: text }));
    inputEl.value = ''; autoGrow();
    stopBtn.disabled = false;
  }
  function interrupt() {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'interrupt' }));
  }
  function autoGrow() {
    inputEl.style.height = 'auto';
    inputEl.style.height = Math.min(inputEl.scrollHeight, 160) + 'px';
  }

  // ---- sidebar (mobile) ----
  function openSidebar() { sidebar.classList.add('open'); overlay.classList.add('show'); }
  function closeSidebar() { sidebar.classList.remove('open'); overlay.classList.remove('show'); }

  // ---- wire up ----
  $('new-chat-btn').addEventListener('click', createSession);
  $('start-chat-btn').addEventListener('click', createSession);
  $('menu-btn').addEventListener('click', openSidebar);
  overlay.addEventListener('click', closeSidebar);
  sendBtn.addEventListener('click', send);
  stopBtn.addEventListener('click', interrupt);
  inputEl.addEventListener('input', autoGrow);
  inputEl.addEventListener('keydown', e => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
  });
  document.addEventListener('keydown', e => { if (e.key === 'Escape') interrupt(); });

  // ---- boot ----
  async function boot() {
    await loadSessions();
    loadFleet();
    setInterval(loadFleet, 5000);
    const hash = location.hash.match(/session=([\w-]+)/);
    if (hash) {
      let s = sessions.find(x => x.id === hash[1]);
      if (!s) { s = { id: hash[1], name: 'Chat' }; sessions.unshift(s); }
      selectSession(s);
    }
  }
  boot();
})();
