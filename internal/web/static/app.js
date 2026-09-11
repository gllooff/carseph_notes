// Main app: session bootstrap, router, notes CRUD (M2), settings.

import { api, post, showError, showMsg } from './api.js';
import { openViewer, openFileMenu, positionFileMenu } from './files.js';

const { startRegistration } = window.SimpleWebAuthnBrowser;

// ----- router -----

const pages = ['notes', 'settings'];

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

// ----- notes state -----

const state = {
  notes: [],
  files: [],
  folders: [],
  tags: [],
  filter: {},          // {folder:'id'|'none', tag:'x', q:'y'}
  dirty: false,
  selecting: false,
  selected: new Set(), // "note:{id}" | "file:{id}"
  sort: 'date-desc',   // 'date-desc'|'date-asc'|'name-asc'|'name-desc'
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
  const qsPart = qs ? `?${qs}` : '';
  const [nRes, fRes] = await Promise.all([
    api('GET', '/api/notes' + qsPart),
    api('GET', '/api/files' + qsPart).catch(() => ({ files: [] })),
  ]);
  state.notes = nRes.notes;
  state.files = fRes.files;
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
    li.dataset.folder = f.id;
    if (state.filter.folder === f.id) li.classList.add('active');

    const nameSpan = document.createElement('span');
    nameSpan.className = 'fname';
    nameSpan.textContent = f.name;
    li.appendChild(nameSpan);

    const count = document.createElement('span');
    count.className = 'count';
    count.textContent = f.count;
    li.appendChild(count);

    const menuBtn = document.createElement('button');
    menuBtn.className = 'kebab';
    menuBtn.title = 'Folder options';
    menuBtn.addEventListener('click', (e) => { e.stopPropagation(); openFolderMenu(f, menuBtn); });
    li.appendChild(menuBtn);

    li.addEventListener('click', () => { state.filter = { folder: f.id }; applyFilter(); });
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
  updateSelectBar();
}

async function applyFilter() {
  try {
    await refreshNotes();
    renderSidebar();
  } catch (err) { showError(err); }
}

