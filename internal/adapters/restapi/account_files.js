  const statusEl = document.getElementById('files-status');
  const tableEl = document.getElementById('files-table');

  // Intentionally doesn't load admin.js (see account.js) -- small local helpers duplicated instead.
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

  function formatSize(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
    return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
  }

  function textCell(text) {
    const td = document.createElement('td');
    td.textContent = text;
    return td;
  }

  function fileActionsCell(f) {
    const td = document.createElement('td');
    td.className = 'actions';
    const downloadLink = document.createElement('a');
    downloadLink.className = 'text-button';
    downloadLink.href = '/account/api/files/' + encodeURIComponent(f.id);
    downloadLink.textContent = 'Download';
    td.appendChild(downloadLink);
    const delBtn = document.createElement('button');
    delBtn.type = 'button';
    delBtn.className = 'text-button';
    delBtn.textContent = 'Delete';
    delBtn.addEventListener('click', () => deleteFile(f));
    td.appendChild(delBtn);
    return td;
  }

  function renderFiles(files) {
    clear(tableEl);
    if (files.length === 0) {
      statusEl.textContent = 'No files yet -- pin a chat on the chat page and use its attach button to add one.';
      return;
    }
    statusEl.textContent = '';
    const table = document.createElement('table');
    const thead = document.createElement('thead');
    const headRow = document.createElement('tr');
    ['filename', 'size', 'uploaded', ''].forEach((label) => {
      const th = document.createElement('th');
      th.textContent = label;
      headRow.appendChild(th);
    });
    thead.appendChild(headRow);
    table.appendChild(thead);
    const tbody = document.createElement('tbody');
    files.forEach((f) => {
      const tr = document.createElement('tr');
      tr.appendChild(textCell(f.filename));
      tr.appendChild(textCell(formatSize(f.size)));
      tr.appendChild(textCell(new Date(f.created_at).toLocaleString()));
      tr.appendChild(fileActionsCell(f));
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    tableEl.appendChild(table);
  }

  async function deleteFile(f) {
    if (!window.confirm('Delete "' + f.filename + '"? This cannot be undone.')) return;
    try {
      await deleteRequest('/account/api/files/' + encodeURIComponent(f.id));
      await loadFiles();
    } catch (err) {
      window.alert('Could not delete: ' + err.message);
    }
  }

  async function loadFiles() {
    try {
      renderFiles(await getJSON('/account/api/files'));
    } catch (err) {
      statusEl.textContent = 'Could not load your files: ' + err.message;
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

  loadFiles();

  // Node test-runner export only; no-op in a browser <script> tag.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderFiles, loadFiles, deleteFile, formatSize };
  }
