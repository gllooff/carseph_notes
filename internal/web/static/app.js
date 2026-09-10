// Main app: session bootstrap, router, notes CRUD (M2), settings.

import { api, post, showError, showMsg } from './api.js';

const { startRegistration } = window.SimpleWebAuthnBrowser;
const { marked } = window.marked;
const DOMPurify = window.DOMPurify;

marked.setOptions({ breaks: true, gfm: true });

// ----- router -----

const pages = ['notes', 'files', 'settings'];

function currentRoute() {
  const p = location.pathname.replace(/^\/|\/$/g, '');
  return pages.includes(p) ? p : 'notes';
}

function render() {
  const route = currentRoute();
  for (const p of pages) {
    document.getElementById(`page-${p}`).hidden = p !== route;
  }
  for (const a of document.querySelectorAll('#mainnav a')) {
    a.classList.toggle('active', a.dataset.nav === route);
  }
  history.replaceState(null, '', route === 'notes' ? '/' : `/${route}`);
  if (route === 'settings') loadSettings();
  if (route === 'notes') loadNotesView();
}

// ----- markdown -----

function renderMarkdown(text) {
  return DOMPurify.sanitize(marked.parse(text || ''));
}

// ----- notes state -----

const state = {
  notes: [],
  folders: [],
  tags: [],
  filter: {},          // {folder:'id'|'none', tag:'x', q:'y'}
  currentId: null,
  dirty: false,
};

const $ = (id) => document.getElementById(id);

function folderLabel() {
  if (state.filter.folder === 'none') return 'Unfiled';
  if (state.filter.folder) {
    const f = state.folders.find((f) => f.id === state.filter.folder);
    return f ? f.name : 'Folder';
  }
  return 'All notes';
}

async function loadNotesView() {
  try {
    const [folders, tags] = await Promise.all([
      api('GET', '/api/folders'),
      api('GET', '/api/tags'),
    ]);
    state.folders = folders.folders;
    state.tags = tags.tags;
    await refreshNotes();
    renderSidebar();
  } catch (err) {
    if (err.status === 401) return redirectLogin();
    showError(err);
  }
}

async function refreshNotes() {
  const params = new URLSearchParams();
  if (state.filter.folder) params.set('folder', state.filter.folder);
  if (state.filter.tag) params.set('tag', state.filter.tag);
  if (state.filter.q) params.set('q', state.filter.q);
  const qs = params.toString();
  const res = await api('GET', '/api/notes' + (qs ? `?${qs}` : ''));
  state.notes = res.notes;
  renderNoteList();
}

function renderSidebar() {
  const fl = $('folder-list');
  fl.textContent = '';
  const all = document.createElement('li');
  all.textContent = 'All notes';
  all.dataset.folder = '';
  if (!state.filter.folder && !state.filter.tag) all.classList.add('active');
  all.addEventListener('click', () => { state.filter = {}; applyFilter(); });
  fl.appendChild(all);

  for (const f of state.folders) {
    const li = document.createElement('li');
    li.textContent = f.name;
    const count = document.createElement('span');
    count.className = 'count';
    count.textContent = f.count;
    li.appendChild(count);
    li.dataset.folder = f.id;
    if (state.filter.folder === f.id) li.classList.add('active');
    li.addEventListener('click', () => { state.filter = { folder: f.id }; applyFilter(); });
    li.addEventListener('contextmenu', (e) => { e.preventDefault(); deleteFolder(f); });
    fl.appendChild(li);
  }

  const tl = $('tag-list');
  tl.textContent = '';
  for (const t of state.tags) {
    const li = document.createElement('li');
    li.textContent = `#${t.name}`;
    const count = document.createElement('span');
    count.className = 'count';
    count.textContent = t.count;
    li.appendChild(count);
    if (state.filter.tag === t.name) li.classList.add('active');
    li.addEventListener('click', () => { state.filter = { tag: t.name }; applyFilter(); });
    tl.appendChild(li);
  }
  $('list-title').textContent = state.filter.tag ? `#${state.filter.tag}` : folderLabel();
}

async function applyFilter() {
  try {
    await refreshNotes();
    renderSidebar();
    closeEditor();
  } catch (err) { showError(err); }
}