function renderNoteList() {
  const ul = $('note-list');
  ul.textContent = '';
  const q = state.filter.q ? state.filter.q.toLowerCase() : '';
  const folderHits = !state.selecting && q && !state.filter.folder
    ? state.folders.filter((f) => f.name.toLowerCase().includes(q))
    : [];
  for (const f of folderHits) {
    const li = document.createElement('li');
    li.classList.add('file-item', 'folder-hit');
    const t = document.createElement('span');
    t.className = 'nt';
    t.textContent = `📁 ${f.name}`;
    const m = document.createElement('span');
    m.className = 'nm';
    m.textContent = `${f.count} item${f.count === 1 ? '' : 's'}`;
    li.appendChild(t);
    li.appendChild(m);
    li.addEventListener('click', () => {
      state.filter = { folder: f.id };
      $('search').value = '';
      applyFilter();
    });
    ul.appendChild(li);
  }
  const items = [
    ...state.notes.map((n) => ({ kind: 'note', ts: n.updated_at, data: n })),
    ...state.files.map((f) => ({ kind: 'file', ts: f.created_at, data: f })),
  ];
  if (state.sort === 'name-asc' || state.sort === 'name-desc') {
    items.sort((a, b) => {
      const na = a.kind === 'note' ? a.data.title : a.data.original_name;
      const nb = b.kind === 'note' ? b.data.title : b.data.original_name;
      const r = na.localeCompare(nb);
      return state.sort === 'name-asc' ? r : -r;
    });
  } else {
    items.sort((a, b) => {
      const r = b.ts - a.ts;
      return state.sort === 'date-desc' ? r : -r;
    });
  }
  if (!items.length && !folderHits.length) {
    const li = document.createElement('li');
    li.className = 'muted';
    li.textContent = state.filter.q || state.filter.tag || state.filter.folder
      ? 'Nothing matches.' : 'No notes yet — create one!';
    ul.appendChild(li);
    updateSelectBar();
    return;
  }
  for (const it of items) {
    const li = document.createElement('li');
    if (state.selecting) {
      const key = `${it.kind}:${it.data.id}`;
      const isSel = state.selected.has(key);
      li.classList.add('selectable');
      if (isSel) li.classList.add('selected');
      const cb = document.createElement('input');
      cb.type = 'checkbox';
      cb.checked = isSel;
      cb.className = 'item-check';
      li.appendChild(cb);
      const t = document.createElement('span');
      t.className = 'nt';
      t.textContent = it.kind === 'note' ? it.data.title
        : `${it.data.kind === 'image' ? '🖼' : it.data.kind === 'markdown' ? '📝' : '📄'} ${it.data.original_name}`;
      li.appendChild(t);
      li.addEventListener('click', () => {
        if (state.selected.has(key)) state.selected.delete(key);
        else state.selected.add(key);
        li.classList.toggle('selected', state.selected.has(key));
        cb.checked = state.selected.has(key);
        updateSelectBar();
      });
    } else if (it.kind === 'note') {
      const n = it.data;
      li.dataset.id = n.id;
      li.classList.add('file-item');
      const t = document.createElement('span');
      t.className = 'nt';
      t.textContent = n.title;
      const m = document.createElement('span');
      m.className = 'nm';
      const tags = n.tags && n.tags.length ? ` · ${n.tags.map((x) => '#' + x).join(' ')}` : '';
      m.textContent = `${new Date(n.updated_at * 1000).toLocaleDateString()}${tags}`;
      li.appendChild(t);
      li.appendChild(m);
      const kebab = document.createElement('button');
      kebab.className = 'kebab';
      kebab.title = 'Note options';
      kebab.addEventListener('click', (e) => {
        e.stopPropagation();
        openNoteMenu(n, kebab);
      });
      li.appendChild(kebab);
      li.addEventListener('click', () => { location.href = `/note/${n.id}`; });
    } else {
      const f = it.data;
      li.classList.add('file-item');
      const t = document.createElement('span');
      t.className = 'nt';
      const icon = f.kind === 'image' ? '🖼' : f.kind === 'markdown' ? '📝' : '📄';
      t.textContent = `${icon} ${f.original_name}`;
      const m = document.createElement('span');
      m.className = 'nm';
      m.textContent = `${new Date(f.created_at * 1000).toLocaleDateString()} · ${fmtSize(f.size)}`;
      li.appendChild(t);
      li.appendChild(m);
      const kebab = document.createElement('button');
      kebab.className = 'kebab';
      kebab.title = 'File options';
      kebab.addEventListener('click', (e) => {
        e.stopPropagation();
        openFileMenu(f, kebab);
      });
      li.appendChild(kebab);
      li.addEventListener('click', () => openViewer(f));
    }
    ul.appendChild(li);
  }
  updateSelectBar();
}

