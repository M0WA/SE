  const statsEl = document.getElementById('pagerank-stats');
  const lastRunEl = document.getElementById('pagerank-last-run');
  const configEl = document.getElementById('pagerank-config');
  const recomputeBtn = document.getElementById('recompute-btn');
  const recomputeStatusEl = document.getElementById('recompute-status');
  const recomputeResultEl = document.getElementById('recompute-result');

  // pollTimer keeps polling while a recompute is in progress, triggered by ANY process (another
  // admin, this click, or cmd/crawl's ticker) -- so this page reflects real cross-process state,
  // not just what this tab started.
  let pollTimer = null;

  function renderStats(s) {
    clear(statsEl);
    kvRow(statsEl, 'Documents', String(s.total_docs));
    kvRow(statsEl, 'Min', s.min_pagerank.toFixed(6));
    kvRow(statsEl, 'Max', s.max_pagerank.toFixed(6));
    kvRow(statsEl, 'Average', s.avg_pagerank.toFixed(6));

    clear(lastRunEl);
    if (s.recompute_in_progress) {
      kvRow(lastRunEl, 'Status', 'Recomputing…');
    } else if (!s.last_recomputed_at) {
      lastRunEl.textContent = 'No recompute has run yet on this instance.';
    } else {
      kvRow(lastRunEl, 'Last recomputed', new Date(s.last_recomputed_at).toLocaleString());
      kvRow(lastRunEl, 'Documents scored', String(s.last_recompute_documents));
      kvRow(lastRunEl, 'Iterations run', String(s.last_recompute_iterations));
      kvRow(lastRunEl, 'Final convergence delta', s.last_recompute_final_delta.toExponential(3));
      kvRow(lastRunEl, 'Duration', s.last_recompute_duration_ms + ' ms');
    }

    clear(configEl);
    kvRow(configEl, 'Damping factor', String(s.damping));
    kvRow(configEl, 'Max iterations', String(s.max_iterations));
    kvRow(configEl, 'Convergence epsilon', String(s.epsilon));
    kvRow(configEl, 'Blend weight (Tuning)', String(s.pagerank_weight));
    kvRow(configEl, 'Recompute interval (Tuning)', s.recompute_interval_minutes + ' min');

    setButtonLoading(recomputeBtn, s.recompute_in_progress, 'Recomputing…');
    pollTimer = pollWhileInProgress(s.recompute_in_progress, pollTimer, () => { pollTimer = null; load(); });
  }

  async function load() {
    try {
      renderStats(await getJSON('/admin/api/pagerank'));
    } catch (err) {
      statsEl.textContent = 'Could not load PageRank stats: ' + err.message;
    }
  }

  // renderStats (via load()) owns recomputeBtn's loading state from here on, reflecting
  // recompute_in_progress from the server (covers any trigger, not just this click) -- this
  // handler only sets it eagerly for immediate feedback, clearing it itself only on an error
  // load() never sees.
  recomputeBtn.addEventListener('click', async () => {
    setButtonLoading(recomputeBtn, true, 'Recomputing…');
    recomputeStatusEl.textContent = 'Recomputing…';
    clear(recomputeResultEl);
    try {
      const r = await postJSON('/admin/api/pagerank/recompute', {});
      recomputeStatusEl.textContent = 'Done in ' + r.duration_ms + ' ms.';
      kvRow(recomputeResultEl, 'Documents scored', String(r.documents));
      kvRow(recomputeResultEl, 'Iterations run', String(r.iterations));
      kvRow(recomputeResultEl, 'Final convergence delta', r.final_delta.toExponential(3));
      kvRow(recomputeResultEl, 'New min', r.min_pagerank.toFixed(6));
      kvRow(recomputeResultEl, 'New max', r.max_pagerank.toFixed(6));
      kvRow(recomputeResultEl, 'New average', r.avg_pagerank.toFixed(6));
      await load();
    } catch (err) {
      recomputeStatusEl.textContent = 'Could not recompute: ' + err.message;
      setButtonLoading(recomputeBtn, false);
    }
  });

  renderAdminNav();
  wireSignOut();
  load();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See admin_pagerank.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderStats, load };
  }