function renderNoteList() {
  const ul = $('note-list');
  ul.textContent = '';
  if (!state.notes.length) {
    const li = document.createElement('li');
    li.className = 'muted';
    li.textContent = state.filter.q || state.filter.tag || state.filter.folder
      ? 'No notes match.' : 'No notes yet — create one!';
    ul.appendChild(li);
    return;
  }
  for (const n of state.notes) {
    const li = document.createElement('li');
    li.dataset.id = n.id;
    if (n.id === state.currentId) li.classList.add('active');
    const t = document.createElement('span');
    t.className = 'nt';
    t.textContent = n.title;
    const m = document.createElement('span');
    m.className = 'nm';
    const tags = n.tags && n.tags.length ? ` · ${n.tags.map((x) => '#' + x).join(' ')}` : '';
    m.textContent = `${new Date(n.updated_at * 1000).toLocaleDateString()}${tags}`;
    li.appendChild(t);
    li.appendChild(m);
    li.addEventListener('click', () => openNote(n.id));
    ul.appendChild(li);
  }
}

// ----- editor -----

async function openNote(id) {
  try {
    const n = await api('GET', `/api/notes/${id}`);
    state.currentId = n.id;
    $('editor-pane').hidden = false;
    $('note-title').value = n.title;
    $('note-body').value = n.body || '';
    $('preview').innerHTML = renderMarkdown(n.body || '');
    populateFolderSelectAndSet(n.folder_id || '');
    $('note-tags').value = (n.tags || []).join(', ');
    for (const li of document.querySelectorAll('#note-list li')) {
      li.classList.toggle('active', li.dataset.id === id);
    }
  } catch (err) { showError(err); }
}

// populateFolderSelectAndSet refills the folder <select> and sets a value.
function populateFolderSelectAndSet(folderId) {
  const sel = $('note-folder');
  sel.textContent = '';
  const optNone = document.createElement('option');
  optNone.value = '';
  optNone.textContent = 'Unfiled';
  sel.appendChild(optNone);
  for (const f of state.folders) {
    const o = document.createElement('option');
    o.value = f.id;
    o.textContent = f.name;
    sel.appendChild(o);
  }
  sel.value = folderId || '';
}

function closeEditor() {
  state.currentId = null;
  $('editor-pane').hidden = true;
}

function newNote() {
  state.currentId = null;
  $('editor-pane').hidden = false;
  $('note-title').value = '';
  $('note-title').focus();
  $('note-body').value = '';
  $('preview').innerHTML = renderMarkdown('');
  populateFolderSelectAndSet(state.filter.folder === 'none' ? '' : state.filter.folder || '');
  $('note-tags').value = state.filter.tag || '';
}

function editorPayload() {
  const folderId = $('note-folder').value || null;
  const tags = $('note-tags').value.split(',').map((s) => s.trim()).filter(Boolean);
  return { title: $('note-title').value.trim() || 'Untitled', body: $('note-body').value, folder_id: folderId, tags };
}

async function saveNote() {
  try {
    const payload = editorPayload();
    if (state.currentId) {
      await api('PUT', `/api/notes/${state.currentId}`, payload);
    } else {
      const n = await post('/api/notes', payload);
      state.currentId = n.id;
    }
    showMsg('Saved.', 'ok');
    await loadNotesView();
    // restore selection after refresh
    for (const li of document.querySelectorAll('#note-list li')) {
      li.classList.toggle('active', li.dataset.id === state.currentId);
    }
  } catch (err) { showError(err); }
}

async function deleteCurrentNote() {
  if (!state.currentId) return;
  if (!confirm('Delete this note? This cannot be undone.')) return;
  try {
    await api('DELETE', `/api/notes/${state.currentId}`);
    closeEditor();
    await refreshNotes();
  } catch (err) { showError(err); }
}

// ----- folders -----

async function createFolder(e) {
  e.preventDefault();
  const name = $('folder-name').value.trim();
  if (!name) return;
  try {
    await post('/api/folders', { name });
    $('folder-name').value = '';
    await loadNotesView();
  } catch (err) { showError(err); }
}

