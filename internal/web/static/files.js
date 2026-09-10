// Files page: gallery, upload, image lightbox, PDF.js viewer.

import { api, post, showError, showMsg } from './api.js';

const $ = (id) => document.getElementById(id);

const state = { files: [], kind: '', current: null, pdfDoc: null, pageNum: 1 };

// ----- loading -----

async function loadFiles() {
  try {
    const qs = state.kind ? `?kind=${state.kind}` : '';
    const res = await api('GET', '/api/files' + qs);
    state.files = res.files;
    renderGrid();
  } catch (err) {
    if (err.status === 401) { location.href = '/login'; return; }
    showError(err);
  }
}

function renderGrid() {
  const grid = $('file-grid');
  grid.textContent = '';
  if (!state.files.length) {
    const p = document.createElement('p');
    p.className = 'muted';
    p.textContent = 'No files yet — upload an image or PDF.';
    grid.appendChild(p);
    return;
  }
  for (const f of state.files) {
    const card = document.createElement('div');
    card.className = 'file-card';
    if (f.kind === 'image') {
      const img = document.createElement('img');
      img.loading = 'lazy';
      img.src = f.url;
      card.appendChild(img);
    } else {
      const badge = document.createElement('div');
      badge.className = 'pdf-badge';
      badge.textContent = '📄';
      card.appendChild(badge);
    }
    const name = document.createElement('span');
    name.className = 'fc-name';
    name.textContent = f.original_name;
    card.appendChild(name);
    const meta = document.createElement('span');
    meta.className = 'fc-meta';
    meta.innerHTML = `<span>${fmtSize(f.size)}</span><span>${new Date(f.created_at * 1000).toLocaleDateString()}</span>`;
    card.appendChild(meta);
    const del = document.createElement('button');
    del.className = 'fc-del';
    del.textContent = 'Delete';
    del.addEventListener('click', (e) => { e.stopPropagation(); deleteFile(f); });
    card.appendChild(del);
    card.addEventListener('click', () => openViewer(f));
    grid.appendChild(card);
  }
}

function fmtSize(n) {
  if (n > 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB';
  if (n > 1 << 10) return (n / (1 << 10)).toFixed(0) + ' KB';
  return n + ' B';
}

// ----- upload -----

$('file-input').addEventListener('change', async (e) => {
  const file = e.target.files[0];
  if (!file) return;
  const fd = new FormData();
  fd.append('file', file);
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
    await loadFiles();
  } catch (err) {
    showError(err);
  } finally {
    e.target.value = '';
  }
});

// ----- tabs -----

for (const [id, kind] of [['tab-all', ''], ['tab-image', 'image'], ['tab-pdf', 'pdf']]) {
  $(id).addEventListener('click', async () => {
    state.kind = kind;
    for (const [tid] of [['tab-all'], ['tab-image'], ['tab-pdf']]) {
      $(tid).classList.toggle('active', tid === id);
    }
    await loadFiles();
  });
}

// ----- viewer -----

async function openViewer(f) {
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
  const scale = 1.5;
  const viewport = page.getViewport({ scale });
  canvas.width = viewport.width;
  canvas.height = viewport.height;
  canvas.style.width = Math.min(viewport.width, window.innerWidth - 80) + 'px';
  const ctx = canvas.getContext('2d');
  await page.render({ canvasContext: ctx, viewport }).promise;
  $('pdf-page-label').textContent = `${state.pageNum} / ${doc.numPages}`;
}

$('pdf-prev').addEventListener('click', async () => {
  if (state.pageNum > 1) { state.pageNum--; await renderPdfPage(); }
});
$('pdf-next').addEventListener('click', async () => {
  if (state.pdfDoc && state.pageNum < state.pdfDoc.numPages) { state.pageNum++; await renderPdfPage(); }
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

async function deleteFile(f) {
  if (!confirm(`Delete "${f.original_name}"? This cannot be undone.`)) return;
  try {
    await api('DELETE', `/api/files/${f.id}`);
    closeViewer();
    await loadFiles();
  } catch (err) { showError(err); }
}

// hook into the main router: re-load files view when it becomes visible
const observer = new MutationObserver(() => {
  const page = $('page-files');
  if (page && !page.hidden) loadFiles();
});
observer.observe($('page-files'), { attributes: true, attributeFilter: ['hidden'] });
