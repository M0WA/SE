  const statusEl = document.getElementById('servers-status');
  const tableEl = document.getElementById('servers-table');

  function serverActionsCell(s) {
    const td = document.createElement('td');
    td.className = 'actions';
    const editLink = document.createElement('a');
    editLink.className = 'text-button';
    editLink.href = '/admin/mcp-servers/' + encodeURIComponent(s.id);
    editLink.textContent = 'Edit';
    td.appendChild(editLink);
    const delBtn = document.createElement('button');
    delBtn.type = 'button';
    delBtn.className = 'text-button';
    delBtn.textContent = 'Delete';
    delBtn.addEventListener('click', () => deleteServer(s));
    td.appendChild(delBtn);
    return td;
  }

  function renderServers(servers) {
    clear(tableEl);
    if (servers.length === 0) {
      statusEl.textContent = 'No MCP servers configured yet -- click "Add server" to add one.';
      return;
    }
    statusEl.textContent = '';
    const table = buildTable(
      [{ label: 'name' }, { label: 'transport' }, { label: 'enabled' }, { label: '' }],
      servers,
      (s) => [
        textCell(s.name),
        textCell(s.transport),
        textCell(s.enabled ? 'yes' : 'no'),
        serverActionsCell(s),
      ],
    );
    tableEl.appendChild(table);
  }

  async function deleteServer(s) {
    if (!window.confirm('Delete the "' + s.name + '" server? Its tools will no longer be offered to the chat model.')) return;
    try {
      await deleteRequest('/admin/api/mcp-servers/' + encodeURIComponent(s.id));
      await loadServers();
    } catch (err) {
      window.alert('Could not delete: ' + err.message);
    }
  }

  async function loadServers() {
    try {
      renderServers(await getJSON('/admin/api/mcp-servers'));
    } catch (err) {
      statusEl.textContent = 'Could not load MCP servers: ' + err.message;
    }
  }

  renderAdminNav();
  wireSignOut();
  loadServers();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See
  // internal/adapters/restapi/admin_mcp_servers.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderServers, loadServers, deleteServer };
  }
