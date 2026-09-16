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
  const urlAliasWWWEnabledEl = document.getElementById('url-alias-www-enabled');
  const contentDedupEnabledEl = document.getElementById('content-dedup-enabled');
  const contentDedupMethodEl = document.getElementById('content-dedup-method');
  const contentDedupSimHashMaxDistanceEl = document.getElementById('content-dedup-simhash-max-distance');
  const contentDedupIntervalEl = document.getElementById('content-dedup-interval');
  const defaultTopKEl = document.getElementById('default-top-k');
  const semanticPoolSizeEl = document.getElementById('semantic-pool-size');
  const annSearchEnabledEl = document.getElementById('ann-search-enabled');
  const embeddingHashEnabledEl = document.getElementById('embedding-hash-enabled');
  const embeddingSearchWeightsEl = document.getElementById('embedding-search-weights');
  const embeddingTitleWeightEl = document.getElementById('embedding-title-weight');
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

  function countLabel(n, noun) {
    return n + ' ' + noun + (n === 1 ? '' : 's');
  }

  function renderOverridesSummary(o) {
    tileBlockedEl.textContent = countLabel((o.blocked_terms || []).length, 'term') +
      ' · ' + countLabel((o.blocked_domains || []).length, 'domain');
    tileBoostedEl.textContent = countLabel(Object.keys(o.boosted_terms || {}).length, 'term') +
      ' · ' + countLabel(Object.keys(o.boosted_domains || {}).length, 'domain');
  }

  // weightInputID is the DOM id for provider's weight <input>, shared
  // between building the list (loadEmbeddingSearchWeights) and reading it
  // back (collectEmbeddingSearchWeights) -- also readable off the input's
  // own data-provider attribute, but a stable id is handy for direct
  // lookups (see the test suite).
  function weightInputID(provider) {
    return 'embedding-search-weight-' + provider;
  }

  function addEmbeddingWeightRow(provider, label, weight, stale) {
    const row = document.createElement('div');
    row.className = 'form-row';
    const labelEl = document.createElement('label');
    labelEl.setAttribute('for', weightInputID(provider));
    labelEl.textContent = label;
    row.appendChild(labelEl);
    row.appendChild(document.createElement('br'));
    const input = document.createElement('input');
    input.id = weightInputID(provider);
    input.className = 'field';
    input.type = 'number';
    input.step = 'any';
    input.min = '0';
    input.value = weight;
    input.dataset.provider = provider;
    input.disabled = !!stale;
    row.appendChild(input);
    embeddingSearchWeightsEl.appendChild(row);
  }

  // loadEmbeddingSearchWeights populates one weight <input> per provider:
  // hash (always offered) plus every currently *enabled* HTTP endpoint
  // (fetched fresh -- endpoints are managed on their own page, not this
  // form), pre-filled from weights (0 for anything not already weighted).
  // A weight entry naming a provider that's no longer enabled (its
  // endpoint deleted or disabled since this value was saved) is shown
  // anyway, locked and clearly labeled, so the form doesn't silently drop
  // it out from under an admin who hasn't saved yet -- but it's excluded
  // from collectEmbeddingSearchWeights's resubmission, since the next save
  // would have it dropped by domain.ReconcileSearchWeights server-side
  // either way.
  async function loadEmbeddingSearchWeights(weights) {
    weights = weights || {};
    clear(embeddingSearchWeightsEl);
    addEmbeddingWeightRow('hash', 'Hash (dependency-free)', weights.hash || 0, false);

    let endpoints = [];
    try {
      const r = await getJSON('/admin/api/embeddings/endpoints');
      if (Array.isArray(r)) endpoints = r;
    } catch (err) {
      // The endpoints list is a convenience for populating this section --
      // a failed (or unexpectedly-shaped) fetch just means "hash only, for
      // now," not a reason to block the rest of the settings page from
      // loading.
    }
    const enabledIDs = new Set(['hash']);
    endpoints.filter((e) => e.enabled).forEach((e) => {
      enabledIDs.add(e.id);
      addEmbeddingWeightRow(e.id, e.name + ' (' + e.id + ')', weights[e.id] || 0, false);
    });

    Object.keys(weights).forEach((provider) => {
      if (!enabledIDs.has(provider)) {
        addEmbeddingWeightRow(provider, provider + ' (not currently enabled — will self-heal on save)', weights[provider], true);
      }
    });
  }

  // collectEmbeddingSearchWeights reads every non-stale weight <input>
  // back into a provider->weight map, omitting anything left at (or
  // parsed as) 0 or below -- a 0 weight simply isn't part of the active
  // set, the same as omitting the provider entirely.
  function collectEmbeddingSearchWeights() {
    const weights = {};
    embeddingSearchWeightsEl.querySelectorAll('input[data-provider]').forEach((input) => {
      if (input.disabled) return;
      const w = parseFloat(input.value);
      if (!Number.isNaN(w) && w > 0) {
        weights[input.dataset.provider] = w;
      }
    });
    return weights;
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
    urlAliasWWWEnabledEl.checked = s.operational.url_alias_www_enabled;
    contentDedupEnabledEl.checked = s.operational.content_dedup_enabled;
    contentDedupMethodEl.value = s.operational.content_dedup_method || 'exact';
    contentDedupSimHashMaxDistanceEl.value = s.operational.content_dedup_simhash_max_distance;
    contentDedupIntervalEl.value = s.operational.content_dedup_interval_minutes;
    defaultTopKEl.value = s.operational.default_top_k;
    semanticPoolSizeEl.value = s.operational.semantic_candidate_pool_size;
    annSearchEnabledEl.checked = s.operational.ann_search_enabled;
    embeddingHashEnabledEl.checked = s.operational.embedding_hash_enabled;
    // Unlike the fields above, 0 is a real, meaningful value here (title
    // blending disabled -- see the field's own doc comment in admin.go),
    // so it's assigned directly rather than falling back to '' on falsy.
    embeddingTitleWeightEl.value = s.operational.embedding_title_weight;
    loadEmbeddingSearchWeights(s.operational.embedding_search_weights);
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
        url_alias_www_enabled: urlAliasWWWEnabledEl.checked,
        content_dedup_enabled: contentDedupEnabledEl.checked,
        content_dedup_method: contentDedupMethodEl.value,
        content_dedup_simhash_max_distance: parseInt(contentDedupSimHashMaxDistanceEl.value, 10),
        content_dedup_interval_minutes: parseInt(contentDedupIntervalEl.value, 10),
        default_top_k: parseInt(defaultTopKEl.value, 10),
        semantic_candidate_pool_size: parseInt(semanticPoolSizeEl.value, 10),
        ann_search_enabled: annSearchEnabledEl.checked,
        embedding_hash_enabled: embeddingHashEnabledEl.checked,
        embedding_search_weights: collectEmbeddingSearchWeights(),
        embedding_title_weight: parseFloat(embeddingTitleWeightEl.value) || 0,
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
    try {
      await saveSettings();
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

  wireSignOut();
  loadSettings();
  loadOverrides();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See
  // internal/adapters/restapi/admin_settings.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      applySettings, loadSettings, saveSettings,
      saveOverrides, applyOverrides, loadOverrides,
      factorsToText, parseFactorLines,
      renderSettingsSummary, renderOverridesSummary,
      loadEmbeddingSearchWeights, collectEmbeddingSearchWeights, weightInputID,
    };
  }
