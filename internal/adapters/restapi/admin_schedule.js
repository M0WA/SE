  const id = decodeURIComponent(window.location.pathname.split('/').pop());

  const titleEl = document.getElementById('schedule-title');
  const metaEl = document.getElementById('schedule-meta');
  const statusEl = document.getElementById('schedule-status');
  const form = document.getElementById('schedule-form');
  const seedURLsEl = document.getElementById('schedule-seed-urls');
  const maxPagesEl = document.getElementById('schedule-max-pages');
  const respectRobotsEl = document.getElementById('schedule-respect-robots');
  const linkScopeEl = document.getElementById('schedule-link-scope');
  const allowedDomainsEl = document.getElementById('schedule-allowed-domains');
  const blockedDomainsEl = document.getElementById('schedule-blocked-domains');
  const followIndexedEl = document.getElementById('schedule-follow-indexed');
  const useSitemapEl = document.getElementById('schedule-use-sitemap');
  const prioritizeUnindexedEl = document.getElementById('schedule-prioritize-unindexed');
  const rendererEl = document.getElementById('schedule-renderer');
  const userAgentEl = document.getElementById('schedule-user-agent');
  const cookieEl = document.getElementById('schedule-cookie');
  const basicUserEl = document.getElementById('schedule-basic-user');
  const basicPassEl = document.getElementById('schedule-basic-pass');
  const fetchTimeoutEl = document.getElementById('schedule-fetch-timeout');
  const minTextLengthEl = document.getElementById('schedule-min-text-length');
  const delayEl = document.getElementById('schedule-delay');
  const maxResponseEl = document.getElementById('schedule-max-response');
  const intervalEl = document.getElementById('schedule-interval');
  const maxRunsEl = document.getElementById('schedule-max-runs');
  const enabledEl = document.getElementById('schedule-enabled');
  const saveBtn = document.getElementById('schedule-save-btn');
  const runNowBtn = document.getElementById('schedule-run-now-btn');
  const deleteBtn = document.getElementById('schedule-delete-btn');

  // linesToText/parseLines now live in admin.js, shared with every other
  // page that has a one-value-per-line <textarea> field.

  function applySchedule(s) {
    titleEl.textContent = seedSummary(s.seed_urls);
    document.title = 'se. — ' + seedSummary(s.seed_urls);
    metaEl.textContent = 'Created ' + new Date(s.created_at).toLocaleString() +
      ' · Runs so far: ' + s.run_count +
      ' · Next run: ' + (s.next_run_at ? new Date(s.next_run_at).toLocaleString() : '—') +
      ' · Last run: ' + (s.last_run_at ? new Date(s.last_run_at).toLocaleString() : 'never');

    seedURLsEl.value = linesToText(s.seed_urls);
    maxPagesEl.value = s.max_pages;
    respectRobotsEl.checked = s.respect_robots;
    linkScopeEl.value = s.link_scope || '';
    allowedDomainsEl.value = linesToText(s.allowed_domains);
    blockedDomainsEl.value = linesToText(s.blocked_domains);
    followIndexedEl.checked = s.follow_indexed_domains;
    useSitemapEl.checked = s.use_sitemap;
    prioritizeUnindexedEl.checked = s.prioritize_unindexed;
    rendererEl.value = s.renderer || '';
    userAgentEl.value = s.user_agent || '';
    cookieEl.value = s.cookie || '';
    basicUserEl.value = s.basic_auth_user || '';
    basicPassEl.value = s.basic_auth_pass || '';
    fetchTimeoutEl.value = s.fetch_timeout_seconds || '';
    minTextLengthEl.value = s.min_text_length || '';
    delayEl.value = s.crawl_delay_ms || '';
    maxResponseEl.value = s.max_response_kb || '';
    intervalEl.value = s.interval_minutes || '';
    maxRunsEl.value = s.max_runs || '';
    enabledEl.checked = s.enabled;

    form.hidden = false;
  }

  // requestBody mirrors crawl.html's scheduledCrawlRequest construction --
  // PATCH is a full replace, so every field is resent, credentials
  // included, or they'd silently be cleared.
  function requestBody() {
    return {
      seed_urls: parseLines(seedURLsEl.value),
      max_pages: parseInt(maxPagesEl.value, 10) || 20,
      respect_robots: respectRobotsEl.checked,
      user_agent: userAgentEl.value,
      cookie: cookieEl.value,
      basic_auth_user: basicUserEl.value,
      basic_auth_pass: basicPassEl.value,
      link_scope: linkScopeEl.value,
      allowed_domains: parseLines(allowedDomainsEl.value),
      blocked_domains: parseLines(blockedDomainsEl.value),
      follow_indexed_domains: followIndexedEl.checked,
      use_sitemap: useSitemapEl.checked,
      prioritize_unindexed: prioritizeUnindexedEl.checked,
      fetch_timeout_seconds: parseInt(fetchTimeoutEl.value, 10) || 0,
      min_text_length: parseInt(minTextLengthEl.value, 10) || 0,
      crawl_delay_ms: parseInt(delayEl.value, 10) || 0,
      max_response_kb: parseInt(maxResponseEl.value, 10) || 0,
      interval_minutes: parseInt(intervalEl.value, 10) || 0,
      max_runs: parseInt(maxRunsEl.value, 10) || 0,
      renderer: rendererEl.value,
      enabled: enabledEl.checked,
    };
  }

  async function load() {
    try {
      const s = await getJSON('/admin/api/schedules/' + encodeURIComponent(id));
      applySchedule(s);
    } catch (err) {
      titleEl.textContent = 'Not found';
      statusEl.textContent = 'Could not load this crawl: ' + err.message;
    }
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    setButtonLoading(saveBtn, true, 'Saving…');
    statusEl.textContent = '';
    try {
      await patchJSON('/admin/api/schedules/' + encodeURIComponent(id), requestBody());
      statusEl.textContent = 'Saved.';
      await load();
    } catch (err) {
      statusEl.textContent = 'Could not save: ' + err.message;
    } finally {
      setButtonLoading(saveBtn, false);
    }
  });

  runNowBtn.addEventListener('click', async () => {
    setButtonLoading(runNowBtn, true, 'Starting…');
    statusEl.textContent = '';
    try {
      await postJSON('/admin/api/schedules/' + encodeURIComponent(id) + '/run', {});
      statusEl.textContent = 'Queued — starting within a few seconds. See Jobs on the Crawl page.';
      await load();
    } catch (err) {
      statusEl.textContent = 'Could not start: ' + err.message;
    } finally {
      setButtonLoading(runNowBtn, false);
    }
  });

  deleteBtn.addEventListener('click', async () => {
    if (!window.confirm('Delete this crawl schedule?')) return;
    deleteBtn.disabled = true;
    try {
      await deleteRequest('/admin/api/schedules/' + encodeURIComponent(id));
      window.location.href = '/admin/crawl';
    } catch (err) {
      deleteBtn.disabled = false;
      window.alert('Could not delete: ' + err.message);
    }
  });

  wireSignOut();
  load();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See admin_schedule.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { applySchedule, requestBody, load };
  }
