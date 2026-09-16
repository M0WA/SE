  const statusEl = document.getElementById('endpoints-status');
  const tableEl = document.getElementById('endpoints-table');

  function endpointEnabledCell(e) {
    const td = document.createElement('td');
    td.textContent = e.enabled ? 'Yes' : 'No';
    return td;
  }

  function endpointActionsCell(e) {
    const td = document.createElement('td');
    td.className = 'actions';
    const editLink = document.createElement('a');
    editLink.className = 'text-button';
    editLink.href = '/admin/embeddings/endpoint/' + encodeURIComponent(e.id);
    editLink.textContent = 'Edit';
    td.appendChild(editLink);
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'text-button';
    btn.textContent = 'Delete';
    btn.addEventListener('click', () => deleteEndpoint(e));
    td.appendChild(btn);
    return td;
  }

  function renderEndpoints(endpoints) {
    clear(tableEl);
    if (endpoints.length === 0) {
      statusEl.textContent = 'No HTTP endpoints configured yet -- click "Add endpoint" to add one.';
      return;
    }
    statusEl.textContent = '';
    const table = buildTable(
      [{ label: 'name' }, { label: 'base url' }, { label: 'model' }, { label: 'dimensions', num: true }, { label: 'rate limit/s', num: true }, { label: 'enabled' }, { label: '' }],
      endpoints,
      (e) => [
        textCell(e.name),
        textCell(e.base_url),
        textCell(e.model),
        textCell(String(e.dimensions), { num: true }),
        textCell(String(e.rate_limit_per_second), { num: true }),
        endpointEnabledCell(e),
        endpointActionsCell(e),
      ],
    );
    tableEl.appendChild(table);
  }

  async function deleteEndpoint(e) {
    if (!window.confirm('Delete the "' + e.name + '" endpoint? Search and recompute will no longer be able to use it.')) return;
    try {
      await deleteRequest('/admin/api/embeddings/endpoints/' + encodeURIComponent(e.id));
      await loadEndpoints();
    } catch (err) {
      window.alert('Could not delete: ' + err.message);
    }
  }

  async function loadEndpoints() {
    try {
      renderEndpoints(await getJSON('/admin/api/embeddings/endpoints'));
    } catch (err) {
      statusEl.textContent = 'Could not load endpoints: ' + err.message;
    }
  }

  wireSignOut();
  loadEndpoints();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See
  // admin_embedding_endpoints.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderEndpoints, loadEndpoints };
  }
