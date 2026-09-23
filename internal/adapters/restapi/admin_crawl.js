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

  // Basic-auth fields start readonly, editable only on focus -- browsers ignore "autocomplete=off"
  // but won't autofill a readonly field. This is a one-time crawl credential, not a saved login.
  for (const el of [crawlBasicUser, crawlBasicPass]) {
    el.addEventListener('focus', () => el.removeAttribute('readonly'), { once: true });
  }

  // The submit button's label matches the action: blank/0 interval reads "Crawl" (one-off), a
  // positive interval reads "Schedule".
  function updateSubmitLabel() {
    crawlSubmitBtn.textContent = (Number.parseInt(crawlInterval.value, 10) || 0) > 0 ? 'Schedule' : 'Crawl';
  }
  crawlInterval.addEventListener('input', updateSubmitLabel);
  updateSubmitLabel();

  // Every crawl, one-off or repeating, is created via POST /admin/api/schedules with this form's
  // options. A one-off crawl is due right away; the scheduler ticker picks it up within seconds
  // and creates the Job shown on the Jobs page.
  crawlForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const url = normalizeURL(crawlURL.value);
    if (!url) {
      crawlStatus.textContent = 'Enter a URL to crawl.';
      return;
    }
    const pages = Number.parseInt(maxPages.value, 10) || 20;
    const interval = Number.parseInt(crawlInterval.value, 10) || 0;
    const recurring = interval > 0;
    const maxRuns = Number.parseInt(crawlMaxRuns.value, 10) || 0;
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
        fetch_timeout_seconds: Number.parseInt(crawlFetchTimeout.value, 10) || 0,
        min_text_length: Number.parseInt(crawlMinTextLength.value, 10) || 0,
        crawl_delay_ms: Number.parseInt(crawlDelay.value, 10) || 0,
        max_response_kb: Number.parseInt(crawlMaxResponse.value, 10) || 0,
        interval_minutes: interval,
        max_runs: maxRuns,
        renderer: crawlRenderer.value,
      });
      if (recurring) {
        const runsSuffix = maxRuns > 0 ? ' (up to ' + maxRuns + ' times)' : '';
        crawlStatus.textContent = 'Scheduled — repeats every ' + interval + ' minutes' + runsSuffix + '. See it under Schedules on the Jobs page.';
      } else {
        crawlStatus.textContent = 'Queued — starting within a few seconds. See it on the Jobs page.';
      }
    } catch (err) {
      crawlStatus.textContent = 'Could not start: ' + err.message;
    } finally {
      setButtonLoading(crawlSubmitBtn, false);
    }
  });

  // showCurrentDefaults fills each override's placeholder with its real fallback value (not a
  // generic string), so a blank field has concrete meaning. Fails silently -- the HTML's generic
  // placeholder is a fine fallback.
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

  renderAdminNav();
  wireSignOut();
  showCurrentDefaults();

  // Node test-runner export only; no-op in a browser. Requiring this file still runs the bootstrap
  // calls above, as loading the real page would -- the test's fetch mock must tolerate that.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      updateSubmitLabel,
    };
  }
