  const domainQ = document.getElementById('domain-q');
  const domainResultsEl = document.getElementById('domain-results');
  const documentResultsEl = document.getElementById('document-results');
  const domainSearchErrorEl = document.getElementById('domain-search-error');

  let searchTimer = null;

  // candidatesFetchPromise caches the one broad fetch of every domain and
  // every document so each keystroke's regex filter runs against in-memory
  // arrays instead of re-querying the server -- the same fetch-once,
  // filter-client-side pattern as admin.js's vocabulary search. A failed
  // fetch clears the cache so the next keystroke gets to retry.
  let candidatesFetchPromise = null;

  function fetchCandidatesOnce() {
    if (!candidatesFetchPromise) {
      candidatesFetchPromise = Promise.all([
        getJSON('/admin/api/domains?q=&limit=1000'),
        getJSON('/admin/api/documents?limit=2000'),
      ]).then(([domains, documents]) => ({ domains, documents })).catch((err) => {
        candidatesFetchPromise = null;
        throw err;
      });
    }
    return candidatesFetchPromise;
  }

  // hostOf pulls the host out of a document's URL for display/filtering --
  // the documents endpoint doesn't carry a separate host field, so it's
  // derived the same way a browser would resolve the link.
  function hostOf(url) {
    try {
      return new URL(url).host;
    } catch (err) {
      return '';
    }
  }

  function renderDomainResults(domains, q) {
    clear(domainResultsEl);
    if (domains.length === 0) {
      domainResultsEl.textContent = 'No domain matches “' + q + '”.';
      return;
    }
    const list = document.createElement('div');
    list.className = 'domain-result-list';
    for (const d of domains) {
      const row = document.createElement('a');
      row.className = 'domain-result';
      row.href = '/admin/documents/' + encodeURIComponent(d.host);
      const name = document.createElement('span');
      name.textContent = d.host;
      const count = document.createElement('span');
      count.className = 'domain-metrics';
      count.textContent = d.doc_count + (d.doc_count === 1 ? ' page' : ' pages');
      row.appendChild(name);
      row.appendChild(count);
      list.appendChild(row);
    }
    domainResultsEl.appendChild(list);
  }

  function renderDocumentResults(documents, q) {
    clear(documentResultsEl);
    if (documents.length === 0) {
      documentResultsEl.textContent = 'No document matches “' + q + '”.';
      return;
    }
    const table = buildTable(
      [{ label: 'url' }, { label: 'title' }, { label: 'host' }],
      documents,
      (doc) => [
        urlCell(doc.url),
        textCell(doc.title || ''),
        textCell(hostOf(doc.url)),
      ],
    );
    documentResultsEl.appendChild(table);
  }

  async function searchDomains() {
    const q = domainQ.value.trim();
    clear(domainSearchErrorEl);
    if (q === '') {
      clear(domainResultsEl);
      clear(documentResultsEl);
      return;
    }
    let re;
    try {
      re = new RegExp(q, 'i');
    } catch (err) {
      clear(domainResultsEl);
      clear(documentResultsEl);
      domainSearchErrorEl.textContent = 'Invalid pattern: ' + err.message;
      return;
    }
    try {
      const { domains, documents } = await fetchCandidatesOnce();
      renderDomainResults(domains.filter((d) => re.test(d.host)), q);
      renderDocumentResults(
        documents.filter((doc) => re.test(doc.url) || re.test(doc.title || '') || re.test(hostOf(doc.url))),
        q,
      );
    } catch (err) {
      clear(domainResultsEl);
      clear(documentResultsEl);
      domainSearchErrorEl.textContent = 'Could not search: ' + err.message;
    }
  }

  domainQ.addEventListener('input', () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(searchDomains, 200);
  });
  document.getElementById('domain-search-form').addEventListener('submit', (e) => {
    e.preventDefault();
    clearTimeout(searchTimer);
    searchDomains();
  });

  wireSignOut();
  wireVocabularySearch();

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { hostOf, renderDomainResults, renderDocumentResults, searchDomains, fetchCandidatesOnce };
  }
