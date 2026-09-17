'use strict';

(function () {
  const crawlsStatusEl = document.getElementById('crawls-status');
  const crawlsFilterEl = document.getElementById('crawls-filter');
  const crawlsFilterErrorEl = document.getElementById('crawls-filter-error');
  const crawlsTableEl = document.getElementById('crawls-table');
  const clearEndedJobsBtn = document.getElementById('clear-ended-jobs');
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
  // jobDetailSortBy/jobDetailSortDir default to newest-fetched-first, the
  // most useful view right after a crawl finishes (or while it's still
  // running) -- reset back to this default whenever a different job is
  // selected (see loadJobDetail), but preserved across a poll refresh or
  // page-turn within the same job.
  let jobDetailSortBy = 'fetched_at';
  let jobDetailSortDir = 'desc';
  let allJobs = [];
  let jobsFilterText = '';
  let allCrawls = [];
  let crawlsFilterText = '';
  // awaitingNewJobUntil keeps loadJobs polling for a short window right
  // after a crawl on the Crawl page (/admin/crawl) creates a new schedule,
  // even though no job exists yet -- it's only created once crawl-server's
  // scheduler ticker (application.TriggerDueCrawls) next runs, not
  // synchronously with that page's request. This page has no crawl-form of
  // its own -- it just keeps polling for a natural window after the admin
  // arrives here (e.g. redirected here right after creating a crawl), same
  // idea, driven purely by whether anything is currently active.
  let awaitingNewJobUntil = 0;

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

  // jobSpeedSamples/jobSpeedRates: speed is the delta between two
  // consecutive polls of the jobs list, not pages_crawled/started_at --
  // that formula breaks the moment a job survives a crawl-server restart
  // (recovered jobs get a fresh started_at from MarkRunning, but
  // pages_crawled is a lifetime counter that doesn't reset), which
  // divides a lifetime page count by a tiny elapsed window and reports a
  // wildly inflated rate. `now` is a parameter (not a bare Date.now()
  // call) so tests can drive it deterministically.
  const jobSpeedSamples = new Map();
  const jobSpeedRates = new Map();

  function updateJobSpeeds(jobs, now) {
    if (now === undefined) now = Date.now();
    for (const job of jobs) {
      const prev = jobSpeedSamples.get(job.id);
      if (prev) {
        const deltaPages = job.pages_crawled - prev.pages;
        const deltaSecs = (now - prev.at) / 1000;
        if (deltaSecs > 0 && deltaPages >= 0) {
          jobSpeedRates.set(job.id, deltaPages / deltaSecs);
        }
      }
      jobSpeedSamples.set(job.id, { pages: job.pages_crawled, at: now });
    }
  }

  // formatSpeed reports the most recently computed poll-to-poll rate for
  // job.id -- '—' until at least two polls have seen this job (a brand
  // new job, or one just noticed for the first time this page load).
  function formatSpeed(job) {
    const rate = jobSpeedRates.get(job.id);
    if (rate === undefined) return '—';
    return rate.toFixed(2) + '/s';
  }

  async function clearEndedJobs() {
    setButtonLoading(clearEndedJobsBtn, true, 'Clearing…');
    try {
      const resp = await deleteRequest('/admin/api/crawl/jobs');
      // loadJobs (via renderJobs) sets jobsStatusEl's text itself based on
      // the refreshed list, so this message must be set after it returns,
      // not before, or renderJobs immediately overwrites it -- same
      // ordering runScheduleNow uses for the same reason.
      await loadJobs();
      jobsStatusEl.textContent = 'Cleared ' + resp.removed + (resp.removed === 1 ? ' ended job.' : ' ended jobs.');
    } catch (err) {
      window.alert('Could not clear ended jobs: ' + err.message);
    } finally {
      setButtonLoading(clearEndedJobsBtn, false);
    }
  }

  clearEndedJobsBtn.addEventListener('click', clearEndedJobs);

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
      [{ label: 'seed' }, { label: 'status' }, { label: 'pages', num: true }, { label: 'duration' }, { label: 'speed', num: true }, { label: '' }],
      filtered,
      (j) => [
        urlCell(seedSummary(j.request.seed_urls)),
        textCell(capitalize(j.status)),
        textCell(String(j.pages_crawled), { num: true }),
        textCell(formatDuration(j.started_at, j.finished_at)),
        textCell(formatSpeed(j), { num: true }),
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
      updateJobSpeeds(jobs);
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

  const LINK_SCOPE_LABELS = { '': 'Site default', host: 'Exact seed host only', domain: 'Seed domain + subdomains', tld: 'Same domain, any subdomain, any TLD', any: 'Any domain' };
  const RENDERER_LABELS = { '': 'Site default', none: 'None (plain HTTP)', chromium: 'Chromium', firefox: 'Firefox' };

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

  // JOB_DETAIL_COLUMNS drives both the sortable headers and the sort
  // comparator below -- this table's data is already fully loaded
  // client-side (one job's whole page list, see loadJobDetail), so
  // sorting is plain in-memory array sort, never a server round-trip
  // (unlike e.g. admin.js's vocabulary table, which is server-paginated).
  const JOB_DETAIL_COLUMNS = [
    { key: 'url', label: 'url' },
    { key: 'status', label: 'status' },
    { key: 'title', label: 'title' },
    { key: 'doc_length', label: 'length', num: true },
    { key: 'links_found', label: 'links', num: true },
    { key: 'duration_ms', label: 'duration', num: true },
    { key: 'fetched_at', label: 'fetched at' },
    { key: 'error', label: 'detail' },
  ];

  // jobDetailDefaultDir picks a sensible starting direction the first time
  // a column is clicked: text columns start ascending (alphabetical),
  // numeric/recency columns start descending (biggest/most-recent first).
  function jobDetailDefaultDir(key) {
    return (key === 'doc_length' || key === 'links_found' || key === 'duration_ms' || key === 'fetched_at') ? 'desc' : 'asc';
  }

  function jobDetailSortValue(p, key) {
    if (key === 'fetched_at') return p.fetched_at ? new Date(p.fetched_at).getTime() : 0;
    if (key === 'doc_length' || key === 'links_found' || key === 'duration_ms') return p[key] || 0;
    return String(p[key] || '').toLowerCase();
  }

  function sortJobDetailPages(pages, sortBy, sortDir) {
    const dir = sortDir === 'asc' ? 1 : -1;
    return [...pages].sort((a, b) => {
      const av = jobDetailSortValue(a, sortBy);
      const bv = jobDetailSortValue(b, sortBy);
      if (av < bv) return -dir;
      if (av > bv) return dir;
      return 0;
    });
  }

  // buildJobDetailTable mirrors admin.js's buildVocabTable click-to-sort
  // header pattern, but re-renders locally (renderJobDetailTable) instead
  // of refetching from the server.
  function buildJobDetailTable(pages) {
    const table = document.createElement('table');
    const thead = document.createElement('thead');
    const headRow = document.createElement('tr');
    for (const col of JOB_DETAIL_COLUMNS) {
      const th = document.createElement('th');
      if (col.num) th.className = 'num';
      th.classList.add('sortable-th');
      th.tabIndex = 0;
      const active = jobDetailSortBy === col.key;
      th.textContent = col.label + (active ? (jobDetailSortDir === 'asc' ? ' ▲' : ' ▼') : '');
      const activate = () => {
        if (jobDetailSortBy === col.key) {
          jobDetailSortDir = jobDetailSortDir === 'asc' ? 'desc' : 'asc';
        } else {
          jobDetailSortBy = col.key;
          jobDetailSortDir = jobDetailDefaultDir(col.key);
        }
        jobDetailPageNum = 1;
        renderJobDetailTable();
      };
      th.addEventListener('click', activate);
      th.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); activate(); }
      });
      headRow.appendChild(th);
    }
    thead.appendChild(headRow);
    table.appendChild(thead);

    const tbody = document.createElement('tbody');
    for (const p of pages) {
      const tr = document.createElement('tr');
      tr.appendChild(urlCell(p.url));
      tr.appendChild(textCell(capitalize(p.status).replace(/_/g, ' ')));
      tr.appendChild(textCell(p.title || ''));
      tr.appendChild(textCell(p.doc_length ? String(p.doc_length) : '—', { num: true }));
      tr.appendChild(textCell(p.links_found ? String(p.links_found) : '—', { num: true }));
      tr.appendChild(textCell(formatMs(p.duration_ms), { num: true }));
      tr.appendChild(textCell(formatTimestamp(p.fetched_at, { timeOnly: true })));
      tr.appendChild(textCell(p.error || ''));
      tbody.appendChild(tr);
    }
    table.appendChild(tbody);
    return table;
  }

  function renderJobDetailTable() {
    const sorted = sortJobDetailPages(filterPages(jobDetailPages, jobDetailFilterText), jobDetailSortBy, jobDetailSortDir);
    const totalPages = Math.max(1, Math.ceil(sorted.length / JOB_DETAIL_PAGE_SIZE));
    jobDetailPageNum = Math.min(Math.max(1, jobDetailPageNum), totalPages);
    const start = (jobDetailPageNum - 1) * JOB_DETAIL_PAGE_SIZE;
    const pageItems = sorted.slice(start, start + JOB_DETAIL_PAGE_SIZE);

    clear(jobDetailTableEl);
    if (pageItems.length === 0) {
      jobDetailTableEl.textContent = jobDetailFilterText ? 'No pages match that filter.' : 'No pages attempted yet.';
      jobDetailPagerEl.hidden = true;
      return;
    }
    const table = buildJobDetailTable(pageItems);
    jobDetailTableEl.appendChild(table);

    jobDetailPagerEl.hidden = totalPages <= 1;
    jobDetailPageInfoEl.textContent = 'Page ' + jobDetailPageNum + ' of ' + totalPages +
      ' (' + sorted.length + (sorted.length === 1 ? ' page)' : ' pages)');
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
      jobDetailSortBy = 'fetched_at';
      jobDetailSortDir = 'desc';
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

  async function runScheduleNow(s, btn) {
    setButtonLoading(btn, true, 'Starting…');
    try {
      await postJSON('/admin/api/schedules/' + encodeURIComponent(s.id) + '/run', {});
      awaitingNewJobUntil = Date.now() + 15000;
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
      crawlsStatusEl.textContent = 'No crawls set up yet -- use the Crawl page to create one.';
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

  renderAdminNav();
  wireSignOut();
  loadCrawls();
  loadJobs();

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      capitalize, formatDuration, formatSpeed, updateJobSpeeds, formatMs,
      clearEndedJobs,
      filterJobs, filterPages, filterCrawls,
      crawlRecurrenceCell, crawlLinkScopeCell,
      LINK_SCOPE_LABELS, RENDERER_LABELS,
      JOB_DETAIL_COLUMNS, jobDetailDefaultDir, jobDetailSortValue,
      sortJobDetailPages, buildJobDetailTable, renderJobDetailTable,
      loadJobDetail,
    };
  }
})();
