'use strict';
// dom_helper.test_util.js -- shared jsdom setup for *.test.js files in this
// directory. Named without a .test.js suffix so node's test runner never
// tries to execute it as a test file itself.
//
// The admin/search JS files (admin.js, admin_crawl.js, ...) are plain scripts
// meant for a <script> tag, not CommonJS modules -- they read/write
// document/window as ambient globals. setupDOM() points those globals at a
// fresh jsdom Document before each test requires the file under test, so
// its top-level DOM lookups (e.g. `document.getElementById(...)` at script
// load time) see the fixture HTML this test controls, not a real browser.
const { JSDOM } = require('jsdom');

function setupDOM(html) {
  const dom = new JSDOM(html || '<!doctype html><html><body></body></html>', {
    url: 'http://localhost/',
  });
  global.window = dom.window;
  global.document = dom.window.document;
  global.navigator = dom.window.navigator;
  return dom;
}

function teardownDOM() {
  delete global.window;
  delete global.document;
  delete global.navigator;
}

// requireFresh re-requires path with an empty module cache entry, so a
// script that runs top-level DOM lookups (e.g. `document.getElementById`
// at load time, as several admin pages' scripts do) sees the *current*
// test's fixture HTML rather than a stale one cached from an earlier test.
function requireFresh(path) {
  delete require.cache[require.resolve(path)];
  return require(path);
}

module.exports = { setupDOM, teardownDOM, requireFresh };
