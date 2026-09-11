// Files: upload, image lightbox, PDF.js viewer, and the file menu (move/rename).

import { api, post, showError, showMsg } from './api.js';

const $ = (id) => document.getElementById(id);

const state = { current: null, pdfDoc: null, pageNum: 1, zoom: 1 };

// ----- upload -----

$('file-input').addEventListener('change', async (e) => {
  const file = e.target.files[0];
  if (!file) return;
  const fd = new FormData();
  fd.append('file', file);
  if (e.target.dataset.folder) fd.append('folder_id', e.target.dataset.folder);
  try {
    // Browsers send Origin automatically on POST; don't set forbidden headers.
    const res = await fetch('/api/files', {
      method: 'POST',
      credentials: 'same-origin',
      body: fd,
    });
    if (!res.ok) {
      const j = await res.json().catch(() => null);
      throw new Error(j && j.error ? j.error.message : `HTTP ${res.status}`);
    }
    showMsg('Uploaded.', 'ok');
    document.dispatchEvent(new CustomEvent('files-changed'));
  } catch (err) {
    showError(err);
  } finally {
    e.target.value = '';
  }
});

// ----- viewer -----

export async function openViewer(f) {
  state.current = f;
  $('viewer-overlay').hidden = false;
  $('viewer-name').textContent = f.original_name;
  $('viewer-copy-md').hidden = f.kind !== 'image';
  $('viewer-img').hidden = f.kind !== 'image';
  const isPdf = f.kind === 'pdf';
  $('viewer-pdf').hidden = !isPdf;
  $('pdf-nav').hidden = !isPdf;
  if (f.kind === 'image') {
    $('viewer-img').src = f.url;
    return;
  }
  // PDF via PDF.js
  try {
    const pdfjs = await import('/static/vendor/pdf.min.mjs');
    pdfjs.GlobalWorkerOptions.workerSrc = '/static/vendor/pdf.worker.min.mjs';
    const task = pdfjs.getDocument({ url: f.url });
    state.pdfDoc = await task.promise;
    state.pageNum = 1;
    state.zoom = 1;
    await renderPdfPage();
  } catch (err) {
    showError(new Error('Could not open PDF: ' + err.message));
  }
}

async function renderPdfPage() {
  const doc = state.pdfDoc;
  if (!doc) return;
  const page = await doc.getPage(state.pageNum);
  const canvas = $('viewer-pdf');
  const base = page.getViewport({ scale: 1 });
  const availW = window.innerWidth - 4 * 16;
  const availH = window.innerHeight - 9 * 16;
  const fit = Math.min(availW / base.width, availH / base.height);
  const scale = fit * state.zoom;
  const viewport = page.getViewport({ scale });
  const dpr = window.devicePixelRatio || 1;
  canvas.width = Math.floor(viewport.width * dpr);
  canvas.height = Math.floor(viewport.height * dpr);
  canvas.style.width = viewport.width + 'px';
  canvas.style.height = viewport.height + 'px';
  const ctx = canvas.getContext('2d');
  await page.render({
    canvasContext: ctx,
    viewport,
    transform: dpr !== 1 ? [dpr, 0, 0, dpr, 0, 0] : null,
  }).promise;
  $('pdf-page-label').textContent = `${state.pageNum} / ${doc.numPages}`;
  $('pdf-zoom-label').textContent = Math.round(state.zoom * 100) + '%';
}

$('pdf-prev').addEventListener('click', async () => {
  if (state.pageNum > 1) { state.pageNum--; await renderPdfPage(); }
});
$('pdf-next').addEventListener('click', async () => {
  if (state.pdfDoc && state.pageNum < state.pdfDoc.numPages) { state.pageNum++; await renderPdfPage(); }
});
$('pdf-zoom-in').addEventListener('click', () => {
  state.zoom = Math.min(4, state.zoom * 1.25);
  renderPdfPage();
});
$('pdf-zoom-out').addEventListener('click', () => {
  state.zoom = Math.max(0.5, state.zoom / 1.25);
  renderPdfPage();
});
$('pdf-zoom-fit').addEventListener('click', () => {
  state.zoom = 1;
  renderPdfPage();
});

function closeViewer() {
  $('viewer-overlay').hidden = true;
  $('viewer-img').src = '';
  state.pdfDoc = null;
  state.current = null;
}
$('viewer-close').addEventListener('click', closeViewer);
$('viewer-overlay').addEventListener('click', (e) => {
  if (e.target === $('viewer-overlay')) closeViewer();
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && !$('viewer-overlay').hidden) closeViewer();
});

