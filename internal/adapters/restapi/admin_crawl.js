  const crawlForm = document.getElementById('crawl-form');
  const crawlURL = document.getElementById('crawl-url');
  const maxPages = document.getElementById('max-pages');
  const crawlCookie = document.getElementById('crawl-cookie');
  const crawlBasicUser = document.getElementById('crawl-basic-user');
  const crawlBasicPass = document.getElementById('crawl-basic-pass');
  const crawlRespectRobots = document.getElementById('crawl-respect-robots');
  const crawlLinkScope = document.getElementById('crawl-link-scope');
  const crawlAllowedDomains = document.getElementById('crawl-allowed-domains');
  const crawlBlockedDomains = document.getElementById('crawl-blocked-domains');
  const crawlFollowIndexed = document.getElementById('crawl-follow-indexed');
  const crawlUseSitemap = document.getElementById('crawl-use-sitemap');
  const crawlPrioritizeUnindexed = document.getElementById('crawl-prioritize-unindexed');
  const crawlInterval = document.getElementById('crawl-interval');
  const crawlMaxRuns = document.getElementById('crawl-max-runs');
  const crawlRenderer = document.getElementById('crawl-renderer');
  const crawlUserAgent = document.getElementById('crawl-user-agent');
  const crawlFetchTimeout = document.getElementById('crawl-fetch-timeout');
  const crawlMinTextLength = document.getElementById('crawl-min-text-length');
  const crawlDelay = document.getElementById('crawl-delay');
  const crawlMaxResponse = document.getElementById('crawl-max-response');
  const crawlStatus = document.getElementById('crawl-status');
  const crawlSubmitBtn = crawlForm.querySelector('button[type="submit"]');

  const prefillURL = new URLSearchParams(window.location.search).get('url');
  if (prefillURL) {
    crawlURL.value = prefillURL;
    crawlURL.focus();
  }
  crawlURL.addEventListener('blur', () => {
    if (crawlURL.value.trim() !== '') crawlURL.value = normalizeURL(crawlURL.value);
  });

  // The Basic auth fields start readonly and only become editable on
  // focus -- a browser won't offer to autofill a saved login into a field
  // that's readonly when the page loads, which "autocomplete=off" alone
  // no longer reliably stops in most browsers. This is a one-time crawl
  // credential, not a saved login, so there's nothing to fill in anyway.
  for (const el of [crawlBasicUser, crawlBasicPass]) {
    el.addEventListener('focus', () => el.removeAttribute('readonly'), { once: true });
  }

  // The submit button's label says exactly what it's about to do -- a
  // one-off crawl (interval left blank/0) reads "Crawl", a repeating one
  // (positive interval) reads "Schedule".
  function updateSubmitLabel() {
    crawlSubmitBtn.textContent = (parseInt(crawlInterval.value, 10) || 0) > 0 ? 'Schedule' : 'Crawl';
  }
  crawlInterval.addEventListener('input', updateSubmitLabel);
  updateSubmitLabel();

  // Every crawl -- one-off or repeating -- is created the same way: a
  // crawl definition (POST /admin/api/schedules) with every option this
  // form carries. A one-off crawl (recurring unchecked) is due right away;
  // crawl-server's scheduler ticker picks it up within a few seconds and
  // creates the Job that now shows up on the separate Jobs page.
  crawlForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const url = normalizeURL(crawlURL.value);
    if (!url) {
      crawlStatus.textContent = 'Enter a URL to crawl.';
      return;
    }
    const pages = parseInt(maxPages.value, 10) || 20;
    const interval = parseInt(crawlInterval.value, 10) || 0;
    const recurring = interval > 0;
    const maxRuns = parseInt(crawlMaxRuns.value, 10) || 0;
    const startingLabel = recurring ? 'Scheduling…' : 'Starting…';
    crawlStatus.textContent = startingLabel;
    setButtonLoading(crawlSubmitBtn, true, startingLabel);
    try {
      await postJSON('/admin/api/schedules', {
        seed_urls: [url],
        max_pages: pages,
        cookie: crawlCookie.value,
        basic_auth_user: crawlBasicUser.value,
        basic_auth_pass: crawlBasicPass.value,
        respect_robots: crawlRespectRobots.checked,
        user_agent: crawlUserAgent.value,
        link_scope: crawlLinkScope.value,
        allowed_domains: parseLines(crawlAllowedDomains.value),
        blocked_domains: parseLines(crawlBlockedDomains.value),
        follow_indexed_domains: crawlFollowIndexed.checked,
        use_sitemap: crawlUseSitemap.checked,
        prioritize_unindexed: crawlPrioritizeUnindexed.checked,
        fetch_timeout_seconds: parseInt(crawlFetchTimeout.value, 10) || 0,
        min_text_length: parseInt(crawlMinTextLength.value, 10) || 0,
        crawl_delay_ms: parseInt(crawlDelay.value, 10) || 0,
        max_response_kb: parseInt(crawlMaxResponse.value, 10) || 0,
        interval_minutes: interval,
        max_runs: maxRuns,
        renderer: crawlRenderer.value,
      });
      crawlStatus.textContent = recurring
        ? 'Scheduled — repeats every ' + interval + ' minutes' + (maxRuns > 0 ? ' (up to ' + maxRuns + ' times)' : '') + '. See it under Schedules on the Jobs page.'
        : 'Queued — starting within a few seconds. See it on the Jobs page.';
    } catch (err) {
      crawlStatus.textContent = 'Could not start: ' + err.message;
    } finally {
      setButtonLoading(crawlSubmitBtn, false);
    }
  });

  // showCurrentDefaults fills each per-crawl override's placeholder with
  // the actual value it would fall back to (rather than a generic "global
  // setting" string), so leaving a field blank has a visible, concrete
  // meaning. Failure here is silent -- the generic placeholder text
  // already in the HTML is a perfectly fine fallback.
  async function showCurrentDefaults() {
    try {
      const s = await getJSON('/admin/api/settings');
      const op = s.operational;
      crawlUserAgent.placeholder = 'Default: ' + op.user_agent;
      crawlFetchTimeout.placeholder = 'Default: ' + op.fetch_timeout_seconds + 's';
      crawlMinTextLength.placeholder = 'Default: ' + op.min_text_length;
      crawlDelay.placeholder = 'Default: ' + op.crawl_delay_ms + 'ms';
      crawlMaxResponse.placeholder = 'Default: ' + op.max_response_kb + 'KB';
    } catch (err) {
      // Leave the generic placeholders in place.
    }
  }

  wireSignOut();
  showCurrentDefaults();

  // Exports for the Node test runner only -- `typeof module` is undefined
  // in a browser's <script> tag, so this is a no-op there. See
  // internal/adapters/restapi/admin_crawl.test.js. Requiring this file still
  // runs the bootstrap calls just above (same as loading the real page
  // would) -- the test file's fetch mock has to tolerate that.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      updateSubmitLabel,
    };
  }
