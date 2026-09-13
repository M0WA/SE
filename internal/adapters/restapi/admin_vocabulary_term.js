  const params = new URLSearchParams(window.location.search);
  const term = params.get('term') || '';

  const titleEl = document.getElementById('term-title');
  const tailEl = document.getElementById('term-tail');
  const statusEl = document.getElementById('term-status');
  const tableEl = document.getElementById('term-table');

  document.title = 'se. — ' + (term || 'term');
  titleEl.textContent = term || '(no term given)';

  function renderPostings(data) {
    const truncated = data.postings.length < data.doc_freq;
    tailEl.appendChild((() => {
      const span = document.createElement('span');
      span.className = 'domain-metrics';
      span.textContent = data.postings.length + (data.postings.length === 1 ? ' page' : ' pages') +
        (truncated ? ' shown of ' : ' · appears in ') +
        data.doc_freq + (data.doc_freq === 1 ? ' document' : ' documents') + ' total';
      return span;
    })());

    if (data.postings.length === 0) {
      statusEl.textContent = 'No pages contain “' + term + '”.';
      return;
    }

    if (truncated) {
      statusEl.textContent = 'Showing the ' + data.postings.length + ' strongest matches — refine the term to narrow further.';
    }

    // Highest term frequency first -- the strongest matches for this term
    // lead the list, same ordering convention as the search debug page's
    // BM25 term breakdown.
    const sorted = data.postings.slice().sort((a, b) => b.term_freq - a.term_freq);

    const table = buildTable(
      [{ label: 'url' }, { label: 'excerpt' }, { label: 'term freq', num: true }, { label: 'doc length', num: true }],
      sorted,
      (p) => {
        const link = document.createElement('a');
        link.href = p.url;
        link.target = '_blank';
        link.rel = 'noopener noreferrer';
        link.style.color = 'var(--ink)';
        link.textContent = p.title || p.url;
        const urlDiv = document.createElement('div');
        urlDiv.className = 'url';
        urlDiv.textContent = p.url;
        const titleTd = document.createElement('td');
        titleTd.appendChild(link);
        titleTd.appendChild(urlDiv);

        return [
          titleTd,
          snippetCell(p.snippet),
          textCell(String(p.term_freq), { num: true }),
          textCell(String(p.doc_length), { num: true }),
        ];
      },
    );
    tableEl.appendChild(table);
  }

  async function load() {
    if (!term) {
      statusEl.textContent = 'This page needs a term in its URL — open it from the Vocabulary list instead of directly.';
      return;
    }
    try {
      const data = await getJSON('/admin/api/postings?term=' + encodeURIComponent(term));
      renderPostings(data);
    } catch (err) {
      statusEl.textContent = 'Could not load pages for “' + term + '”: ' + err.message;
    }
  }

  wireSignOut();
  load();