async function deleteFolder(f) {
  if (!confirm(`Delete folder "${f.name}"? Notes inside become unfiled.`)) return;
  try {
    await api('DELETE', `/api/folders/${f.id}`);
    if (state.filter.folder === f.id) state.filter = {};
    await loadNotesView();
  } catch (err) { showError(err); }
}

// ----- settings (unchanged from M1) -----

async function loadSettings() {
  try {
    const me = await api('GET', '/api/auth/me');
    document.getElementById('whoami').textContent = me.user.username;
    renderPasskeys(me.passkeys);
  } catch (err) {
    if (err.status === 401) redirectLogin();
  }
}

function renderPasskeys(pks) {
  const tbody = document.getElementById('pk-rows');
  tbody.textContent = '';
  for (const pk of pks) {
    const tr = document.createElement('tr');

    const nameTd = document.createElement('td');
    nameTd.textContent = pk.name || '(unnamed)';
    tr.appendChild(nameTd);

    const createdTd = document.createElement('td');
    createdTd.textContent = new Date(pk.created_at * 1000).toLocaleDateString();
    tr.appendChild(createdTd);

    const usedTd = document.createElement('td');
    usedTd.textContent = pk.last_used_at ? new Date(pk.last_used_at * 1000).toLocaleString() : '—';
    tr.appendChild(usedTd);

    const actionsTd = document.createElement('td');

    const renameBtn = document.createElement('button');
    renameBtn.className = 'linklike';
    renameBtn.textContent = 'Rename';
    renameBtn.addEventListener('click', async () => {
      const name = prompt('Passkey name', pk.name || '');
      if (name === null) return;
      try {
        await api('PATCH', `/api/passkeys/${pk.id}`, { name });
        loadSettings();
      } catch (err) { showError(err); }
    });
    actionsTd.appendChild(renameBtn);

    const delBtn = document.createElement('button');
    delBtn.className = 'linklike';
    delBtn.textContent = 'Delete';
    delBtn.addEventListener('click', async () => {
      if (!confirm('Delete this passkey?')) return;
      try {
        await api('DELETE', `/api/passkeys/${pk.id}`);
        loadSettings();
      } catch (err) { showError(err); }
    });
    actionsTd.appendChild(delBtn);

    tr.appendChild(actionsTd);
    tbody.appendChild(tr);
  }
}

document.getElementById('btn-add-passkey').addEventListener('click', async () => {
  try {
    const options = await post('/api/passkeys/begin', {});
    const attResp = await startRegistration({ optionsJSON: options });
    await post('/api/passkeys/finish', { response: attResp, name: `Passkey ${new Date().toLocaleDateString()}` });
    showMsg('Passkey added.', 'ok');
    loadSettings();
  } catch (err) {
    showError(err);
  }
});

document.getElementById('btn-logout').addEventListener('click', async () => {
  try { await post('/api/auth/logout', {}); } catch { /* ignore */ }
  location.href = '/login';
});

// ----- wire notes UI -----

document.getElementById('btn-new-note').addEventListener('click', newNote);
document.getElementById('btn-save').addEventListener('click', saveNote);
document.getElementById('btn-delete').addEventListener('click', async () => {
  if (!state.currentId) return;
  if (!confirm('Delete this note? This cannot be undone.')) return;
  try {
    await api('DELETE', `/api/notes/${state.currentId}`);
    closeEditor();
    await refreshNotes();
  } catch (err) { showError(err); }
});
document.getElementById('note-body').addEventListener('input', (e) => {
  $('preview').innerHTML = renderMarkdown(e.target.value);
});
document.getElementById('search').addEventListener('input', (e) => {
  state.filter.q = e.target.value.trim() || undefined;
  if (!state.filter.q) delete state.filter.q;
  refreshNotes().catch(showError);
});
document.getElementById('folder-form').addEventListener('submit', createFolder);

function redirectLogin() { location.href = '/login'; }

// ----- boot -----

async function boot() {
  try {
    await api('GET', '/api/auth/me'); // 401 → redirect to login
    document.getElementById('mainnav').hidden = false;
    render();
  } catch (err) {
    if (err.status === 401) { location.href = '/login'; return; }
    showError(err);
  }
}

boot();
