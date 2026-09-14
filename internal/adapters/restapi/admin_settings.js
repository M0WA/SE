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
    maxDocumentVersionsEl.value = s.operational.max_document_versions;
    dbMaxOpenConnsEl.value = s.operational.db_max_open_conns;
    dbMaxIdleConnsEl.value = s.operational.db_max_idle_conns;
    dbConnMaxLifetimeEl.value = s.operational.db_conn_max_lifetime_minutes;
    fuzzyEnabledEl.checked = s.operational.fuzzy_match_enabled;
    fuzzyMaxEditDistanceEl.value = s.operational.fuzzy_max_edit_distance;
    pageRankWeightEl.value = s.tuning.pagerank_weight;
    pageRankIntervalEl.value = s.operational.pagerank_recompute_interval_minutes;
    sessionTTLEl.value = s.operational.session_ttl_hours;
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
