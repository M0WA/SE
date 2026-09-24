'use strict';
// dom_helper.test_util.js -- shared jsdom setup for *.test.js files here. Named without .test.js
// so node's test runner doesn't execute it directly.
//
// Admin/search JS files are plain <script>-tag scripts, not CommonJS modules -- they read/write
// document/window as ambient globals. setupDOM() points those at a fresh jsdom Document before
// each test requires the file under test, so top-level DOM lookups see this test's fixture HTML,
// not a real browser.
const { JSDOM } = require('jsdom');

function setupDOM(html, url) {
  const dom = new JSDOM(html || '<!doctype html><html><body></body></html>', {
    url: url || 'http://localhost/',
  });
  global.window = dom.window;
  global.document = dom.window.document;
  // Node 21+ defines a getter-only `navigator` on globalThis -- plain assignment throws in
  // strict mode. It's configurable (just setter-less), so defineProperty can still replace it
  // with jsdom's Navigator for the test.
  Object.defineProperty(global, 'navigator', {
    value: dom.window.navigator, configurable: true, writable: true,
  });

  // jsdom implements neither <dialog>'s showModal()/close() nor Element.scrollIntoView() -- both
  // are real, no-layout-required browser APIs a script here calls, so without a stub any test
  // touching either throws "not a function" instead of exercising the code around it. Actually
  // showing/scrolling still needs a real browser (see CLAUDE.md's screenshot recipe for that) --
  // these just make the call safe, as a no-op a test can still override/spy on, the same way
  // index.test.js already stubs offsetTop on window.HTMLElement.prototype.
  dom.window.HTMLDialogElement.prototype.showModal = function () { this.setAttribute('open', ''); };
  dom.window.HTMLDialogElement.prototype.close = function () {
    this.removeAttribute('open');
    this.dispatchEvent(new dom.window.Event('close'));
  };
  dom.window.HTMLElement.prototype.scrollIntoView = function () {};

  return dom;
}

function teardownDOM() {
  delete global.window;
  delete global.document;
  delete global.navigator;
}

// requireFresh re-requires path with an empty module cache entry, so a script with top-level DOM
// lookups sees the current test's fixture HTML, not one cached from an earlier test.
function requireFresh(path) {
  delete require.cache[require.resolve(path)];
  return require(path);
}

module.exports = { setupDOM, teardownDOM, requireFresh };
