  const statusEl = document.getElementById('files-status');
  const tableEl = document.getElementById('files-table');
  const uploadForm = document.getElementById('upload-form');
  const uploadInput = document.getElementById('upload-input');
  const uploadBtn = document.getElementById('upload-btn');
  const uploadStatusEl = document.getElementById('upload-status');

  // This page intentionally does NOT load admin.js (see account.js's own
  // comment on why self-service pages stay decoupled from it) -- same
  // small local helpers account_mcp_servers.js already duplicates.
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
      statusEl.textContent = 'No files uploaded yet -- use the form above to add one.';
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

  function setUploading(uploading) {
    uploadBtn.disabled = uploading;
    uploadBtn.textContent = uploading ? 'Uploading…' : 'Upload';
  }

  async function uploadSelectedFile() {
    const selected = uploadInput.files?.[0];
    if (!selected) {
      uploadStatusEl.textContent = 'Choose a file first.';
      return;
    }
    setUploading(true);
    uploadStatusEl.textContent = '';
    try {
      const body = new FormData();
      body.append('file', selected);
      const resp = await fetch('/account/api/files', { method: 'POST', body });
      if (!resp.ok) throw new Error(await resp.text() || resp.statusText);
      uploadForm.reset();
      await loadFiles();
    } catch (err) {
      uploadStatusEl.textContent = 'Could not upload: ' + err.message;
    } finally {
      setUploading(false);
    }
  }

  uploadForm.addEventListener('submit', (e) => {
    e.preventDefault();
    uploadSelectedFile();
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

  loadFiles();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See
  // internal/adapters/restapi/account_files.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderFiles, loadFiles, deleteFile, formatSize, uploadSelectedFile };
  }
