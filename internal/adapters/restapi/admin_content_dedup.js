  const recomputeSummaryEl = document.getElementById('dedup-recompute-summary');
  const recomputeBtn = document.getElementById('dedup-recompute-btn');
  const recomputeStatusEl = document.getElementById('dedup-recompute-status');
  const recomputeResultEl = document.getElementById('dedup-recompute-result');
  const groupsSummaryEl = document.getElementById('dedup-groups-summary');
  const groupsTableEl = document.getElementById('dedup-groups-table');
  const groupsPagerEl = document.getElementById('dedup-groups-pager');

  // pollTimer keeps polling while a recompute is in progress, triggered by ANY process
  // (cmd/crawl's ticker, another admin instance's click, etc.), not just this tab -- mirrors
  // admin_embeddings.js's pollTimer so this page reflects real cross-process state.
  let pollTimer = null;

  function renderRecomputeStatus(s) {
    setButtonLoading(recomputeBtn, s.in_progress, 'Recomputing…');
    if (s.in_progress) {
      clear(recomputeSummaryEl);
      recomputeStatusEl.textContent = 'Recomputing…';
      clear(recomputeResultEl);
    } else if (!s.last_run_at) {
      clear(recomputeSummaryEl);
      recomputeStatusEl.textContent = 'No recompute has run yet on this instance.';
      clear(recomputeResultEl);
    } else {
      clear(recomputeSummaryEl);
      recomputeStatusEl.textContent = 'Last run: ' + new Date(s.last_run_at).toLocaleString();
      clear(recomputeResultEl);
      kvRow(recomputeResultEl, 'Groups merged', String(s.groups_found));
      kvRow(recomputeResultEl, 'Documents removed', String(s.documents_merged));
      kvRow(recomputeResultEl, 'Duration', s.duration_ms + ' ms');
    }

    pollTimer = pollWhileInProgress(s.in_progress, pollTimer, () => { pollTimer = null; loadRecomputeStatus(); });
  }

  async function loadRecomputeStatus() {
    try {
      renderRecomputeStatus(await getJSON('/admin/api/content-dedup'));
    } catch (err) {
      recomputeStatusEl.textContent = 'Could not load recompute status: ' + err.message;
    }
  }

  // The recompute runs server-side in the background -- this click only starts it and switches
  // to polling loadRecomputeStatus, since a corpus-wide scan can be a real, long-running job.
  recomputeBtn.addEventListener('click', async () => {
    setButtonLoading(recomputeBtn, true, 'Recomputing…');
    recomputeStatusEl.textContent = 'Starting…';
    clear(recomputeResultEl);
    try {
      await postJSON('/admin/api/content-dedup/recompute', {});
      await loadRecomputeStatus();
      await loadGroups();
    } catch (err) {
      recomputeStatusEl.textContent = 'Could not start recompute: ' + err.message;
      setButtonLoading(recomputeBtn, false);
    }
  });

  const GROUPS_PAGE_SIZE = 20;
  let groupsPage = 0;

  // Reason labels match domain.DocumentAliasReason* -- canonical_tag is ordinary crawl-time
  // bookkeeping (nothing deleted); content_exact/content_simhash are real dedup merges (a
  // document row WAS deleted). Shown per alias, since one canonical document can accumulate several.
  const ALIAS_REASON_LABELS = {
    canonical_tag: 'canonical tag',
    content_exact: 'exact-content merge',
    content_simhash: 'near-duplicate merge',
  };

  function formatAlias(a) {
    return a.url + ' (' + (ALIAS_REASON_LABELS[a.reason] || a.reason) + ')';
  }

  function buildGroupsTable(groups) {
    return buildTable(
      [{ label: 'canonical URL' }, { label: 'aliases' }],
      groups,
      (g) => [urlCell(g.canonical_url), urlCell(g.aliases.map(formatAlias).join(', '))],
    );
  }

  function renderGroupsPager(total) {
    const totalPages = Math.max(1, Math.ceil(total / GROUPS_PAGE_SIZE));
    groupsPagerEl.hidden = totalPages <= 1;
    const info = document.getElementById('dedup-groups-page-info');
    if (info) info.textContent = 'Page ' + (groupsPage + 1) + ' of ' + totalPages;
    const prev = document.getElementById('dedup-groups-prev');
    const next = document.getElementById('dedup-groups-next');
    if (prev) prev.disabled = groupsPage <= 0;
    if (next) next.disabled = groupsPage + 1 >= totalPages;
  }

  async function loadGroups() {
    try {
      const params = new URLSearchParams({
        limit: String(GROUPS_PAGE_SIZE),
        offset: String(groupsPage * GROUPS_PAGE_SIZE),
      });
      const resp = await getJSON('/admin/api/content-dedup/alias-groups?' + params.toString());
      clear(groupsSummaryEl);
      kvRow(groupsSummaryEl, 'Merged groups', String(resp.total));
      clear(groupsTableEl);
      if (resp.groups.length === 0) {
        groupsTableEl.textContent = resp.total === 0 ? 'No documents have been merged yet.' : 'No groups on this page.';
        groupsPagerEl.hidden = true;
        return;
      }
      groupsTableEl.appendChild(buildGroupsTable(resp.groups));
      renderGroupsPager(resp.total);
    } catch (err) {
      clear(groupsTableEl);
      groupsSummaryEl.textContent = 'Could not load merged documents: ' + err.message;
      groupsPagerEl.hidden = true;
    }
  }

  document.getElementById('dedup-groups-prev').addEventListener('click', () => {
    if (groupsPage > 0) { groupsPage--; loadGroups(); }
  });
  document.getElementById('dedup-groups-next').addEventListener('click', () => {
    groupsPage++;
    loadGroups();
  });

  renderAdminNav();
  wireSignOut();
  loadRecomputeStatus();
  loadGroups();

  // Node test-runner export only; no-op in a browser <script> tag.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      renderRecomputeStatus, loadRecomputeStatus, buildGroupsTable, renderGroupsPager, loadGroups,
    };
  }
