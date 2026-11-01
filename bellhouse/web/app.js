// Bellhouse PWA. Sign-in mirrors binday's auth.js (PKCE against id.swee.net);
// trimmed here for the prototype.
const $ = (id) => document.getElementById(id);
const TOK = 'bellhouse.tokens';
let cfg, me;

// ── auth ────────────────────────────────────────────────────────────────
const rand = (n) => btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(n)))).replace(/[+/=]/g, (c) => ({ '+': '-', '/': '_', '=': '' })[c]);
async function challenge(v) {
  const h = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(v));
  return btoa(String.fromCharCode(...new Uint8Array(h))).replace(/[+/=]/g, (c) => ({ '+': '-', '/': '_', '=': '' })[c]);
}
const tokens = () => { try { return JSON.parse(localStorage.getItem(TOK)) || {}; } catch { return {}; } };
const saveTokens = (t) => localStorage.setItem(TOK, JSON.stringify(t));

async function login() {
  const verifier = rand(32), state = rand(16);
  sessionStorage.setItem('pkce', JSON.stringify({ verifier, state }));
  location.href = `${cfg.identity_url}/oauth/authorize?` + new URLSearchParams({
    response_type: 'code', client_id: cfg.client_id, redirect_uri: `${location.origin}/`,
    code_challenge: await challenge(verifier), code_challenge_method: 'S256', state,
  });
}

async function completeLogin() {
  const q = new URLSearchParams(location.search);
  if (!q.get('code')) return;
  history.replaceState(null, '', location.pathname);
  const { verifier, state } = JSON.parse(sessionStorage.getItem('pkce') || '{}');
  if (q.get('state') !== state) throw new Error('Sign-in state mismatch — try again');
  await tokenRequest({ grant_type: 'authorization_code', code: q.get('code'), redirect_uri: `${location.origin}/`, code_verifier: verifier });
}

async function tokenRequest(params) {
  const r = await fetch(`${cfg.identity_url}/oauth/token`, {
    method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ client_id: cfg.client_id, ...params }),
  });
  if (!r.ok) return false;
  saveTokens(await r.json());
  return true;
}

let refreshing;
async function api(method, path, body, retry = true) {
  const headers = { 'Content-Type': 'application/json' };
  if (cfg.auth) headers.Authorization = `Bearer ${tokens().access_token}`;
  const r = await fetch(path, { method, headers, body: body && JSON.stringify(body) });
  if (r.status === 401 && cfg.auth && retry && tokens().refresh_token) {
    // One refresh at a time: identity rotates refresh tokens and treats reuse as theft.
    refreshing ??= tokenRequest({ grant_type: 'refresh_token', refresh_token: tokens().refresh_token }).finally(() => { refreshing = null; });
    if (await refreshing) return api(method, path, body, false);
  }
  if (r.status === 401) { localStorage.removeItem(TOK); throw Object.assign(new Error('signed out'), { signedOut: true }); }
  if (!r.ok) throw new Error(`${method} ${path}: ${r.status}`);
  return r.status === 204 ? null : r.json();
}

// ── push ────────────────────────────────────────────────────────────────
const isIOS = /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
const standalone = matchMedia('(display-mode: standalone)').matches || navigator.standalone === true;
const pushCapable = 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;

function keyBytes(b64) {
  const s = atob(b64.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (b64.length % 4)) % 4));
  return Uint8Array.from(s, (c) => c.charCodeAt(0));
}

function deviceLabel() {
  const ua = navigator.userAgent;
  const kind = /iPad/.test(ua) ? 'iPad' : /iPhone/.test(ua) ? 'iPhone' : /Android/.test(ua) ? 'Android' : /Mac/.test(ua) ? 'Mac' : 'Browser';
  return `${me?.username || 'My'}'s ${kind}`;
}

async function registration() { return navigator.serviceWorker.register('/sw.js'); }

// Called on every open while permission is granted. iOS can drop a
// subscription without telling us (no pushsubscriptionchange), so re-post it:
// the server refreshes last_seen, and a lost one is replaced here.
async function syncSubscription() {
  const reg = await registration();
  let sub = await reg.pushManager.getSubscription();
  if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(cfg.vapid_public_key) });
  const saved = await api('POST', '/api/me/subscriptions', { ...sub.toJSON(), label: deviceLabel() });
  localStorage.setItem('bellhouse.sub', saved.id);
  return saved;
}

async function enable() {
  // Must run inside the tap handler: iOS only prompts from a user gesture.
  const perm = await Notification.requestPermission();
  if (perm !== 'granted') { showError('Notifications were not allowed. Enable them in Settings → Notifications → Bells.'); return; }
  await syncSubscription();
  await refresh();
}

// ── render ──────────────────────────────────────────────────────────────
function showError(msg) { $('error').textContent = msg; $('error').hidden = !msg; }

function el(tag, attrs = {}, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) k.startsWith('on') ? e.addEventListener(k.slice(2), v) : k in e ? (e[k] = v) : e.setAttribute(k, v);
  e.append(...kids.filter((k) => k != null));
  return e;
}

