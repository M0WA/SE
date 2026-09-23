  const id = decodeURIComponent(window.location.pathname.split('/').pop());
  const isNew = id === 'new';

  const titleEl = document.getElementById('user-title');
  const metaEl = document.getElementById('user-meta');
  const statusEl = document.getElementById('user-status');
  const form = document.getElementById('user-form');
  const usernameEl = document.getElementById('user-username');
  const passwordEl = document.getElementById('user-password');
  const passwordLabelEl = document.getElementById('user-password-label');
  const isAdminEl = document.getElementById('user-is-admin');
  const customPromptEl = document.getElementById('user-custom-prompt');
  const saveBtn = document.getElementById('user-save-btn');
  const deleteBtn = document.getElementById('user-delete-btn');
  const formStatusEl = document.getElementById('user-form-status');

  if (isNew) {
    titleEl.textContent = 'Add user';
    document.title = 'se. — add user';
    metaEl.hidden = true;
    deleteBtn.hidden = true;
    passwordEl.required = true;
    passwordEl.placeholder = '';
    form.hidden = false;
  } else {
    deleteBtn.hidden = false;
    passwordLabelEl.textContent = 'New password';
    passwordEl.placeholder = 'Leave blank to keep current password';
  }

  function applyUser(u) {
    titleEl.textContent = u.username;
    document.title = 'se. — ' + u.username;
    metaEl.textContent = 'ID: ' + u.id + ' · Created ' + new Date(u.created_at).toLocaleString();

    usernameEl.value = u.username;
    usernameEl.disabled = true;
    passwordEl.value = '';
    isAdminEl.checked = !!u.is_admin;
    customPromptEl.value = u.custom_prompt || '';

    form.hidden = false;
  }

  // requestBody omits password when blank in edit mode (blank = keep current, see applyUser),
  // but always includes it in new mode (required there). is_admin/custom_prompt are always
  // sent, even cleared/unchecked, since their own form controls are the single source of truth.
  function requestBody() {
    const body = { is_admin: isAdminEl.checked, custom_prompt: customPromptEl.value };
    if (isNew || passwordEl.value) {
      body.password = passwordEl.value;
    }
    if (isNew) {
      body.username = usernameEl.value;
    }
    return body;
  }

  async function load() {
    if (isNew) return;
    try {
      applyUser(await getJSON('/admin/api/users/' + encodeURIComponent(id)));
    } catch (err) {
      titleEl.textContent = 'Not found';
      statusEl.textContent = 'Could not load this user: ' + err.message;
      form.hidden = true;
    }
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    setButtonLoading(saveBtn, true, 'Saving…');
    formStatusEl.textContent = '';
    try {
      if (isNew) {
        const created = await postJSON('/admin/api/users', requestBody());
        window.location.href = '/admin/users/' + encodeURIComponent(created.id);
        return;
      }
      await patchJSON('/admin/api/users/' + encodeURIComponent(id), requestBody());
      formStatusEl.textContent = 'Saved.';
      await load();
    } catch (err) {
      formStatusEl.textContent = 'Could not save: ' + err.message;
    } finally {
      setButtonLoading(saveBtn, false);
    }
  });

  deleteBtn.addEventListener('click', async () => {
    if (!window.confirm('Delete the "' + usernameEl.value + '" account? They will no longer be able to sign in.')) return;
    deleteBtn.disabled = true;
    try {
      await deleteRequest('/admin/api/users/' + encodeURIComponent(id));
      window.location.href = '/admin/users';
    } catch (err) {
      deleteBtn.disabled = false;
      window.alert('Could not delete: ' + err.message);
    }
  });

  renderAdminNav();
  wireSignOut();
  load();

  // Node test-runner export only; no-op in a browser <script> tag.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { applyUser, requestBody, load };
  }
