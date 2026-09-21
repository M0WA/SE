  const statusEl = document.getElementById('users-status');
  const tableEl = document.getElementById('users-table');

  function userActionsCell(u) {
    const td = document.createElement('td');
    td.className = 'actions';
    const editLink = document.createElement('a');
    editLink.className = 'text-button';
    editLink.href = '/admin/users/' + encodeURIComponent(u.id);
    editLink.textContent = 'Edit';
    td.appendChild(editLink);
    const delBtn = document.createElement('button');
    delBtn.type = 'button';
    delBtn.className = 'text-button';
    delBtn.textContent = 'Delete';
    delBtn.addEventListener('click', () => deleteUser(u));
    td.appendChild(delBtn);
    return td;
  }

  function renderUsers(users) {
    clear(tableEl);
    if (users.length === 0) {
      statusEl.textContent = 'No user accounts yet -- click "Add user" to add one.';
      return;
    }
    statusEl.textContent = '';
    const table = buildTable(
      [{ label: 'username' }, { label: 'created' }, { label: '' }],
      users,
      (u) => [
        textCell(u.username),
        textCell(u.created_at ? new Date(u.created_at).toLocaleString() : ''),
        userActionsCell(u),
      ],
    );
    tableEl.appendChild(table);
  }

  async function deleteUser(u) {
    if (!window.confirm('Delete the "' + u.username + '" account? They will no longer be able to sign in.')) return;
    try {
      await deleteRequest('/admin/api/users/' + encodeURIComponent(u.id));
      await loadUsers();
    } catch (err) {
      window.alert('Could not delete: ' + err.message);
    }
  }

  async function loadUsers() {
    try {
      renderUsers(await getJSON('/admin/api/users'));
    } catch (err) {
      statusEl.textContent = 'Could not load users: ' + err.message;
    }
  }

  renderAdminNav();
  wireSignOut();
  loadUsers();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See
  // internal/adapters/restapi/admin_users.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderUsers, loadUsers, deleteUser };
  }
