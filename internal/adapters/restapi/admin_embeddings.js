  const configEl = document.getElementById('embeddings-config');
  const modelsBtn = document.getElementById('embeddings-models-btn');
  const modelsStatusEl = document.getElementById('embeddings-models-status');
  const modelsResultEl = document.getElementById('embeddings-models-result');
  const recomputeSummaryEl = document.getElementById('embeddings-recompute-summary');
  const recomputeBtn = document.getElementById('embeddings-recompute-btn');
  const recomputeStatusEl = document.getElementById('embeddings-recompute-status');
  const recomputeResultEl = document.getElementById('embeddings-recompute-result');

  function renderConfig(s) {
    clear(configEl);
    const op = s.operational;
    kvRow(configEl, 'Provider', op.embedding_provider === 'http' ? 'HTTP (trained model)' : 'Hash (dependency-free, default)');
    if (op.embedding_provider === 'http') {
      kvRow(configEl, 'Base URL', op.embedding_http_base_url || '(not set)');
      kvRow(configEl, 'Model', op.embedding_http_model || '(not set)');
      kvRow(configEl, 'Dimensions', String(op.embedding_http_dimensions));
      kvRow(configEl, 'API key', op.embedding_http_api_key_set ? 'configured' : 'not configured');
    }
    kvRow(configEl, 'Rate limit', op.embedding_rate_limit_per_second + ' req/s (shared by recompute and crawling)');
    kvRow(configEl, 'Title weight', op.embedding_title_weight + ' (0 = body only, 1 = title only)');
  }

  async function loadConfig() {
    try {
      renderConfig(await getJSON('/admin/api/settings'));
    } catch (err) {
      configEl.textContent = 'Could not load configuration: ' + err.message;
    }
  }

  // pollTimer keeps polling GET /admin/api/embeddings/recompute while a
  // recompute is in progress -- triggered by ANY admin-server instance,
  // not just this browser's own click below -- mirroring admin_pagerank.js's
  // identical pollTimer for the same reason: this page should reflect real
  // cross-process state.
  let pollTimer = null;

  function renderRecomputeStatus(s) {
    clear(recomputeSummaryEl);
    kvRow(recomputeSummaryEl, 'Corpus size', s.total_docs + ' document(s)');

    setButtonLoading(recomputeBtn, s.in_progress, 'Recomputing…');
    if (s.in_progress) {
      recomputeStatusEl.textContent = 'Recomputing…';
      clear(recomputeResultEl);
    } else if (!s.last_run_at) {
      recomputeStatusEl.textContent = 'No recompute has run yet on this instance.';
      clear(recomputeResultEl);
    } else {
      recomputeStatusEl.textContent = 'Last run: ' + new Date(s.last_run_at).toLocaleString();
      clear(recomputeResultEl);
      kvRow(recomputeResultEl, 'Documents recomputed', String(s.documents));
      kvRow(recomputeResultEl, 'Failed', String(s.failed));
      kvRow(recomputeResultEl, 'Duration', s.duration_ms + ' ms');
    }

    if (s.in_progress) {
      if (!pollTimer) pollTimer = setTimeout(() => { pollTimer = null; loadRecomputeStatus(); }, 2000);
    } else if (pollTimer) {
      clearTimeout(pollTimer);
      pollTimer = null;
    }
  }

  async function loadRecomputeStatus() {
    try {
      renderRecomputeStatus(await getJSON('/admin/api/embeddings/recompute'));
    } catch (err) {
      recomputeStatusEl.textContent = 'Could not load recompute status: ' + err.message;
    }
  }

  // The recompute itself runs in the background on the server -- this
  // click only starts it and switches to polling loadRecomputeStatus for
  // the result, since a corpus-wide recompute is a real, possibly
  // minutes-long background job, not something to await inline.
  recomputeBtn.addEventListener('click', async () => {
    setButtonLoading(recomputeBtn, true, 'Recomputing…');
    recomputeStatusEl.textContent = 'Starting…';
    clear(recomputeResultEl);
    try {
      await postJSON('/admin/api/embeddings/recompute', {});
      await loadRecomputeStatus();
    } catch (err) {
      recomputeStatusEl.textContent = 'Could not start recompute: ' + err.message;
      setButtonLoading(recomputeBtn, false);
    }
  });

  // Listing models is a real network call against whatever endpoint is
  // configured (and can fail, e.g. bad credentials) -- a deliberate click,
  // not something fetched automatically on page load.
  modelsBtn.addEventListener('click', async () => {
    setButtonLoading(modelsBtn, true, 'Listing…');
    modelsStatusEl.textContent = '';
    clear(modelsResultEl);
    try {
      const r = await getJSON('/admin/api/embeddings/models');
      if (r.error) {
        modelsStatusEl.textContent = 'Could not list models: ' + r.error;
      } else if (!r.models || r.models.length === 0) {
        modelsStatusEl.textContent = 'No models reported -- either the provider is "hash", no base URL is saved yet, or this endpoint does not support listing models.';
      } else {
        modelsStatusEl.textContent = r.models.length + ' model(s) available from this endpoint.';
        r.models.forEach((id) => kvRow(modelsResultEl, 'Model', id));
      }
    } catch (err) {
      modelsStatusEl.textContent = 'Could not list models: ' + err.message;
    } finally {
      setButtonLoading(modelsBtn, false);
    }
  });

  wireSignOut();
  loadConfig();
  loadRecomputeStatus();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See admin_embeddings.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      renderConfig, loadConfig,
      renderRecomputeStatus, loadRecomputeStatus,
    };
  }
