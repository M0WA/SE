  const form = document.getElementById('settings-form');
  const alphaEl = document.getElementById('alpha');
  const k1El = document.getElementById('k1');
  const bEl = document.getElementById('b');
  const titleWeightEl = document.getElementById('title-weight');
  const fetchTimeoutEl = document.getElementById('fetch-timeout');
  const userAgentEl = document.getElementById('user-agent');
  const defaultMaxPagesEl = document.getElementById('default-max-pages');
  const minTextLengthEl = document.getElementById('min-text-length');
  const crawlDelayEl = document.getElementById('crawl-delay');
  const maxResponseKBEl = document.getElementById('max-response-kb');
  const maxRetainedCrawlJobsEl = document.getElementById('max-retained-crawl-jobs');
  const defaultRendererEl = document.getElementById('default-renderer');
  const defaultLinkScopeEl = document.getElementById('default-link-scope');
  const defaultTopKEl = document.getElementById('default-top-k');
  const semanticPoolSizeEl = document.getElementById('semantic-pool-size');
  const annSearchEnabledEl = document.getElementById('ann-search-enabled');
  const embeddingProviderEl = document.getElementById('embedding-provider');
  const embeddingHTTPFieldsEl = document.getElementById('embedding-http-fields');
  const embeddingHTTPBaseURLEl = document.getElementById('embedding-http-base-url');
  const embeddingHTTPModelEl = document.getElementById('embedding-http-model');
  const embeddingHTTPDimensionsEl = document.getElementById('embedding-http-dimensions');
  const embeddingHTTPAPIKeyEl = document.getElementById('embedding-http-api-key');
  const embeddingHTTPAPIKeyHintEl = document.getElementById('embedding-http-api-key-hint');
  const embeddingHTTPModelOptionsEl = document.getElementById('embedding-http-model-options');
  const embeddingHTTPModelHintEl = document.getElementById('embedding-http-model-hint');
  const maxDocumentVersionsEl = document.getElementById('max-document-versions');
  const dbMaxOpenConnsEl = document.getElementById('db-max-open-conns');
  const dbMaxIdleConnsEl = document.getElementById('db-max-idle-conns');
  const dbConnMaxLifetimeEl = document.getElementById('db-conn-max-lifetime');
  const fuzzyEnabledEl = document.getElementById('fuzzy-enabled');
  const fuzzyMaxEditDistanceEl = document.getElementById('fuzzy-max-edit-distance');
  const pageRankWeightEl = document.getElementById('pagerank-weight');
  const pageRankIntervalEl = document.getElementById('pagerank-interval');
  const sessionTTLEl = document.getElementById('session-ttl');
  const status = document.getElementById('settings-status');
  const blockedTermsEl = document.getElementById('blocked-terms');
  const blockedDomainsEl = document.getElementById('blocked-domains');
  const boostedTermsEl = document.getElementById('boosted-terms');
  const boostedDomainsEl = document.getElementById('boosted-domains');
  const tileRankingEl = document.getElementById('tile-ranking');
  const tileTitleWeightEl = document.getElementById('tile-title-weight');
  const tileAnnEl = document.getElementById('tile-ann');
  const tileFuzzyEl = document.getElementById('tile-fuzzy');
  const tileSessionEl = document.getElementById('tile-session');
  const tileCrawlDefaultEl = document.getElementById('tile-crawl-default');
  const tileBlockedEl = document.getElementById('tile-blocked');
  const tileBoostedEl = document.getElementById('tile-boosted');
  const embeddingRecomputeBtnEl = document.getElementById('embedding-recompute-btn');
  const embeddingRecomputeStatusEl = document.getElementById('embedding-recompute-status');
  const embeddingRecomputeResultEl = document.getElementById('embedding-recompute-result');

  // renderSettingsSummary/renderOverridesSummary fill the read-only "at a
  // glance" tiles above the (collapsed-by-default) settings groups, so the
  // current configuration is visible without opening anything -- see
  // CLAUDE.md/the settings-page redesign discussion. Called from
  // applySettings/applyOverrides, so the tiles refresh on both initial load
  // and right after a save.
  function renderSettingsSummary(s) {
    tileRankingEl.textContent = s.tuning.alpha + ' · ' + s.tuning.k1 + ' · ' + s.tuning.b;
    tileTitleWeightEl.textContent = s.operational.title_weight + '×';
    tileAnnEl.textContent = s.operational.ann_search_enabled ? 'enabled' : 'disabled';
    tileFuzzyEl.textContent = s.operational.fuzzy_match_enabled
      ? 'on, d≤' + s.operational.fuzzy_max_edit_distance
      : 'off';
    tileSessionEl.textContent = s.operational.session_ttl_hours + 'h';
    tileCrawlDefaultEl.textContent = s.operational.default_max_pages + ' pages';
  }

  // toggleEmbeddingHTTPFields shows the HTTP-provider-only fields only when
  // that provider is actually selected -- they're meaningless (and
  // confusing to leave visible) while the default hash provider is active.
  function toggleEmbeddingHTTPFields() {
    embeddingHTTPFieldsEl.hidden = embeddingProviderEl.value !== 'http';
  }
  embeddingProviderEl.addEventListener('change', toggleEmbeddingHTTPFields);

  // The API key field starts readonly and only becomes editable on focus --
  // same reasoning as crawl.html's Basic auth password field (see
  // admin_crawl.js): a browser won't offer to autofill a saved login into a
  // field that's readonly when the page loads. Unlike that one-off crawl
  // form, this field also never shows the real stored value (see
  // applySettings/embeddingHTTPAPIKeyHintEl below) -- leaving it blank on
  // save means "keep whatever's already configured," not "clear it."
  embeddingHTTPAPIKeyEl.addEventListener('focus', () => embeddingHTTPAPIKeyEl.removeAttribute('readonly'), { once: true });

  function countLabel(n, noun) {
    return n + ' ' + noun + (n === 1 ? '' : 's');
  }

  function renderOverridesSummary(o) {
    tileBlockedEl.textContent = countLabel((o.blocked_terms || []).length, 'term') +
      ' · ' + countLabel((o.blocked_domains || []).length, 'domain');
    tileBoostedEl.textContent = countLabel(Object.keys(o.boosted_terms || {}).length, 'term') +
      ' · ' + countLabel(Object.keys(o.boosted_domains || {}).length, 'domain');
  }

  // loadEmbeddingModels prefills the model field's <datalist> suggestions
  // from GET /admin/api/embeddings/models, using whatever base URL/API key
  // are already saved server-side (see handleAdminEmbeddingsModels) --
  // never values just typed into the form but not yet saved. Only
  // attempted when the HTTP provider is selected and a base URL is
  // already saved -- there's nothing to ask otherwise, so a freshly-typed
  // base URL needs a save first before this prefills anything.
  async function loadEmbeddingModels(s) {
    clear(embeddingHTTPModelOptionsEl);
    embeddingHTTPModelHintEl.textContent = '';
    if (s.operational.embedding_provider !== 'http' || !s.operational.embedding_http_base_url) {
      return;
    }
    try {
      const r = await getJSON('/admin/api/embeddings/models');
      if (r.error) {
        embeddingHTTPModelHintEl.textContent = 'Could not list models: ' + r.error;
        return;
      }
      (r.models || []).forEach((id) => {
        const opt = document.createElement('option');
        opt.value = id;
        embeddingHTTPModelOptionsEl.appendChild(opt);
      });
      if (r.models && r.models.length > 0) {
        embeddingHTTPModelHintEl.textContent = r.models.length + ' model(s) available from this endpoint.';
      }
    } catch (err) {
      embeddingHTTPModelHintEl.textContent = 'Could not list models: ' + err.message;
    }
  }

  function applySettings(s) {
    alphaEl.value = s.tuning.alpha;
    k1El.value = s.tuning.k1;
    bEl.value = s.tuning.b;
    titleWeightEl.value = s.operational.title_weight;
    fetchTimeoutEl.value = s.operational.fetch_timeout_seconds;
    userAgentEl.value = s.operational.user_agent;
    defaultMaxPagesEl.value = s.operational.default_max_pages;
    minTextLengthEl.value = s.operational.min_text_length;
    crawlDelayEl.value = s.operational.crawl_delay_ms;
    maxResponseKBEl.value = s.operational.max_response_kb;
    maxRetainedCrawlJobsEl.value = s.operational.max_retained_crawl_jobs;
    defaultRendererEl.value = s.operational.default_renderer || 'none';
    defaultLinkScopeEl.value = s.operational.link_scope || 'domain';
    defaultTopKEl.value = s.operational.default_top_k;
    semanticPoolSizeEl.value = s.operational.semantic_candidate_pool_size;
    annSearchEnabledEl.checked = s.operational.ann_search_enabled;
    embeddingProviderEl.value = s.operational.embedding_provider || 'hash';
    embeddingHTTPBaseURLEl.value = s.operational.embedding_http_base_url || '';
    embeddingHTTPModelEl.value = s.operational.embedding_http_model || '';
    embeddingHTTPDimensionsEl.value = s.operational.embedding_http_dimensions || '';
    // The real key is never sent back (see toOperationalValues in admin.go)
    // -- this field always starts blank, only ever showing whether one is
    // currently configured, never the value itself.
    embeddingHTTPAPIKeyEl.value = '';
    embeddingHTTPAPIKeyHintEl.textContent = s.operational.embedding_http_api_key_set
      ? 'A key is currently configured. Leave blank to keep it, or type a new one to replace it.'
      : 'No key currently configured.';
    toggleEmbeddingHTTPFields();
    loadEmbeddingModels(s);
    maxDocumentVersionsEl.value = s.operational.max_document_versions;
    dbMaxOpenConnsEl.value = s.operational.db_max_open_conns;
    dbMaxIdleConnsEl.value = s.operational.db_max_idle_conns;
    dbConnMaxLifetimeEl.value = s.operational.db_conn_max_lifetime_minutes;
    fuzzyEnabledEl.checked = s.operational.fuzzy_match_enabled;
    fuzzyMaxEditDistanceEl.value = s.operational.fuzzy_max_edit_distance;
    pageRankWeightEl.value = s.tuning.pagerank_weight;
    pageRankIntervalEl.value = s.operational.pagerank_recompute_interval_minutes;
    sessionTTLEl.value = s.operational.session_ttl_hours;
    renderSettingsSummary(s);
  }

  async function loadSettings() {
    try {
      applySettings(await getJSON('/admin/api/settings'));
    } catch (err) {
      status.textContent = 'Could not load settings: ' + err.message;
    }
  }

  // Ranking/Crawler/.../Session and Blocked/Boosted are two logically
  // independent resources (GET/POST /admin/api/settings vs.
  // /admin/api/overrides, merged onto this page from the old standalone
  // Overrides page) but share this one form and one Save button -- saving
  // both together on a single click, rather than needing to remember to
  // click two separate buttons for one page of settings. Each save is
  // attempted independently (one endpoint failing doesn't stop the other
  // from being tried), and the combined result is reported on one status
  // line.
  async function saveSettings() {
    const s = await postJSON('/admin/api/settings', {
      tuning: {
        alpha: parseFloat(alphaEl.value),
        k1: parseFloat(k1El.value),
        b: parseFloat(bEl.value),
        pagerank_weight: parseFloat(pageRankWeightEl.value),
      },
      operational: {
        title_weight: parseInt(titleWeightEl.value, 10),
        fetch_timeout_seconds: parseInt(fetchTimeoutEl.value, 10),
        user_agent: userAgentEl.value,
        default_max_pages: parseInt(defaultMaxPagesEl.value, 10),
        min_text_length: parseInt(minTextLengthEl.value, 10),
        crawl_delay_ms: parseInt(crawlDelayEl.value, 10),
        max_response_kb: parseInt(maxResponseKBEl.value, 10),
        max_retained_crawl_jobs: parseInt(maxRetainedCrawlJobsEl.value, 10),
        default_renderer: defaultRendererEl.value,
        link_scope: defaultLinkScopeEl.value,
        default_top_k: parseInt(defaultTopKEl.value, 10),
        semantic_candidate_pool_size: parseInt(semanticPoolSizeEl.value, 10),
        ann_search_enabled: annSearchEnabledEl.checked,
        embedding_provider: embeddingProviderEl.value,
        embedding_http_base_url: embeddingHTTPBaseURLEl.value,
        embedding_http_model: embeddingHTTPModelEl.value,
        embedding_http_dimensions: parseInt(embeddingHTTPDimensionsEl.value, 10) || 0,
        embedding_http_api_key: embeddingHTTPAPIKeyEl.value,
        max_document_versions: parseInt(maxDocumentVersionsEl.value, 10),
        db_max_open_conns: parseInt(dbMaxOpenConnsEl.value, 10),
        db_max_idle_conns: parseInt(dbMaxIdleConnsEl.value, 10),
        db_conn_max_lifetime_minutes: parseInt(dbConnMaxLifetimeEl.value, 10),
        fuzzy_match_enabled: fuzzyEnabledEl.checked,
        fuzzy_max_edit_distance: parseInt(fuzzyMaxEditDistanceEl.value, 10),
        pagerank_recompute_interval_minutes: parseInt(pageRankIntervalEl.value, 10),
        session_ttl_hours: parseInt(sessionTTLEl.value, 10),
      },
    });
    applySettings(s);
    return s.embedding_test_error || '';
  }

  async function saveOverrides() {
    const o = await postJSON('/admin/api/overrides', {
      blocked_terms: parseLines(blockedTermsEl.value),
      blocked_domains: parseLines(blockedDomainsEl.value),
      boosted_terms: parseFactorLines(boostedTermsEl.value),
      boosted_domains: parseFactorLines(boostedDomainsEl.value),
    });
    applyOverrides(o);
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    status.textContent = '';
    const errors = [];
    const warnings = [];
    try {
      const embeddingTestError = await saveSettings();
      if (embeddingTestError) {
        warnings.push('embedding provider test failed: ' + embeddingTestError);
      }
    } catch (err) {
      errors.push('settings: ' + err.message);
    }
    try {
      await saveOverrides();
    } catch (err) {
      errors.push('overrides: ' + err.message);
    }
    if (errors.length > 0) {
      status.style.color = 'var(--accent)';
      status.textContent = 'Could not save ' + errors.join('; ');
    } else if (warnings.length > 0) {
      status.style.color = 'var(--accent)';
      status.textContent = 'Saved, but ' + warnings.join('; ');
    } else {
      status.style.color = 'var(--ink-muted)';
      status.textContent = 'Saved.';
    }
  });

  function factorsToText(factors) {
    return Object.entries(factors || {}).map(([k, v]) => k + ' ' + v).join('\n');
  }

  function parseFactorLines(text) {
    const out = {};
    for (const line of parseLines(text)) {
      const parts = line.split(/\s+/);
      if (parts.length < 2) continue;
      const factor = parseFloat(parts[parts.length - 1]);
      if (Number.isNaN(factor)) continue;
      out[parts.slice(0, -1).join(' ')] = factor;
    }
    return out;
  }

  function applyOverrides(o) {
    blockedTermsEl.value = linesToText(o.blocked_terms);
    blockedDomainsEl.value = linesToText(o.blocked_domains);
    boostedTermsEl.value = factorsToText(o.boosted_terms);
    boostedDomainsEl.value = factorsToText(o.boosted_domains);
    renderOverridesSummary(o);
  }

  async function loadOverrides() {
    try {
      applyOverrides(await getJSON('/admin/api/overrides'));
    } catch (err) {
      status.textContent = 'Could not load overrides: ' + err.message;
    }
  }

  // embeddingRecomputePollTimer keeps polling GET
  // /admin/api/embeddings/recompute while a recompute is in progress --
  // triggered by ANY admin-server instance, not just this browser's own
  // click below -- mirroring admin_pagerank.js's pollTimer for the same
  // reason: this page should reflect real cross-process state.
  let embeddingRecomputePollTimer = null;

  function renderEmbeddingRecomputeStatus(s) {
    setButtonLoading(embeddingRecomputeBtnEl, s.in_progress, 'Recomputing…');
    if (s.in_progress) {
      embeddingRecomputeStatusEl.textContent = 'Recomputing… (' + s.total_docs + ' documents in the corpus)';
      clear(embeddingRecomputeResultEl);
      if (!embeddingRecomputePollTimer) {
        embeddingRecomputePollTimer = setTimeout(() => { embeddingRecomputePollTimer = null; loadEmbeddingRecomputeStatus(); }, 2000);
      }
      return;
    }
    if (embeddingRecomputePollTimer) {
      clearTimeout(embeddingRecomputePollTimer);
      embeddingRecomputePollTimer = null;
    }
    if (!s.last_run_at) {
      embeddingRecomputeStatusEl.textContent = 'No recompute has run yet on this instance.';
      clear(embeddingRecomputeResultEl);
      return;
    }
    embeddingRecomputeStatusEl.textContent = 'Last run: ' + new Date(s.last_run_at).toLocaleString();
    clear(embeddingRecomputeResultEl);
    kvRow(embeddingRecomputeResultEl, 'Documents recomputed', String(s.documents));
    kvRow(embeddingRecomputeResultEl, 'Failed', String(s.failed));
    kvRow(embeddingRecomputeResultEl, 'Duration', s.duration_ms + ' ms');
  }

  async function loadEmbeddingRecomputeStatus() {
    try {
      renderEmbeddingRecomputeStatus(await getJSON('/admin/api/embeddings/recompute'));
    } catch (err) {
      embeddingRecomputeStatusEl.textContent = 'Could not load embedding recompute status: ' + err.message;
    }
  }

  // The recompute itself runs in the background on the server (see
  // handleAdminEmbeddingsRecomputeStart) -- this click only starts it and
  // switches to polling loadEmbeddingRecomputeStatus for the result, unlike
  // saveSettings's plain await-then-render (PageRank's own recompute is
  // synchronous; this one, one Embed call per document, is not).
  embeddingRecomputeBtnEl.addEventListener('click', async () => {
    setButtonLoading(embeddingRecomputeBtnEl, true, 'Recomputing…');
    embeddingRecomputeStatusEl.textContent = 'Starting…';
    clear(embeddingRecomputeResultEl);
    try {
      await postJSON('/admin/api/embeddings/recompute', {});
      await loadEmbeddingRecomputeStatus();
    } catch (err) {
      embeddingRecomputeStatusEl.textContent = 'Could not start recompute: ' + err.message;
      setButtonLoading(embeddingRecomputeBtnEl, false);
    }
  });

  wireSignOut();
  loadSettings();
  loadOverrides();
  loadEmbeddingRecomputeStatus();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See
  // internal/adapters/restapi/admin_settings.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      applySettings, loadSettings, saveSettings,
      saveOverrides, applyOverrides, loadOverrides,
      factorsToText, parseFactorLines,
      renderSettingsSummary, renderOverridesSummary,
      toggleEmbeddingHTTPFields,
      renderEmbeddingRecomputeStatus, loadEmbeddingRecomputeStatus,
      loadEmbeddingModels,
    };
  }
