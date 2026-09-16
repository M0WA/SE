  const configEl = document.getElementById('embeddings-config');
  const recomputeSummaryEl = document.getElementById('embeddings-recompute-summary');
  const recomputeBtn = document.getElementById('embeddings-recompute-btn');
  const recomputeStatusEl = document.getElementById('embeddings-recompute-status');
  const recomputeResultEl = document.getElementById('embeddings-recompute-result');

  // activeProviderLabel resolves op.embedding_provider (either the literal
  // "hash" or a configured HTTP endpoint's ID) against the fetched endpoint
  // list to a human name -- falling back to the raw ID if it names an
  // endpoint that's been deleted since (self-heals on the next settings
  // sync, see domain.ReconcileActiveProvider).
  function activeProviderLabel(providerID, endpoints) {
    if (providerID === 'hash') return 'Hash (dependency-free)';
    const match = endpoints.find((e) => e.id === providerID);
    return match ? match.name + ' (' + match.id + ')' : providerID;
  }

  function renderConfig(op, endpoints) {
    clear(configEl);
    const enabled = [];
    if (op.embedding_hash_enabled) enabled.push('hash');
    endpoints.filter((e) => e.enabled).forEach((e) => enabled.push(e.name));
    kvRow(configEl, 'Enabled providers', enabled.length ? enabled.join(', ') : 'none');
    kvRow(configEl, 'Active for search', activeProviderLabel(op.embedding_provider, endpoints));
    kvRow(configEl, 'HTTP endpoints configured', String(endpoints.length));
    kvRow(configEl, 'Title weight', op.embedding_title_weight + ' (0 = body only, 1 = title only)');
  }

  async function loadConfig() {
    try {
      const [settings, endpoints] = await Promise.all([
        getJSON('/admin/api/settings'),
        getJSON('/admin/api/embeddings/endpoints'),
      ]);
      renderConfig(settings.operational, endpoints);
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

  wireSignOut();
  loadConfig();
  loadRecomputeStatus();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See admin_embeddings.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      activeProviderLabel, renderConfig, loadConfig,
      renderRecomputeStatus, loadRecomputeStatus,
    };
  }