function fmtSize(n) {
  if (n > 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB';
  if (n > 1 << 10) return (n / (1 << 10)).toFixed(0) + ' KB';
  return n + ' B';
}

function updateSelectBar() {
  $('btn-new-note').hidden = state.selecting;
  $('btn-select').hidden = state.selecting;
  $('btn-delete-selected').hidden = !state.selecting;
  $('btn-cancel-select').hidden = !state.selecting;
  const n = state.selected.size;
  $('btn-delete-selected').textContent = n ? `Delete (${n})` : 'Delete';
  $('list-title').textContent = state.selecting
    ? (n ? `${n} selected` : 'Select items…')
    : folderLabel();
  updateSortButton();
}

const SORT_LABELS = {
  'date-desc': 'Newest',
  'date-asc': 'Oldest',
  'name-asc': 'A–Z',
  'name-desc': 'Z–A',
};

function updateSortButton() {
  $('btn-sort').textContent = `Sort: ${SORT_LABELS[state.sort]}`;
}

let activeSortMenu = null;

function toggleSortMenu(anchor) {
  if (activeSortMenu) { closeSortMenu(); return; }
  const menu = document.createElement('div');
  menu.className = 'folder-menu sort-menu';
  const opts = [
    { id: 'date-desc', label: 'Date modified · newest' },
    { id: 'date-asc', label: 'Date modified · oldest' },
    { id: 'name-asc', label: 'Name · A–Z' },
    { id: 'name-desc', label: 'Name · Z–A' },
  ];
  for (const o of opts) {
    const btn = document.createElement('button');
    btn.className = 'mf-item' + (state.sort === o.id ? ' checked' : '');
    btn.textContent = o.label;
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      closeSortMenu();
      if (state.sort === o.id) return;
      state.sort = o.id;
      renderNoteList();
    });
    menu.appendChild(btn);
  }
  anchor.parentElement.appendChild(menu);
  positionFileMenu(menu, anchor);
  activeSortMenu = menu;
  setTimeout(() => document.addEventListener('click', closeSortMenuOnClick, true), 0);
}

function closeSortMenu() {
  if (activeSortMenu) {
    activeSortMenu.remove();
    activeSortMenu = null;
  }
  document.removeEventListener('click', closeSortMenuOnClick, true);
}

function closeSortMenuOnClick(e) {
  if (activeSortMenu && !activeSortMenu.contains(e.target)) {
    closeSortMenu();
  }
}

function toggleSelectMode() {
  state.selecting = !state.selecting;
  state.selected.clear();
  renderNoteList();
}

async function deleteSelected() {
  const n = state.selected.size;
  if (!n) return;
  if (!confirm(`Delete ${n} item${n > 1 ? 's' : ''}? This cannot be undone.`)) return;
  try {
    for (const key of state.selected) {
      const sep = key.indexOf(':');
      const kind = key.slice(0, sep);
      const id = key.slice(sep + 1);
      if (kind === 'note') await api('DELETE', `/api/notes/${id}`);
      else await api('DELETE', `/api/files/${id}`);
    }
    state.selected.clear();
    state.selecting = false;
    await loadNotesView();
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

let activeFolderMenu = null;

function openFolderMenu(f, anchor) {
  closeFolderMenu();
  const menu = document.createElement('div');
  menu.className = 'folder-menu';
  menu.innerHTML = `
    <button class="mf-item" data-action="rename">Rename</button>
    <button class="mf-item mf-danger" data-action="delete">Delete</button>
  `;
  menu.addEventListener('click', async (e) => {
    const btn = e.target.closest('button');
    if (!btn) return;
    const action = btn.dataset.action;
    if (action === 'rename') {
      closeFolderMenu();
      renameFolder(f);
    } else if (action === 'delete') {
      closeFolderMenu();
      deleteFolder(f);
    }
  });
  anchor.parentElement.appendChild(menu);
  positionFolderMenu(menu, anchor);
  activeFolderMenu = menu;
  setTimeout(() => document.addEventListener('click', closeFolderMenuOnClick, true), 0);
}

function positionFolderMenu(menu, anchor) {
  const r = anchor.getBoundingClientRect();
  const pad = 4;
  let top = r.bottom + pad;
  menu.style.maxWidth = '160px';
  const mW = menu.getBoundingClientRect().width;
  let left = r.left;
  if (left + mW > window.innerWidth - pad) left = Math.max(pad, window.innerWidth - mW - pad);
  menu.style.top = top + 'px';
  menu.style.left = left + 'px';
}

function closeFolderMenu() {
  if (activeFolderMenu) {
    activeFolderMenu.remove();
    activeFolderMenu = null;
  }
  document.removeEventListener('click', closeFolderMenuOnClick, true);
}

function closeFolderMenuOnClick(e) {
  if (activeFolderMenu && !activeFolderMenu.contains(e.target)) {
    closeFolderMenu();
  }
}

let activeNoteMenu = null;

function openNoteMenu(n, anchor) {
  closeNoteMenu();
  const menu = document.createElement('div');
  menu.className = 'folder-menu';

  const refBtn = document.createElement('button');
  refBtn.className = 'mf-item';
  refBtn.textContent = 'Reference';
  refBtn.title = 'Copy a markdown note link to the clipboard';
  refBtn.addEventListener('click', async (e) => {
    e.stopPropagation();
    closeNoteMenu();
    const md = `[${n.title}](/note/${n.id})`;
    try {
      await navigator.clipboard.writeText(md);
      showMsg('Markdown link copied.', 'ok');
    } catch {
      prompt('Copy this snippet:', md);
    }
  });
  menu.appendChild(refBtn);

  const moveBtn = document.createElement('button');
  moveBtn.className = 'mf-item';
  moveBtn.textContent = 'Move';
  moveBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    closeNoteMenu();
    openNoteMoveDialog(n);
  });
  menu.appendChild(moveBtn);

  const renameBtn = document.createElement('button');
  renameBtn.className = 'mf-item';
  renameBtn.textContent = 'Rename';
  renameBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    closeNoteMenu();
    renameNote(n);
  });
  menu.appendChild(renameBtn);

  anchor.parentElement.appendChild(menu);
  positionFileMenu(menu, anchor);
  activeNoteMenu = menu;
  setTimeout(() => document.addEventListener('click', closeNoteMenuOnClick, true), 0);
}

