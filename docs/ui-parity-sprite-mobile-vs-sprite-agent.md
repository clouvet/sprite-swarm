# UI Parity Analysis: sprite-mobile vs sprite-agent

Comparing `sprite-mobile/public/` (index.html, app.js, styles.css) against `sprite-agent/web/assets/` (index.html, app.js, styles.css).

---

## 1. Image Attachment

**sprite-mobile** — full implementation:
- Paperclip attach button (`#attach-btn`) and hidden `<input type="file" accept="image/*">` in the input row.
- Image preview panel (`#image-preview`) above the textarea shows a 48×48 thumbnail, filename, and ×-remove button.
- Client-side resizing: images wider/taller than 2048 px are downscaled to JPEG via `<canvas>` before upload.
- Upload via `POST /api/upload?session=<id>` (multipart); server returns `{ id, filename, mediaType, url }`.
- WS send payload carries `imageId`, `imageFilename`, `imageMediaType`; server stores the association.
- History replay re-fetches images from `/api/uploads/<session>/<filename>` and inlines them into user message bubbles (max 200×200 px thumbnail).
- Attach button only appears when input area is in "focused" CSS state.

**sprite-agent** — not implemented. No attach button, no file input, no preview, no upload endpoint call, no image fields in WS payload, no image rendering in history.

---

## 2. Voice Input (Speech Recognition)

**sprite-mobile** — full implementation:
- Microphone button (`#mic-btn`) with SVG icon, always visible in the input row.
- Uses `window.SpeechRecognition || window.webkitSpeechRecognition`; hidden with `.unsupported` class if API absent.
- Mode: `continuous = true`, `interimResults = true`, language `en-US`.
- Interim transcripts update the textarea in real-time; final transcripts are appended to any pre-existing text.
- Toggle on/off via button click; send clears voice state (`voiceInputSent` flag prevents `onresult` from overwriting cleared input).
- Recording state: button gains `.recording` class → red border + background + CSS `pulse-recording` keyframe animation.
- Graceful error handling: `not-allowed` error shows `alert()` about microphone permissions.

**sprite-agent** — not implemented. No mic button, no SpeechRecognition code.

---

## 3. Sprite Management / Navigation Modal

**sprite-mobile** — full implementation:
- Gear button (`#settings-btn`) in the header opens a "Manage Sprites" modal.
- **Manual sprites**: saved by name + Tailscale IP to `/api/sprites`; list rendered with current-sprite badge, delete button, click-to-navigate.
- **Network sprites** (auto-discovery): section shown when `/api/network/status` returns `enabled: true`; lists sprites from `/api/network/sprites` with online/recent/offline dot status, owner email, refresh button, remove button.
- Navigation calls `window.location.href` to the sprite's public URL or Tailscale URL; sprite wake-up is handled transparently by the tailnet-gate at the target URL.
- A "Switching to sprite..." full-screen overlay (`#switching-overlay`) provides visual feedback during navigation.

**sprite-agent** — not implemented. No settings button, no sprites modal, no `/api/sprites` or `/api/network/*` calls.

---

## 4. Fleet Panel (sidebar section)

**sprite-agent** — implemented:
- `#fleet-section` at the bottom of the sidebar shows a live roster polled every 5 s from `GET /api/fleet`.
- Each row: alive/dead dot, agent ID, role (accent color), present-human badge (👤), reapable badge (⌛), phase (truncated).
- Clicking an "attachable" row (agent has a `.url`) opens the agent's session service in a new browser tab.
- "Spawn worker" button (`#spawn-btn`) `POST /api/fleet/spawn`; result logged as a system message.
- Policy line (`#policy-line`) displays effective capability policy fetched from `GET /api/policy` (merge mode, spawn limits, permission mode) in monospace under the fleet header.

**sprite-mobile** — not implemented. No fleet section, no policy display, no spawn button.

---

## 5. Service Worker / PWA / Offline Shell

