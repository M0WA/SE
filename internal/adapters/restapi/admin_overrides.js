  const form = document.getElementById('overrides-form');
  const blockedTermsEl = document.getElementById('blocked-terms');
  const blockedDomainsEl = document.getElementById('blocked-domains');
  const boostedTermsEl = document.getElementById('boosted-terms');
  const boostedDomainsEl = document.getElementById('boosted-domains');
  const status = document.getElementById('overrides-status');

  // linesToText/parseLines now live in admin.js, shared with every other
  // page that has a one-value-per-line <textarea> field.

  function factorsToText(factors) {
    return Object.entries(factors || {}).map(([k, v]) => k + ' ' + v).join('\n');
  }

  function parseFactorLines(text) {
    const out = {};
    for (const line of parseLines(text)) {
      const parts = line.split(/\s+/);
      if (parts.length < 2) continue;
      const factor = parseFloat(parts[parts.length - 1]);
      if (Number.isNaN(factor)) continue;
      out[parts.slice(0, -1).join(' ')] = factor;
    }
    return out;
  }

  function applyOverrides(o) {
    blockedTermsEl.value = linesToText(o.blocked_terms);
    blockedDomainsEl.value = linesToText(o.blocked_domains);
    boostedTermsEl.value = factorsToText(o.boosted_terms);
    boostedDomainsEl.value = factorsToText(o.boosted_domains);
  }

  async function loadOverrides() {
    try {
      applyOverrides(await getJSON('/admin/api/overrides'));
    } catch (err) {
      status.textContent = 'Could not load overrides: ' + err.message;
    }
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    status.textContent = '';
    try {
      const o = await postJSON('/admin/api/overrides', {
        blocked_terms: parseLines(blockedTermsEl.value),
        blocked_domains: parseLines(blockedDomainsEl.value),
        boosted_terms: parseFactorLines(boostedTermsEl.value),
        boosted_domains: parseFactorLines(boostedDomainsEl.value),
      });
      applyOverrides(o);
      status.style.color = 'var(--ink-muted)';
      status.textContent = 'Saved.';
    } catch (err) {
      status.style.color = 'var(--accent)';
      status.textContent = 'Could not save: ' + err.message;
    }
  });

  wireSignOut();
  loadOverrides();