function closeNoteMenu() {
  if (activeNoteMenu) {
    activeNoteMenu.remove();
    activeNoteMenu = null;
  }
  document.removeEventListener('click', closeNoteMenuOnClick, true);
}

function closeNoteMenuOnClick(e) {
  if (activeNoteMenu && !activeNoteMenu.contains(e.target)) {
    closeNoteMenu();
  }
}

async function renameNote(n) {
  const name = prompt('Rename note', n.title);
  if (name === null || !name.trim()) return;
  try {
    await api('PUT', `/api/notes/${n.id}`, { title: name.trim() });
    await loadNotesView();
  } catch (err) { showError(err); }
}

async function moveNoteToFolder(n, folderId) {
  try {
    await api('PUT', `/api/notes/${n.id}`, { folder_id: folderId || null });
    showMsg('Moved.', 'ok');
    await loadNotesView();
  } catch (err) { showError(err); }
}

async function openNoteMoveDialog(n) {
  let folders;
  try {
    const res = await api('GET', '/api/folders');
    folders = res.folders;
  } catch (err) { showError(err); return; }
  const overlay = document.createElement('div');
  overlay.className = 'modal-overlay';

  const box = document.createElement('div');
  box.className = 'modal-box';

  const h3 = document.createElement('h3');
  h3.textContent = `Move "${n.title}" to…`;
  box.appendChild(h3);

  const list = document.createElement('div');
  list.className = 'modal-folder-list';
  const opts = [{ id: '', name: 'Unfiled' }, ...(folders || [])];
  let selected = n.folder_id || '';
  for (const o of opts) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'mf-folder-opt' + (o.id === selected ? ' selected' : '');
    btn.textContent = o.name;
    btn.addEventListener('click', () => {
      selected = o.id;
      for (const el of list.querySelectorAll('.mf-folder-opt')) {
        el.classList.toggle('selected', el === btn);
      }
    });
    list.appendChild(btn);
  }
  box.appendChild(list);

  const row = document.createElement('div');
  row.className = 'modal-actions';
  const cancel = document.createElement('button');
  cancel.type = 'button';
  cancel.className = 'secondary';
  cancel.textContent = 'Cancel';
  const move = document.createElement('button');
  move.className = 'primary';
  move.textContent = 'Move';
  row.append(cancel, move);
  box.appendChild(row);
  overlay.appendChild(box);
  document.body.appendChild(overlay);

  const onKey = (e) => { if (e.key === 'Escape') close(); };
  const close = () => {
    document.removeEventListener('keydown', onKey);
    overlay.remove();
  };
  cancel.addEventListener('click', close);
  move.addEventListener('click', () => {
    close();
    moveNoteToFolder(n, selected || null);
  });
  overlay.addEventListener('click', (e) => { if (e.target === overlay) close(); });
  document.addEventListener('keydown', onKey);
}

