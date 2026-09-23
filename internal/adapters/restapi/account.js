  const statusEl = document.getElementById('account-status');
  const panelEl = document.getElementById('account-panel');
  const form = document.getElementById('account-form');
  const usernameEl = document.getElementById('account-username');
  const passwordEl = document.getElementById('account-password');
  const customPromptEl = document.getElementById('account-custom-prompt');
  const saveBtn = document.getElementById('account-save-btn');
  const formStatusEl = document.getElementById('account-form-status');

  async function getJSON(url) {
    const resp = await fetch(url);
    if (!resp.ok) throw new Error(await resp.text() || resp.statusText);
    return resp.json();
  }

  async function patchJSON(url, body) {
    const resp = await fetch(url, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!resp.ok) throw new Error(await resp.text() || resp.statusText);
    return resp.json();
  }

  function setSaving(saving) {
    saveBtn.disabled = saving;
    saveBtn.textContent = saving ? 'Saving…' : 'Save';
  }

  async function loadAccount() {
    try {
      const account = await getJSON('/account/api');
      usernameEl.value = account.username;
      customPromptEl.value = account.custom_prompt || '';
      statusEl.textContent = '';
      panelEl.hidden = false;
    } catch (err) {
      statusEl.textContent = 'Could not load your account: ' + err.message;
    }
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    formStatusEl.textContent = '';
    setSaving(true);
    try {
      // Blank password = keep current; custom_prompt is always sent, even cleared.
      const body = { custom_prompt: customPromptEl.value };
      if (passwordEl.value) {
        body.password = passwordEl.value;
      }
      await patchJSON('/account/api', body);
      passwordEl.value = '';
      formStatusEl.textContent = 'Saved.';
    } catch (err) {
      formStatusEl.textContent = 'Could not save: ' + err.message;
    } finally {
      setSaving(false);
    }
  });

  // Mirrors index.js's own inline sign-out (see its comment) -- this page
  // doesn't load admin.js either.
  document.getElementById('sign-out').addEventListener('click', async () => {
    try {
      await fetch('/logout', { method: 'POST' });
    } finally {
      window.location = '/';
    }
  });

  loadAccount();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag. See account.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { loadAccount };
  }
