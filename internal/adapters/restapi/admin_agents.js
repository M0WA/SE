  const statusEl = document.getElementById('agents-status');
  const tableEl = document.getElementById('agents-table');

  function agentActionsCell(a) {
    return actionsCell('/admin/agents/' + encodeURIComponent(a.id), 'Delete', () => deleteAgent(a));
  }

  function renderAgents(agents) {
    clear(tableEl);
    if (agents.length === 0) {
      statusEl.textContent = 'No agents configured yet -- click "Add agent" to add one.';
      return;
    }
    statusEl.textContent = '';
    const table = buildTable(
      [{ label: 'name' }, { label: 'description' }, { label: 'enabled' }, { label: '' }],
      agents,
      (a) => [
        textCell(a.name),
        textCell(a.description),
        textCell(a.enabled ? 'yes' : 'no'),
        agentActionsCell(a),
      ],
    );
    tableEl.appendChild(table);
  }

  async function deleteAgent(a) {
    if (!window.confirm('Delete the "' + a.name + '" agent?')) return;
    try {
      await deleteRequest('/admin/api/agents/' + encodeURIComponent(a.id));
      await loadAgents();
    } catch (err) {
      window.alert('Could not delete: ' + err.message);
    }
  }

  async function loadAgents() {
    try {
      renderAgents(await getJSON('/admin/api/agents'));
    } catch (err) {
      statusEl.textContent = 'Could not load agents: ' + err.message;
    }
  }

  renderAdminNav();
  wireSignOut();
  loadAgents();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See
  // internal/adapters/restapi/admin_agents.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderAgents, loadAgents, deleteAgent };
  }
