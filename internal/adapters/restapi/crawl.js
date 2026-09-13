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
  const crawlsStatusEl = document.getElementById('crawls-status');
  const crawlsFilterEl = document.getElementById('crawls-filter');
  const crawlsFilterErrorEl = document.getElementById('crawls-filter-error');
  const crawlsTableEl = document.getElementById('crawls-table');
  const jobsStatusEl = document.getElementById('jobs-status');
  const jobsFilterEl = document.getElementById('jobs-filter');
  const jobsFilterErrorEl = document.getElementById('jobs-filter-error');
  const jobsTableEl = document.getElementById('jobs-table');
  const jobDetailHeading = document.getElementById('job-detail-heading');
  const jobDetailSummaryEl = document.getElementById('job-detail-summary');
  const jobDetailOptionsEl = document.getElementById('job-detail-options');
  const jobDetailFilterEl = document.getElementById('job-detail-filter');
  const jobDetailFilterErrorEl = document.getElementById('job-detail-filter-error');
  const jobDetailTableEl = document.getElementById('job-detail-table');
  const jobDetailPagerEl = document.getElementById('job-detail-pager');
  const jobDetailPrevBtn = document.getElementById('job-detail-prev');
  const jobDetailNextBtn = document.getElementById('job-detail-next');
  const jobDetailPageInfoEl = document.getElementById('job-detail-page-info');

  const ACTIVE_STATUSES = ['queued', 'running'];
  const JOB_DETAIL_PAGE_SIZE = 50;
  let selectedJobID = null;
  let pollTimer = null;
  let jobDetailPages = [];
  let jobDetailFilterText = '';
  let jobDetailPageNum = 1;
  let allJobs = [];
  let jobsFilterText = '';
  let allCrawls = [];
  let crawlsFilterText = '';
  // awaitingNewJobUntil keeps loadJobs polling for a short window right
  // after creating a one-off crawl, even though no job exists yet -- it's
  // only created once crawl-server's scheduler ticker (application.
  // TriggerDueCrawls) next runs, not synchronously with this page's
  // request. Without this, loadJobs' own poll-only-while-something-is-
  // active logic would never notice the new job appear.
  let awaitingNewJobUntil = 0;

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

  function capitalize(s) {
    return s.charAt(0).toUpperCase() + s.slice(1);
  }

  function formatDuration(startedAt, finishedAt) {
    if (!startedAt) return '—';
    const start = new Date(startedAt).getTime();
    const end = finishedAt ? new Date(finishedAt).getTime() : Date.now();
    const secs = Math.max(0, (end - start) / 1000);
    if (secs < 60) return secs.toFixed(1) + 's';
    return Math.floor(secs / 60) + 'm ' + Math.round(secs % 60) + 's';
  }

  function viewButtonCell(job) {
    const td = document.createElement('td');
    td.className = 'actions';
    const viewBtn = document.createElement('button');
    viewBtn.type = 'button';
    viewBtn.className = 'text-button';
    viewBtn.textContent = 'View';
    viewBtn.addEventListener('click', () => loadJobDetail(job.id));
    td.appendChild(viewBtn);
    if (ACTIVE_STATUSES.includes(job.status)) {
      const cancelBtn = document.createElement('button');
      cancelBtn.type = 'button';
      cancelBtn.className = 'text-button';
      cancelBtn.textContent = 'Cancel';
      cancelBtn.addEventListener('click', () => cancelJob(job.id, cancelBtn));
      td.appendChild(cancelBtn);
    }
    return td;
  }

  async function cancelJob(jobID, btn) {
    setButtonLoading(btn, true, 'Cancelling…');
    try {
      await postJSON('/admin/api/crawl/jobs/' + encodeURIComponent(jobID) + '/cancel', {});
      await loadJobs();
    } catch (err) {
      window.alert('Could not cancel: ' + err.message);
    } finally {
      setButtonLoading(btn, false);
    }
  }

  // filterJobs applies the admin's regex (matched against the seed URLs
  // summary and status, case-insensitive) client-side -- the API already
  // returned every retained job, so filtering here just narrows what's
  // displayed. An invalid pattern shows a message instead of silently
  // discarding it, same convention as the job-detail page filter below.
  function filterJobs(jobs, pattern) {
    if (!pattern) {
      jobsFilterErrorEl.textContent = '';
      return jobs;
    }
    try {
      const re = new RegExp(pattern, 'i');
      jobsFilterErrorEl.textContent = '';
      return jobs.filter((j) => re.test(seedSummary(j.request.seed_urls)) || re.test(j.status));
    } catch (err) {
      jobsFilterErrorEl.textContent = 'Invalid regex: ' + err.message;
      return jobs;
    }
  }

  function renderJobs(jobs) {
    jobsFilterEl.hidden = jobs.length === 0;
    const filtered = filterJobs(jobs, jobsFilterText);
    clear(jobsTableEl);
    if (jobs.length === 0) {
      jobsStatusEl.textContent = 'No crawls triggered yet.';
      return;
    }
    if (filtered.length === 0) {
      jobsStatusEl.textContent = 'No jobs match that filter.';
      return;
    }
    jobsStatusEl.textContent = '';
    const table = buildTable(
      [{ label: 'seed' }, { label: 'status' }, { label: 'pages', num: true }, { label: 'duration' }, { label: '' }],
      filtered,
      (j) => [
        urlCell(seedSummary(j.request.seed_urls)),
        textCell(capitalize(j.status)),
        textCell(String(j.pages_crawled), { num: true }),
        textCell(formatDuration(j.started_at, j.finished_at)),
        viewButtonCell(j),
      ],
    );
    jobsTableEl.appendChild(table);
  }

  jobsFilterEl.addEventListener('input', () => {
    jobsFilterText = jobsFilterEl.value.trim();
    renderJobs(allJobs);
  });

  async function loadJobs() {
    try {
      const jobs = await getJSON('/admin/api/crawl/jobs');
      allJobs = jobs;
      renderJobs(jobs);
      if (selectedJobID && jobs.some((j) => j.id === selectedJobID && ACTIVE_STATUSES.includes(j.status))) {
        loadJobDetail(selectedJobID);
      }
      if (jobs.some((j) => ACTIVE_STATUSES.includes(j.status)) || Date.now() < awaitingNewJobUntil) {
        scheduleNextPoll();
      }
    } catch (err) {
      jobsStatusEl.textContent = 'Could not load crawl jobs: ' + err.message;
    }
  }

  function scheduleNextPoll() {
    if (pollTimer) return;
    pollTimer = setTimeout(() => {
      pollTimer = null;
      loadJobs();
    }, 1500);
  }

  // linesToText/parseLines now live in admin.js, shared with every other
  // page that has a one-value-per-line <textarea> field.

  const LINK_SCOPE_LABELS = { '': 'Site default', host: 'Exact seed host only', domain: 'Seed domain + subdomains', tld: 'Same domain, any subdomain, any TLD', any: 'Any domain' };
  const RENDERER_LABELS = { '': 'Site default', none: 'None (plain HTTP)', chromium: 'Chromium', firefox: 'Firefox' };

  // renderRequestOptions shows every option this job actually ran with --
  // the full domain.CrawlJobRequest, not just a hand-picked subset -- so
  // "what exactly did this crawl do" is always answerable from the job
  // detail view alone, without having to go find (or guess at) the
  // schedule that originally triggered it.
  function renderRequestOptions(container, request) {
    clear(container);
    kvRow(container, 'Seed URLs', (request.seed_urls || []).join(', '));
    kvRow(container, 'Max pages', String(request.max_pages));
    kvRow(container, 'Respect robots.txt', request.respect_robots ? 'Yes' : 'No');
    kvRow(container, 'User-Agent', request.user_agent || 'Default');
    kvRow(container, 'Link scope', LINK_SCOPE_LABELS[request.link_scope || ''] || request.link_scope);
    kvRow(container, 'Allowed domains', (request.allowed_domains || []).join(', ') || 'None');
    kvRow(container, 'Blocked domains', (request.blocked_domains || []).join(', ') || 'None');
    kvRow(container, 'Follow indexed domains', request.follow_indexed_domains ? 'Yes' : 'No');
    kvRow(container, 'Renderer', RENDERER_LABELS[request.renderer || ''] || request.renderer);
    kvRow(container, 'Discover via sitemap.xml', request.use_sitemap ? 'Yes' : 'No');
    kvRow(container, 'Prioritize unindexed pages', request.prioritize_unindexed ? 'Yes' : 'No');
    kvRow(container, 'Fetch timeout', request.fetch_timeout_seconds ? request.fetch_timeout_seconds + 's' : 'Default');
    kvRow(container, 'Minimum text length', request.min_text_length ? String(request.min_text_length) : 'Default');
    kvRow(container, 'Crawl delay', request.crawl_delay_ms ? request.crawl_delay_ms + 'ms' : 'Default');
    kvRow(container, 'Max response size', request.max_response_kb ? request.max_response_kb + 'KB' : 'Default');
    kvRow(container, 'Cookie header', request.has_cookie ? 'Set' : 'Not set');
    kvRow(container, 'Basic auth', request.has_basic_auth ? 'Set' : 'Not set');
  }

  function formatMs(ms) {
    if (!ms) return '—';
    return ms < 1000 ? ms + 'ms' : (ms / 1000).toFixed(1) + 's';
  }

  // filterPages applies the admin's regex (matched against url/status/title/
  // detail, case-insensitive) client-side -- the API already returned every
  // page for this job, so filtering here just narrows what's displayed.
  // An invalid pattern shows a message instead of silently discarding it.
  function filterPages(pages, pattern) {
    if (!pattern) {
      jobDetailFilterErrorEl.textContent = '';
      return pages;
    }
    try {
      const re = new RegExp(pattern, 'i');
      jobDetailFilterErrorEl.textContent = '';
      return pages.filter((p) =>
        re.test(p.url) || re.test(p.status) || re.test(p.title || '') || re.test(p.error || ''));
    } catch (err) {
      jobDetailFilterErrorEl.textContent = 'Invalid regex: ' + err.message;
      return pages;
    }
  }

  // renderJobDetailTable re-derives the visible page from jobDetailPages
  // every time -- never renders more than JOB_DETAIL_PAGE_SIZE rows at
  // once, however large the underlying crawl was.
  function renderJobDetailTable() {
    const filtered = filterPages(jobDetailPages, jobDetailFilterText);
    const totalPages = Math.max(1, Math.ceil(filtered.length / JOB_DETAIL_PAGE_SIZE));
    jobDetailPageNum = Math.min(Math.max(1, jobDetailPageNum), totalPages);
    const start = (jobDetailPageNum - 1) * JOB_DETAIL_PAGE_SIZE;
    const pageItems = filtered.slice(start, start + JOB_DETAIL_PAGE_SIZE);

    clear(jobDetailTableEl);
    if (pageItems.length === 0) {
      jobDetailTableEl.textContent = jobDetailFilterText ? 'No pages match that filter.' : 'No pages attempted yet.';
      jobDetailPagerEl.hidden = true;
      return;
    }
    const table = buildTable(
      [
        { label: 'url' }, { label: 'status' }, { label: 'title' },
        { label: 'length', num: true }, { label: 'links', num: true },
        { label: 'duration', num: true }, { label: 'fetched at' }, { label: 'detail' },
      ],
      pageItems,
      (p) => [
        urlCell(p.url),
        textCell(capitalize(p.status).replace(/_/g, ' ')),
        textCell(p.title || ''),
        textCell(p.doc_length ? String(p.doc_length) : '—', { num: true }),
        textCell(p.links_found ? String(p.links_found) : '—', { num: true }),
        textCell(formatMs(p.duration_ms), { num: true }),
        textCell(formatTimestamp(p.fetched_at, { timeOnly: true })),
        textCell(p.error || ''),
      ],
    );
    jobDetailTableEl.appendChild(table);

    jobDetailPagerEl.hidden = totalPages <= 1;
    jobDetailPageInfoEl.textContent = 'Page ' + jobDetailPageNum + ' of ' + totalPages +
      ' (' + filtered.length + (filtered.length === 1 ? ' page)' : ' pages)');
    jobDetailPrevBtn.disabled = jobDetailPageNum <= 1;
    jobDetailNextBtn.disabled = jobDetailPageNum >= totalPages;
  }

  jobDetailFilterEl.addEventListener('input', () => {
    jobDetailFilterText = jobDetailFilterEl.value.trim();
    jobDetailPageNum = 1;
    renderJobDetailTable();
  });
  jobDetailPrevBtn.addEventListener('click', () => {
    jobDetailPageNum--;
    renderJobDetailTable();
  });
  jobDetailNextBtn.addEventListener('click', () => {
    jobDetailPageNum++;
    renderJobDetailTable();
  });

  function renderJobDetail(job) {
    jobDetailHeading.hidden = false;
    clear(jobDetailSummaryEl);
    const summary = document.createElement('p');
    summary.className = 'panel-status';
    summary.textContent = seedSummary(job.request.seed_urls) + ' — ' + capitalize(job.status) +
      (job.error ? ': ' + job.error : '');
    jobDetailSummaryEl.appendChild(summary);

    renderRequestOptions(jobDetailOptionsEl, job.request);

    jobDetailPages = job.pages || [];
    jobDetailFilterEl.hidden = jobDetailPages.length === 0;
    renderJobDetailTable();
  }

  async function loadJobDetail(jobID) {
    if (jobID !== selectedJobID) {
      jobDetailFilterText = '';
      jobDetailFilterEl.value = '';
      jobDetailPageNum = 1;
    }
    selectedJobID = jobID;
    try {
      const job = await getJSON('/admin/api/crawl/jobs/' + encodeURIComponent(jobID));
      renderJobDetail(job);
    } catch (err) {
      jobDetailHeading.hidden = false;
      clear(jobDetailSummaryEl);
      jobDetailSummaryEl.textContent = 'Could not load job: ' + err.message;
      clear(jobDetailOptionsEl);
      clear(jobDetailTableEl);
      jobDetailPagerEl.hidden = true;
      jobDetailFilterEl.hidden = true;
    }
  }

  // filterCrawls mirrors filterJobs/filterPages -- a case-insensitive regex
  // against the seed URLs, client-side over the already-fetched list.
  function filterCrawls(crawls, pattern) {
    if (!pattern) {
      crawlsFilterErrorEl.textContent = '';
      return crawls;
    }
    try {
      const re = new RegExp(pattern, 'i');
      crawlsFilterErrorEl.textContent = '';
      return crawls.filter((s) => re.test(seedSummary(s.seed_urls)));
    } catch (err) {
      crawlsFilterErrorEl.textContent = 'Invalid regex: ' + err.message;
      return crawls;
    }
  }

  function crawlRecurrenceCell(s) {
    if (!s.recurring) return textCell('once');
    let label = s.interval_minutes + ' min';
    if (s.max_runs > 0) label += ' (' + s.run_count + '/' + s.max_runs + ' runs)';
    return textCell(label);
  }

  function crawlLinkScopeCell(s) {
    return textCell(LINK_SCOPE_LABELS[s.link_scope || ''] || s.link_scope);
  }

  function crawlEnabledCell(s) {
    const td = document.createElement('td');
    const label = document.createElement('label');
    label.className = 'form-row-check';
    const checkbox = document.createElement('input');
    checkbox.type = 'checkbox';
    checkbox.checked = s.enabled;
    checkbox.addEventListener('change', () => toggleCrawlEnabled(s));
    label.appendChild(checkbox);
    td.appendChild(label);
    return td;
  }

  // runScheduleNow marks a schedule due immediately (POST .../run) -- the
  // actual CrawlJob is created by crawl-server's own scheduler ticker on
  // its next tick, same as a freshly submitted one-off crawl, so this just
  // gives the same "starting within a few seconds" feedback and starts the
  // Jobs table polling for it.
  async function runScheduleNow(s, btn) {
    setButtonLoading(btn, true, 'Starting…');
    try {
      await postJSON('/admin/api/schedules/' + encodeURIComponent(s.id) + '/run', {});
      awaitingNewJobUntil = Date.now() + 15000;
      // loadCrawls() re-renders #crawls-status itself (clearing it, or
      // reporting a filter/empty state) -- setting this feedback after
      // that reload, not before, is what keeps it from being immediately
      // overwritten.
      await loadCrawls();
      await loadJobs();
      crawlsStatusEl.textContent = 'Queued — starting within a few seconds.';
    } catch (err) {
      window.alert('Could not start: ' + err.message);
    } finally {
      setButtonLoading(btn, false);
    }
  }

  function crawlActionsCell(s) {
    const td = document.createElement('td');
    td.className = 'actions';
    const runBtn = document.createElement('button');
    runBtn.type = 'button';
    runBtn.className = 'text-button';
    runBtn.textContent = 'Run now';
    runBtn.addEventListener('click', () => runScheduleNow(s, runBtn));
    td.appendChild(runBtn);
    const editLink = document.createElement('a');
    editLink.className = 'text-button';
    editLink.href = '/admin/schedule/' + encodeURIComponent(s.id);
    editLink.textContent = 'Edit';
    td.appendChild(editLink);
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'text-button';
    btn.textContent = 'Delete';
    btn.addEventListener('click', () => deleteCrawl(s.id));
    td.appendChild(btn);
    return td;
  }

  function renderCrawls(crawls) {
    crawlsFilterEl.hidden = crawls.length === 0;
    const filtered = filterCrawls(crawls, crawlsFilterText);
    clear(crawlsTableEl);
    if (crawls.length === 0) {
      crawlsStatusEl.textContent = 'No crawls set up yet -- use the form above.';
      return;
    }
    if (filtered.length === 0) {
      crawlsStatusEl.textContent = 'No crawls match that filter.';
      return;
    }
    crawlsStatusEl.textContent = '';
    const table = buildTable(
      [{ label: 'seed' }, { label: 'repeats' }, { label: 'links' }, { label: 'next run' }, { label: 'last run' }, { label: 'enabled' }, { label: '' }],
      filtered,
      (s) => [
        urlCell(seedSummary(s.seed_urls)),
        crawlRecurrenceCell(s),
        crawlLinkScopeCell(s),
        textCell(formatTimestamp(s.next_run_at)),
        textCell(formatTimestamp(s.last_run_at)),
        crawlEnabledCell(s),
        crawlActionsCell(s),
      ],
    );
    crawlsTableEl.appendChild(table);
  }

  crawlsFilterEl.addEventListener('input', () => {
    crawlsFilterText = crawlsFilterEl.value.trim();
    renderCrawls(allCrawls);
  });

  async function loadCrawls() {
    try {
      allCrawls = await getJSON('/admin/api/schedules');
      renderCrawls(allCrawls);
    } catch (err) {
      crawlsStatusEl.textContent = 'Could not load crawls: ' + err.message;
    }
  }

  // requestBodyFor round-trips every field a crawl carries -- toggling
  // "enabled" (the only edit this page's table itself makes) must resend
  // the rest unchanged, credentials included, or PATCH's full-replace
  // semantics would silently blank them out.
  function requestBodyFor(s) {
    return {
      seed_urls: s.seed_urls, max_pages: s.max_pages,
      respect_robots: s.respect_robots, user_agent: s.user_agent,
      cookie: s.cookie, basic_auth_user: s.basic_auth_user, basic_auth_pass: s.basic_auth_pass,
      link_scope: s.link_scope, use_sitemap: s.use_sitemap,
      allowed_domains: s.allowed_domains, blocked_domains: s.blocked_domains,
      follow_indexed_domains: s.follow_indexed_domains,
      fetch_timeout_seconds: s.fetch_timeout_seconds, min_text_length: s.min_text_length,
      crawl_delay_ms: s.crawl_delay_ms, max_response_kb: s.max_response_kb,
      prioritize_unindexed: s.prioritize_unindexed,
      interval_minutes: s.interval_minutes, max_runs: s.max_runs,
      renderer: s.renderer,
      enabled: s.enabled,
    };
  }

  async function toggleCrawlEnabled(s) {
    try {
      await patchJSON('/admin/api/schedules/' + encodeURIComponent(s.id), requestBodyFor({ ...s, enabled: !s.enabled }));
      await loadCrawls();
    } catch (err) {
      window.alert('Could not update: ' + err.message);
    }
  }

  async function deleteCrawl(id) {
    try {
      await deleteRequest('/admin/api/schedules/' + encodeURIComponent(id));
      await loadCrawls();
    } catch (err) {
      window.alert('Could not delete: ' + err.message);
    }
  }

  // Every crawl -- one-off or repeating -- is created the same way: a
  // crawl definition (POST /admin/api/schedules) with every option this
  // form carries. A one-off crawl (recurring unchecked) is due right away;
  // crawl-server's scheduler ticker picks it up within a few seconds and
  // creates the Job this page's Jobs table then tracks -- there's no
  // separate "just run this now" path anymore.
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
        ? 'Scheduled — repeats every ' + interval + ' minutes' + (maxRuns > 0 ? ' (up to ' + maxRuns + ' times)' : '') + '.'
        : 'Queued — starting within a few seconds.';
      await loadCrawls();
      if (!recurring) {
        awaitingNewJobUntil = Date.now() + 15000;
      }
      await loadJobs();
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
  loadCrawls();
  loadJobs();
  showCurrentDefaults();

  // Exports for the Node test runner only -- `typeof module` is undefined
  // in a browser's <script> tag, so this is a no-op there. See
  // internal/adapters/restapi/crawl.test.js. Requiring this file still
  // runs the four bootstrap calls just above (same as loading the real
  // page would) -- the test file's fetch mock has to tolerate that.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      capitalize, formatDuration, formatMs,
      filterJobs, filterPages, filterCrawls,
      crawlRecurrenceCell, crawlLinkScopeCell,
      LINK_SCOPE_LABELS, RENDERER_LABELS,
    };
  }
