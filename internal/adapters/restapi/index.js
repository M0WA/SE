  const form = document.getElementById('search-form');
  const input = document.getElementById('q');
  const status = document.getElementById('status');
  const correctionNote = document.getElementById('correction-note');
  const results = document.getElementById('results');

  // renderCorrectionNote shows a quiet, transparent note when the search
  // service fuzzy-corrected a misspelled query term (see corrected_terms on
  // each result) -- the displayed query itself is never silently rewritten,
  // this just says which term(s) were substituted for scoring.
  function renderCorrectionNote(list) {
    const corrected = (list[0] && list[0].corrected_terms) || [];
    if (corrected.length === 0) {
      correctionNote.hidden = true;
      correctionNote.textContent = '';
      return;
    }
    correctionNote.textContent = 'Showing results for ' +
      corrected.map((c) => '“' + c.corrected + '”').join(', ') +
      ' instead of ' + corrected.map((c) => '“' + c.original + '”').join(', ') + '.';
    correctionNote.hidden = false;
  }

  function clear(el) {
    while (el.firstChild) el.removeChild(el.firstChild);
  }

  function scoreRow(label, value) {
    const row = document.createElement('div');
    row.className = 'score-row';
    const k = document.createElement('span');
    k.className = 'score-label';
    k.textContent = label;
    const v = document.createElement('span');
    v.className = 'score-value';
    v.textContent = Number(value).toFixed(3);
    row.appendChild(k);
    row.appendChild(v);
    return row;
  }

  function renderResults(query, list) {
    clear(results);
    renderCorrectionNote(list);
    if (list.length === 0) {
      status.textContent = 'No matches for “' + query + '”.';
      return;
    }
    status.textContent = list.length === 1 ? '1 match' : list.length + ' matches';
    for (const r of list) {
      const row = document.createElement('div');
      row.className = 'result';

      const head = document.createElement('div');
      head.className = 'result-head';

      const title = document.createElement('a');
      title.className = 'result-title';
      title.href = r.url;
      title.target = '_blank';
      title.rel = 'noopener noreferrer';
      title.textContent = r.title || r.url;
      head.appendChild(title);

      const score = document.createElement('span');
      score.className = 'result-score';
      score.textContent = Number(r.score).toFixed(3);
      head.appendChild(score);

      row.appendChild(head);

      const url = document.createElement('div');
      url.className = 'result-url';
      url.textContent = r.url;
      row.appendChild(url);

      if (r.snippet) {
        const snippet = document.createElement('div');
        snippet.className = 'result-snippet';
        snippet.innerHTML = r.snippet;
        row.appendChild(snippet);
      }

      if (r.bm25_score !== undefined && r.semantic_sim !== undefined) {
        const details = document.createElement('details');
        details.className = 'result-details';
        const summary = document.createElement('summary');
        summary.textContent = 'Details';
        details.appendChild(summary);
        details.appendChild(scoreRow('bm25', r.bm25_score));
        details.appendChild(scoreRow('semantic', r.semantic_sim));
        details.appendChild(scoreRow('final', r.score));
        row.appendChild(details);
      }

      results.appendChild(row);
    }
  }

  async function runSearch(query, sort) {
    clear(results);
    correctionNote.hidden = true;
    status.textContent = 'Searching…';
    try {
      const resp = await fetch('/search?q=' + encodeURIComponent(query) + '&sort=' + encodeURIComponent(sort));
      if (!resp.ok) {
        const msg = await resp.text();
        status.textContent = 'Search failed: ' + msg.trim();
        return;
      }
      const data = await resp.json();
      renderResults(query, data.results || []);
    } catch (err) {
      status.textContent = 'Search failed: could not reach the server.';
    }
  }

  const sortSelect = document.getElementById('sort');

  form.addEventListener('submit', (e) => {
    e.preventDefault();
    const query = input.value.trim();
    if (!query) {
      status.textContent = 'Type something to search for.';
      clear(results);
      correctionNote.hidden = true;
      return;
    }
    runSearch(query, sortSelect.value);
  });

  // Mirrors admin.js's wireSignOut -- this page doesn't load admin.js (it's
  // the public site, not the admin backend), so the same few lines are
  // inlined here rather than pulling in the whole admin script for one
  // function.
  document.getElementById('sign-out').addEventListener('click', async () => {
    try {
      await fetch('/logout', { method: 'POST' });
    } finally {
      window.location = '/';
    }
  });

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag. See index.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderCorrectionNote, clear, scoreRow, renderResults, runSearch };
  }
