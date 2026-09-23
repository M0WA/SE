  const id = decodeURIComponent(window.location.pathname.split('/').pop());
  const isNew = id === 'new';

  const titleEl = document.getElementById('server-title');
  const metaEl = document.getElementById('server-meta');
  const statusEl = document.getElementById('server-status');
  const form = document.getElementById('server-form');
  const nameEl = document.getElementById('server-name');
  const baseURLEl = document.getElementById('server-base-url');
  const apiKeyEl = document.getElementById('server-api-key');
  const clearAPIKeyEl = document.getElementById('server-clear-api-key');
  const promptEl = document.getElementById('server-prompt');
  const enabledEl = document.getElementById('server-enabled');
  const gatedEl = document.getElementById('server-gated-by-web-search');
  const saveBtn = document.getElementById('server-save-btn');
  const deleteBtn = document.getElementById('server-delete-btn');
  const formStatusEl = document.getElementById('server-form-status');

  // Doesn't load admin.js (see account.js) -- these are small local duplicates of the helpers it needs.
  async function getJSON(url) {
    const resp = await fetch(url);
    if (!resp.ok) throw new Error(await resp.text() || resp.statusText);
    return resp.json();
  }
  async function postJSON(url, body) {
    const resp = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
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
  async function deleteRequest(url) {
    const resp = await fetch(url, { method: 'DELETE' });
    if (!resp.ok) throw new Error(await resp.text() || resp.statusText);
    return resp.json();
  }
  function setButtonLoading(btn, loading, loadingLabel) {
    if (loading) {
      if (btn.dataset.originalLabel === undefined) btn.dataset.originalLabel = btn.textContent;
      btn.disabled = true;
      btn.textContent = loadingLabel || btn.dataset.originalLabel;
    } else {
      btn.disabled = false;
      if (btn.dataset.originalLabel !== undefined) {
        btn.textContent = btn.dataset.originalLabel;
        delete btn.dataset.originalLabel;
      }
    }
  }

  if (isNew) {
    titleEl.textContent = 'Add server';
    document.title = 'se. — add mcp server';
    metaEl.hidden = true;
    deleteBtn.hidden = true;
    enabledEl.checked = true;
    clearAPIKeyEl.disabled = true;
    form.hidden = false;
  } else {
    deleteBtn.hidden = false;
  }

  function applyServer(s) {
    titleEl.textContent = s.name;
    document.title = 'se. — ' + s.name;
    metaEl.textContent = 'ID: ' + s.id;

    nameEl.value = s.name;
    baseURLEl.value = s.base_url || '';
    // Server never echoes a stored API key (see mcpServerResponse); field starts blank, and blank on save
    // keeps it (see requestBody). has_api_key only drives the placeholder/remove-checkbox UI.
    apiKeyEl.value = '';
    apiKeyEl.placeholder = s.has_api_key ? '(unchanged — a key is already set)' : '';
    clearAPIKeyEl.checked = false;
    clearAPIKeyEl.disabled = !s.has_api_key;
    promptEl.value = s.prompt || '';
    enabledEl.checked = s.enabled;
    gatedEl.checked = s.gated_by_web_search;

    form.hidden = false;
  }

  function requestBody() {
    return {
      name: nameEl.value,
      transport: 'http',
      base_url: baseURLEl.value,
      api_key: apiKeyEl.value,
      clear_api_key: clearAPIKeyEl.checked,
      prompt: promptEl.value,
      enabled: enabledEl.checked,
      gated_by_web_search: gatedEl.checked,
    };
  }

  async function load() {
    if (isNew) return;
    try {
      applyServer(await getJSON('/account/api/mcp-servers/' + encodeURIComponent(id)));
    } catch (err) {
      titleEl.textContent = 'Not found';
      statusEl.textContent = 'Could not load this server: ' + err.message;
      form.hidden = true;
    }
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    setButtonLoading(saveBtn, true, 'Saving…');
    formStatusEl.textContent = '';
    try {
      if (isNew) {
        const created = await postJSON('/account/api/mcp-servers', requestBody());
        window.location.href = '/account/mcp-servers/' + encodeURIComponent(created.id);
        return;
      }
      await patchJSON('/account/api/mcp-servers/' + encodeURIComponent(id), requestBody());
      formStatusEl.textContent = 'Saved.';
      await load();
    } catch (err) {
      formStatusEl.textContent = 'Could not save: ' + err.message;
    } finally {
      setButtonLoading(saveBtn, false);
    }
  });

  deleteBtn.addEventListener('click', async () => {
    if (!window.confirm('Delete the "' + nameEl.value + '" server? Its tools will no longer be offered to the chat model.')) return;
    deleteBtn.disabled = true;
    try {
      await deleteRequest('/account/api/mcp-servers/' + encodeURIComponent(id));
      window.location.href = '/account/mcp-servers';
    } catch (err) {
      deleteBtn.disabled = false;
      window.alert('Could not delete: ' + err.message);
    }
  });

  // Mirrors account.js's own inline sign-out (see its comment) -- this page
  // doesn't load admin.js either.
  document.getElementById('sign-out').addEventListener('click', async () => {
    try {
      await fetch('/logout', { method: 'POST' });
    } finally {
      window.location = '/';
    }
  });

  load();

  // Node test-runner export only; no-op in a browser <script> tag.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { applyServer, requestBody, load };
  }
