  const statusEl = document.getElementById('servers-status');
  const tableEl = document.getElementById('servers-table');

  // Doesn't load admin.js (see account.js) -- small local duplicates of the needed helpers,
  // per account.js's own convention.
  function clear(el) {
    while (el.firstChild) el.firstChild.remove();
  }

  async function getJSON(url) {
    const resp = await fetch(url);
    if (!resp.ok) throw new Error(await resp.text() || resp.statusText);
    return resp.json();
  }

  async function deleteRequest(url) {
    const resp = await fetch(url, { method: 'DELETE' });
    if (!resp.ok) throw new Error(await resp.text() || resp.statusText);
    return resp.json();
  }

  function textCell(text) {
    const td = document.createElement('td');
    td.textContent = text;
    return td;
  }

  function serverActionsCell(s) {
    const td = document.createElement('td');
    td.className = 'actions';
    const editLink = document.createElement('a');
    editLink.className = 'text-button';
    editLink.href = '/account/mcp-servers/' + encodeURIComponent(s.id);
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
      statusEl.textContent = 'No personal MCP servers configured yet -- click "Add server" to add one.';
      return;
    }
    statusEl.textContent = '';
    const table = document.createElement('table');
    const thead = document.createElement('thead');
    const headRow = document.createElement('tr');
    ['name', 'base url', 'enabled', ''].forEach((label) => {
      const th = document.createElement('th');
      th.textContent = label;
      headRow.appendChild(th);
    });
    thead.appendChild(headRow);
    table.appendChild(thead);
    const tbody = document.createElement('tbody');
    servers.forEach((s) => {
      const tr = document.createElement('tr');
      tr.appendChild(textCell(s.name));
      tr.appendChild(textCell(s.base_url));
      tr.appendChild(textCell(s.enabled ? 'yes' : 'no'));
      tr.appendChild(serverActionsCell(s));
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    tableEl.appendChild(table);
  }

  async function deleteServer(s) {
    if (!window.confirm('Delete the "' + s.name + '" server? Its tools will no longer be offered to the chat model.')) return;
    try {
      await deleteRequest('/account/api/mcp-servers/' + encodeURIComponent(s.id));
      await loadServers();
    } catch (err) {
      window.alert('Could not delete: ' + err.message);
    }
  }

  async function loadServers() {
    try {
      renderServers(await getJSON('/account/api/mcp-servers'));
    } catch (err) {
      statusEl.textContent = 'Could not load your MCP servers: ' + err.message;
    }
  }

  // Mirrors account.js's own inline sign-out (see its comment) -- this page
  // doesn't load admin.js either.
  document.getElementById('sign-out').addEventListener('click', async () => {
    try {
      await fetch('/logout', { method: 'POST' });
    } finally {
      window.location = '/';
    }
  });

  loadServers();

  // Node test-runner export only; no-op in a browser <script> tag.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderServers, loadServers, deleteServer };
  }
