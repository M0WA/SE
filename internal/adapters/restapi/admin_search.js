  document.getElementById('debug-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const q = document.getElementById('debug-q').value.trim();
    const sort = document.getElementById('debug-sort').value;
    const status = document.getElementById('debug-status');
    const result = document.getElementById('debug-result');
    clear(result);
    if (!q) { status.textContent = 'Type a query to debug.'; return; }
    status.textContent = 'Running…';
    try {
      const results = await getJSON('/admin/api/search?q=' + encodeURIComponent(q) + '&sort=' + encodeURIComponent(sort));
      const corrected = (results[0] && results[0].corrected_terms) || [];
      const correctionSuffix = corrected.length === 0 ? '' :
        ' (corrected ' + corrected.map((c) => '"' + c.original + '"→"' + c.corrected + '"').join(', ') + ')';
      if (results.length === 0) {
        status.textContent = 'No matches for “' + q + '”.' + correctionSuffix;
        return;
      }
      status.textContent = (results.length === 1 ? '1 match' : results.length + ' matches') + correctionSuffix;
      const table = buildTable(
        [{ label: 'title' }, { label: 'excerpt' }, { label: 'bm25', num: true }, { label: 'semantic', num: true }, { label: 'pagerank', num: true }, { label: 'final', num: true }],
        results,
        (r) => {
          const detailHref = '/admin/search/result?doc_id=' + encodeURIComponent(r.doc_id) +
            '&q=' + encodeURIComponent(q) + '&sort=' + encodeURIComponent(sort);
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
      result.appendChild(table);
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

  wireSignOut();
