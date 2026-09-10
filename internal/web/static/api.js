// Shared fetch + DOM helpers.

export async function api(method, path, body) {
  const opts = { method, credentials: 'same-origin', headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  let data = null;
  try { data = await res.json(); } catch { /* non-JSON */ }
  if (!res.ok) {
    const msg = data && data.error ? data.error.message : `HTTP ${res.status}`;
    const err = new Error(msg);
    err.code = data && data.error ? data.error.code : null;
    err.status = res.status;
    throw err;
  }
  return data;
}

export function post(path, body) { return api('POST', path, body); }

export function showMsg(text, kind = 'error') {
  const el = document.getElementById('msg');
  el.textContent = text;
  el.className = `msg ${kind}`;
  if (kind === 'ok') setTimeout(() => { el.className = 'msg'; }, 4000);
}

export function showError(err) { showMsg(err.message || String(err), 'error'); }
