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
  const listToolsBtn = document.getElementById('server-list-tools-btn');
  const listToolsStatusEl = document.getElementById('server-list-tools-status');
  const listToolsResultEl = document.getElementById('server-list-tools-result');

  // updateTransportVisibility shows only the fields for the selected transport (Command/Arguments
  // for stdio, Base URL/API key for http), matching mcpclient.connect's switch -- so the form
  // never invites filling a field the backend ignores.
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
    // Server never echoes a stored API key (see mcpServerResponse); field starts blank, and
    // blank on save keeps it (see requestBody). has_api_key only drives the placeholder/
    // remove-checkbox UI, so the admin never sees the actual value.
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

  // candidateBody is what the "List tools" probe sees for a not-yet-saved server -- just the
  // fields connect+tools/list needs. id (blank for new) lets the server fall back to the real
  // stored key when api_key is blank -- mirrors admin_embedding_endpoint.js's candidateBody.
  function candidateBody() {
    return {
      id: isNew ? '' : id,
      name: nameEl.value,
      transport: transportEl.value,
      command: commandEl.value,
      args: argsEl.value.split('\n').map((a) => a.trim()).filter((a) => a !== ''),
      base_url: baseURLEl.value,
      api_key: apiKeyEl.value,
    };
  }

  // listTools connects using the form's current fields and shows the tools actually exposed --
  // runs automatically after an existing server loads, and on demand via the button, so the
  // admin never has to save first to find out.
  async function listTools() {
    setButtonLoading(listToolsBtn, true, 'Listing…');
    listToolsStatusEl.textContent = '';
    clear(listToolsResultEl);
    try {
      const r = await postJSON('/admin/api/mcp-servers/test', candidateBody());
      if (r.error) {
        listToolsStatusEl.textContent = 'Could not list tools: ' + r.error;
      } else if (!r.tools || r.tools.length === 0) {
        listToolsStatusEl.textContent = 'No tools reported -- fill in the fields above and try again.';
      } else {
        listToolsStatusEl.textContent = r.tools.length + ' tool(s) exposed by this server.';
        r.tools.forEach((t) => listItem(listToolsResultEl, t.name + (t.description ? ' — ' + t.description : '')));
      }
    } catch (err) {
      listToolsStatusEl.textContent = 'Could not list tools: ' + err.message;
    } finally {
      setButtonLoading(listToolsBtn, false);
    }
  }
  listToolsBtn.addEventListener('click', listTools);

  async function load() {
    if (isNew) return;
    try {
      applyServer(await getJSON('/admin/api/mcp-servers/' + encodeURIComponent(id)));
      listTools();
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

  // Node test-runner export only; no-op in a browser <script> tag.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { applyServer, requestBody, candidateBody, listTools, load, updateTransportVisibility };
  }
