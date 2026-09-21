  const id = decodeURIComponent(window.location.pathname.split('/').pop());
  const isNew = id === 'new';

  const titleEl = document.getElementById('server-title');
  const metaEl = document.getElementById('server-meta');
  const statusEl = document.getElementById('server-status');
  const form = document.getElementById('server-form');
  const nameEl = document.getElementById('server-name');
  const transportEl = document.getElementById('server-transport');
  const commandRowEl = document.getElementById('server-command-row');
  const commandEl = document.getElementById('server-command');
  const argsRowEl = document.getElementById('server-args-row');
  const argsEl = document.getElementById('server-args');
  const baseURLRowEl = document.getElementById('server-base-url-row');
  const baseURLEl = document.getElementById('server-base-url');
  const apiKeyRowEl = document.getElementById('server-api-key-row');
  const apiKeyEl = document.getElementById('server-api-key');
  const clearAPIKeyRowEl = document.getElementById('server-clear-api-key-row');
  const clearAPIKeyEl = document.getElementById('server-clear-api-key');
  const promptEl = document.getElementById('server-prompt');
  const enabledEl = document.getElementById('server-enabled');
  const gatedEl = document.getElementById('server-gated-by-web-search');
  const saveBtn = document.getElementById('server-save-btn');
  const deleteBtn = document.getElementById('server-delete-btn');
  const formStatusEl = document.getElementById('server-form-status');

  // updateTransportVisibility shows only the fields relevant to the
  // selected transport -- Command/Arguments for "stdio", Base URL/API key
  // for "http" -- matching mcpclient.connect's own switch on Transport, so
  // the form never invites filling in a field the backend will ignore.
  function updateTransportVisibility() {
    const isStdio = transportEl.value === 'stdio';
    commandRowEl.hidden = !isStdio;
    argsRowEl.hidden = !isStdio;
    baseURLRowEl.hidden = isStdio;
    apiKeyRowEl.hidden = isStdio;
    clearAPIKeyRowEl.hidden = isStdio;
  }
  transportEl.addEventListener('change', updateTransportVisibility);

  if (isNew) {
    titleEl.textContent = 'Add server';
    document.title = 'se. — add mcp server';
    metaEl.hidden = true;
    deleteBtn.hidden = true;
    enabledEl.checked = true;
    clearAPIKeyEl.disabled = true;
    updateTransportVisibility();
    form.hidden = false;
  } else {
    deleteBtn.hidden = false;
  }

  function applyServer(s) {
    titleEl.textContent = s.name;
    document.title = 'se. — ' + s.name;
    metaEl.textContent = 'ID: ' + s.id;

    nameEl.value = s.name;
    transportEl.value = s.transport;
    commandEl.value = s.command || '';
    argsEl.value = (s.args || []).join('\n');
    baseURLEl.value = s.base_url || '';
    // The server never echoes a stored API key's real value (see
    // mcpServerResponse) -- this field always starts blank, and saving with
    // it left blank keeps whatever key is already stored (see
    // requestBody). has_api_key only drives the placeholder text and the
    // "remove" checkbox's availability, so the admin can see whether a key
    // is set without ever seeing its value.
    apiKeyEl.value = '';
    apiKeyEl.placeholder = s.has_api_key ? '(unchanged — a key is already set)' : '';
    clearAPIKeyEl.checked = false;
    clearAPIKeyEl.disabled = !s.has_api_key;
    promptEl.value = s.prompt || '';
    enabledEl.checked = s.enabled;
    gatedEl.checked = s.gated_by_web_search;

    updateTransportVisibility();
    form.hidden = false;
  }

  function requestBody() {
    return {
      name: nameEl.value,
      transport: transportEl.value,
      command: commandEl.value,
      args: argsEl.value.split('\n').map((a) => a.trim()).filter((a) => a !== ''),
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
      applyServer(await getJSON('/admin/api/mcp-servers/' + encodeURIComponent(id)));
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
        const created = await postJSON('/admin/api/mcp-servers', requestBody());
        window.location.href = '/admin/mcp-servers/' + encodeURIComponent(created.id);
        return;
      }
      await patchJSON('/admin/api/mcp-servers/' + encodeURIComponent(id), requestBody());
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
      await deleteRequest('/admin/api/mcp-servers/' + encodeURIComponent(id));
      window.location.href = '/admin/mcp-servers';
    } catch (err) {
      deleteBtn.disabled = false;
      window.alert('Could not delete: ' + err.message);
    }
  });

  renderAdminNav();
  wireSignOut();
  load();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See
  // admin_mcp_server.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { applyServer, requestBody, load, updateTransportVisibility };
  }
