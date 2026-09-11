// Note editor page: opens a single note (or creates a new one) on its own page.

import { api, post, showError, showMsg } from './api.js';

const { marked } = window.marked;
const DOMPurify = window.DOMPurify;

marked.setOptions({ breaks: true, gfm: true });

const $ = (id) => document.getElementById(id);

// ----- state -----

const state = { currentId: null, folders: [], savedPayload: null };

function renderMarkdown(text) {
  return DOMPurify.sanitize(marked.parse(text || ''));
}

// ----- boot / routing -----

const rawId = location.pathname.replace(/\/+$/, '').split('/').pop();
const isNew = rawId === 'new';

async function boot() {
  try {
    const me = await api('GET', '/api/auth/me');
    $('nav-user').textContent = me.user.username;
    $('mainnav').hidden = false;
    const res = await api('GET', '/api/folders');
    state.folders = res.folders;
    if (isNew) initNewNote();
    else await openNote(rawId);
  } catch (err) {
    if (err.status === 401) { location.href = '/login'; return; }
    showError(err);
  }
}

// ----- editor -----

let editorMode = 'edit'; // 'edit' | 'preview'

function setEditorMode(mode) {
  editorMode = mode;
  const editing = mode === 'edit';
  $('note-body').hidden = !editing;
  $('preview').hidden = editing;
  $('btn-save').hidden = !editing;
  $('btn-toggle-mode').textContent = editing ? 'Preview' : 'Edit';
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

async function openNote(id) {
  try {
    const n = await api('GET', `/api/notes/${id}`);
    state.currentId = n.id;
    document.title = `${n.title || 'Untitled'} — Carseph Notes`;
    setEditorMode('preview');
    $('note-title').value = n.title;
    $('note-body').value = n.body || '';
    $('preview').innerHTML = renderMarkdown(n.body || '');
    populateFolderSelectAndSet(n.folder_id || '');
    $('note-tags').value = (n.tags || []).join(', ');
    state.savedPayload = editorPayload();
  } catch (err) {
    showError(err);
    if (err.status === 404) location.href = '/';
  }
}

function initNewNote() {
  state.currentId = null;
  $('btn-delete').hidden = true;
  $('btn-save-title').hidden = true;
  $('btn-save-folder').hidden = true;
  $('btn-save-tags').hidden = true;
  setEditorMode('edit');
  $('note-title').value = '';
  $('note-title').focus();
  $('note-body').value = '';
  $('preview').innerHTML = renderMarkdown('');
  const params = new URLSearchParams(location.search);
  populateFolderSelectAndSet(params.get('folder') || '');
  $('note-tags').value = params.get('tag') || '';
  state.savedPayload = editorPayload();
}

function editorPayload() {
  const folderId = $('note-folder').value || null;
  const tags = $('note-tags').value.split(',').map((s) => s.trim()).filter(Boolean);
  return { title: $('note-title').value.trim() || 'Untitled', body: $('note-body').value, folder_id: folderId, tags };
}

function payloadsEqual(a, b) {
  return JSON.stringify(a) === JSON.stringify(b);
}

function hasUnsavedChanges() {
  const baseline = state.savedPayload;
  if (!baseline) return false;
  return !payloadsEqual(baseline, editorPayload());
}

async function saveNote() {
  try {
    const payload = editorPayload();
    if (state.currentId) {
      await api('PUT', `/api/notes/${state.currentId}`, payload);
    } else {
      const n = await post('/api/notes', payload);
      state.currentId = n.id;
      $('btn-save-title').hidden = false;
      $('btn-save-folder').hidden = false;
      $('btn-save-tags').hidden = false;
      history.replaceState(null, '', `/note/${n.id}`);
    }
    state.savedPayload = payload;
    showMsg('Saved.', 'ok');
  } catch (err) { showError(err); }
}

async function saveTitle() {
  if (!state.currentId) return;
  const title = $('note-title').value.trim() || 'Untitled';
  try {
    await api('PUT', `/api/notes/${state.currentId}`, { title });
    document.title = `${title} — Carseph Notes`;
    if (state.savedPayload) state.savedPayload.title = title;
    showMsg('Title saved.', 'ok');
  } catch (err) { showError(err); }
}

async function saveFolder() {
  if (!state.currentId) return;
  try {
    await api('PUT', `/api/notes/${state.currentId}`, { folder_id: $('note-folder').value || null });
    if (state.savedPayload) state.savedPayload.folder_id = $('note-folder').value || null;
    showMsg('Folder saved.', 'ok');
  } catch (err) { showError(err); }
}

async function saveTags() {
  if (!state.currentId) return;
  const tags = $('note-tags').value.split(',').map((s) => s.trim()).filter(Boolean);
  try {
    await api('PUT', `/api/notes/${state.currentId}`, { tags });
    if (state.savedPayload) state.savedPayload.tags = tags;
    showMsg('Tags saved.', 'ok');
  } catch (err) { showError(err); }
}

async function deleteCurrentNote() {
  if (!state.currentId) return;
  if (!confirm('Delete this note? This cannot be undone.')) return;
  try {
    await api('DELETE', `/api/notes/${state.currentId}`);
    location.href = '/';
  } catch (err) { showError(err); }
}

// ----- wire UI -----

document.getElementById('btn-save').addEventListener('click', saveNote);
document.getElementById('btn-save-title').addEventListener('click', saveTitle);
document.getElementById('note-title').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') { e.preventDefault(); saveTitle(); }
});
document.getElementById('btn-save-folder').addEventListener('click', saveFolder);
document.getElementById('btn-save-tags').addEventListener('click', saveTags);
document.getElementById('note-tags').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') { e.preventDefault(); saveTags(); }
});
document.getElementById('btn-cancel').addEventListener('click', () => {
  if (hasUnsavedChanges() && !confirm('Discard unsaved changes and close this note?')) return;
  location.href = '/';
});
document.getElementById('btn-toggle-mode').addEventListener('click', () => {
  setEditorMode(editorMode === 'edit' ? 'preview' : 'edit');
});
document.getElementById('btn-delete').addEventListener('click', deleteCurrentNote);
document.getElementById('note-body').addEventListener('input', (e) => {
  $('preview').innerHTML = renderMarkdown(e.target.value);
});
document.getElementById('btn-logout').addEventListener('click', async () => {
  try { await post('/api/auth/logout', {}); } catch { /* ignore */ }
  location.href = '/login';
});

boot();