// sprite-agent reference web client.
//
// Speaks the WebSocket/REST protocol the Go hub broadcasts: stream-json events
// forwarded per-session. UI parity with sprite-mobile: rich activity indicator,
// syntax highlighting, image attach, voice input, pull-to-refresh, swipe-to-close,
// session restore, dynamic sprite name.
'use strict';

(() => {
  // ---- markdown + syntax highlighting (progressive enhancement) ----
  const hasHljs = () => window.hljs && !window.__noHljs;
  const hasMarked = () => window.marked && !window.__noMarked;
  if (hasMarked()) {
    window.marked.setOptions({
      breaks: true,
      highlight: (code, lang) => {
        if (!hasHljs()) return null;
        try {
          if (lang && hljs.getLanguage(lang)) return hljs.highlight(code, { language: lang }).value;
          return hljs.highlightAuto(code).value;
        } catch (e) { return null; }
      },
    });
  }
  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, c => (
      { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
    ));
  }
  function renderMarkdown(text) {
    if (hasMarked()) {
      try { return window.marked.parse(text); } catch (e) { /* fall through */ }
    }
    return '<p>' + escapeHtml(text).replace(/\n/g, '<br>') + '</p>';
  }
  function highlightWithin(el) {
    if (!hasHljs() || !el) return;
    el.querySelectorAll('pre code').forEach(b => { try { hljs.highlightElement(b); } catch (e) {} });
  }

  // ---- DOM ----
  const $ = id => document.getElementById(id);
  const sessionsList = $('sessions-list');
  const fleetList = $('fleet-list');
  const messagesEl = $('messages');
  const inputEl = $('input');
  const inputArea = $('input-area');
  const sendBtn = $('send');
  const stopBtn = $('stop-btn');
  const attachBtn = $('attach-btn');
  const micBtn = $('mic-btn');
  const fileInput = $('file-input');
  const imagePreview = $('image-preview');
  const imagePreviewImg = $('image-preview-img');
  const imagePreviewName = $('image-preview-name');
  const statusEl = $('status');
  const chatTitle = $('chat-title');
  const emptyState = $('empty-state');
  const mainEl = $('main');
  const sidebar = $('sidebar');
  const overlay = $('overlay');
  const pullIndicator = $('pull-indicator');

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
  let pendingImage = null;
  let isOpeningFilePicker = false;
  let spriteName = 'sprite-agent';

  // ---- dynamic sprite name (item #24) ----
  async function loadConfig() {
    try {
      const res = await fetch('/api/config');
      if (!res.ok) return;
      const c = await res.json();
      if (c.agentID) {
        spriteName = 'sprite agent #' + c.agentID;
        document.title = spriteName;
        if (!currentSession) showBaselineTitle();
      }
    } catch (e) { /* keep default */ }
  }
  function showBaselineTitle() {
    chatTitle.textContent = spriteName;
    const h2 = emptyState.querySelector('h2');
    if (h2) h2.innerHTML = '👾 ' + escapeHtml(spriteName);
  }

  // ---- sessions REST ----
  async function loadSessions() {
    try {
      const res = await fetch('/api/sessions');
      sessions = await res.json() || [];
    } catch (e) { sessions = []; }
    renderSessions();
    return sessions;
  }
  // Composing state: a new chat shows a centered, large composer; once it has
  // messages the composer docks to the bottom. Driven purely by message presence.
  function setComposing(on) {
    mainEl.classList.toggle('composing', on);
    emptyState.style.display = on ? 'flex' : 'none';
  }
  function updateComposing() {
    setComposing(!messagesEl.querySelector('.message'));
  }
  function isEmptyChat() {
    return currentSession && isDefaultName(currentSession.name) && !messagesEl.querySelector('.message');
  }

  // newChat resets to the empty centered composer WITHOUT creating a session — the
  // session is created on first send/attach (no empty-session clutter).
  function newChat() {
    closeSidebar();
    if (isEmptyChat()) { inputEl.focus(); return; }
    disconnectWs();
    currentSession = null;
    currentAssistantEl = null; assistantText = ''; assistantTurns = 0;
    messagesEl.innerHTML = '';
    inputEl.value = ''; autoGrow();
    clearPendingImage();
    showBaselineTitle();
    renderSessions();
    updateComposing();
    history.replaceState(null, '', location.pathname);
    inputEl.focus();
  }

  // ensureSession activates a session for the composer (creating one if needed)
  // WITHOUT clearing the input/image, so typed text + attachments survive.
  async function ensureSession() {
    if (currentSession) return true;
    try {
      const res = await fetch('/api/sessions', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: 'New chat' }),
      });
      const s = await res.json();
      sessions.unshift(s);
      currentSession = s;
      chatTitle.textContent = s.name || 'Chat';
      assistantTurns = 0;
      renderSessions();
      connectWs(s.id);
      history.replaceState(null, '', '#session=' + s.id);
      try { localStorage.setItem('lastSessionId', s.id); } catch (e) {}
      return true;
    } catch (e) { return false; }
  }
  function waitForWsOpen(timeoutMs) {
    return new Promise(resolve => {
      const start = Date.now();
      (function chk() {
        if (ws && ws.readyState === WebSocket.OPEN) return resolve(true);
        if (Date.now() - start > timeoutMs) return resolve(false);
        setTimeout(chk, 50);
      })();
    });
  }
  async function deleteSession(id, ev) {
    ev.stopPropagation();
    await fetch('/api/sessions/' + id, { method: 'DELETE' });
    sessions = sessions.filter(s => s.id !== id);
    if (currentSession && currentSession.id === id) {
      newChat();
    }
    renderSessions();
  }
  window.deleteSession = deleteSession;

  function formatTime(ts) {
    if (!ts) return '';
    const d = new Date(ts);
    const now = new Date();
    if (d.toDateString() === now.toDateString()) {
      return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    }
    return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
  }

  function renderSessions() {
    sessionsList.innerHTML = sessions.map(s => `
      <div class="session-item ${currentSession && currentSession.id === s.id ? 'active' : ''}" data-id="${s.id}">
        <div class="session-name"><span>${escapeHtml(s.name || 'Chat')}</span><button class="session-delete" onclick="deleteSession('${s.id}', event)">×</button></div>
        <div class="session-preview">${escapeHtml(s.lastMessage || 'No messages yet')}</div>
        <div class="session-time">${formatTime(s.lastMessageAt)}</div>
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
    messagesEl.innerHTML = '';
    currentAssistantEl = null;
    assistantText = '';
    clearPendingImage();
    restoreDraft();
    assistantTurns = 0;
    renderSessions();
    // Assume docked while history loads (it almost always has messages) so we
    // don't flash the centered new-chat composer; history then corrects it.
    setComposing(false);
    connectWs(s.id);
    history.replaceState(null, '', '#session=' + s.id);
    try { localStorage.setItem('lastSessionId', s.id); } catch (e) {}
    closeSidebar();
  }

  // ---- fleet (P2.4): glance view + attach-to-worker, read from the roster ----
  let fleetRoster = [];
  async function loadFleet() {
    try {
      const res = await fetch('/api/fleet');
      if (!res.ok) { fleetList.innerHTML = '<div class="fleet-empty">no brain</div>'; return; }
      fleetRoster = await res.json() || [];
      fleetList.innerHTML = fleetRoster.map(a => {
        const badges =
          (a.present ? '<span class="fleet-badge present" title="a human is attached">👤</span>' : '') +
          (a.reapable ? '<span class="fleet-badge reap" title="reapable">⌛</span>' : '');
        const attachable = a.url ? ' attachable' : '';
        return `<div class="fleet-item${attachable}" data-id="${escapeHtml(a.id)}" title="${a.url ? 'Attach (open session)' : 'no URL'}">
          <span class="dot ${a.alive ? 'on' : 'off'}"></span>
          <span class="fleet-id">${escapeHtml(a.id)}</span>
          <span class="fleet-role">${escapeHtml(a.role || '')}</span>
          ${badges}
          <span class="fleet-phase">${escapeHtml(a.phase || '')}</span>
        </div>`;
      }).join('') || '<div class="fleet-empty">empty</div>';
      fleetList.querySelectorAll('.fleet-item.attachable').forEach(el => {
        el.addEventListener('click', () => attachToAgent(el.dataset.id));
      });
    } catch (e) { fleetList.innerHTML = '<div class="fleet-empty">—</div>'; }
  }

  function attachToAgent(id) {
    const a = fleetRoster.find(x => x.id === id);
    if (!a || !a.url) return;
    window.open(a.url, '_blank', 'noopener');
  }

  async function spawnWorker() {
    const btn = $('spawn-btn');
    if (btn) { btn.disabled = true; btn.textContent = '…'; }
    try {
      const res = await fetch('/api/fleet/spawn', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name_prefix: 'wk-', role: 'worker' }),
      });
      if (!res.ok) { addSystem('Spawn failed: ' + (await res.text())); }
      else { const w = await res.json(); addSystem('Spawned ' + (w.name || w.id) + ' — booting + registering…'); }
      loadFleet();
    } catch (e) { addSystem('Spawn error: ' + e.message); }
    finally { if (btn) { btn.disabled = false; btn.textContent = '+ worker'; } }
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
      reconnectAttempts = 0;
    };
    ws.onclose = () => {
      statusEl.textContent = 'Disconnected'; statusEl.className = 'error';
      scheduleReconnect();
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
    statusEl.textContent = `Reconnecting in ${Math.round(delay / 1000)}s…`; statusEl.className = 'error';
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
          if (m.role === 'user') addUser(m.content, m.images);
          else if (m.role === 'assistant') addStoredAssistant(m.content);
        });
        if (msg.isGenerating) showThinking();
        updateComposing();
        break;
      case 'processing':
        if (msg.isProcessing) showThinking();
        break;
      case 'user_message':
        if (msg.message) {
          const im = msg.message.image;
          addUser(msg.message.content, im && im.filename ? [uploadUrl(im.filename)] : null);
          showThinking();
        }
        break;
      case 'assistant':
        if (msg.message && msg.message.content) renderAssistantContent(msg.message.content);
        break;
      case 'content_block_start':
        if (msg.content_block?.type === 'text') startAssistant();
        else if (msg.content_block?.type === 'tool_use') addTool(msg.content_block.name, msg.content_block.input);
        break;
      case 'content_block_delta':
        if (msg.delta?.type === 'text_delta') appendAssistant(msg.delta.text);
        else if (msg.delta?.type === 'input_json_delta') accumulateToolInput(msg.delta.partial_json);
        break;
      case 'message_stop':
        finalizeAssistant();
        break;
      case 'result':
        removeActivity(); finalizeAssistant(); setGenerating(false);
        onAssistantTurnComplete();
        break;
      case 'error':
        addSystem('⚠ ' + (msg.message || 'error'));
        finalizeAssistant(); setGenerating(false);
        break;
    }
    scrollDown();
  }

  function renderAssistantContent(content) {
    if (Array.isArray(content)) {
      for (const b of content) {
        if (b.type === 'text') { startAssistant(); appendAssistant(b.text); }
        else if (b.type === 'tool_use') addTool(b.name, b.input);
      }
    } else if (typeof content === 'string') { startAssistant(); appendAssistant(content); }
    finalizeAssistant();
  }

  // ---- message DOM ----
  function uploadUrl(filename) {
    return currentSession ? '/api/uploads/' + currentSession.id + '/' + encodeURIComponent(filename) : '';
  }
  // addUser renders the user turn. imageSrcs is an array of ready-to-use <img src>
  // values: upload URLs for live turns, data URLs when replayed from history.
  function addUser(text, imageSrcs) {
    removeThinking();
    const imgs = (imageSrcs || []).filter(Boolean)
      .map(s => `<img class="message-image" src="${s}" alt="attachment">`).join('');
    const el = document.createElement('div');
    el.className = 'message user';
    el.innerHTML = `<div class="message-content">${imgs}${escapeHtml(text || '')}</div>`;
    messagesEl.appendChild(el);
    updateComposing(); // first message → dock the composer to the bottom
    scrollDown();
  }
  function addSystem(text) {
    const el = document.createElement('div');
    el.className = 'message system';
    el.textContent = text;
    messagesEl.appendChild(el); scrollDown();
  }
  // Copy button shown on each assistant message; copies the raw text (el._raw).
  const COPY_SVG = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>';
  const copyButton = `<button class="copy-btn" title="Copy" aria-label="Copy">${COPY_SVG}</button>`;
  function addStoredAssistant(text) {
    const el = document.createElement('div');
    el.className = 'message assistant';
    el.innerHTML = `<div class="message-content">${renderMarkdown(text)}</div>${copyButton}`;
    el._raw = text;
    messagesEl.appendChild(el);
    highlightWithin(el.querySelector('.message-content'));
  }
  function startAssistant() {
    if (currentAssistantEl) return;
    removeThinking(); removeActivity();
    currentAssistantEl = document.createElement('div');
    currentAssistantEl.className = 'message assistant';
    currentAssistantEl.innerHTML = `<div class="message-content streaming"></div>${copyButton}`;
    messagesEl.appendChild(currentAssistantEl);
    assistantText = '';
    setGenerating(true);
  }
  function appendAssistant(text) {
    if (!currentAssistantEl) startAssistant();
    assistantText += text;
    currentAssistantEl._raw = assistantText;
    currentAssistantEl.querySelector('.message-content').innerHTML = renderMarkdown(assistantText);
    scrollDown();
  }
  function finalizeAssistant() {
    removeActivity();
    if (currentAssistantEl) {
      const content = currentAssistantEl.querySelector('.message-content');
      content.classList.remove('streaming');
      highlightWithin(content);
      currentAssistantEl = null; assistantText = '';
    }
  }

  // ---- thinking indicator (three bouncing dots) ----
  function showThinking() {
    if ($('thinking')) return;
    setGenerating(true);
    const el = document.createElement('div');
    el.id = 'thinking'; el.className = 'thinking-indicator';
    el.innerHTML = `<div class="thinking-dots"><span></span><span></span><span></span></div><span class="thinking-text">Claude is thinking…</span>`;
    messagesEl.appendChild(el); scrollDown();
  }
  function removeThinking() { const t = $('thinking'); if (t) t.remove(); }

  // ---- activity / tool indicator ----
  const toolActions = {
    'Read':            { action: 'Reading',            getDetail: i => i?.file_path },
    'Write':           { action: 'Writing',            getDetail: i => i?.file_path },
    'Edit':            { action: 'Editing',            getDetail: i => i?.file_path },
    'Bash':            { action: 'Running',            getDetail: i => i?.command?.slice(0, 60) },
    'Grep':            { action: 'Searching',          getDetail: i => i?.pattern ? `"${i.pattern}"` : null },
    'Glob':            { action: 'Finding files',      getDetail: i => i?.pattern },
    'Task':            { action: 'Working on subtask', getDetail: i => i?.description },
    'WebFetch':        { action: 'Fetching',           getDetail: i => i?.url },
    'WebSearch':       { action: 'Searching web',      getDetail: i => i?.query },
    'LSP':             { action: 'Analyzing code',     getDetail: i => i?.operation },
    'TodoWrite':       { action: 'Updating tasks',     getDetail: () => null },
    'AskUserQuestion': { action: 'Asking question',    getDetail: () => null },
    'NotebookEdit':    { action: 'Editing notebook',   getDetail: i => i?.notebook_path },
  };
  function getToolAction(name) {
    return toolActions[name] || { action: 'Using ' + name, getDetail: () => null };
  }
  function truncatePath(path, maxLen = 40) {
    if (!path) return '';
    const s = String(path);
    return s.length <= maxLen ? s : '...' + s.slice(-(maxLen - 3));
  }
  function addTool(name, input) {
    currentToolName = name; currentToolInput = '';
    removeThinking(); removeActivity();
    setGenerating(true);
    const ta = getToolAction(name);
    const detail = input ? ta.getDetail(input) : null;
    const el = document.createElement('div');
    el.id = 'activity'; el.className = 'activity-indicator';
    el.innerHTML = `<div class="activity-spinner"><span class="spinner-sprite">👾</span></div>
      <div class="activity-content"><div class="activity-action">${escapeHtml(ta.action)}…</div>${detail ? `<div class="activity-detail">${escapeHtml(truncatePath(detail))}</div>` : ''}</div>`;
    messagesEl.appendChild(el); scrollDown();
  }
  function accumulateToolInput(partial) {
    if (!currentToolName) return;
    currentToolInput += partial || '';
    try {
      const parsed = JSON.parse(currentToolInput);
      const detail = getToolAction(currentToolName).getDetail(parsed);
      if (detail) updateActivity(detail);
    } catch (e) { /* incomplete JSON; wait for more deltas */ }
  }
  function updateActivity(detail) {
    const el = $('activity'); if (!el) return;
    let d = el.querySelector('.activity-detail');
    if (!d) {
      d = document.createElement('div'); d.className = 'activity-detail';
      el.querySelector('.activity-content').appendChild(d);
    }
    d.textContent = truncatePath(detail);
  }
  function removeActivity() { const t = $('activity'); if (t) t.remove(); currentToolName = null; currentToolInput = ''; }

  // ---- generating state (drives send/stop button swap) ----
  function setGenerating(on) {
    inputArea.classList.toggle('generating', !!on);
    stopBtn.disabled = !on;
  }

  function scrollDown() { messagesEl.scrollTop = messagesEl.scrollHeight; }

  // ---- image attachment ----
  function clearPendingImage() {
    if (pendingImage && pendingImage.localUrl) URL.revokeObjectURL(pendingImage.localUrl);
    pendingImage = null;
    imagePreview.classList.remove('has-image');
    imagePreviewImg.src = '';
    imagePreviewName.textContent = '';
  }
  function resizeImage(file, maxSize = 2048) {
    return new Promise(resolve => {
      const img = new Image();
      const url = URL.createObjectURL(file);
      img.onload = () => {
        URL.revokeObjectURL(url);
        if (img.width <= maxSize && img.height <= maxSize) { resolve(file); return; }
        const scale = maxSize / Math.max(img.width, img.height);
        const canvas = document.createElement('canvas');
        canvas.width = Math.round(img.width * scale);
        canvas.height = Math.round(img.height * scale);
        canvas.getContext('2d').drawImage(img, 0, 0, canvas.width, canvas.height);
        canvas.toBlob(blob => {
          resolve(blob ? new File([blob], file.name.replace(/\.\w+$/, '.jpg'), { type: 'image/jpeg' }) : file);
        }, 'image/jpeg', 0.85);
      };
      img.onerror = () => { URL.revokeObjectURL(url); resolve(file); };
      img.src = url;
    });
  }
  async function uploadImage(file) {
    if (!currentSession && !(await ensureSession())) { addSystem('Could not start a chat.'); return; }
    try {
      const resized = await resizeImage(file);
      const form = new FormData();
      form.append('file', resized);
      const res = await fetch('/api/upload?session=' + currentSession.id, { method: 'POST', body: form });
      if (!res.ok) { addSystem('Upload failed: ' + (await res.text())); return; }
      const data = await res.json();
      const localUrl = URL.createObjectURL(file);
      pendingImage = { id: data.id, filename: data.filename, mediaType: data.mediaType, localUrl };
      imagePreviewImg.src = localUrl;
      imagePreviewName.textContent = data.filename;
      imagePreview.classList.add('has-image');
    } catch (e) { addSystem('Upload error: ' + e.message); }
  }

  // ---- send ----
  async function send() {
    const text = inputEl.value.trim();
    const hasImage = !!pendingImage;
    if (!text && !hasImage) return;
    if (isRecording) { voiceInputSent = true; try { recognition.stop(); } catch (e) {} }

    // Composing a brand-new chat: create + connect the session first (text/image
    // are captured above, so ensureSession won't clobber them).
    if (!currentSession) {
      if (!(await ensureSession())) { addSystem('Could not start a chat.'); return; }
    }
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      await waitForWsOpen(5000);
      if (!ws || ws.readyState !== WebSocket.OPEN) { addSystem('Not connected — try again.'); return; }
    }

    maybeAutoTitle(text);
    addUser(text, hasImage ? [uploadUrl(pendingImage.filename)] : null);
    showThinking();
    const payload = { type: 'user', content: text };
    if (hasImage) {
      payload.imageId = pendingImage.id;
      payload.imageFilename = pendingImage.filename;
      payload.imageMediaType = pendingImage.mediaType;
    }
    ws.send(JSON.stringify(payload));
    inputEl.value = ''; autoGrow();
    clearDraft();
    clearPendingImage();
    setGenerating(true);
  }
  function interrupt() {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'interrupt' }));
    setGenerating(false); removeThinking(); removeActivity();
  }
  function autoGrow() {
    inputEl.style.height = 'auto';
    inputEl.style.height = Math.min(inputEl.scrollHeight, 200) + 'px';
  }

  // ---- voice input (SpeechRecognition) ----
  let recognition = null;
  let isRecording = false;
  let voiceInputSent = false;
  function setupVoice() {
    const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
    if (!SR) { micBtn.classList.add('unsupported'); return; }
    recognition = new SR();
    recognition.continuous = true;
    recognition.interimResults = true;
    recognition.lang = 'en-US';
    let finalTranscript = '';
    let originalInputText = '';
    recognition.onstart = () => {
      isRecording = true; voiceInputSent = false; finalTranscript = '';
      originalInputText = inputEl.value;
      micBtn.classList.add('recording');
    };
    recognition.onend = () => { isRecording = false; micBtn.classList.remove('recording'); };
    recognition.onerror = (e) => {
      isRecording = false; micBtn.classList.remove('recording');
      if (e.error === 'not-allowed') addSystem('Microphone permission denied.');
    };
    recognition.onresult = (event) => {
      if (voiceInputSent) return;
      let interim = '';
      for (let i = event.resultIndex; i < event.results.length; i++) {
        const t = event.results[i][0].transcript;
        if (event.results[i].isFinal) finalTranscript += t; else interim += t;
      }
      const spacer = originalInputText && !originalInputText.endsWith(' ') ? ' ' : '';
      inputEl.value = originalInputText + spacer + finalTranscript + interim;
      autoGrow();
    };
    micBtn.addEventListener('click', () => {
      if (!currentSession) return;
      if (isRecording) { try { recognition.stop(); } catch (e) {} }
      else { try { recognition.start(); } catch (e) {} }
    });
  }

  // ---- input focus/collapse ----
  // Keep textarea focus when tapping a toolbar button.
  [attachBtn, micBtn, sendBtn, stopBtn].forEach(b => b.addEventListener('mousedown', e => e.preventDefault()));

  // ---- sidebar ----
  const appEl = $('app');
  function openSidebar() { sidebar.classList.add('open'); overlay.classList.add('show'); }
  function closeSidebar() { sidebar.classList.remove('open'); overlay.classList.remove('show'); }
  const mqMobile = window.matchMedia('(max-width: 768px)');
  // The ☰ button: on mobile it opens the slide-in sidebar; on desktop it
  // shows/hides the persistent sidebar (preference remembered across refresh).
  function toggleSidebar() {
    if (mqMobile.matches) {
      sidebar.classList.contains('open') ? closeSidebar() : openSidebar();
    } else {
      const collapsed = appEl.classList.toggle('sidebar-collapsed');
      try { localStorage.setItem('sidebarCollapsed', collapsed ? '1' : '0'); } catch (e) {}
    }
  }

  // ---- input draft persistence (don't lose typed text on refresh) ----
  function draftKey() { return currentSession ? 'draft:' + currentSession.id : null; }
  function saveDraft() {
    const k = draftKey(); if (!k) return;
    try { inputEl.value ? localStorage.setItem(k, inputEl.value) : localStorage.removeItem(k); } catch (e) {}
  }
  function restoreDraft() {
    const k = draftKey();
    try { inputEl.value = (k && localStorage.getItem(k)) || ''; } catch (e) { inputEl.value = ''; }
    autoGrow();
  }
  function clearDraft() { const k = draftKey(); if (k) { try { localStorage.removeItem(k); } catch (e) {} } }

  // ---- auto chat titles ----
  function isDefaultName(n) { return !n || n === 'New chat' || n === 'Chat'; }
  async function renameSession(id, name) {
    try {
      await fetch('/api/sessions/' + id, {
        method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }),
      });
    } catch (e) {}
  }
  function applyTitle(id, name) {
    if (currentSession && currentSession.id === id) {
      currentSession.name = name;
      chatTitle.textContent = name;
    }
    const s = sessions.find(x => x.id === id);
    if (s) s.name = name;
    renderSessions();
  }
  // On a chat's first message, derive an instant title from it (and persist) so
  // chats never sit on "New chat" while the LLM title is generated.
  function maybeAutoTitle(text) {
    if (!currentSession || !text || !isDefaultName(currentSession.name)) return;
    let title = text.replace(/\s+/g, ' ').trim();
    if (title.length > 48) title = title.slice(0, 48) + '…';
    if (!title) return;
    applyTitle(currentSession.id, title);
    renameSession(currentSession.id, title);
  }
  // Continuously-evolving title: after assistant turns, ask the server to
  // regenerate the title from the conversation (cheap one-shot model).
  let assistantTurns = 0;
  async function retitle() {
    const id = currentSession && currentSession.id;
    if (!id) return;
    try {
      const res = await fetch('/api/sessions/' + id + '/retitle', { method: 'POST' });
      if (!res.ok) return;
      const data = await res.json();
      if (data.name) applyTitle(id, data.name);
    } catch (e) {}
  }
  function onAssistantTurnComplete() {
    assistantTurns++;
    // Evolve quickly at first, then periodically.
    if (assistantTurns <= 2 || assistantTurns % 3 === 0) retitle();
  }

  // sidebar swipe-to-close (item #9)
  let sbStartX = 0, sbSwiping = false;
  sidebar.addEventListener('touchstart', e => {
    if (!sidebar.classList.contains('open')) return;
    sbStartX = e.touches[0].clientX; sbSwiping = true; sidebar.style.transition = 'none';
  }, { passive: true });
  sidebar.addEventListener('touchmove', e => {
    if (!sbSwiping) return;
    const diff = e.touches[0].clientX - sbStartX;
    if (diff < 0) { sidebar.style.transform = `translateX(${diff}px)`; overlay.style.opacity = Math.max(0, 1 + diff / 280); }
  }, { passive: true });
  sidebar.addEventListener('touchend', e => {
    if (!sbSwiping) return;
    sbSwiping = false;
    const diff = e.changedTouches[0].clientX - sbStartX;
    sidebar.style.transition = ''; sidebar.style.transform = ''; overlay.style.opacity = '';
    if (diff < -80) closeSidebar();
  });

  // pull-to-refresh (item #8)
  const PULL_THRESHOLD = 80;
  let pullStartY = 0, pullDistance = 0, isPulling = false;
  function canPull(target) {
    if (sidebar.classList.contains('open')) return false;
    if (document.querySelector('header').contains(target)) return true;
    return messagesEl.scrollTop <= 0;
  }
  document.addEventListener('touchstart', e => {
    if (!canPull(e.target)) { isPulling = false; return; }
    pullStartY = e.touches[0].clientY; isPulling = true; pullDistance = 0;
  }, { passive: true });
  document.addEventListener('touchmove', e => {
    if (!isPulling) return;
    pullDistance = e.touches[0].clientY - pullStartY;
    if (pullDistance > 0) {
      const progress = Math.min(pullDistance / PULL_THRESHOLD, 1);
      pullIndicator.style.transition = 'none';
      pullIndicator.style.transform = `translateX(-50%) translateY(${-60 + progress * 80}px)`;
      pullIndicator.classList.add('visible');
      pullIndicator.classList.toggle('ready', pullDistance >= PULL_THRESHOLD);
    }
  }, { passive: true });
  document.addEventListener('touchend', () => {
    if (!isPulling) return;
    isPulling = false;
    if (pullDistance >= PULL_THRESHOLD) {
      pullIndicator.classList.add('refreshing');
      if (currentSession) { try { localStorage.setItem('lastSessionId', currentSession.id); } catch (e) {} }
      setTimeout(() => window.location.reload(), 300);
    } else {
      pullIndicator.style.transition = '';
      pullIndicator.style.transform = '';
      pullIndicator.classList.remove('visible', 'ready');
    }
  });

  // Copy an assistant message's raw text (delegated; works on hover-click + touch).
  function fallbackCopy(text, done) {
    const ta = document.createElement('textarea');
    ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0';
    document.body.appendChild(ta); ta.select();
    try { document.execCommand('copy'); done(); } catch (e) {}
    document.body.removeChild(ta);
  }
  messagesEl.addEventListener('click', (e) => {
    const btn = e.target.closest('.copy-btn');
    if (!btn) return;
    const msg = btn.closest('.message');
    if (!msg || !msg._raw) return;
    const done = () => { btn.classList.add('copied'); setTimeout(() => btn.classList.remove('copied'), 1200); };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(msg._raw).then(done).catch(() => fallbackCopy(msg._raw, done));
    } else { fallbackCopy(msg._raw, done); }
  });

  // Tapping anywhere outside the composer dismisses the mobile keyboard.
  $('stage').addEventListener('click', (e) => {
    if (!e.target.closest('#input-area')) inputEl.blur();
  });

  // ---- wire up ----
  $('new-chat-btn').addEventListener('click', newChat);
  { const sb = $('spawn-btn'); if (sb) sb.addEventListener('click', spawnWorker); }
  $('menu-btn').addEventListener('click', toggleSidebar);
  overlay.addEventListener('click', closeSidebar);
  sendBtn.addEventListener('click', send);
  stopBtn.addEventListener('click', interrupt);
  attachBtn.addEventListener('click', () => {
    isOpeningFilePicker = true; fileInput.click();
    setTimeout(() => { isOpeningFilePicker = false; }, 400);
  });
  fileInput.addEventListener('change', () => {
    if (fileInput.files[0]) uploadImage(fileInput.files[0]);
    fileInput.value = '';
  });
  $('remove-image').addEventListener('click', clearPendingImage);
  inputEl.addEventListener('input', () => { autoGrow(); saveDraft(); });
  inputEl.addEventListener('keydown', e => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
  });
  // Esc interrupts Claude while it's generating (item #26).
  document.addEventListener('keydown', e => { if (e.key === 'Escape' && !stopBtn.disabled) interrupt(); });

  // ---- boot ----
  async function boot() {
    try { if (localStorage.getItem('sidebarCollapsed') === '1') appEl.classList.add('sidebar-collapsed'); } catch (e) {}
    setupVoice();
    loadConfig();
    showBaselineTitle();
    await loadSessions();
    loadFleet();
    setInterval(loadFleet, 5000);

    // Restore the session on refresh (item #17): URL hash first, then localStorage.
    let restoreId = null;
    const hash = location.hash.match(/session=([\w-]+)/);
    if (hash) restoreId = hash[1];
    else { try { restoreId = localStorage.getItem('lastSessionId'); } catch (e) {} }
    if (restoreId) {
      let s = sessions.find(x => x.id === restoreId);
      if (!s) { s = { id: restoreId, name: 'Chat' }; sessions.unshift(s); }
      selectSession(s);
    } else {
      newChat(); // no session → centered composer
    }
    try { localStorage.removeItem('lastSessionId'); } catch (e) {}
  }
  boot();
})();
