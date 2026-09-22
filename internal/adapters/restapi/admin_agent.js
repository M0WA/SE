  const id = decodeURIComponent(window.location.pathname.split('/').pop());
  const isNew = id === 'new';

  const titleEl = document.getElementById('agent-title');
  const metaEl = document.getElementById('agent-meta');
  const statusEl = document.getElementById('agent-status');
  const form = document.getElementById('agent-form');
  const nameEl = document.getElementById('agent-name');
  const descriptionEl = document.getElementById('agent-description');
  const systemPromptEl = document.getElementById('agent-system-prompt');
  const mcpServersListEl = document.getElementById('agent-mcp-servers-list');
  const enabledEl = document.getElementById('agent-enabled');
  const saveBtn = document.getElementById('agent-save-btn');
  const deleteBtn = document.getElementById('agent-delete-btn');
  const formStatusEl = document.getElementById('agent-form-status');

  // renderMCPServerCheckboxes builds one checkbox per globally configured
  // MCP server (fetched fresh every load, since the catalog can change
  // independently of this agent) -- checkedIDs is which of them this
  // agent's own mcp_server_ids already names. Empty means this agent has NO
  // global tools at all (there is no "unscoped" state -- see
  // Agent.MCPServerIDs' own doc comment), so every box renders unchecked,
  // not every box checked.
  function renderMCPServerCheckboxes(servers, checkedIDs) {
    clear(mcpServersListEl);
    if (servers.length === 0) {
      const p = document.createElement('p');
      p.className = 'panel-status';
      p.textContent = 'No MCP servers configured yet.';
      mcpServersListEl.appendChild(p);
      return;
    }
    const checked = new Set(checkedIDs || []);
    for (const s of servers) {
      const label = document.createElement('label');
      label.className = 'checkbox-list-row';
      const input = document.createElement('input');
      input.type = 'checkbox';
      input.value = s.id;
      input.checked = checked.has(s.id);
      input.dataset.mcpServerCheckbox = 'true';
      label.appendChild(input);
      label.appendChild(document.createTextNode(' ' + s.name));
      mcpServersListEl.appendChild(label);
    }
  }

  function checkedMCPServerIDs() {
    return Array.from(mcpServersListEl.querySelectorAll('[data-mcp-server-checkbox]'))
      .filter((el) => el.checked)
      .map((el) => el.value);
  }

  // loadMCPServerOptions fetches the global MCP server catalog to populate
  // the checkbox list -- best-effort, same convention as every other
  // best-effort auxiliary fetch on an edit page (e.g.
  // admin_chat_settings.js's loadEnabledServerPrompts): a failure here
  // shouldn't block the agent's own fields from loading.
  async function loadMCPServerOptions(checkedIDs) {
    try {
      renderMCPServerCheckboxes(await getJSON('/admin/api/mcp-servers'), checkedIDs);
    } catch (err) {
      clear(mcpServersListEl);
      const p = document.createElement('p');
      p.className = 'panel-status';
      p.textContent = 'Could not load MCP servers: ' + err.message;
      mcpServersListEl.appendChild(p);
    }
  }

  if (isNew) {
    titleEl.textContent = 'Add agent';
    document.title = 'se. — add agent';
    metaEl.hidden = true;
    deleteBtn.hidden = true;
    enabledEl.checked = true;
    loadMCPServerOptions([]);
    form.hidden = false;
  } else {
    deleteBtn.hidden = false;
  }

  function applyAgent(a) {
    titleEl.textContent = a.name;
    document.title = 'se. — ' + a.name;
    metaEl.textContent = 'ID: ' + a.id;

    nameEl.value = a.name;
    descriptionEl.value = a.description || '';
    systemPromptEl.value = a.system_prompt || '';
    enabledEl.checked = a.enabled;

    loadMCPServerOptions(a.mcp_server_ids || []);
    form.hidden = false;
  }

  function requestBody() {
    return {
      name: nameEl.value,
      description: descriptionEl.value,
      system_prompt: systemPromptEl.value,
      mcp_server_ids: checkedMCPServerIDs(),
      enabled: enabledEl.checked,
    };
  }

  async function load() {
    if (isNew) return;
    try {
      applyAgent(await getJSON('/admin/api/agents/' + encodeURIComponent(id)));
    } catch (err) {
      titleEl.textContent = 'Not found';
      statusEl.textContent = 'Could not load this agent: ' + err.message;
      form.hidden = true;
    }
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    setButtonLoading(saveBtn, true, 'Saving…');
    formStatusEl.textContent = '';
    try {
      if (isNew) {
        const created = await postJSON('/admin/api/agents', requestBody());
        window.location.href = '/admin/agents/' + encodeURIComponent(created.id);
        return;
      }
      await patchJSON('/admin/api/agents/' + encodeURIComponent(id), requestBody());
      formStatusEl.textContent = 'Saved.';
      await load();
    } catch (err) {
      formStatusEl.textContent = 'Could not save: ' + err.message;
    } finally {
      setButtonLoading(saveBtn, false);
    }
  });

  deleteBtn.addEventListener('click', async () => {
    if (!window.confirm('Delete the "' + nameEl.value + '" agent?')) return;
    deleteBtn.disabled = true;
    try {
      await deleteRequest('/admin/api/agents/' + encodeURIComponent(id));
      window.location.href = '/admin/agents';
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
  // admin_agent.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { applyAgent, requestBody, load, renderMCPServerCheckboxes, checkedMCPServerIDs };
  }