**sprite-mobile**:
- Registers `sw.js` on boot; forces an immediate `reg.update()` check.
- Service worker caches the app config (public URL, sprite name) via `GET_CACHED_CONFIG` / `CACHE_CONFIG` postMessages.
- Used to wake a sleeping sprite on next open: cached `publicUrl` is pinged before the WebSocket connects.
- `manifest.json` present with icons (192 + 512 px), `sw.js` file included, `apple-touch-icon` link tag.
- iOS PWA meta: `apple-mobile-web-app-capable`, `apple-mobile-web-app-status-bar-style: black-translucent`.
- Turbo/Hotwire Native bridge snippet to hide native nav bar when running inside an iOS native wrapper.

**sprite-agent**:
- No service worker registration.
- `manifest.json` present (referenced in HTML) but no SW, no icon assets in the repo.
- Meta tags: `mobile-web-app-capable` + `apple-mobile-web-app-capable` only (no status-bar-style, no apple-touch-icon).

---

## 6. Waking Overlay (sprite cold-start)

**sprite-mobile**:
- Full-screen `#waking-overlay` (z-index 1000) shown immediately on load; fades out after the sprite responds.
- `wakeUpSprite()` flow: read cached config → ping public URL → poll `GET /api/config` with up to 10 retries at 1 s intervals.
- On-screen debug log (`#waking-log`) appended with timestamped status lines (visible to user during boot).
- If sprite never responds: overlay stays, error text shown, `init()` retried after 5 s.
- After wake: service worker cache refreshed, keepalive WS connected, sessions and sprites loaded in parallel.

**sprite-agent** — not implemented. `boot()` calls `loadSessions()` directly with no wake gate.

---

## 7. Public-URL Keepalive

**sprite-mobile**:
- WebSocket keepalive to `/ws/keepalive` keeps the sprite process alive while the app is open; reconnects on close (2 s delay).
- Public-URL ping interval: `fetch(spritePublicUrl, { mode: 'no-cors' })` every 30 s to prevent the hosting platform from suspending the VM.
- Keepalive WS also handles server-pushed `reload` messages (triggers `location.reload()`).

**sprite-agent** — not implemented. Standard WS reconnect with exponential backoff only; no keepalive endpoint, no public-URL pinging.

---

## 8. Pull-to-Refresh

**sprite-mobile**:
- Native-style pull-to-refresh gesture: `touchstart` / `touchmove` / `touchend` on the document.
- Animated `#pull-indicator` circular widget (arrow → spinner → saves session + reloads).
- Threshold: 80 px pull distance; arrow rotates 180° when threshold reached.
- Guarded: disabled when sidebar or sprites modal is open; only fires when message list is scrolled to top (or in empty state).

**sprite-agent** — not implemented.

---

## 9. Sidebar Swipe-to-Close

**sprite-mobile**:
- Touch events on the open sidebar track horizontal swipe; translates the sidebar in real-time and fades the overlay proportionally.
- Closes if swipe left ≥ 80 px.

**sprite-agent** — not implemented. Sidebar closes only by tapping the overlay.

---

## 10. Chat Title Editing & Auto-Regeneration

**sprite-mobile**:
- Clicking the header title while a session is active replaces it with an `<input>` (inline edit).
- Blur or Enter commits the new name via `PATCH /api/sessions/:id`; Escape cancels.
- Auto-regeneration: after every 6th assistant message the title is refreshed via `POST /api/sessions/:id/regenerate-title`.

**sprite-agent** — not implemented. Title is static (set to session name on select, reset to `'sprite-agent'` on delete). No edit, no regeneration.

---

## 11. Tool/Activity Indicator

**sprite-mobile**:
- Rich activity indicator (`div.activity-indicator`): human-readable action label ("Reading...", "Editing...", "Running...") + file-path/command detail line (truncated to 40 chars from the tail).
- Detail updates live as `input_json_delta` events stream in and the JSON can be partially parsed.
- Mapping table covers 13 tools: Read, Write, Edit, Bash, Grep, Glob, Task, WebFetch, WebSearch, LSP, TodoWrite, AskUserQuestion, NotebookEdit. Unknown tools fall back to "Using <name>".

**sprite-agent**:
- Simple single-line `div.tool-indicator` rendered as a centered muted message: `🔧 <toolName>`.
- No detail, no live updates, no tool mapping.

---

## 12. Thinking Indicator

**sprite-mobile**:
- `div.thinking-indicator` with three bouncing purple dots and "Claude is thinking..." text label.
- Animated via `@keyframes bounce` with staggered delays.

