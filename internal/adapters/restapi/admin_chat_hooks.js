  const statusEl = document.getElementById('hooks-status');
  const tableEl = document.getElementById('hooks-table');
  const addHookBtn = document.getElementById('add-hook-btn');
  const formPanel = document.getElementById('hook-form-panel');
  const formTitleEl = document.getElementById('hook-form-title');
  const form = document.getElementById('hook-form');
  const nameEl = document.getElementById('hook-name');
  const patternEl = document.getElementById('hook-pattern');
  const scriptEl = document.getElementById('hook-script');
  const enabledEl = document.getElementById('hook-enabled');
  const saveBtn = document.getElementById('hook-save-btn');
  const cancelBtn = document.getElementById('hook-cancel-btn');
  const formStatusEl = document.getElementById('hook-form-status');

  // editingID is '' while adding a brand new hook, and the hook's own id
  // while editing an existing one -- form.addEventListener('submit') below
  // is the one place that branches POST (create) vs PATCH (update) on it.
  let editingID = '';

  function hookEnabledCell(h) {
    const td = document.createElement('td');
    td.textContent = h.enabled ? 'Yes' : 'No';
    return td;
  }

  function hookActionsCell(h) {
    const td = document.createElement('td');
    td.className = 'actions';
    const editBtn = document.createElement('button');
    editBtn.type = 'button';
    editBtn.className = 'text-button';
    editBtn.textContent = 'Edit';
    editBtn.addEventListener('click', () => openEditForm(h));
    td.appendChild(editBtn);
    const delBtn = document.createElement('button');
    delBtn.type = 'button';
    delBtn.className = 'text-button';
    delBtn.textContent = 'Delete';
    delBtn.addEventListener('click', () => deleteHook(h));
    td.appendChild(delBtn);
    return td;
  }

  function renderHooks(hooks) {
    clear(tableEl);
    if (hooks.length === 0) {
      statusEl.textContent = 'No chat hooks configured yet -- click "Add hook" to add one.';
      return;
    }
    statusEl.textContent = '';
    const table = buildTable(
      [{ label: 'name' }, { label: 'pattern' }, { label: 'script' }, { label: 'enabled' }, { label: '' }],
      hooks,
      (h) => [
        textCell(h.name),
        textCell(h.pattern),
        textCell(h.script),
        hookEnabledCell(h),
        hookActionsCell(h),
      ],
    );
    tableEl.appendChild(table);
  }

  async function deleteHook(h) {
    if (!window.confirm('Delete the "' + h.name + '" hook? It will no longer run against chat answers.')) return;
    try {
      await deleteRequest('/admin/api/chat-hooks/' + encodeURIComponent(h.id));
      await loadHooks();
    } catch (err) {
      window.alert('Could not delete: ' + err.message);
    }
  }

  async function loadHooks() {
    try {
      renderHooks(await getJSON('/admin/api/chat-hooks'));
    } catch (err) {
      statusEl.textContent = 'Could not load chat hooks: ' + err.message;
    }
  }

  function openAddForm() {
    editingID = '';
    formTitleEl.textContent = 'Add hook';
    nameEl.value = '';
    patternEl.value = '';
    scriptEl.value = '';
    enabledEl.checked = true;
    formStatusEl.textContent = '';
    formPanel.hidden = false;
  }

  function openEditForm(h) {
    editingID = h.id;
    formTitleEl.textContent = 'Edit hook';
    nameEl.value = h.name;
    patternEl.value = h.pattern;
    scriptEl.value = h.script;
    enabledEl.checked = !!h.enabled;
    formStatusEl.textContent = '';
    formPanel.hidden = false;
  }

  function closeForm() {
    formPanel.hidden = true;
    formStatusEl.textContent = '';
  }

  function requestBody() {
    return {
      name: nameEl.value,
      pattern: patternEl.value,
      script: scriptEl.value,
      enabled: enabledEl.checked,
    };
  }

  addHookBtn.addEventListener('click', openAddForm);
  cancelBtn.addEventListener('click', closeForm);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    setButtonLoading(saveBtn, true, 'Saving…');
    formStatusEl.textContent = '';
    try {
      if (editingID) {
        await patchJSON('/admin/api/chat-hooks/' + encodeURIComponent(editingID), requestBody());
      } else {
        await postJSON('/admin/api/chat-hooks', requestBody());
      }
      closeForm();
      await loadHooks();
    } catch (err) {
      formStatusEl.textContent = 'Could not save: ' + err.message;
    } finally {
      setButtonLoading(saveBtn, false);
    }
  });

  renderAdminNav();
  wireSignOut();
  loadHooks();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See
  // internal/adapters/restapi/admin_chat_hooks.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderHooks, loadHooks, openAddForm, openEditForm, closeForm, requestBody };
  }
