  const form = document.getElementById('settings-form');
  const alphaEl = document.getElementById('alpha');
  const k1El = document.getElementById('k1');
  const bEl = document.getElementById('b');
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
  const status = document.getElementById('settings-status');

  function applySettings(s) {
    alphaEl.value = s.tuning.alpha;
    k1El.value = s.tuning.k1;
    bEl.value = s.tuning.b;
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
      const current = await getJSON('/admin/api/settings');
      const s = await postJSON('/admin/api/settings', {
        tuning: {
          alpha: parseFloat(alphaEl.value),
          k1: parseFloat(k1El.value),
          b: parseFloat(bEl.value),
          pagerank_weight: parseFloat(pageRankWeightEl.value),
        },
        operational: {
          ...current.operational,
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
