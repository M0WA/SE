  const chatEnabledEl = document.getElementById('chat-enabled');
  const chatBaseURLEl = document.getElementById('chat-base-url');
  const chatModelEl = document.getElementById('chat-model');
  const chatAPIKeyEl = document.getElementById('chat-api-key');
  const chatClearAPIKeyEl = document.getElementById('chat-clear-api-key');
  const chatSystemPromptEl = document.getElementById('chat-system-prompt');
  const chatMaxContextTokensEl = document.getElementById('chat-max-context-tokens');
  const chatWebSearchEnabledEl = document.getElementById('chat-web-search-enabled');
  const chatWebSearchBaseURLEl = document.getElementById('chat-web-search-base-url');
  const chatWebSearchResultCountEl = document.getElementById('chat-web-search-result-count');
  const chatDefaultAgentEl = document.getElementById('chat-default-agent');
  const chatSettingsStatusEl = document.getElementById('chat-settings-status');
  const saveChatSettingsBtn = document.getElementById('save-chat-settings-btn');
  const chatTokenUsageEl = document.getElementById('chat-token-usage');

  // enabledServerPrompts is populated once by loadChatEndpoint (see below)
  // -- rendered every time the system prompt textarea changes, so an admin
  // sees the split update live while editing without waiting for a save.
  let enabledServerPrompts = [];

  // renderTokenUsageDonut is the static, settings-page counterpart of
  // index.js's per-turn donut: since there's no live chat turn here, it
  // estimates each piece the same way the backend's own estimateTokens
  // does (see admin.js's estimateTokensClient) from whatever's currently
  // in the form, rather than showing a real server-computed count. Global
  // prompt + MCP server prompts are shown against the configured max
  // conversation length as "remaining" budget for web-search context and
  // history -- when no budget is configured (0), there's nothing to show as
  // "remaining", so the chart just compares the two prompt pieces to each
  // other.
  function renderTokenUsageDonut() {
    clear(chatTokenUsageEl);
    const globalTokens = estimateTokensClient(chatSystemPromptEl.value);
    const toolTokens = enabledServerPrompts.reduce((sum, p) => sum + estimateTokensClient(p), 0);
    const maxTokens = Number.parseInt(chatMaxContextTokensEl.value, 10) || 0;
    const segments = [
      { label: 'Global prompt', value: globalTokens, color: 'var(--chart-1)' },
      { label: 'Active MCP server prompts', value: toolTokens, color: 'var(--chart-2)' },
    ];
    if (maxTokens > 0) {
      segments.push({ label: 'Remaining for context/history', value: Math.max(0, maxTokens - globalTokens - toolTokens), color: 'var(--rule)' });
    }
    chatTokenUsageEl.appendChild(buildDonutSVG(segments));
    chatTokenUsageEl.appendChild(buildDonutLegend(segments));
  }

  chatSystemPromptEl.addEventListener('input', renderTokenUsageDonut);
  chatMaxContextTokensEl.addEventListener('input', renderTokenUsageDonut);

  // applyChatEndpoint mirrors admin_embedding_endpoint.js's applyEndpoint API-
  // key masking: the server never echoes a stored key's real value, so this
  // field always starts blank -- has_api_key only drives the placeholder
  // text and whether "remove stored key" is available, never the field's
  // value.
  function applyChatEndpoint(c) {
    chatEnabledEl.checked = !!c.enabled;
    chatBaseURLEl.value = c.base_url || '';
    chatModelEl.value = c.model || '';
    chatAPIKeyEl.value = '';
    chatAPIKeyEl.placeholder = c.has_api_key ? 'Leave blank to keep the current key' : '';
    chatClearAPIKeyEl.checked = false;
    chatClearAPIKeyEl.disabled = !c.has_api_key;
    chatSystemPromptEl.value = c.system_prompt || '';
    chatMaxContextTokensEl.value = c.max_context_tokens || 0;
    chatWebSearchEnabledEl.checked = !!c.web_search_enabled;
    chatWebSearchBaseURLEl.value = c.web_search_base_url || '';
    chatWebSearchResultCountEl.value = c.web_search_result_count || 0;
    chatDefaultAgentEl.value = c.default_agent_id || '';
  }

  // loadAgentOptions populates the "Default agent" select from every
  // configured agent (Settings -> Chat -> Agents), including disabled ones
  // -- an admin can pick a not-yet-enabled agent as the default ahead of
  // enabling it. Best-effort, same convention as loadEnabledServerPrompts:
  // a failure here leaves the select at just its built-in "(none)" option
  // rather than blocking the rest of the page.
  async function loadAgentOptions() {
    while (chatDefaultAgentEl.options.length > 1) chatDefaultAgentEl.remove(1);
    try {
      const agents = await getJSON('/admin/api/agents');
      for (const a of agents) {
        const opt = document.createElement('option');
        opt.value = a.id;
        opt.textContent = a.name;
        chatDefaultAgentEl.appendChild(opt);
      }
    } catch (err) {
      // Leave just the "(none)" option in place.
    }
  }

  // loadEnabledServerPrompts fetches the MCP servers list from its own
  // settings page (Settings -> Chat -> MCP servers) purely to feed this
  // page's context-budget preview -- best-effort, same convention as every
  // other best-effort fetch on this page: a failure here shouldn't block
  // the chat endpoint's own settings from loading, so it just leaves the
  // server-prompt slice at 0 rather than surfacing an error.
  async function loadEnabledServerPrompts() {
    try {
      const servers = await getJSON('/admin/api/mcp-servers');
      enabledServerPrompts = servers.filter((s) => s.enabled && s.prompt).map((s) => s.prompt);
    } catch (err) {
      // Best-effort, per this function's doc comment above -- leave the
      // server-prompt slice at 0 rather than surfacing the error.
      enabledServerPrompts = [];
    }
  }

  // The chat-endpoint fetch, loadEnabledServerPrompts, and loadAgentOptions
  // hit disjoint endpoints and populate disjoint state -- run them
  // concurrently rather than one after another, so this page's load time
  // isn't paying for three round-trips back to back. loadAgentOptions must
  // still finish before applyChatEndpoint sets the select's value below
  // (Promise.allSettled waits for all three, so it always does).
  async function loadChatEndpoint() {
    const [endpointResult] = await Promise.allSettled([
      getJSON('/admin/api/chat-endpoint'),
      loadEnabledServerPrompts(),
      loadAgentOptions(),
    ]);
    if (endpointResult.status === 'fulfilled') {
      applyChatEndpoint(endpointResult.value);
    } else {
      chatSettingsStatusEl.style.color = 'var(--accent)';
      chatSettingsStatusEl.textContent = 'Could not load chat settings: ' + endpointResult.reason.message;
    }
    renderTokenUsageDonut();
  }

  async function saveChatEndpoint() {
    setButtonLoading(saveChatSettingsBtn, true, 'Saving…');
    chatSettingsStatusEl.textContent = '';
    try {
      await patchJSON('/admin/api/chat-endpoint', {
        base_url: chatBaseURLEl.value,
        api_key: chatAPIKeyEl.value,
        clear_api_key: chatClearAPIKeyEl.checked,
        model: chatModelEl.value,
        enabled: chatEnabledEl.checked,
        system_prompt: chatSystemPromptEl.value,
        max_context_tokens: Number.parseInt(chatMaxContextTokensEl.value, 10) || 0,
        web_search_enabled: chatWebSearchEnabledEl.checked,
        web_search_base_url: chatWebSearchBaseURLEl.value,
        web_search_result_count: Number.parseInt(chatWebSearchResultCountEl.value, 10) || 0,
        default_agent_id: chatDefaultAgentEl.value,
      });
      chatSettingsStatusEl.style.color = 'var(--ink-muted)';
      chatSettingsStatusEl.textContent = 'Saved.';
      // Re-fetch so the API-key field reflects the masked state (blank,
      // with a placeholder if one is now stored) rather than whatever was
      // just typed -- same post-save refresh as
      // admin_embedding_endpoint.js's submit handler.
      await loadChatEndpoint();
    } catch (err) {
      chatSettingsStatusEl.style.color = 'var(--accent)';
      chatSettingsStatusEl.textContent = 'Could not save: ' + err.message;
    } finally {
      setButtonLoading(saveChatSettingsBtn, false);
    }
  }

  saveChatSettingsBtn.addEventListener('click', saveChatEndpoint);

  renderAdminNav();
  wireSignOut();
  loadChatEndpoint();

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag, so this is a no-op there. See
  // internal/adapters/restapi/admin_chat_settings.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { applyChatEndpoint, loadChatEndpoint, saveChatEndpoint, renderTokenUsageDonut, loadAgentOptions };
  }
