  // DEBUG_TOP_K asks the debug endpoint for far more matches than the
  // public search page's own default_top_k operational setting would (that
  // setting governs end-user search UX, not this diagnostic tool) --
  // DEBUG_PAGE_SIZE then paginates the already-fetched results client-side,
  // the same one-fetch/paginate-locally pattern the Jobs detail table uses.
  const DEBUG_TOP_K = 5000;
  const DEBUG_PAGE_SIZE = 50;
  let debugResults = [];
  let debugQuery = '';
  let debugSort = 'relevance';
  let debugPageNum = 1;

  const debugResultEl = document.getElementById('debug-result');
  const debugPagerEl = document.getElementById('debug-pager');
  const debugPrevBtn = document.getElementById('debug-prev');
  const debugNextBtn = document.getElementById('debug-next');
  const debugPageInfoEl = document.getElementById('debug-page-info');

  function renderDebugResultsPage() {
    const totalPages = Math.max(1, Math.ceil(debugResults.length / DEBUG_PAGE_SIZE));
    debugPageNum = Math.min(Math.max(1, debugPageNum), totalPages);
    const start = (debugPageNum - 1) * DEBUG_PAGE_SIZE;
    const pageItems = debugResults.slice(start, start + DEBUG_PAGE_SIZE);

    clear(debugResultEl);
    const table = buildTable(
      [{ label: 'title' }, { label: 'excerpt' }, { label: 'bm25', num: true }, { label: 'semantic', num: true }, { label: 'pagerank', num: true }, { label: 'final', num: true }],
      pageItems,
      (r) => {
        const detailHref = '/admin/search/result?doc_id=' + encodeURIComponent(r.doc_id) +
          '&q=' + encodeURIComponent(debugQuery) + '&sort=' + encodeURIComponent(debugSort);
        const link = document.createElement('a');
        link.href = detailHref;
        link.style.color = 'var(--ink)';
        link.textContent = r.title || r.url;
        link.title = 'See the full score breakdown for this result';
        const titleTd = document.createElement('td');
        titleTd.appendChild(link);
        return [
          titleTd,
          snippetCell(r.snippet),
          textCell(r.bm25_score.toFixed(3), { num: true }),
          textCell(r.semantic_sim.toFixed(3), { num: true }),
          textCell(r.pagerank.toFixed(4), { num: true }),
          textCell(r.final_score.toFixed(3), { num: true }),
        ];
      },
    );
    debugResultEl.appendChild(table);

    debugPagerEl.hidden = totalPages <= 1;
    debugPageInfoEl.textContent = 'Page ' + debugPageNum + ' of ' + totalPages +
      ' (' + debugResults.length + (debugResults.length === 1 ? ' result)' : ' results)');
    debugPrevBtn.disabled = debugPageNum <= 1;
    debugNextBtn.disabled = debugPageNum >= totalPages;
  }

  debugPrevBtn.addEventListener('click', () => {
    debugPageNum--;
    renderDebugResultsPage();
  });
  debugNextBtn.addEventListener('click', () => {
    debugPageNum++;
    renderDebugResultsPage();
  });

  document.getElementById('debug-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const q = document.getElementById('debug-q').value.trim();
    const sort = document.getElementById('debug-sort').value;
    const status = document.getElementById('debug-status');
    clear(debugResultEl);
    debugPagerEl.hidden = true;
    if (!q) { status.textContent = 'Type a query to debug.'; return; }
    status.textContent = 'Running…';
    try {
      const results = await getJSON('/admin/api/search?q=' + encodeURIComponent(q) +
        '&sort=' + encodeURIComponent(sort) + '&top_k=' + DEBUG_TOP_K);
      const corrected = (results[0] && results[0].corrected_terms) || [];
      const correctionSuffix = corrected.length === 0 ? '' :
        ' (corrected ' + corrected.map((c) => '"' + c.original + '"→"' + c.corrected + '"').join(', ') + ')';
      if (results.length === 0) {
        status.textContent = 'No matches for “' + q + '”.' + correctionSuffix;
        return;
      }
      status.textContent = (results.length === 1 ? '1 match' : results.length + ' matches') + correctionSuffix;
      debugResults = results;
      debugQuery = q;
      debugSort = sort;
      debugPageNum = 1;
      renderDebugResultsPage();
    } catch (err) {
      status.textContent = 'Debug search failed: ' + err.message;
    }
  });

  document.getElementById('postings-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const term = document.getElementById('postings-term').value.trim().toLowerCase();
    const status = document.getElementById('postings-status');
    const result = document.getElementById('postings-result');
    clear(result);
    if (!term) { status.textContent = 'Enter a term to look up.'; return; }
    status.textContent = 'Looking up…';
    try {
      const data = await getJSON('/admin/api/postings?term=' + encodeURIComponent(term));
      if (data.postings.length === 0) {
        status.textContent = '"' + term + '" does not appear in the index.';
        return;
      }
      status.textContent = 'Appears in ' + data.doc_freq + (data.doc_freq === 1 ? ' document.' : ' documents.');
      const table = buildTable(
        [{ label: 'doc id' }, { label: 'term freq', num: true }, { label: 'doc length', num: true }],
        data.postings,
        (p) => [
          textCell(p.doc_id),
          textCell(String(p.term_freq), { num: true }),
          textCell(String(p.doc_length), { num: true }),
        ],
      );
      result.appendChild(table);
    } catch (err) {
      status.textContent = 'Look up failed: ' + err.message;
    }
  });

  renderAdminNav();
  wireSignOut();
