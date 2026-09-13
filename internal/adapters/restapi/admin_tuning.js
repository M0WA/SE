  const form = document.getElementById('tuning-form');
  const alphaEl = document.getElementById('alpha');
  const k1El = document.getElementById('k1');
  const bEl = document.getElementById('b');
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
  const dbMaxOpenConnsEl = document.getElementById('db-max-open-conns');
  const dbMaxIdleConnsEl = document.getElementById('db-max-idle-conns');
  const dbConnMaxLifetimeEl = document.getElementById('db-conn-max-lifetime');
  const fuzzyEnabledEl = document.getElementById('fuzzy-enabled');
  const fuzzyMaxEditDistanceEl = document.getElementById('fuzzy-max-edit-distance');
  const pageRankWeightEl = document.getElementById('pagerank-weight');
  const pageRankIntervalEl = document.getElementById('pagerank-interval');
  const sessionTTLEl = document.getElementById('session-ttl');
  const status = document.getElementById('tuning-status');

  function applySettings(s) {
    alphaEl.value = s.tuning.alpha;
    k1El.value = s.tuning.k1;
    bEl.value = s.tuning.b;
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

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    status.textContent = '';
    try {
      const s = await postJSON('/admin/api/settings', {
        tuning: {
          alpha: parseFloat(alphaEl.value),
          k1: parseFloat(k1El.value),
          b: parseFloat(bEl.value),
          pagerank_weight: parseFloat(pageRankWeightEl.value),
        },
        operational: {
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
      status.style.color = 'var(--ink-muted)';
      status.textContent = 'Saved.';
    } catch (err) {
      status.style.color = 'var(--accent)';
      status.textContent = 'Could not save: ' + err.message;
    }
  });

  wireSignOut();
  loadSettings();
