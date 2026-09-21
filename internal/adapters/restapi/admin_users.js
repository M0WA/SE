  const statusEl = document.getElementById('users-status');
  const tableEl = document.getElementById('users-table');
  const addUserBtn = document.getElementById('add-user-btn');
  const formPanel = document.getElementById('user-form-panel');
  const formTitleEl = document.getElementById('user-form-title');
  const form = document.getElementById('user-form');
  const usernameEl = document.getElementById('user-username');
  const passwordEl = document.getElementById('user-password');
  const passwordLabelEl = document.getElementById('user-password-label');
  const saveBtn = document.getElementById('user-save-btn');
  const cancelBtn = document.getElementById('user-cancel-btn');
  const formStatusEl = document.getElementById('user-form-status');

  // editingID is '' while adding a brand new user (username editable, POST
  // on submit), and an existing user's id while resetting their password
  // (username locked to that user, PATCH on submit) -- form.addEventListener
  // ('submit') below is the one place that branches on it, the same pattern
  // admin_chat_hooks.js uses for add vs edit.
  let editingID = '';

  function userActionsCell(u) {
    const td = document.createElement('td');
    td.className = 'actions';
    const resetBtn = document.createElement('button');
    resetBtn.type = 'button';
    resetBtn.className = 'text-button';
    resetBtn.textContent = 'Reset password';
    resetBtn.addEventListener('click', () => openResetForm(u));
    td.appendChild(resetBtn);
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

  function openAddForm() {
    editingID = '';
    formTitleEl.textContent = 'Add user';
    passwordLabelEl.textContent = 'Password';
    usernameEl.value = '';
    usernameEl.disabled = false;
    passwordEl.value = '';
    formStatusEl.textContent = '';
    formPanel.hidden = false;
  }

  function openResetForm(u) {
    editingID = u.id;
    formTitleEl.textContent = 'Reset password for "' + u.username + '"';
    passwordLabelEl.textContent = 'New password';
    usernameEl.value = u.username;
    usernameEl.disabled = true;
    passwordEl.value = '';
    formStatusEl.textContent = '';
    formPanel.hidden = false;
  }

  function closeForm() {
    formPanel.hidden = true;
    usernameEl.disabled = false;
    formStatusEl.textContent = '';
  }

  function requestBody() {
    if (editingID) {
      return { password: passwordEl.value };
    }
    return { username: usernameEl.value, password: passwordEl.value };
  }

  addUserBtn.addEventListener('click', openAddForm);
  cancelBtn.addEventListener('click', closeForm);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    setButtonLoading(saveBtn, true, 'Saving…');
    formStatusEl.textContent = '';
    try {
      if (editingID) {
        await patchJSON('/admin/api/users/' + encodeURIComponent(editingID), requestBody());
      } else {
        await postJSON('/admin/api/users', requestBody());
      }
      closeForm();
      await loadUsers();
    } catch (err) {
      formStatusEl.textContent = 'Could not save: ' + err.message;
    } finally {
      setButtonLoading(saveBtn, false);
    }
  });

  renderAdminNav();
  wireSignOut();
  loadUsers();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See
  // internal/adapters/restapi/admin_users.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderUsers, loadUsers, openAddForm, openResetForm, closeForm, requestBody };
  }