async function renameFolder(f) {
  const name = prompt('Rename folder', f.name);
  if (name === null || !name.trim()) return;
  try {
    await api('PATCH', `/api/folders/${f.id}`, { name: name.trim() });
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
    delBtn.className = 'linklike danger';
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

let newNoteMenu = null;

function toggleNewNoteMenu(anchor) {
  if (newNoteMenu) { closeNewNoteMenu(); return; }
  closeNewNoteMenu();
  const menu = document.createElement('div');
  menu.className = 'folder-menu';
  const md = document.createElement('button');
  md.className = 'mf-item';
  md.textContent = 'Markdown note';
  md.addEventListener('click', () => {
    closeNewNoteMenu();
    const params = new URLSearchParams();
    if (state.filter.folder && state.filter.folder !== 'none') params.set('folder', state.filter.folder);
    if (state.filter.tag) params.set('tag', state.filter.tag);
    const qs = params.toString();
    location.href = '/note/new' + (qs ? `?${qs}` : '');
  });
  const up = document.createElement('button');
  up.className = 'mf-item';
  up.textContent = 'Upload file…';
  up.addEventListener('click', () => {
    closeNewNoteMenu();
    $('file-input').dataset.folder = state.filter.folder || '';
    $('file-input').click();
  });
  menu.appendChild(md);
  menu.appendChild(up);
  document.body.appendChild(menu);
  const r = anchor.getBoundingClientRect();
  const m = menu.getBoundingClientRect();
  menu.style.top = (r.bottom + 4) + 'px';
  menu.style.left = Math.max(4, r.right - m.width) + 'px';
  newNoteMenu = menu;
  setTimeout(() => document.addEventListener('click', closeNewNoteMenuOnClick, true), 0);
}

function closeNewNoteMenu() {
  if (newNoteMenu) { newNoteMenu.remove(); newNoteMenu = null; }
  document.removeEventListener('click', closeNewNoteMenuOnClick, true);
}

function closeNewNoteMenuOnClick(e) {
  if (newNoteMenu && !newNoteMenu.contains(e.target) && e.target !== document.getElementById('btn-new-note')) {
    closeNewNoteMenu();
  }
}

document.getElementById('btn-new-note').addEventListener('click', (e) => toggleNewNoteMenu(e.currentTarget));
document.getElementById('btn-sort').addEventListener('click', (e) => toggleSortMenu(e.currentTarget));
document.getElementById('btn-select').addEventListener('click', toggleSelectMode);
document.getElementById('btn-cancel-select').addEventListener('click', toggleSelectMode);
document.getElementById('btn-delete-selected').addEventListener('click', deleteSelected);
document.getElementById('search').addEventListener('input', (e) => {
  state.filter.q = e.target.value.trim() || undefined;
  if (!state.filter.q) delete state.filter.q;
  refreshNotes().catch(showError);
});
document.getElementById('folder-form').addEventListener('submit', createFolder);
document.addEventListener('files-changed', () => { refreshNotes().catch(showError); });

function redirectLogin() { location.href = '/login'; }

// ----- boot -----

async function boot() {
  try {
    const me = await api('GET', '/api/auth/me'); // 401 → redirect to login
    document.getElementById('nav-user').textContent = me.user.username;
    document.getElementById('mainnav').hidden = false;
    render();
  } catch (err) {
    if (err.status === 401) { location.href = '/login'; return; }
    showError(err);
  }
}

boot();