**sprite-agent**:
- `div#thinking.message.assistant.thinking` with three `·` spans pulsing via `@keyframes pulse`.
- No text label.

---

## 13. Streaming Cursor

**sprite-mobile** — none. The `.streaming` class on `.message-content` is a hook for styling only (no visible cursor animation).

**sprite-agent** — blinking block cursor: `.message.assistant .message-content.streaming::after { content: '▋'; animation: blink 1s steps(2) infinite; }`.

---

## 14. Syntax Highlighting

**sprite-mobile**:
- highlight.js loaded from CDN (`highlight.min.js` + `github-dark.min.css`).
- `marked` configured with `highlight` option; `hljs.highlightElement()` applied to all `pre code` blocks on message finalize and history render.

**sprite-agent** — not implemented. No highlight.js, no syntax coloring. Code blocks styled with dark background only.

---

## 15. Markdown Offline Fallback

**sprite-mobile** — implicit: if `marked` is undefined the `marked.parse()` call would throw. No explicit guard.

**sprite-agent** — explicit: `<script onerror="window.__noMarked=true">` on the CDN script tag; `renderMarkdown()` checks `window.__noMarked` and falls back to `escapeHtml + <br>` replacement.

---

## 16. Session List — Timestamp Column

**sprite-mobile**: Each session item renders a `div.session-time` showing the last-message time (today → HH:MM, older → Mon DD).

**sprite-agent**: No timestamp shown. Session items show name + preview only.

---

## 17. Session Restore Strategy

**sprite-mobile**: On boot, restores from `#session=<id>` hash first, then falls back to `localStorage.getItem('lastSessionId')` (written before pull-to-refresh reloads).

**sprite-agent**: Hash only. No localStorage fallback.

---

## 18. Iframe Embedding Support

**sprite-mobile**:
- Detects `window.parent !== window`.
- Syncs `location.hash` bidirectionally with the parent via `postMessage` (`hashchange` type).
- Sends a `ready` message to the parent before the wake sequence starts (used for unauthorized-sprite detection in the parent frame).
- Sprite navigation in iframes redirects `window.parent.location.href` to update the parent's address bar.

**sprite-agent** — not implemented.

---

## 19. User Message Header

**sprite-mobile**: User bubbles include a `div.message-header` with the text "You".

**sprite-agent**: User bubbles have no header; content only.

---

## 20. Send / Stop Button Design

| | sprite-mobile | sprite-agent |
|---|---|---|
| Send icon | SVG paper-plane | Unicode `▶` |
| Send shape | Circle (44 px) | Rounded square (40 px) |
| Send disabled | `opacity: 0` (invisible) | `opacity: 0.4` (visible but muted) |
| Stop icon | SVG square | Unicode `■` |
| Stop shape | Circle, purple | Rounded square, red (`#f87171`) |
| Stop visibility | Hidden; appears only when `#input-area.focused` | Always visible |

---

## 21. Input Area Layout & Collapsed State

**sprite-mobile**:
- `#input-area` is `display: none` until a session is active (`.active` class).
- Within an active session, the input row is "collapsed" by default: only the textarea and send/stop are shown. Focusing the textarea adds `.focused` to `#input-area`, which reveals the attach and mic buttons and forces the textarea to full width via CSS `order: -1; flex: 1 1 100%`.
- `mousedown` on action buttons calls `preventDefault()` to prevent input blur.
- `inputEl.blur()` on send to dismiss the mobile keyboard.
- Auto-grow textarea: max 120 px.

**sprite-agent**:
- Input area is always visible (no `.active` gating), constrained to `max-width: 792px` centered.
- No collapsed/expanded states; attach and mic buttons do not exist.
- Auto-grow textarea: max 160 px.

---

## 22. Status Indicator Style

**sprite-mobile**: Status text with a `👾` emoji prefix, colorized via CSS filters (grayscale/sepia/hue-rotate) for connected/error/idle states.

**sprite-agent**: Pill-shaped badge with colored background (`rgba(74,222,128,.15)` green for connected, `rgba(248,113,113,.15)` red for error).

---

## 23. Mobile Viewport / iOS Safe-Area Handling

