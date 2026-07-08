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
  const modelSelect = $('model-select');
  const modelLabel = $('model-label');
  const contextPill = $('context-pill');
  const contextPopover = $('context-popover');
  const contextCount = $('context-count');
  const contextList = $('context-list');
  const statusEl = $('status');
  const chatTitle = $('chat-title');
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
  let pendingAttachments = [];
  let currentModel = 'opus'; // chosen model for the active conversation; Opus when unspecified
  let generating = false;    // a turn is in progress (send disabled, timer running)
  let genStart = 0;          // turn start (ms) for the working-indicator elapsed timer
  let genTimer = null;       // interval id for the elapsed timer
  let thinkingText = '';     // accumulates streamed reasoning for the live preview
  let isOpeningFilePicker = false;
  let spriteName = 'sprite-agent';

  // ---- dynamic sprite name (item #24) ----
  async function loadConfig() {
    try {
      const res = await fetch('/api/config');
      if (!res.ok) return;
      const c = await res.json();
      if (c.agentID) {
        spriteName = c.agentID;
        document.title = spriteName;
        try { localStorage.setItem('spriteName', spriteName); } catch (e) {}
        if (!currentSession) showBaselineTitle();
      }
    } catch (e) { /* keep default */ }
  }
  function showBaselineTitle() {
    chatTitle.textContent = spriteName;
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
    document.documentElement.setAttribute('data-view', on ? 'new' : 'chat');
    inputEl.placeholder = on ? 'How can I help you?' : 'Write a message';
    // Recompute height AFTER the view switch. autoGrow sets an inline style.height,
    // and the "new" view's min-height:120px makes it tall; without this, that tall
    // inline height sticks when we dock to "chat" (large composer on existing chats).
    autoGrow();
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
    clearAttachments();
    renderContext(null);
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
      if (currentModel) persistModel(); // carry the picker's choice onto the new session
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
    stickToBottom = true; // opening a chat starts pinned to the latest
    chatTitle.textContent = s.name || 'Chat';
    messagesEl.innerHTML = '';
    currentAssistantEl = null;
    assistantText = '';
    applySessionModel(s.model);
    fetchContext();
    clearAttachments();
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
        // Workers get a reap (destroy) button; home is never reaped.
        const reap = a.role === 'home' ? '' : '<button class="fleet-reap" title="Reap (destroy) this worker">🗑</button>';
        return `<div class="fleet-item${attachable}" data-id="${escapeHtml(a.id)}" title="${a.url ? 'Attach (open session)' : 'no URL'}">
          <span class="dot ${a.alive ? 'on' : 'off'}"></span>
          <span class="fleet-id">${escapeHtml(a.id)}</span>
          <span class="fleet-role">${escapeHtml(a.role || '')}</span>
          ${badges}
          <span class="fleet-phase">${escapeHtml(a.phase || '')}</span>
          ${reap}
        </div>`;
      }).join('') || '<div class="fleet-empty">empty</div>';
      fleetList.querySelectorAll('.fleet-item.attachable').forEach(el => {
        el.addEventListener('click', (e) => { if (!e.target.closest('.fleet-reap')) attachToAgent(el.dataset.id); });
      });
      fleetList.querySelectorAll('.fleet-reap').forEach(el => {
        el.addEventListener('click', (e) => { e.stopPropagation(); reapWorker(el.closest('.fleet-item').dataset.id); });
      });
    } catch (e) { fleetList.innerHTML = '<div class="fleet-empty">—</div>'; }
  }

  // Reap (destroy) a worker via the teardown endpoint, honoring the presence guard.
  // Reap is destructive, so it goes through an in-app modal that requires typing the
  // worker's exact name (no native confirm()). The 409 "human attached" case is handled
  // in-modal: the button turns into "Force reap" rather than a second prompt.
  const reapModal = $('reap-modal');
  const reapInput = $('reap-modal-input');
  const reapConfirmBtn = $('reap-modal-confirm');
  const reapMsg = $('reap-modal-msg');
  let reapTarget = null;

  function reapWorker(id) {
    reapTarget = id;
    $('reap-modal-name').textContent = id;
    reapInput.value = '';
    reapMsg.textContent = '';
    reapConfirmBtn.textContent = 'Reap';
    reapConfirmBtn.dataset.force = '';
    reapConfirmBtn.disabled = true;
    reapModal.hidden = false;
    reapInput.focus();
  }
  function closeReapModal() { reapModal.hidden = true; reapTarget = null; }

  reapInput.addEventListener('input', () => {
    reapConfirmBtn.disabled = reapInput.value.trim() !== reapTarget;
  });
  reapInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !reapConfirmBtn.disabled) reapConfirmBtn.click();
  });
  // Single tap/click (or Enter/Space) on the name copies it to the clipboard.
  const reapName = $('reap-modal-name');
  reapName.addEventListener('click', () => copyText(reapTarget || reapName.textContent, reapName));
  reapName.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); copyText(reapTarget || reapName.textContent, reapName); }
  });
  $('reap-modal-cancel').addEventListener('click', closeReapModal);
  reapModal.addEventListener('click', (e) => { if (e.target === reapModal) closeReapModal(); });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !reapModal.hidden) closeReapModal(); });

  reapConfirmBtn.addEventListener('click', async () => {
    const id = reapTarget;
    const force = reapConfirmBtn.dataset.force === '1';
    reapConfirmBtn.disabled = true;
    reapMsg.textContent = 'Reaping…';
    try {
      const body = force ? { target: id, force: true } : { target: id };
      const res = await fetch('/api/fleet/destroy', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
      });
      if (res.status === 409) {
        // A human is attached — offer force without leaving the modal.
        reapMsg.textContent = (await res.text()).trim();
        reapConfirmBtn.textContent = 'Force reap';
        reapConfirmBtn.dataset.force = '1';
        reapConfirmBtn.disabled = false;
        return;
      }
      const ok = res.ok;
      const detail = ok ? '' : (await res.text()).trim();
      closeReapModal();
      addSystem(ok ? 'Reaped ' + id : 'Reap failed: ' + detail);
      loadFleet();
    } catch (e) {
      reapMsg.textContent = 'Error: ' + e.message;
      reapConfirmBtn.disabled = false;
    }
  });

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
      statusEl.className = 'connected'; // 👾 indicator (no text)
      reconnectAttempts = 0;
    };
    ws.onclose = () => {
      statusEl.className = 'error';
      scheduleReconnect();
    };
    ws.onerror = () => { statusEl.className = 'error'; };
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
    statusEl.className = 'error';
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
          if (m.role === 'user') addUser(m.content, { images: m.images });
          else if (m.role === 'assistant') addStoredAssistant(m.content);
        });
        if (msg.isGenerating) showThinking();
        // A history event only arrives for an existing session we've opened, so
        // dock the composer — never flip to the big centered "new chat" composer
        // just because the rendered list is momentarily empty (history still
        // loading, mid-generation, or every line filtered as harness noise).
        setComposing(false);
        break;
      case 'processing':
        if (msg.isProcessing) showThinking();
        break;
      case 'user_message':
        if (msg.message) {
          // New shape: attachments[]. Fall back to the old singular attachment.
          const atts = msg.message.attachments || (msg.message.attachment ? [msg.message.attachment] : []);
          let opts = null;
          if (atts.length) {
            const isImg = a => (a.type || '').startsWith('image/');
            opts = {
              images: atts.filter(isImg).map(a => uploadUrl(a.file)),
              files: atts.filter(a => !isImg(a)).map(a => ({ name: a.name || a.file, url: uploadUrl(a.file) })),
            };
          }
          addUser(msg.message.content, opts);
          showThinking();
        }
        break;
      case 'assistant':
        if (msg.message && msg.message.content) renderAssistantContent(msg.message.content);
        break;
      case 'content_block_start':
        if (msg.content_block?.type === 'text') startAssistant();
        else if (msg.content_block?.type === 'thinking') { thinkingText = ''; showThinking(); }
        else if (msg.content_block?.type === 'tool_use') addTool(msg.content_block.name, msg.content_block.input);
        break;
      case 'content_block_delta':
        if (msg.delta?.type === 'text_delta') appendAssistant(msg.delta.text);
        else if (msg.delta?.type === 'thinking_delta') appendThinking(msg.delta.thinking);
        else if (msg.delta?.type === 'input_json_delta') accumulateToolInput(msg.delta.partial_json);
        break;
      case 'message_stop':
        finalizeAssistant();
        // Between one assistant message and the next step (a tool running, or more
        // thinking) there's a real gap — keep a live indicator so it never goes
        // blank. 'result' clears it when the turn actually ends.
        if (generating) showActivity('Working');
        break;
      case 'result':
        removeActivity(); finalizeAssistant(); setGenerating(false);
        onAssistantTurnComplete();
        fetchContext(); // the turn may have cloned/removed a repo — remirror the workspace
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
  // addUser renders the user turn. opts: { images: [<img src>...], file: {name,url} }.
  // images use upload URLs live / data URLs in history; file renders a chip.
  function addUser(text, opts) {
    opts = opts || {};
    removeThinking();
    const imgs = (opts.images || []).filter(Boolean)
      .map(s => `<img class="message-image" src="${s}" alt="attachment">`).join('');
    const files = opts.files || (opts.file ? [opts.file] : []);
    const chips = files
      .map(f => `<a class="file-chip" href="${f.url}" target="_blank" rel="noopener">📎 ${escapeHtml(f.name)}</a>`).join('');
    const el = document.createElement('div');
    el.className = 'message user';
    el.innerHTML = `<div class="message-content">${imgs}${chips}${escapeHtml(text || '')}</div>`;
    messagesEl.appendChild(el);
    updateComposing(); // first message → dock the composer to the bottom
    forceScrollDown(); // your own turn always jumps to the bottom and re-pins
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
  // Give each code block its own copy button (top-right of the <pre>).
  function decorateCodeBlocks(contentEl) {
    if (!contentEl) return;
    contentEl.querySelectorAll('pre').forEach(pre => {
      if (pre.querySelector(':scope > .code-copy')) return;
      const btn = document.createElement('button');
      btn.className = 'code-copy';
      btn.title = 'Copy code';
      btn.innerHTML = COPY_SVG;
      pre.appendChild(btn);
    });
  }
  function addStoredAssistant(text) {
    const el = document.createElement('div');
    el.className = 'message assistant';
    el.innerHTML = `<div class="message-content">${renderMarkdown(text)}</div>${copyButton}`;
    el._raw = text;
    messagesEl.appendChild(el);
    const content = el.querySelector('.message-content');
    highlightWithin(content);
    decorateCodeBlocks(content);
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
      decorateCodeBlocks(content);
      currentAssistantEl = null; assistantText = '';
    }
  }

  // ---- working indicator: one persistent element for the whole turn ----
  // Reflects the current phase (Thinking / Working / a specific tool) with a live
  // elapsed timer, reused in place so it never flickers or goes blank between phases.
  function fmtElapsed(ms) {
    const s = Math.round(ms / 1000);
    return s < 60 ? s + 's' : Math.floor(s / 60) + 'm ' + (s % 60) + 's';
  }
  function tickElapsed() {
    const el = $('activity'); if (!el || !genStart) return;
    const e = el.querySelector('.activity-elapsed');
    if (e) e.textContent = fmtElapsed(Date.now() - genStart);
  }
  function showActivity(action) {
    setGenerating(true);
    let el = $('activity');
    if (!el) {
      el = document.createElement('div');
      el.id = 'activity'; el.className = 'activity-indicator';
      el.innerHTML = '<div class="activity-spinner"><span class="spinner-sprite">👾</span></div>' +
        '<div class="activity-content"><div class="activity-action"></div><div class="activity-detail"></div></div>';
      messagesEl.appendChild(el);
    }
    el.querySelector('.activity-action').innerHTML = escapeHtml(action) + '… <span class="activity-elapsed"></span>';
    tickElapsed();
    scrollDown();
  }
  function setActivityDetail(text) {
    const el = $('activity'); if (!el) return;
    const d = el.querySelector('.activity-detail');
    d.textContent = text || '';
    d.style.display = text ? '' : 'none';
  }
  function removeActivity() { const t = $('activity'); if (t) t.remove(); currentToolName = ''; currentToolInput = ''; }
  // "Thinking" is the default phase of the same indicator (replaces the old dots).
  function showThinking() { showActivity('Thinking'); setActivityDetail(''); }
  function removeThinking() { /* unified into the working indicator */ }
  // Stream a preview of Claude's reasoning into the detail line when thinking text
  // is exposed, keeping the most recent words visible so a long think reads as live.
  function appendThinking(text) {
    thinkingText += text || '';
    const t = thinkingText.replace(/\s+/g, ' ').trim();
    if (t) setActivityDetail('… ' + t.slice(-140));
  }

  // ---- tool indicator (a phase of the working indicator) ----
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
    const ta = getToolAction(name);
    showActivity(ta.action);
    const detail = input ? ta.getDetail(input) : null;
    setActivityDetail(detail ? truncatePath(detail) : '');
  }
  function accumulateToolInput(partial) {
    if (!currentToolName) return;
    currentToolInput += partial || '';
    try {
      const parsed = JSON.parse(currentToolInput);
      const detail = getToolAction(currentToolName).getDetail(parsed);
      if (detail) setActivityDetail(truncatePath(detail));
    } catch (e) { /* incomplete JSON; wait for more deltas */ }
  }

  // ---- generating state (drives the send/stop button swap + the turn timer) ----
  function setGenerating(on) {
    generating = !!on;
    inputArea.classList.toggle('generating', generating);
    stopBtn.disabled = !generating;
    if (generating) {
      if (!genTimer) { genStart = Date.now(); genTimer = setInterval(tickElapsed, 1000); }
    } else {
      clearInterval(genTimer); genTimer = null; genStart = 0; thinkingText = '';
    }
  }

  // Stick-to-bottom: auto-scroll while streaming ONLY if you're already at the
  // bottom. Scroll up to read mid-stream and the viewport stays put; scroll back
  // down and it re-engages. forceScrollDown re-pins (you sent a message / opened a chat).
  let stickToBottom = true;
  function atBottom() {
    return messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight <= 80;
  }
  messagesEl.addEventListener('scroll', () => { stickToBottom = atBottom(); }, { passive: true });
  function scrollDown() { if (stickToBottom) messagesEl.scrollTop = messagesEl.scrollHeight; }
  function forceScrollDown() { stickToBottom = true; messagesEl.scrollTop = messagesEl.scrollHeight; }

  // ---- model selection ----
  // Reflect a session's stored model in the picker (called when opening a chat).
  // No model stored → Opus.
  function applySessionModel(model) {
    currentModel = model || 'opus';
    if (modelSelect) modelSelect.value = currentModel;
    syncModelLabel();
  }
  // Show the selected model's name next to the chevron.
  function syncModelLabel() {
    if (modelLabel && modelSelect) modelLabel.textContent = (modelSelect.selectedOptions[0] || {}).text || 'Opus';
  }
  // Persist the picker's choice so it survives reloads. The turn itself also
  // carries `model`, so the hub applies it (respawning if it changed) on send.
  function persistModel() {
    if (currentSession) currentSession.model = currentModel;
    if (!currentSession) return;
    fetch('/api/sessions/' + currentSession.id, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ model: currentModel }),
    }).catch(() => {});
  }

  // ---- context: on-disk mirror of what's been added to the chat ----
  // (git repos in ~/chats/<id> plus files uploaded to it)
  async function fetchContext() {
    if (!currentSession) { renderContext(null); return; }
    const id = currentSession.id;
    try {
      const res = await fetch('/api/sessions/' + id + '/context');
      if (!res.ok) return;
      const ctx = await res.json();
      if (currentSession && currentSession.id === id) renderContext(ctx); // ignore if switched away
    } catch (e) {}
  }
  // Update the count pill and (re)build the popover list. Pill hides when empty.
  function renderContext(ctx) {
    const repos = (ctx && ctx.repos) || [];
    const files = (ctx && ctx.files) || [];
    const total = repos.length + files.length;
    contextCount.textContent = String(total);
    contextPill.hidden = total === 0;
    if (total === 0) closeContextPopover();

    contextList.innerHTML = '';
    if (repos.length) {
      contextList.appendChild(ctxGroup('Repos'));
      for (const r of repos) {
        const row = document.createElement('span');
        row.className = 'ctx-row';
        row.title = [r.remote, r.branch ? 'branch: ' + r.branch : '', r.dirty ? 'uncommitted changes' : '']
          .filter(Boolean).join('\n');
        if (r.dirty) { const d = document.createElement('span'); d.className = 'repo-dirty'; row.appendChild(d); }
        const n = document.createElement('span'); n.className = 'ctx-name'; n.textContent = r.name; row.appendChild(n);
        if (r.branch) { const b = document.createElement('span'); b.className = 'repo-branch'; b.textContent = r.branch; row.appendChild(b); }
        contextList.appendChild(row);
      }
    }
    if (files.length) {
      contextList.appendChild(ctxGroup('Files'));
      for (const f of files) {
        const a = document.createElement('a');
        a.className = 'ctx-row'; a.href = f.url; a.target = '_blank'; a.rel = 'noopener'; a.title = f.name;
        const ic = document.createElement('span'); ic.textContent = f.image ? '🖼' : '📎'; a.appendChild(ic);
        const n = document.createElement('span'); n.className = 'ctx-name'; n.textContent = f.name; a.appendChild(n);
        contextList.appendChild(a);
      }
    }
  }
  function ctxGroup(text) { const d = document.createElement('div'); d.className = 'ctx-group'; d.textContent = text; return d; }
  function openContextPopover() { contextPopover.hidden = false; contextPill.setAttribute('aria-expanded', 'true'); }
  function closeContextPopover() { contextPopover.hidden = true; contextPill.setAttribute('aria-expanded', 'false'); }
  contextPill.addEventListener('click', (e) => {
    e.stopPropagation();
    contextPopover.hidden ? openContextPopover() : closeContextPopover();
  });
  document.addEventListener('click', (e) => {
    if (!contextPopover.hidden && !contextPopover.contains(e.target) && !contextPill.contains(e.target)) closeContextPopover();
  });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !contextPopover.hidden) closeContextPopover(); });
  $('context-add').addEventListener('click', () => { closeContextPopover(); addRepo(); });
  // Adding a repo just asks the agent to clone it — reuses its git/gh auth, and the
  // bar refreshes from disk when the turn completes. The URL is collected in a modal.
  const repoModal = $('repo-modal');
  const repoModalInput = $('repo-modal-input');
  const repoModalConfirm = $('repo-modal-confirm');
  function addRepo() {
    repoModalInput.value = '';
    repoModalConfirm.disabled = true;
    repoModal.hidden = false;
    repoModalInput.focus();
  }
  function closeRepoModal() { repoModal.hidden = true; }
  function submitRepoModal() {
    const url = repoModalInput.value.trim();
    if (!url) return;
    closeRepoModal();
    inputEl.value = 'Clone this repo into my workspace: ' + url;
    autoGrow();
    send();
  }
  repoModalInput.addEventListener('input', () => { repoModalConfirm.disabled = !repoModalInput.value.trim(); });
  repoModalInput.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !repoModalConfirm.disabled) submitRepoModal(); });
  repoModalConfirm.addEventListener('click', submitRepoModal);
  $('repo-modal-cancel').addEventListener('click', closeRepoModal);
  repoModal.addEventListener('click', (e) => { if (e.target === repoModal) closeRepoModal(); });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !repoModal.hidden) closeRepoModal(); });

  // ---- worker env vars (in-memory secrets) ----
  const envModal = $('env-modal');
  const envList = $('env-list');
  const envName = $('env-name');
  const envValue = $('env-value');
  const envMsg = $('env-msg');
  async function openEnvModal() {
    closeSidebar();
    envMsg.textContent = '';
    envName.value = ''; envValue.value = '';
    renderEnvList([]);
    envModal.hidden = false;
    await refreshEnv();
    envName.focus();
  }
  function closeEnvModal() { envModal.hidden = true; }
  async function refreshEnv() {
    try {
      const res = await fetch('/api/env');
      if (!res.ok) return;
      const data = await res.json();
      renderEnvList(data.names || []);
    } catch (e) {}
  }
  function renderEnvList(names) {
    envList.innerHTML = '';
    if (!names.length) {
      const p = document.createElement('p');
      p.className = 'env-empty';
      p.textContent = 'No variables set on this worker.';
      envList.appendChild(p);
      return;
    }
    for (const name of names) {
      const row = document.createElement('div');
      row.className = 'env-row';
      row.innerHTML =
        `<span class="env-name">${escapeHtml(name)}</span>` +
        `<span class="env-mask">••••••</span>` +
        `<button class="env-del" title="Remove" data-name="${escapeHtml(name)}">×</button>`;
      envList.appendChild(row);
    }
  }
  async function setEnv() {
    const name = envName.value.trim();
    const value = envValue.value;
    if (!name || !value) { envMsg.textContent = 'Name and value are both required.'; return; }
    try {
      const res = await fetch('/api/env', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, value }),
      });
      if (!res.ok) { envMsg.textContent = await res.text(); return; }
      const data = await res.json();
      renderEnvList(data.names || []);
      envMsg.textContent = '';
      envName.value = ''; envValue.value = '';
      envName.focus();
    } catch (e) { envMsg.textContent = 'Failed: ' + e.message; }
  }
  async function deleteEnv(name) {
    try {
      await fetch('/api/env/' + encodeURIComponent(name), { method: 'DELETE' });
      await refreshEnv();
    } catch (e) {}
  }
  $('env-btn').addEventListener('click', openEnvModal);
  $('env-add-btn').addEventListener('click', setEnv);
  $('env-close').addEventListener('click', closeEnvModal);
  envValue.addEventListener('keydown', (e) => { if (e.key === 'Enter') setEnv(); });
  envName.addEventListener('keydown', (e) => { if (e.key === 'Enter') envValue.focus(); });
  envList.addEventListener('click', (e) => {
    const btn = e.target.closest('.env-del');
    if (btn) deleteEnv(btn.dataset.name);
  });
  envModal.addEventListener('click', (e) => { if (e.target === envModal) closeEnvModal(); });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !envModal.hidden) closeEnvModal(); });

  // ---- attachments (images + documents), multiple at a time ----
  function clearAttachments() {
    for (const a of pendingAttachments) if (a.localUrl) URL.revokeObjectURL(a.localUrl);
    pendingAttachments = [];
    renderAttachments();
  }
  function removeAttachment(id) {
    const i = pendingAttachments.findIndex(a => a.id === id);
    if (i < 0) return;
    if (pendingAttachments[i].localUrl) URL.revokeObjectURL(pendingAttachments[i].localUrl);
    pendingAttachments.splice(i, 1);
    renderAttachments();
  }
  // Rebuild the preview row: one chip per pending attachment (thumbnail for
  // images, 📎 name for files), each with its own × remove button.
  function renderAttachments() {
    imagePreview.innerHTML = '';
    imagePreview.classList.toggle('has-image', pendingAttachments.length > 0);
    for (const a of pendingAttachments) {
      const chip = document.createElement('div');
      chip.className = 'attach-chip';
      const label = a.name || a.filename;
      chip.innerHTML =
        (a.isImage ? `<img class="attach-thumb" src="${a.localUrl}" alt="">` : '') +
        `<span class="attach-name">${a.isImage ? '' : '📎 '}${escapeHtml(label)}</span>` +
        `<button class="attach-remove" title="Remove" data-id="${escapeHtml(a.id)}">×</button>`;
      imagePreview.appendChild(chip);
    }
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
  async function uploadAttachment(file) {
    if (!currentSession && !(await ensureSession())) { addSystem('Could not start a chat.'); return; }
    try {
      const isImage = (file.type || '').startsWith('image/');
      const toSend = isImage ? await resizeImage(file) : file;
      const form = new FormData();
      form.append('file', toSend, file.name);
      const res = await fetch('/api/upload?session=' + currentSession.id, { method: 'POST', body: form });
      if (!res.ok) { addSystem('Upload failed: ' + (await res.text())); return; }
      const data = await res.json();
      const img = data.kind === 'image';
      const localUrl = img ? URL.createObjectURL(file) : null;
      pendingAttachments.push({ id: data.id, filename: data.filename, name: data.name, mediaType: data.mediaType, isImage: img, localUrl });
      renderAttachments();
      fetchContext(); // the file is now in the workspace — reflect it in the context bar
    } catch (e) { addSystem('Upload error: ' + e.message); }
  }

  // ---- send ----
  async function send() {
    const text = inputEl.value.trim();
    const atts = pendingAttachments.slice();
    if (!text && !atts.length) return;
    if (isRecording) { voiceInputSent = true; try { recognition.stop(); } catch (e) {} }

    // Composing a brand-new chat: create + connect the session first (text/attachment
    // are captured above, so ensureSession won't clobber them).
    if (!currentSession) {
      if (!(await ensureSession())) { addSystem('Could not start a chat.'); return; }
    }
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      await waitForWsOpen(5000);
      if (!ws || ws.readyState !== WebSocket.OPEN) { addSystem('Not connected — try again.'); return; }
    }

    maybeAutoTitle(text);
    addUser(text, attachmentRender(atts));
    showThinking();
    const payload = { type: 'user', content: text, model: currentModel };
    if (atts.length) {
      payload.attachments = atts.map(a => ({ id: a.id, file: a.filename, name: a.name, type: a.mediaType }));
    }
    ws.send(JSON.stringify(payload));
    inputEl.value = ''; autoGrow();
    clearDraft();
    clearAttachments();
    setGenerating(true);
  }
  // attachmentRender turns pending attachments into addUser opts: images render as
  // thumbnails, everything else as file chips.
  function attachmentRender(atts) {
    if (!atts || !atts.length) return null;
    return {
      images: atts.filter(a => a.isImage).map(a => uploadUrl(a.filename)),
      files: atts.filter(a => !a.isImage).map(a => ({ name: a.name || a.filename, url: uploadUrl(a.filename) })),
    };
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
      // No session needed — voice just transcribes into the composer; the session
      // is created on send. (Previously this bailed in a new chat → mic dead there.)
      if (isRecording) { try { recognition.stop(); } catch (e) {} }
      else { try { recognition.start(); } catch (e) {} }
    });
  }

  // ---- input focus/collapse ----
  // Keep textarea focus when tapping a toolbar button.
  [attachBtn, micBtn, sendBtn, stopBtn].forEach(b => b.addEventListener('mousedown', e => e.preventDefault()));

  // ---- sidebar ----
  const appEl = $('app');
  // Mobile: the sidebar sits underneath; opening slides the main page right to
  // reveal it (#app.sidebar-open). Closing happens via ☰, selecting a chat, or swipe.
  function openSidebar() { appEl.classList.add('sidebar-open'); }
  function closeSidebar() { appEl.classList.remove('sidebar-open'); }
  const mqMobile = window.matchMedia('(max-width: 768px)');
  // The ☰ button: on mobile it reveals the under-sidebar; on desktop it shows/hides
  // the persistent sidebar (state on <html data-sidebar>, remembered across refresh
  // and applied pre-paint by a <head> script so there's no flash).
  function toggleSidebar() {
    if (mqMobile.matches) {
      appEl.classList.toggle('sidebar-open');
    } else {
      const el = document.documentElement;
      const collapsed = el.getAttribute('data-sidebar') !== 'collapsed';
      if (collapsed) el.setAttribute('data-sidebar', 'collapsed');
      else el.removeAttribute('data-sidebar');
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

  // Swipe left on the revealed sidebar to close it (item #9).
  let sbStartX = 0, sbSwiping = false;
  sidebar.addEventListener('touchstart', e => {
    if (!appEl.classList.contains('sidebar-open')) return;
    sbStartX = e.touches[0].clientX; sbSwiping = true;
  }, { passive: true });
  sidebar.addEventListener('touchend', e => {
    if (!sbSwiping) return;
    sbSwiping = false;
    if (e.changedTouches[0].clientX - sbStartX < -60) closeSidebar();
  });

  // pull-to-refresh: DISABLED — too easy to trigger accidentally (it reloaded the
  // page mid-session). The gesture handlers are intentionally not wired; the
  // #pull-indicator stays hidden. Refresh the browser normally if needed.

  // Copy helpers (delegated; work on hover-click + touch).
  function fallbackCopy(text, done) {
    const ta = document.createElement('textarea');
    ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0';
    document.body.appendChild(ta); ta.select();
    try { document.execCommand('copy'); done(); } catch (e) {}
    document.body.removeChild(ta);
  }
  function copyText(text, btn) {
    const done = () => { btn.classList.add('copied'); setTimeout(() => btn.classList.remove('copied'), 1200); };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done).catch(() => fallbackCopy(text, done));
    } else { fallbackCopy(text, done); }
  }
  messagesEl.addEventListener('click', (e) => {
    const codeBtn = e.target.closest('.code-copy');
    if (codeBtn) {
      const pre = codeBtn.closest('pre');
      const code = pre && pre.querySelector('code');
      copyText(code ? code.textContent : (pre ? pre.textContent : ''), codeBtn);
      return;
    }
    const btn = e.target.closest('.copy-btn');
    if (btn) {
      const msg = btn.closest('.message');
      if (msg && msg._raw) copyText(msg._raw, btn);
    }
  });

  // Tapping anywhere outside the composer dismisses the mobile keyboard.
  $('stage').addEventListener('click', (e) => {
    if (!e.target.closest('#input-area')) inputEl.blur();
  });

  // Mobile: when the sidebar is open, a tap anywhere on the main panel (outside the
  // sidebar) closes it — and is consumed, so it doesn't also trigger what it hit.
  mainEl.addEventListener('click', (e) => {
    if (appEl.classList.contains('sidebar-open')) {
      e.preventDefault();
      e.stopPropagation();
      closeSidebar();
    }
  }, true);

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
    for (const f of Array.from(fileInput.files || [])) uploadAttachment(f);
    fileInput.value = '';
  });
  // Per-chip remove (delegated: chips are rebuilt on every change).
  imagePreview.addEventListener('click', e => {
    const btn = e.target.closest('.attach-remove');
    if (btn) removeAttachment(btn.dataset.id);
  });
  modelSelect.addEventListener('change', () => { currentModel = modelSelect.value; syncModelLabel(); persistModel(); });
  applySessionModel(currentModel); // initialize the picker (defaults to Opus until a session sets it)
  inputEl.addEventListener('input', () => { autoGrow(); saveDraft(); });
  inputEl.addEventListener('keydown', e => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
  });
  // Esc interrupts Claude while it's generating (item #26).
  document.addEventListener('keydown', e => { if (e.key === 'Escape' && !stopBtn.disabled) interrupt(); });

  // ---- theme (light/dark) ----
  // data-theme is set pre-paint from localStorage; this wires the toggle and keeps
  // the mobile status-bar color in sync. Dark is the default (no attribute / :root).
  function currentTheme() {
    return document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark';
  }
  function syncThemeColor() {
    const m = document.querySelector('meta[name="theme-color"]');
    if (!m) return;
    const bg = getComputedStyle(document.documentElement).getPropertyValue('--bg').trim();
    if (bg) m.setAttribute('content', bg);
  }
  function setTheme(t) {
    document.documentElement.setAttribute('data-theme', t);
    try { localStorage.setItem('theme', t); } catch (e) {}
    syncThemeColor();
  }
  function setupTheme() {
    syncThemeColor();
    const btn = $('theme-toggle');
    if (btn) btn.addEventListener('click', () => setTheme(currentTheme() === 'light' ? 'dark' : 'light'));
  }

  // ---- boot ----
  async function boot() {
    // (desktop sidebar-collapsed state is applied pre-paint by the <head> script)
    setupTheme();
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
