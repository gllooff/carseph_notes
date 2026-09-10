// Main app: session bootstrap, tiny client-side router, settings page (M1).

import { api, post, showError, showMsg } from './api.js';

const { startRegistration } = window.SimpleWebAuthnBrowser;

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
}

// ----- settings -----

async function loadSettings() {
  try {
    const me = await api('GET', '/api/auth/me');
    document.getElementById('whoami').textContent = me.user.username;
    renderPasskeys(me.passkeys);
  } catch (err) {
    if (err.status === 401) location.href = '/login';
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

// ----- passkey add -----

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

// ----- logout -----

document.getElementById('btn-logout').addEventListener('click', async () => {
  try { await post('/api/auth/logout', {}); } catch { /* ignore */ }
  location.href = '/login';
});

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
