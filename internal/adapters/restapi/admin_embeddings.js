  const configEl = document.getElementById('embeddings-config');
  const recomputeSummaryEl = document.getElementById('embeddings-recompute-summary');
  const recomputeBtn = document.getElementById('embeddings-recompute-btn');
  const recomputeStatusEl = document.getElementById('embeddings-recompute-status');
  const recomputeProgressEl = document.getElementById('embeddings-recompute-progress');
  const recomputeResultEl = document.getElementById('embeddings-recompute-result');
  const concurrencyInput = document.getElementById('embeddings-recompute-concurrency');
  const concurrencySaveBtn = document.getElementById('embeddings-recompute-concurrency-save');
  const concurrencyStatusEl = document.getElementById('embeddings-recompute-concurrency-status');

  // providerLabel resolves a provider id ("hash" or an endpoint ID) to a human name -- falls
  // back to the raw ID if the endpoint's since been deleted (self-heals via
  // domain.ReconcileSearchWeights).
  function providerLabel(providerID, endpoints) {
    if (providerID === 'hash') return 'Hash (dependency-free)';
    const match = endpoints.find((e) => e.id === providerID);
    return match ? match.name + ' (' + match.id + ')' : providerID;
  }

  // activeSearchWeightsLabel summarizes embedding_search_weights as "name (weight)" pairs, one
  // per contributing provider -- one absent or at weight <=0 doesn't appear, same as not
  // contributing at all.
  function activeSearchWeightsLabel(weights, endpoints) {
    const active = Object.entries(weights || {}).filter(([, w]) => w > 0);
    if (active.length === 0) return 'none';
    return active.map(([id, w]) => providerLabel(id, endpoints) + ' (' + w + ')').join(', ');
  }

  function renderConfig(op, endpoints) {
    clear(configEl);
    const enabled = [];
    if (op.embedding_hash_enabled) enabled.push('hash');
    endpoints.filter((e) => e.enabled).forEach((e) => enabled.push(e.name));
    kvRow(configEl, 'Enabled providers', enabled.length ? enabled.join(', ') : 'none');
    kvRow(configEl, 'Active for search', activeSearchWeightsLabel(op.embedding_search_weights, endpoints));
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

  // pollTimer keeps polling while a recompute is in progress, triggered by ANY admin-server
  // instance, not just this click -- mirrors admin_pagerank.js's pollTimer so this page reflects
  // real cross-process state.
  let pollTimer = null;

  function renderRecomputeStatus(s) {
    clear(recomputeSummaryEl);
    kvRow(recomputeSummaryEl, 'Corpus size', s.total_docs + ' document(s)');

    setButtonLoading(recomputeBtn, s.in_progress, 'Recomputing…');
    if (s.in_progress) {
      recomputeStatusEl.textContent = 'Recomputing…';
      clear(recomputeResultEl);
      clear(recomputeProgressEl);
      // documents/failed are checkpointed live after every batch -- see
      // RunEmbeddingRecomputeJob's onBatchDone -- so this reflects real,
      // moving progress during a run that can take hours, not a frozen 0.
      const pct = s.total_docs > 0 ? Math.min(100, Math.round((100 * s.documents) / s.total_docs)) : 0;
      kvRow(recomputeProgressEl, 'Progress', s.documents + ' / ' + s.total_docs + ' document(s) (' + pct + '%)');
      if (s.failed > 0) kvRow(recomputeProgressEl, 'Failed so far', String(s.failed));
    } else if (!s.last_run_at) {
      recomputeStatusEl.textContent = 'No recompute has run yet on this instance.';
      clear(recomputeResultEl);
      clear(recomputeProgressEl);
    } else {
      recomputeStatusEl.textContent = 'Last run: ' + new Date(s.last_run_at).toLocaleString();
      clear(recomputeProgressEl);
      clear(recomputeResultEl);
      kvRow(recomputeResultEl, 'Documents recomputed', String(s.documents));
      kvRow(recomputeResultEl, 'Failed', String(s.failed));
      kvRow(recomputeResultEl, 'Duration', s.duration_ms + ' ms');
    }

    pollTimer = pollWhileInProgress(s.in_progress, pollTimer, () => { pollTimer = null; loadRecomputeStatus(); });
  }

  async function loadRecomputeStatus() {
    try {
      renderRecomputeStatus(await getJSON('/admin/api/embeddings/recompute'));
    } catch (err) {
      recomputeStatusEl.textContent = 'Could not load recompute status: ' + err.message;
    }
  }

  // The recompute runs server-side in the background -- this click only starts it and switches
  // to polling loadRecomputeStatus, since it can be a minutes-long job.
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

  // loadConcurrency/saveConcurrency read and write
  // operational.embedding_recompute_concurrency through the same general
  // /admin/api/settings endpoint the Settings page uses -- this page just
  // gives that one field its own control here instead, since it's specific
  // to the recompute job rather than an always-active knob. Applies live:
  // RunEmbeddingRecomputeJob re-reads it at the start of every batch, so a
  // save here takes effect on a running recompute's very next batch.
  async function loadConcurrency() {
    try {
      const settings = await getJSON('/admin/api/settings');
      concurrencyInput.value = settings.operational.embedding_recompute_concurrency;
    } catch (err) {
      concurrencyStatusEl.textContent = 'Could not load concurrency: ' + err.message;
    }
  }

  concurrencySaveBtn.addEventListener('click', async () => {
    concurrencyStatusEl.textContent = 'Saving…';
    try {
      const settings = await getJSON('/admin/api/settings');
      settings.operational.embedding_recompute_concurrency = Number.parseInt(concurrencyInput.value, 10);
      await postJSON('/admin/api/settings', settings);
      concurrencyStatusEl.textContent = 'Saved. Takes effect on the recompute’s next batch.';
    } catch (err) {
      concurrencyStatusEl.textContent = 'Could not save: ' + err.message;
    }
  });

  renderAdminNav();
  wireSignOut();
  loadConfig();
  loadRecomputeStatus();
  loadConcurrency();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See admin_embeddings.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      providerLabel, activeSearchWeightsLabel, renderConfig, loadConfig,
      renderRecomputeStatus, loadRecomputeStatus, loadConcurrency,
    };
  }