**sprite-mobile**:
- `env(safe-area-inset-bottom/top)` CSS variables (`--safe-bottom`, `--safe-top`) applied to sidebar, header, input area, and modals.
- `#input-area::after` pseudo-element extends the background 200 px below the bottom to cover the gap above the home indicator.
- `touchend` double-tap zoom prevention (`e.preventDefault()` if two touches within 300 ms).
- `autocorrect="on"`, `autocapitalize="sentences"`, `spellcheck="true"` on textarea.

**sprite-agent** — no safe-area handling, no double-tap prevention, no extra textarea attributes beyond `autocapitalize` and `spellcheck`.

---

## 24. Dynamic Sprite Name

**sprite-mobile**: The sprite's display name is derived from the public URL subdomain (`getSpriteNameFromUrl()`) or from `config.spriteName`; used in the page title, the header (when no session selected), and the welcome message.

**sprite-agent**: Application name is hardcoded as `"sprite-agent"` in the HTML title and empty-state heading; no dynamic naming.

---

## 25. Error Message Handling in Protocol

**sprite-mobile** — not handled. The `handleMessage` switch has no `error` case; server errors are silently dropped.

**sprite-agent** — handled: `case 'error'` adds a system message `"⚠ <message>"` and finalizes any in-progress assistant message.

---

## 26. Escape Key Interrupt

**sprite-mobile** — not implemented at the keyboard level. Stop is only triggered via the stop button click.

**sprite-agent** — global `document.addEventListener('keydown', e => { if (e.key === 'Escape') interrupt(); })`.

---

## 27. Color Palette

| Token | sprite-mobile | sprite-agent |
|---|---|---|
| Background | `#1a1a2e` | `#15151f` |
| Panel | `#252539` | `#1a1a2e` |
| Accent | `#a855f7` (purple) | `#6c63ff` (indigo) |
| User bubble | `#3b3b4f` | `#2a3a5a` (blue-tinted) |
| Border | `#333347` | `#2c2c44` |
| Font stack | `-apple-system, BlinkMacSystemFont, "SF Pro Text", "Helvetica Neue", "Noto Sans JP"` | `-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto` |

sprite-mobile also loads and embeds the Noto Sans JP font (`NotoSansJP-Regular.ttf`) as a local asset for Japanese text support. sprite-agent does not.

---

## Summary Table

| Feature | sprite-mobile | sprite-agent |
|---|---|---|
| Image attach & upload | ✅ Full | ✗ |
| Voice input (Speech API) | ✅ Full | ✗ |
| Sprite management modal | ✅ Full | ✗ |
| Network sprite discovery | ✅ Full | ✗ |
| Fleet panel (sidebar) | ✗ | ✅ Full |
| Spawn worker button | ✗ | ✅ |
| Policy display | ✗ | ✅ |
| Service worker / PWA | ✅ Full | ✗ |
| Waking overlay (cold-start) | ✅ Full | ✗ |
| Public-URL keepalive | ✅ | ✗ |
| WS keepalive endpoint | ✅ `/ws/keepalive` | ✗ |
| Pull-to-refresh | ✅ | ✗ |
| Sidebar swipe-to-close | ✅ | ✗ |
| Chat title inline edit | ✅ | ✗ |
| Auto-regenerate title | ✅ (every 6 msgs) | ✗ |
| Rich tool activity indicator | ✅ (13 tools, live detail) | Minimal (name only) |
| Streaming cursor `▋` | ✗ | ✅ |
| Syntax highlighting (hljs) | ✅ | ✗ |
| Markdown offline fallback | Implicit | ✅ Explicit |
| Session timestamp in list | ✅ | ✗ |
| localStorage session restore | ✅ | ✗ |
| Iframe embedding support | ✅ | ✗ |
| Error WS message type | ✗ | ✅ |
| Escape-key interrupt | ✗ | ✅ |
| Safe-area inset handling | ✅ | ✗ |
| iOS PWA meta tags | ✅ Full | Partial |
| Double-tap zoom prevention | ✅ | ✗ |
| Dynamic sprite name | ✅ | ✗ (hardcoded) |
| "You" header on user msgs | ✅ | ✗ |
| Switching overlay | ✅ | ✗ |
| Collapsed input on mobile | ✅ | ✗ |
