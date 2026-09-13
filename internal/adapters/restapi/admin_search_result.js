  const params = new URLSearchParams(window.location.search);
  const docID = params.get('doc_id') || '';
  const q = params.get('q') || '';
  const sort = params.get('sort') || 'relevance';

  const statusEl = document.getElementById('result-status');
  const bodyEl = document.getElementById('result-body');

  // barRow builds one .bar-row: a label, a track filled to fraction (0..1)
  // of its own width, a right-aligned value, and an optional meta string --
  // the one bar shape reused for both the per-term BM25 bars and the
  // semantic-similarity gauge below.
  function barRow(label, fraction, value, meta) {
    const row = document.createElement('div');
    row.className = 'bar-row';
    const labelEl = document.createElement('span');
    labelEl.className = 'bar-label';
    labelEl.textContent = label;
    labelEl.title = label;
    const track = document.createElement('div');
    track.className = 'bar-track';
    const fill = document.createElement('div');
    fill.className = 'bar-fill';
    fill.style.width = (Math.max(0, Math.min(1, fraction)) * 100) + '%';
    track.appendChild(fill);
    const valueEl = document.createElement('span');
    valueEl.className = 'bar-value';
    valueEl.textContent = value;
    row.appendChild(labelEl);
    row.appendChild(track);
    if (meta) {
      const metaEl = document.createElement('span');
      metaEl.className = 'bar-meta';
      metaEl.textContent = meta;
      row.appendChild(metaEl);
    }
    row.appendChild(valueEl);
    return row;
  }

  function renderComposition(r) {
    const w = r.pagerank_weight;
    const bm25Contribution = r.alpha * (1 - w) * r.norm_bm25;
    const semanticContribution = (1 - r.alpha) * (1 - w) * Math.max(0, r.semantic_sim);
    const pagerankContribution = w * r.normalized_pagerank;

    const track = document.getElementById('composition-track');
    clear(track);
    [
      ['seg-bm25', bm25Contribution],
      ['seg-semantic', semanticContribution],
      ['seg-pagerank', pagerankContribution],
    ].forEach(([cls, v]) => {
      const seg = document.createElement('div');
      seg.className = 'composition-seg ' + cls;
      seg.style.width = (Math.max(0, v) * 100) + '%';
      track.appendChild(seg);
    });

    const legend = document.getElementById('composition-legend');
    clear(legend);
    const entries = [
      ['seg-bm25', 'bm25 (α ' + r.alpha.toFixed(2) + ')', bm25Contribution],
      ['seg-semantic', 'semantic (1−α ' + (1 - r.alpha).toFixed(2) + ')', semanticContribution],
      ['seg-pagerank', 'pagerank (w ' + w.toFixed(2) + ')', pagerankContribution],
    ];
    for (const [cls, label, v] of entries) {
      const item = document.createElement('span');
      const swatch = document.createElement('span');
      swatch.className = 'swatch ' + cls;
      const valueEl = document.createElement('span');
      valueEl.className = 'v';
      valueEl.textContent = v.toFixed(3);
      item.appendChild(swatch);
      item.append(label + ' ');
      item.appendChild(valueEl);
      legend.appendChild(item);
    }

    const composed = bm25Contribution + semanticContribution + pagerankContribution;
    const note = document.getElementById('composition-note');
    if (Math.abs(composed - r.final_score) > 1e-6) {
      note.hidden = false;
      note.textContent = 'An admin ranking boost or block (see Overrides) was also applied to this result — the components above are before that adjustment.';
    } else {
      note.hidden = true;
    }
  }

  function renderBM25Terms(r) {
    document.getElementById('bm25-caption').textContent = 'computed with k1=' + r.k1.toFixed(2) + ', b=' + r.b.toFixed(2);
    const container = document.getElementById('bm25-terms');
    clear(container);
    const terms = r.bm25_terms || [];
    if (terms.length === 0) {
      container.textContent = 'No query term matched this document directly — it was found through semantic similarity alone.';
      return;
    }
    const maxScore = Math.max.apply(null, terms.map((t) => t.score).concat([0.0001]));
    for (const t of terms) {
      container.appendChild(barRow(
        t.term,
        t.score / maxScore,
        t.score.toFixed(3),
        'tf ' + t.term_freq + ' · df ' + t.doc_freq,
      ));
    }
  }

  function render(r) {
    document.title = 'se. — ' + (r.title || r.url);
    const titleLink = document.getElementById('result-title');
    titleLink.href = r.url;
    titleLink.textContent = r.title || r.url;
    document.getElementById('result-url').textContent = r.url;
    document.getElementById('result-final').textContent = r.final_score.toFixed(3);
    document.getElementById('result-snippet').innerHTML = r.snippet || '';

    renderComposition(r);
    renderBM25Terms(r);

    const semantic = Math.max(0, r.semantic_sim);
    document.getElementById('semantic-fill').style.width = (semantic * 100) + '%';
    document.getElementById('semantic-value').textContent = r.semantic_sim.toFixed(3);

    const pagerankEl = document.getElementById('pagerank-kv');
    clear(pagerankEl);
    kvRow(pagerankEl, 'Raw', r.pagerank.toFixed(6));
    kvRow(pagerankEl, 'Normalized (this batch)', r.normalized_pagerank.toFixed(3));
    kvRow(pagerankEl, 'Blend weight', r.pagerank_weight.toFixed(2));

    bodyEl.hidden = false;
  }

  async function load() {
    if (!docID || !q) {
      statusEl.textContent = 'This page needs both a doc_id and q in its URL — open it from a search debug result instead of directly.';
      return;
    }
    statusEl.textContent = 'Loading…';
    try {
      const results = await getJSON('/admin/api/search?q=' + encodeURIComponent(q) + '&sort=' + encodeURIComponent(sort));
      const r = results.find((x) => x.doc_id === docID);
      if (!r) {
        statusEl.textContent = 'No result for “' + q + '” matches this document anymore — it may have dropped out of the top results, or been removed from the index.';
        return;
      }
      clear(statusEl);
      render(r);
    } catch (err) {
      statusEl.textContent = 'Could not load this result: ' + err.message;
    }
  }

  wireSignOut();
  load();
