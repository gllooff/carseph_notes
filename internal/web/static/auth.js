// Login page: WebAuthn registration and authentication ceremonies.

import { post, showError, showMsg } from './api.js';

const { startAuthentication, startRegistration } = window.SimpleWebAuthnBrowser;

function username() {
  return document.getElementById('username').value.trim();
}

async function ceremony(beginPath, beginBody, finishPath) {
  // Begin: server returns PublicKeyCredentialCreation/Request options JSON.
  const options = await post(beginPath, beginBody);

  // Ask the authenticator (this triggers the fingerprint/FaceID prompt).
  const attResp = beginPath.endsWith('register/begin')
    ? await startRegistration({ optionsJSON: options })
    : await startAuthentication({ optionsJSON: options });

  // Finish: server verifies and (on success) sets the session cookie.
  return post(finishPath, { response: attResp });
}

async function signIn() {
  try {
    await ceremony('/api/auth/login/begin', { username: username() }, '/api/auth/login/finish');
    location.href = '/';
  } catch (err) {
    showError(err);
  }
}

async function register() {
  const invite = document.getElementById('invite').value.trim();
  if (!invite) {
    document.getElementById('invite-row').hidden = false;
    showMsg('Enter the invite code you were given.', 'error');
    return;
  }
  try {
    await ceremony(
      '/api/auth/register/begin',
      { username: username(), invite_code: invite },
      '/api/auth/register/finish',
    );
    location.href = '/';
  } catch (err) {
    showError(err);
  }
}

document.getElementById('btn-signin').addEventListener('click', signIn);
document.getElementById('btn-register').addEventListener('click', register);
document.getElementById('username').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') signIn();
});