// ----- actions -----

$('viewer-copy-md').addEventListener('click', async () => {
  if (!state.current) return;
  const md = `![${state.current.original_name}](${state.current.url})`;
  try {
    await navigator.clipboard.writeText(md);
    showMsg('Markdown snippet copied.', 'ok');
  } catch {
    // clipboard API needs focus/permission; fall back to prompt
    prompt('Copy this snippet:', md);
  }
});

$('viewer-download').addEventListener('click', () => {
  if (state.current) location.href = state.current.url + '?download=1';
});

$('viewer-delete').addEventListener('click', async () => {
  if (!state.current) return;
  await deleteFile(state.current);
  closeViewer();
});

const viewerMoveBtn = document.createElement('button');
viewerMoveBtn.className = 'kebab mini';
viewerMoveBtn.title = 'File options';
$('viewer-actions').appendChild(viewerMoveBtn);
viewerMoveBtn.addEventListener('click', (e) => {
  e.stopPropagation();
  if (!state.current) return;
  openFileMenu(state.current, viewerMoveBtn);
});

async function deleteFile(f) {
  if (!confirm(`Delete "${f.original_name}"? This cannot be undone.`)) return;
  try {
    await api('DELETE', `/api/files/${f.id}`);
    closeViewer();
    document.dispatchEvent(new CustomEvent('files-changed'));
  } catch (err) { showError(err); }
}

async function moveFileToFolder(f, folderId) {
  try {
    await api('PATCH', `/api/files/${f.id}`, { folder_id: folderId || null });
    showMsg('Moved.', 'ok');
    document.dispatchEvent(new CustomEvent('files-changed'));
    if (state.current && state.current.id === f.id) {
      state.current.folder_id = folderId || null;
      $('viewer-name').textContent = f.original_name;
    }
  } catch (err) { showError(err); }
}

async function renameFile(f) {
  const name = prompt('Rename file', f.original_name);
  if (name === null || !name.trim()) return;
  try {
    const res = await api('PATCH', `/api/files/${f.id}`, { name: name.trim() });
    f.original_name = res.original_name;
    if (state.current && state.current.id === f.id) {
      state.current.original_name = f.original_name;
      $('viewer-name').textContent = f.original_name;
    }
    document.dispatchEvent(new CustomEvent('files-changed'));
  } catch (err) { showError(err); }
}

async function openMoveDialog(f) {
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
  h3.textContent = `Move "${f.original_name}" to…`;
  box.appendChild(h3);

  const list = document.createElement('div');
  list.className = 'modal-folder-list';
  const opts = [{ id: '', name: 'Unfiled' }, ...(folders || [])];
  let selected = f.folder_id || '';
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
    moveFileToFolder(f, selected || null);
  });
  overlay.addEventListener('click', (e) => { if (e.target === overlay) close(); });
  document.addEventListener('keydown', onKey);
}

let activeFileMenu = null;

export function openFileMenu(f, anchor) {
  closeFileMenu();
  const menu = document.createElement('div');
  menu.className = 'folder-menu';

  const moveBtn = document.createElement('button');
  moveBtn.className = 'mf-item';
  moveBtn.textContent = 'Move';
  moveBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    closeFileMenu();
    openMoveDialog(f);
  });
  menu.appendChild(moveBtn);

  const renameBtn = document.createElement('button');
  renameBtn.className = 'mf-item';
  renameBtn.textContent = 'Rename';
  renameBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    closeFileMenu();
    renameFile(f);
  });
  menu.appendChild(renameBtn);

  anchor.parentElement.appendChild(menu);
  positionFileMenu(menu, anchor);
  activeFileMenu = menu;
  setTimeout(() => document.addEventListener('click', closeFileMenuOnClick, true), 0);
}

function positionFileMenu(menu, anchor) {
  const r = anchor.getBoundingClientRect();
  const pad = 4;
  let top = r.bottom + pad;
  const mR = menu.getBoundingClientRect();
  let left = r.right - mR.width;
  if (left > window.innerWidth - mR.width - pad) left = window.innerWidth - mR.width - pad;
  if (left < pad) left = pad;
  menu.style.top = top + 'px';
  menu.style.left = left + 'px';
}

function closeFileMenu() {
  if (activeFileMenu) {
    activeFileMenu.remove();
    activeFileMenu = null;
  }
  document.removeEventListener('click', closeFileMenuOnClick, true);
}

function closeFileMenuOnClick(e) {
  if (activeFileMenu && !activeFileMenu.contains(e.target)) {
    closeFileMenu();
  }
}