const ago = (t) => { if (!t || t.startsWith('0001')) return 'never'; const m = Math.round((Date.now() - Date.parse(t)) / 60000);
  return m < 1 ? 'just now' : m < 60 ? `${m} min ago` : m < 1440 ? `${Math.round(m / 60)} h ago` : `${Math.round(m / 1440)} d ago`; };

async function savePrefs() { await api('PUT', '/api/me/prefs', me.prefs); }

function renderKinds() {
  const box = $('kinds'); box.replaceChildren();
  for (const c of cfg.catalog) {
    const kp = me.prefs.kinds[c.kind] ??= { enabled: c.default_on };
    const only = el('input', { type: 'text', placeholder: 'All — or list ids, comma-separated', value: (kp.only || []).join(', '),
      onchange: (e) => { kp.only = e.target.value.split(',').map((s) => s.trim()).filter(Boolean); savePrefs(); } });
    const onlyRow = el('div', { className: 'row', hidden: !kp.enabled || c.kind === 'doorbell' }, el('div', { className: 'grow' }, el('div', { className: 'sub' }, 'Only these'), only));
    box.append(el('div', { className: 'row' },
      el('div', { className: 'grow' }, c.label, el('div', { className: 'sub' }, c.description + (c.bypass_quiet ? ' · rings in quiet hours' : ''))),
      el('input', { type: 'checkbox', className: 'switch', checked: kp.enabled,
        onchange: (e) => { kp.enabled = e.target.checked; onlyRow.hidden = !kp.enabled || c.kind === 'doorbell'; savePrefs(); } })), onlyRow);
  }
  const q = me.prefs.quiet;
  $('quietOn').checked = !!q; $('quietTimes').hidden = !q;
  if (q) { $('qs').value = q.start; $('qe').value = q.end; }
}

function renderSubs() {
  const box = $('subs'); box.replaceChildren();
  const mine = localStorage.getItem('bellhouse.sub');
  if (!me.subs?.length) box.append(el('div', { className: 'row sub' }, 'No devices yet.'));
  for (const s of me.subs || []) {
    box.append(el('div', { className: 'row' },
      el('div', { className: 'grow' }, `${s.label}${s.id === mine ? ' (this one)' : ''}`,
        el('div', { className: 'sub' }, s.last_err ? el('span', { className: 'bad' }, s.last_err) : `Last delivered ${ago(s.last_ok)} · opened ${ago(s.last_seen)}`)),
      el('button', { className: 'ghost', onclick: async () => { await api('DELETE', `/api/me/subscriptions/${s.id}`); refresh(); } }, 'Remove')));
  }
}

async function renderDevice() {
  const [state, sub, enableBtn, testBtn] = [$('devstate'), $('devsub'), $('enable'), $('test')];
  enableBtn.hidden = testBtn.hidden = true;
  if (isIOS && !standalone) { $('install').hidden = false; state.textContent = 'Open from the Home Screen to enable'; return; }
  if (!pushCapable) { state.textContent = 'This browser cannot receive push'; sub.textContent = isIOS ? 'Needs iOS 16.4 or later' : ''; return; }
  if (Notification.permission === 'granted') {
    try { await syncSubscription(); state.innerHTML = '<span class="ok">Notifications on</span>'; testBtn.hidden = false; }
    catch (e) { state.textContent = 'Could not subscribe'; sub.textContent = e.message; enableBtn.hidden = false; }
  } else if (Notification.permission === 'denied') {
    state.textContent = 'Notifications blocked'; sub.textContent = 'Re-allow them in Settings → Notifications → Bells';
  } else { state.textContent = 'Notifications off'; enableBtn.hidden = false; }
}

async function refresh() { me = await api('GET', '/api/me'); $('who').textContent = `Signed in as ${me.username}`; renderKinds(); renderSubs(); }

// ── boot ────────────────────────────────────────────────────────────────
$('login').onclick = login;
$('enable').onclick = () => enable().catch((e) => showError(e.message));
$('test').onclick = async () => { const r = await api('POST', '/api/me/test'); showError(r?.some((d) => d.err) ? r.map((d) => `${d.sub}: ${d.err || 'sent'}`).join('\n') : ''); refresh(); };
$('quietOn').onchange = (e) => { me.prefs.quiet = e.target.checked ? { start: $('qs').value, end: $('qe').value } : null; $('quietTimes').hidden = !e.target.checked; savePrefs(); };
$('qs').onchange = $('qe').onchange = () => { if (me.prefs.quiet) { me.prefs.quiet.start = $('qs').value; me.prefs.quiet.end = $('qe').value; savePrefs(); } };

(async () => {
  cfg = await (await fetch('/api/config')).json();
  if (pushCapable) registration(); // register early so the SW is ready by the time they tap Enable
  try {
    if (cfg.auth) { await completeLogin(); if (!tokens().access_token) { $('signin').hidden = false; return; } }
    await refresh();
    $('app').hidden = false;
    await renderDevice();
    renderSubs();
  } catch (e) {
    if (e.signedOut) { $('signin').hidden = false; return; }
    showError(e.message);
  }
})();
// Re-sync when resumed: iOS resumes installed web apps rather than reloading them.
document.addEventListener('visibilitychange', () => { if (!document.hidden && me && Notification.permission === 'granted') syncSubscription().catch(() => {}); });
