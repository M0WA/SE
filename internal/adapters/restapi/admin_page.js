  function buildDonut(domains, total) {
    const size = 120;
    const r = 46;
    const strokeWidth = 16;
    const circumference = 2 * Math.PI * r;

    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 ' + size + ' ' + size);
    svg.setAttribute('width', size);
    svg.setAttribute('height', size);
    svg.classList.add('donut-chart');

    const track = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
    track.setAttribute('cx', size / 2);
    track.setAttribute('cy', size / 2);
    track.setAttribute('r', r);
    track.setAttribute('fill', 'none');
    track.setAttribute('stroke', 'var(--rule)');
    track.setAttribute('stroke-width', strokeWidth);
    svg.appendChild(track);

    let offset = 0;
    domains.forEach((d, i) => {
      const frac = total > 0 ? d.doc_count / total : 0;
      const len = frac * circumference;
      const arc = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
      arc.setAttribute('cx', size / 2);
      arc.setAttribute('cy', size / 2);
      arc.setAttribute('r', r);
      arc.setAttribute('fill', 'none');
      arc.setAttribute('stroke', 'var(--accent)');
      arc.setAttribute('stroke-opacity', Math.max(0.25, 1 - i * (0.65 / Math.max(1, domains.length - 1))).toFixed(2));
      arc.setAttribute('stroke-width', strokeWidth);
      arc.setAttribute('stroke-dasharray', len.toFixed(1) + ' ' + (circumference - len).toFixed(1));
      arc.setAttribute('stroke-dashoffset', (-offset).toFixed(1));
      arc.setAttribute('transform', 'rotate(-90 ' + size / 2 + ' ' + size / 2 + ')');
      const title = document.createElementNS('http://www.w3.org/2000/svg', 'title');
      title.textContent = d.host + ' — ' + d.doc_count + (d.doc_count === 1 ? ' page' : ' pages');
      arc.appendChild(title);
      svg.appendChild(arc);
      offset += len;
    });
    return svg;
  }

  function buildDonutLegend(domains) {
    const legend = document.createElement('div');
    legend.className = 'donut-legend';
    domains.forEach((d, i) => {
      const row = document.createElement('div');
      row.className = 'donut-legend-row';
      const swatch = document.createElement('span');
      swatch.className = 'donut-swatch';
      swatch.style.opacity = Math.max(0.25, 1 - i * (0.65 / Math.max(1, domains.length - 1))).toFixed(2);
      const label = document.createElement('a');
      label.href = '/admin/documents/' + encodeURIComponent(d.host);
      label.textContent = d.host;
      const count = document.createElement('span');
      count.className = 'domain-metrics';
      count.textContent = String(d.doc_count);
      row.appendChild(swatch);
      row.appendChild(label);
      row.appendChild(count);
      legend.appendChild(row);
    });
    return legend;
  }

  function buildAgeBars(buckets) {
    const maxCount = Math.max.apply(null, buckets.map((b) => b.count).concat([1]));
    const wrap = document.createElement('div');
    wrap.className = 'age-bars';
    for (const b of buckets) {
      const col = document.createElement('div');
      col.className = 'age-bar-col';
      const bar = document.createElement('div');
      bar.className = 'age-bar';
      bar.style.height = Math.max(2, (b.count / maxCount) * 80) + 'px';
      bar.title = b.label + ': ' + b.count + (b.count === 1 ? ' page' : ' pages');
      const count = document.createElement('div');
      count.className = 'age-bar-count';
      count.textContent = String(b.count);
      const label = document.createElement('div');
      label.className = 'age-bar-label';
      label.textContent = b.label;
      col.appendChild(count);
      col.appendChild(bar);
      col.appendChild(label);
      wrap.appendChild(col);
    }
    return wrap;
  }

  // buildVersionBars mirrors buildAgeBars' bar-chart shape (same CSS
  // classes, same "count above a height-scaled bar, label below" layout)
  // for the version-number breakdown -- a document's version count is a
  // small, discrete distribution just like an age bucket, so the same
  // visual treatment reads the same way at a glance.
  function buildVersionBars(versionCounts) {
    const maxCount = Math.max.apply(null, versionCounts.map((v) => v.count).concat([1]));
    const wrap = document.createElement('div');
    wrap.className = 'age-bars';
    for (const v of versionCounts) {
      const col = document.createElement('div');
      col.className = 'age-bar-col';
      const bar = document.createElement('div');
      bar.className = 'age-bar';
      bar.style.height = Math.max(2, (v.count / maxCount) * 80) + 'px';
      bar.title = 'Version ' + v.version + ': ' + v.count + (v.count === 1 ? ' document' : ' documents');
      const count = document.createElement('div');
      count.className = 'age-bar-count';
      count.textContent = String(v.count);
      const label = document.createElement('div');
      label.className = 'age-bar-label';
      label.textContent = 'v' + v.version;
      col.appendChild(count);
      col.appendChild(bar);
      col.appendChild(label);
      wrap.appendChild(col);
    }
    return wrap;
  }

  // buildStoredVersionsBars mirrors buildVersionBars' bar-chart shape for
  // a different distribution: how many documents currently have exactly N
  // versions actually retained in storage (current + archived, bounded by
  // the Settings > Documents > "Version history" limit) -- distinct from
  // "Documents by version" above, which buckets by a document's version
  // NUMBER (total historical changes, unaffected by pruning).
  function buildStoredVersionsBars(storedVersionCounts) {
    const maxCount = Math.max.apply(null, storedVersionCounts.map((s) => s.doc_count).concat([1]));
    const wrap = document.createElement('div');
    wrap.className = 'age-bars';
    for (const s of storedVersionCounts) {
      const col = document.createElement('div');
      col.className = 'age-bar-col';
      const bar = document.createElement('div');
      bar.className = 'age-bar';
      bar.style.height = Math.max(2, (s.doc_count / maxCount) * 80) + 'px';
      const versionsLabel = s.stored_versions + (s.stored_versions === 1 ? ' version' : ' versions');
      bar.title = versionsLabel + ': ' + s.doc_count + (s.doc_count === 1 ? ' document' : ' documents');
      const count = document.createElement('div');
      count.className = 'age-bar-count';
      count.textContent = String(s.doc_count);
      const label = document.createElement('div');
      label.className = 'age-bar-label';
      label.textContent = versionsLabel;
      col.appendChild(count);
      col.appendChild(bar);
      col.appendChild(label);
      wrap.appendChild(col);
    }
    return wrap;
  }

  async function loadCorpusOverview(statsEl, statusEl, chartsEl) {
    let overview;
    try {
      overview = await getJSON('/admin/api/documents/overview');
    } catch (err) {
      statusEl.textContent = 'Could not load overview: ' + err.message;
      return;
    }

    clear(chartsEl);
    kvRow(statsEl, 'Domains', overview.total_domains);

    const total = overview.top_domains.reduce((sum, d) => sum + d.doc_count, 0);
    if (total === 0) {
      statusEl.textContent = 'No documents indexed yet.';
    } else {
      statusEl.textContent = '';

      const row = document.createElement('div');
      row.className = 'overview-row';

      const domainBlock = document.createElement('div');
      domainBlock.className = 'overview-block';
      const domainHeading = document.createElement('h3');
      domainHeading.textContent = 'Pages per domain';
      domainBlock.appendChild(domainHeading);
      const donutWrap = document.createElement('div');
      donutWrap.className = 'donut-wrap';
      donutWrap.appendChild(buildDonut(overview.top_domains, total));
      donutWrap.appendChild(buildDonutLegend(overview.top_domains));
      domainBlock.appendChild(donutWrap);
      row.appendChild(domainBlock);

      const ageBlock = document.createElement('div');
      ageBlock.className = 'overview-block';
      const ageHeading = document.createElement('h3');
      ageHeading.textContent = 'Age of indexed content';
      ageBlock.appendChild(ageHeading);
      ageBlock.appendChild(buildAgeBars(overview.age_buckets));
      row.appendChild(ageBlock);

      if (overview.version_counts && overview.version_counts.length > 0) {
        const versionBlock = document.createElement('div');
        versionBlock.className = 'overview-block';
        const versionHeading = document.createElement('h3');
        versionHeading.textContent = 'Documents by version';
        versionBlock.appendChild(versionHeading);
        versionBlock.appendChild(buildVersionBars(overview.version_counts));
        row.appendChild(versionBlock);
      }

      if (overview.stored_version_counts && overview.stored_version_counts.length > 0) {
        const storedVersionBlock = document.createElement('div');
        storedVersionBlock.className = 'overview-block';
        const storedVersionHeading = document.createElement('h3');
        storedVersionHeading.textContent = 'Documents by number of versions';
        storedVersionBlock.appendChild(storedVersionHeading);
        storedVersionBlock.appendChild(buildStoredVersionsBars(overview.stored_version_counts));
        row.appendChild(storedVersionBlock);
      }

      chartsEl.appendChild(row);
    }

    try {
      const vocab = await getJSON('/admin/api/vocabulary?limit=1');
      kvRow(statsEl, 'Vocabulary', vocab.vocabulary_size);
    } catch (err) {
      // Vocabulary size is a nice-to-have extra stat here; don't let a
      // failure here blot out the overview panel that already rendered.
    }
  }

  async function loadOverview() {
    await loadStats();
    await loadCorpusOverview(
      document.getElementById('stats'),
      document.getElementById('overview-status'),
      document.getElementById('overview-charts')
    );
  }

  loadOverview();
  wireSignOut();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See admin_page.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      buildDonut, buildDonutLegend, buildAgeBars, buildVersionBars, buildStoredVersionsBars,
      loadCorpusOverview, loadOverview,
    };
  }
