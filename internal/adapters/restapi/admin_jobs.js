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

  const ACTIVE_STATUSES = new Set(['queued', 'running']);
  const JOB_DETAIL_PAGE_SIZE = 50;
  let selectedJobID = null;
  let pollTimer = null;
  let jobDetailPages = [];
  let jobDetailFilterText = '';
  let jobDetailPageNum = 1;
  // jobDetailSortBy/Dir default to newest-first, the most useful view right after/during a crawl
  // -- reset on selecting a different job (loadJobDetail), preserved across a poll refresh or
  // page-turn.
  let jobDetailSortBy = 'fetched_at';
  let jobDetailSortDir = 'desc';
  let allJobs = [];
  let jobsFilterText = '';
  let allCrawls = [];
  let crawlsFilterText = '';
  // awaitingNewJobUntil keeps loadJobs polling for a short window after the Crawl page creates a
  // new schedule, since the Job itself is only created once the scheduler ticker
  // (TriggerDueCrawls) next runs, not synchronously. This page has no crawl-form; it just polls
  // for a window after arriving, driven by whatever's currently active.
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

  // jobSpeedSamples/Rates: speed is the delta between two consecutive polls, not
  // pages_crawled/started_at -- that formula breaks after a crawl-server restart (started_at
  // resets via MarkRunning but pages_crawled is a lifetime counter), inflating the rate wildly.
  // `now` is a parameter so tests can drive it deterministically.
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

  // formatSpeed reports the last computed poll-to-poll rate for job.id -- '—' until at least two
  // polls have seen this job.
  function formatSpeed(job) {
    const rate = jobSpeedRates.get(job.id);
    if (rate === undefined) return '—';
    return rate.toFixed(2) + '/s';
  }

  async function clearEndedJobs() {
    setButtonLoading(clearEndedJobsBtn, true, 'Clearing…');
    try {
      const resp = await deleteRequest('/admin/api/crawl/jobs');
      // loadJobs (via renderJobs) sets jobsStatusEl itself -- this message must be set after it
      // returns, or renderJobs overwrites it immediately (same ordering as runScheduleNow).
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
    setIconLabel(viewBtn, 'view', 'View');
    viewBtn.addEventListener('click', () => loadJobDetail(job.id));
    td.appendChild(viewBtn);
    if (ACTIVE_STATUSES.has(job.status)) {
      const cancelBtn = document.createElement('button');
      cancelBtn.type = 'button';
      cancelBtn.className = 'text-button';
      setIconLabel(cancelBtn, 'delete', 'Cancel');
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
    return regexFilter(jobs, pattern, jobsFilterErrorEl,
      (re, j) => re.test(seedSummary(j.request.seed_urls)) || re.test(j.status));
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
      if (selectedJobID && jobs.some((j) => j.id === selectedJobID && ACTIVE_STATUSES.has(j.status))) {
        loadJobDetail(selectedJobID);
      }
      if (jobs.some((j) => ACTIVE_STATUSES.has(j.status)) || Date.now() < awaitingNewJobUntil) {
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
    return regexFilter(pages, pattern, jobDetailFilterErrorEl,
      (re, p) => re.test(p.url) || re.test(p.status) || re.test(p.title || '') || re.test(p.error || ''));
  }

  // JOB_DETAIL_COLUMNS drives both the sortable headers and the comparator below -- this table's
  // data is already fully loaded client-side, so sorting is a plain in-memory sort, never a
  // server round-trip (unlike admin.js's server-paginated vocabulary table).
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

  // jobDetailDefaultDir picks a first-click direction: text columns start ascending,
  // numeric/recency columns start descending.
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

  // buildJobDetailTable mirrors admin.js's buildVocabTable click-to-sort pattern, but re-renders
  // locally instead of refetching.
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
      let sortSuffix = '';
      if (active) {
        sortSuffix = jobDetailSortDir === 'asc' ? ' ▲' : ' ▼';
      }
      th.textContent = col.label + sortSuffix;
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
      tr.appendChild(textCell(capitalize(p.status).replaceAll('_', ' ')));
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
    return regexFilter(crawls, pattern, crawlsFilterErrorEl, (re, s) => re.test(seedSummary(s.seed_urls)));
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
    setIconLabel(runBtn, 'run', 'Run now');
    runBtn.addEventListener('click', () => runScheduleNow(s, runBtn));
    td.appendChild(runBtn);
    const editLink = document.createElement('a');
    editLink.className = 'text-button';
    editLink.href = '/admin/schedule/' + encodeURIComponent(s.id);
    setIconLabel(editLink, 'edit', 'Edit');
    td.appendChild(editLink);
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'text-button';
    setIconLabel(btn, 'delete', 'Delete');
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
      [{ label: 'enabled' }, { label: 'seed' }, { label: 'next run' }, { label: 'last run' }, { label: '' }],
      filtered,
      (s) => [
        crawlEnabledCell(s),
        urlCell(seedSummary(s.seed_urls)),
        textCell(formatTimestamp(s.next_run_at)),
        textCell(formatTimestamp(s.last_run_at)),
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

  // Uses the dedicated toggle endpoint, not the full-schedule PATCH -- that one always
  // reschedules (next_run_at = interval from now), reordering this list and pushing a resumed
  // crawl's next run out further than a plain pause/resume implies.
  async function toggleCrawlEnabled(s) {
    try {
      await postJSON('/admin/api/schedules/' + encodeURIComponent(s.id) + '/toggle', { enabled: !s.enabled });
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

  // --- Document uploads (see admin_document_upload.js -- a Document job is
  // a single file, not a many-page crawl, so it gets its own small table
  // here rather than being forced into the crawl-jobs table's
  // seed/pages/speed columns, which don't apply to it.) ---
  const documentJobsStatusEl = document.getElementById('document-jobs-status');
  const documentJobsTableEl = document.getElementById('document-jobs-table');

  function documentJobViewCell(job) {
    const td = document.createElement('td');
    td.className = 'actions';
    const viewLink = document.createElement('a');
    viewLink.className = 'text-button';
    viewLink.href = '/admin/document-upload/' + encodeURIComponent(job.id);
    setIconLabel(viewLink, 'view', 'View');
    td.appendChild(viewLink);
    return td;
  }

  function renderDocumentJobs(jobs) {
    clear(documentJobsTableEl);
    if (jobs.length === 0) {
      documentJobsStatusEl.textContent = 'No documents uploaded yet.';
      return;
    }
    documentJobsStatusEl.textContent = '';
    const table = buildTable(
      [{ label: 'filename' }, { label: 'content type' }, { label: 'status' }, { label: 'created' }, { label: '' }],
      jobs,
      (job) => [
        textCell(job.filename),
        textCell(job.content_type),
        textCell(capitalize(job.status)),
        textCell(formatTimestamp(job.created_at)),
        documentJobViewCell(job),
      ],
    );
    documentJobsTableEl.appendChild(table);
  }

  async function loadDocumentJobs() {
    try {
      renderDocumentJobs(await getJSON('/admin/api/document-jobs'));
    } catch (err) {
      documentJobsStatusEl.textContent = 'Could not load document uploads: ' + err.message;
    }
  }

  renderAdminNav();
  wireSignOut();
  loadCrawls();
  loadJobs();
  loadDocumentJobs();

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      capitalize, formatDuration, formatSpeed, updateJobSpeeds, formatMs,
      clearEndedJobs,
      viewButtonCell, crawlActionsCell, renderCrawls,
      filterJobs, filterPages, filterCrawls,
      toggleCrawlEnabled,
      LINK_SCOPE_LABELS, RENDERER_LABELS,
      JOB_DETAIL_COLUMNS, jobDetailDefaultDir, jobDetailSortValue,
      sortJobDetailPages, buildJobDetailTable, renderJobDetailTable,
      loadJobDetail,
      documentJobViewCell, renderDocumentJobs, loadDocumentJobs,
    };
  }
})();
