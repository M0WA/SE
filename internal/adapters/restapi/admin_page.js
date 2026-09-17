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

  // buildJobOutcomeDonut mirrors buildDonut's arc math exactly (same size/
  // radius/stroke, same opacity ramp per slice) for a different field shape
  // -- crawl job outcomes (status/count) rather than domains (host/
  // doc_count) -- see the admin Overview page's "Crawl job outcomes" panel.
  function buildJobOutcomeDonut(outcomes, total) {
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
    outcomes.forEach((o, i) => {
      const frac = total > 0 ? o.count / total : 0;
      const len = frac * circumference;
      const arc = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
      arc.setAttribute('cx', size / 2);
      arc.setAttribute('cy', size / 2);
      arc.setAttribute('r', r);
      arc.setAttribute('fill', 'none');
      arc.setAttribute('stroke', 'var(--accent)');
      arc.setAttribute('stroke-opacity', Math.max(0.25, 1 - i * (0.65 / Math.max(1, outcomes.length - 1))).toFixed(2));
      arc.setAttribute('stroke-width', strokeWidth);
      arc.setAttribute('stroke-dasharray', len.toFixed(1) + ' ' + (circumference - len).toFixed(1));
      arc.setAttribute('stroke-dashoffset', (-offset).toFixed(1));
      arc.setAttribute('transform', 'rotate(-90 ' + size / 2 + ' ' + size / 2 + ')');
      const title = document.createElementNS('http://www.w3.org/2000/svg', 'title');
      title.textContent = o.status + ' — ' + o.count + (o.count === 1 ? ' job' : ' jobs');
      arc.appendChild(title);
      svg.appendChild(arc);
      offset += len;
    });
    return svg;
  }

  // buildJobOutcomeLegend mirrors buildDonutLegend's row shape, minus the
  // link (a job status isn't a page to navigate to) -- reuses
  // .donut-legend-row directly since that class's anchor-specific rules
  // (`a`, `a:hover`) simply don't match here, with no link ever appended.
  function buildJobOutcomeLegend(outcomes) {
    const legend = document.createElement('div');
    legend.className = 'donut-legend';
    outcomes.forEach((o, i) => {
      const row = document.createElement('div');
      row.className = 'donut-legend-row';
      const swatch = document.createElement('span');
      swatch.className = 'donut-swatch';
      swatch.style.opacity = Math.max(0.25, 1 - i * (0.65 / Math.max(1, outcomes.length - 1))).toFixed(2);
      const label = document.createElement('span');
      label.textContent = o.status;
      const count = document.createElement('span');
      count.className = 'domain-metrics';
      count.textContent = String(o.count);
      row.appendChild(swatch);
      row.appendChild(label);
      row.appendChild(count);
      legend.appendChild(row);
    });
    return legend;
  }

  // FETCH_OUTCOME_ORDER/OPACITY fixes one opacity per outcome across every
  // day's column, so the same outcome always reads as the same shade
  // regardless of which outcomes a particular day actually had. Any status
  // not listed here (a future CrawlPageStatus this list hasn't been
  // updated for) still gets a segment -- see FETCH_OUTCOME_FALLBACK_OPACITY
  // below -- rather than silently vanishing from the stack while still
  // counting toward the bar's total height and count label.
  const FETCH_OUTCOME_ORDER = ['indexed', 'thin_content', 'robots_disallowed', 'fetch_failed'];
  const FETCH_OUTCOME_OPACITY = { indexed: 1, thin_content: 0.7, robots_disallowed: 0.45, fetch_failed: 0.25 };
  const FETCH_OUTCOME_FALLBACK_OPACITY = 0.15;

  // buildThroughputStackBars draws one column per day (see .stack-bars),
  // each a bottom-up stack of same-hue, per-outcome-opacity segments sized
  // by that outcome's share of the day -- the admin Overview page's fetch
  // throughput/outcome-breakdown panel.
  function buildThroughputStackBars(dailyOutcomes) {
    const totals = dailyOutcomes.map((d) => Object.values(d.outcomes || {}).reduce((a, b) => a + b, 0));
    const maxTotal = Math.max.apply(null, totals.concat([1]));
    const wrap = document.createElement('div');
    wrap.className = 'stack-bars';
    dailyOutcomes.forEach((d, i) => {
      const col = document.createElement('div');
      col.className = 'age-bar-col';
      const bar = document.createElement('div');
      bar.className = 'stack-bar';
      const total = totals[i];
      bar.style.height = Math.max(2, (total / maxTotal) * 80) + 'px';
      const outcomes = d.outcomes || {};
      // FETCH_OUTCOME_ORDER first (fixed order/shade), then any status this
      // day has that isn't in that list, so every counted outcome always
      // gets a segment and the segments' flex sizes always sum to the
      // day's real total.
      const statusesToday = FETCH_OUTCOME_ORDER.concat(
        Object.keys(outcomes).filter((s) => !FETCH_OUTCOME_ORDER.includes(s)).sort()
      );
      for (const status of statusesToday) {
        const count = outcomes[status] || 0;
        if (count === 0) continue;
        const seg = document.createElement('div');
        seg.className = 'stack-seg';
        seg.style.flex = String(count);
        seg.style.opacity = String(
          Object.prototype.hasOwnProperty.call(FETCH_OUTCOME_OPACITY, status)
            ? FETCH_OUTCOME_OPACITY[status]
            : FETCH_OUTCOME_FALLBACK_OPACITY
        );
        seg.title = d.date + ' ' + status + ': ' + count;
        bar.appendChild(seg);
      }
      const count = document.createElement('div');
      count.className = 'age-bar-count';
      count.textContent = String(total);
      const label = document.createElement('div');
      label.className = 'age-bar-label';
      label.textContent = d.date.slice(5); // "MM-DD" -- the year rarely adds anything at this width.
      col.appendChild(count);
      col.appendChild(bar);
      col.appendChild(label);
      wrap.appendChild(col);
    });
    return wrap;
  }

  // buildPageRankHistogram mirrors buildVersionBars' bar-chart shape (see
  // its own comment) for the PageRank distribution's pre-labeled buckets.
  function buildPageRankHistogram(buckets) {
    const maxCount = Math.max.apply(null, buckets.map((b) => b.count).concat([1]));
    const wrap = document.createElement('div');
    wrap.className = 'age-bars';
    for (const b of buckets) {
      const col = document.createElement('div');
      col.className = 'age-bar-col';
      const bar = document.createElement('div');
      bar.className = 'age-bar';
      bar.style.height = Math.max(2, (b.count / maxCount) * 80) + 'px';
      bar.title = b.label + ': ' + b.count + (b.count === 1 ? ' document' : ' documents');
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

  // buildLineChart draws a simple accent-colored trend line for a
  // day-bucketed series (points: [{date, value}]) -- shared by the
  // documents-indexed and fetch-duration-trend panels, whose data shares
  // the same shape (one value per day; a day with nothing to report simply
  // has no point, never a zero-filled gap -- see admin.go's
  // DocumentsIndexedByDay/DailyFetchDuration doc comments). Points are
  // spaced along x by actual elapsed days since the first point, not by
  // array index, so a gap where several days reported nothing shows up as
  // real horizontal space rather than silently compressing the line and
  // distorting its apparent slope. formatValue renders one point's value
  // for its hover title.
  function buildLineChart(points, formatValue) {
    const wrap = document.createElement('div');
    wrap.className = 'line-chart-wrap';
    if (points.length === 0) {
      wrap.classList.add('line-chart-empty');
      wrap.textContent = 'No data in this window.';
      return wrap;
    }

    const width = 320;
    const height = 110;
    const padTop = 10;
    const padBottom = 4;
    const padX = 8;
    const plotW = width - 2 * padX;
    const plotH = height - padTop - padBottom;

    const values = points.map((p) => p.value);
    const maxV = Math.max.apply(null, values.concat([0]));
    const minV = Math.min.apply(null, values.concat([0]));
    const span = maxV - minV || 1;

    // dayOffset turns a "YYYY-MM-DD" date into its day count since the
    // first point (parsed as UTC midnight so this never shifts by the
    // viewer's local timezone), so xAt places points by real elapsed time
    // rather than assuming every point is one uniform day apart.
    const dayMs = 24 * 60 * 60 * 1000;
    const dayOffset = (date) => (Date.parse(date + 'T00:00:00Z') - Date.parse(points[0].date + 'T00:00:00Z')) / dayMs;
    const totalDays = dayOffset(points[points.length - 1].date) || 1;
    const xAt = (i) => padX + (points.length === 1 ? plotW / 2 : (dayOffset(points[i].date) / totalDays) * plotW);
    const yAt = (v) => padTop + plotH - ((v - minV) / span) * plotH;
    const coords = points.map((p, i) => [xAt(i), yAt(p.value)]);

    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 ' + width + ' ' + height);
    svg.setAttribute('width', '100%');
    svg.setAttribute('height', height);
    svg.classList.add('line-chart');

    const linePoints = coords.map(([x, y]) => x.toFixed(1) + ',' + y.toFixed(1)).join(' ');

    const area = document.createElementNS('http://www.w3.org/2000/svg', 'polygon');
    area.setAttribute('points', padX + ',' + (padTop + plotH) + ' ' + linePoints + ' ' + (padX + plotW) + ',' + (padTop + plotH));
    area.setAttribute('fill', 'var(--accent-soft)');
    area.setAttribute('stroke', 'none');
    svg.appendChild(area);

    const line = document.createElementNS('http://www.w3.org/2000/svg', 'polyline');
    line.setAttribute('points', linePoints);
    line.setAttribute('fill', 'none');
    line.setAttribute('stroke', 'var(--accent)');
    line.setAttribute('stroke-width', '2');
    svg.appendChild(line);

    coords.forEach(([x, y], i) => {
      const dot = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
      dot.setAttribute('cx', x.toFixed(1));
      dot.setAttribute('cy', y.toFixed(1));
      dot.setAttribute('r', 2.5);
      dot.setAttribute('fill', 'var(--accent)');
      const title = document.createElementNS('http://www.w3.org/2000/svg', 'title');
      title.textContent = points[i].date + ': ' + formatValue(points[i].value);
      dot.appendChild(title);
      svg.appendChild(dot);
    });

    wrap.appendChild(svg);

    const labels = document.createElement('div');
    labels.className = 'line-chart-labels';
    const first = document.createElement('span');
    first.textContent = points[0].date;
    const last = document.createElement('span');
    last.textContent = points[points.length - 1].date;
    labels.appendChild(first);
    labels.appendChild(last);
    wrap.appendChild(labels);

    return wrap;
  }

  // loadOverviewMetrics fetches /admin/api/overview/metrics -- everything
  // on the Overview page beyond the corpus summary loadCorpusOverview
  // already renders: crawl job/schedule health and DB pool utilization as
  // stat tiles, plus the tier-2 trend/breakdown charts as a second chart
  // row. A fetch failure here is reported in tilesEl and stops there,
  // without touching chartsEl -- it never blots out the corpus overview
  // row loadCorpusOverview already rendered above it.
  async function loadOverviewMetrics(tilesEl, chartsEl) {
    let m;
    try {
      m = await getJSON('/admin/api/overview/metrics');
    } catch (err) {
      tilesEl.textContent = 'Could not load operational metrics: ' + err.message;
      return;
    }

    clear(tilesEl);
    tilesEl.appendChild(buildTile('Running crawl jobs', String(m.running_crawl_jobs || 0)));
    tilesEl.appendChild(buildTile('Queued crawl jobs', String(m.queued_crawl_jobs || 0)));
    tilesEl.appendChild(buildTile('Schedules enabled', String(m.schedules_enabled || 0)));
    tilesEl.appendChild(buildTile('Schedules disabled', String(m.schedules_disabled || 0)));
    tilesEl.appendChild(buildTile('Schedules in progress', String(m.schedules_in_progress || 0)));
    tilesEl.appendChild(buildTile('Schedules overdue', String(m.schedules_overdue || 0)));
    if (m.pool) {
      tilesEl.appendChild(buildTile('DB connections in use', (m.pool.in_use || 0) + ' / ' + (m.pool.max_open_connections || 0)));
      tilesEl.appendChild(buildTile('DB connections idle', String(m.pool.idle || 0)));
      tilesEl.appendChild(buildTile('DB connections open', String(m.pool.open_connections || 0)));
    }
    if (m.pagerank_total_docs > 0) {
      const percent = (m.pagerank_orphan_percent || 0).toFixed(1);
      tilesEl.appendChild(buildTile('Orphan pages (pagerank ≤ ' + m.pagerank_orphan_threshold + ')',
        (m.pagerank_orphan_count || 0) + ' (' + percent + '%)'));
    }

    const row = document.createElement('div');
    row.className = 'overview-row';

    const runningJobs = m.running_jobs || [];
    if (runningJobs.length > 0) {
      const block = document.createElement('div');
      block.className = 'overview-block';
      const heading = document.createElement('h3');
      heading.textContent = 'Running crawl jobs';
      block.appendChild(heading);
      for (const j of runningJobs) {
        kvRow(block, seedSummary(j.seed_urls), j.pages_crawled + (j.pages_crawled === 1 ? ' page crawled' : ' pages crawled'));
      }
      row.appendChild(block);
    }

    const jobOutcomes = m.job_outcomes || [];
    if (jobOutcomes.length > 0) {
      const total = jobOutcomes.reduce((sum, o) => sum + o.count, 0);
      const block = document.createElement('div');
      block.className = 'overview-block';
      const heading = document.createElement('h3');
      heading.textContent = 'Crawl job outcomes (30d)';
      block.appendChild(heading);
      const donutWrap = document.createElement('div');
      donutWrap.className = 'donut-wrap';
      donutWrap.appendChild(buildJobOutcomeDonut(jobOutcomes, total));
      donutWrap.appendChild(buildJobOutcomeLegend(jobOutcomes));
      block.appendChild(donutWrap);
      row.appendChild(block);
    }

    const dailyFetchOutcomes = m.daily_fetch_outcomes || [];
    if (dailyFetchOutcomes.length > 0) {
      const block = document.createElement('div');
      block.className = 'overview-block';
      const heading = document.createElement('h3');
      heading.textContent = 'Fetch throughput (14d)';
      block.appendChild(heading);
      block.appendChild(buildThroughputStackBars(dailyFetchOutcomes));
      row.appendChild(block);
    }

    const documentsByDay = m.documents_by_day || [];
    if (documentsByDay.length > 0) {
      const block = document.createElement('div');
      block.className = 'overview-block';
      const heading = document.createElement('h3');
      heading.textContent = 'Documents indexed (30d)';
      block.appendChild(heading);
      const points = documentsByDay.map((d) => ({ date: d.date, value: d.count }));
      block.appendChild(buildLineChart(points, (v) => v + (v === 1 ? ' document' : ' documents')));
      row.appendChild(block);
    }

    const fetchDurationByDay = m.fetch_duration_by_day || [];
    if (fetchDurationByDay.length > 0) {
      const block = document.createElement('div');
      block.className = 'overview-block';
      const heading = document.createElement('h3');
      heading.textContent = 'Fetch duration trend (14d)';
      block.appendChild(heading);
      const points = fetchDurationByDay.map((d) => ({ date: d.date, value: d.avg_duration_ms }));
      block.appendChild(buildLineChart(points, (v) => v.toFixed(0) + ' ms avg'));
      row.appendChild(block);
    }

    const pageRankBuckets = m.pagerank_buckets || [];
    if (pageRankBuckets.length > 0) {
      const block = document.createElement('div');
      block.className = 'overview-block';
      const heading = document.createElement('h3');
      heading.textContent = 'PageRank distribution';
      block.appendChild(heading);
      block.appendChild(buildPageRankHistogram(pageRankBuckets));
      row.appendChild(block);
    }

    if (row.children.length > 0) chartsEl.appendChild(row);
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
    const chartsEl = document.getElementById('overview-charts');
    // loadCorpusOverview clears chartsEl before rendering its own row, so
    // it must run before loadOverviewMetrics appends its second row --
    // reversing the order would wipe out the tier-2 charts the instant the
    // corpus overview's own fetch resolved.
    await loadCorpusOverview(
      document.getElementById('stats'),
      document.getElementById('overview-status'),
      chartsEl
    );
    await loadOverviewMetrics(document.getElementById('overview-tiles'), chartsEl);
  }

  loadOverview();
  renderAdminNav();
  wireSignOut();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See admin_page.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      buildDonut, buildDonutLegend, buildAgeBars, buildVersionBars, buildStoredVersionsBars,
      buildJobOutcomeDonut, buildJobOutcomeLegend, buildThroughputStackBars,
      buildPageRankHistogram, buildLineChart,
      loadCorpusOverview, loadOverviewMetrics, loadOverview,
    };
  }
