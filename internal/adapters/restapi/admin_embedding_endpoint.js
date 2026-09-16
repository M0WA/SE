  const id = decodeURIComponent(window.location.pathname.split('/').pop());
  const isNew = id === 'new';

  const titleEl = document.getElementById('endpoint-title');
  const metaEl = document.getElementById('endpoint-meta');
  const statusEl = document.getElementById('endpoint-status');
  const form = document.getElementById('endpoint-form');
  const nameEl = document.getElementById('endpoint-name');
  const baseURLEl = document.getElementById('endpoint-base-url');
  const apiKeyEl = document.getElementById('endpoint-api-key');
  const clearAPIKeyEl = document.getElementById('endpoint-clear-api-key');
  const modelEl = document.getElementById('endpoint-model');
  const listModelsBtn = document.getElementById('endpoint-list-models-btn');
  const modelsStatusEl = document.getElementById('endpoint-models-status');
  const modelsResultEl = document.getElementById('endpoint-models-result');
  const dimensionsEl = document.getElementById('endpoint-dimensions');
  const rateLimitEl = document.getElementById('endpoint-rate-limit');
  const enabledEl = document.getElementById('endpoint-enabled');
  const testBtn = document.getElementById('endpoint-test-btn');
  const testStatusEl = document.getElementById('endpoint-test-status');
  const saveBtn = document.getElementById('endpoint-save-btn');
  const deleteBtn = document.getElementById('endpoint-delete-btn');

  if (isNew) {
    titleEl.textContent = 'Add endpoint';
    document.title = 'se. — add embedding endpoint';
    metaEl.hidden = true;
    deleteBtn.hidden = true;
    enabledEl.checked = true;
    clearAPIKeyEl.disabled = true;
    form.hidden = false;
  } else {
    deleteBtn.hidden = false;
  }

  function applyEndpoint(e) {
    titleEl.textContent = e.name;
    document.title = 'se. — ' + e.name;
    metaEl.textContent = 'ID: ' + e.id + ' · Created ' + new Date(e.created_at).toLocaleString();

    nameEl.value = e.name;
    baseURLEl.value = e.base_url;
    // The server never echoes a stored API key's real value (see
    // embeddingEndpointResponse) -- this field always starts blank, and
    // saving with it left blank keeps whatever key is already stored (see
    // requestBody). has_api_key only drives the placeholder text and the
    // "remove" checkbox's availability, so the admin can see whether a key
    // is set without ever seeing its value.
    apiKeyEl.value = '';
    apiKeyEl.placeholder = e.has_api_key ? '(unchanged — a key is already set)' : '';
    clearAPIKeyEl.checked = false;
    clearAPIKeyEl.disabled = !e.has_api_key;
    modelEl.value = e.model;
    dimensionsEl.value = e.dimensions;
    rateLimitEl.value = e.rate_limit_per_second;
    enabledEl.checked = e.enabled;

    form.hidden = false;
  }

  // candidateBody is what a not-yet-saved (or being-edited) endpoint looks
  // like to the test-connection/list-models probes -- just the fields a
  // real Embed/ListModels call needs, independent of whether this form is
  // in "new" or "edit" mode.
  function candidateBody() {
    return {
      base_url: baseURLEl.value,
      api_key: apiKeyEl.value,
      model: modelEl.value,
      dimensions: parseInt(dimensionsEl.value, 10) || 0,
    };
  }

  function requestBody() {
    return {
      name: nameEl.value,
      base_url: baseURLEl.value,
      api_key: apiKeyEl.value,
      clear_api_key: clearAPIKeyEl.checked,
      model: modelEl.value,
      dimensions: parseInt(dimensionsEl.value, 10) || 0,
      rate_limit_per_second: parseFloat(rateLimitEl.value) || 0,
      enabled: enabledEl.checked,
    };
  }

  async function load() {
    if (isNew) return;
    try {
      applyEndpoint(await getJSON('/admin/api/embeddings/endpoints/' + encodeURIComponent(id)));
    } catch (err) {
      titleEl.textContent = 'Not found';
      statusEl.textContent = 'Could not load this endpoint: ' + err.message;
      form.hidden = true;
    }
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    setButtonLoading(saveBtn, true, 'Saving…');
    statusEl.textContent = '';
    try {
      if (isNew) {
        const created = await postJSON('/admin/api/embeddings/endpoints', requestBody());
        window.location.href = '/admin/embeddings/endpoint/' + encodeURIComponent(created.id);
        return;
      }
      await patchJSON('/admin/api/embeddings/endpoints/' + encodeURIComponent(id), requestBody());
      statusEl.textContent = 'Saved.';
      await load();
    } catch (err) {
      statusEl.textContent = 'Could not save: ' + err.message;
    } finally {
      setButtonLoading(saveBtn, false);
    }
  });

  listModelsBtn.addEventListener('click', async () => {
    setButtonLoading(listModelsBtn, true, 'Listing…');
    modelsStatusEl.textContent = '';
    clear(modelsResultEl);
    try {
      const r = await postJSON('/admin/api/embeddings/models', candidateBody());
      if (r.error) {
        modelsStatusEl.textContent = 'Could not list models: ' + r.error;
      } else if (!r.models || r.models.length === 0) {
        modelsStatusEl.textContent = 'No models reported -- either no base URL is set yet, or this endpoint does not support listing models.';
      } else {
        modelsStatusEl.textContent = r.models.length + ' model(s) available from this endpoint.';
        r.models.forEach((m) => kvRow(modelsResultEl, 'Model', m));
      }
    } catch (err) {
      modelsStatusEl.textContent = 'Could not list models: ' + err.message;
    } finally {
      setButtonLoading(listModelsBtn, false);
    }
  });

  testBtn.addEventListener('click', async () => {
    setButtonLoading(testBtn, true, 'Testing…');
    testStatusEl.textContent = '';
    try {
      const r = await postJSON('/admin/api/embeddings/test', candidateBody());
      testStatusEl.textContent = r.error ? 'Connection failed: ' + r.error : 'Connection succeeded.';
    } catch (err) {
      testStatusEl.textContent = 'Could not test connection: ' + err.message;
    } finally {
      setButtonLoading(testBtn, false);
    }
  });

  deleteBtn.addEventListener('click', async () => {
    if (!window.confirm('Delete this endpoint? Search and recompute will no longer be able to use it.')) return;
    deleteBtn.disabled = true;
    try {
      await deleteRequest('/admin/api/embeddings/endpoints/' + encodeURIComponent(id));
      window.location.href = '/admin/embeddings/endpoints';
    } catch (err) {
      deleteBtn.disabled = false;
      window.alert('Could not delete: ' + err.message);
    }
  });

  wireSignOut();
  load();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See
  // admin_embedding_endpoint.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { applyEndpoint, requestBody, candidateBody, load };
  }
