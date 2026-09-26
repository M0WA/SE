  const chatEnabledEl = document.getElementById('chat-enabled');
  const chatBaseURLEl = document.getElementById('chat-base-url');
  const chatModelEl = document.getElementById('chat-model');
  const chatCompletionTimeoutSecondsEl = document.getElementById('chat-completion-timeout-seconds');
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

  const visionSimilarityEnabledEl = document.getElementById('vision-similarity-enabled');
  const visionSimilarityProviderEl = document.getElementById('vision-similarity-provider');
  const visionCaptionEnabledEl = document.getElementById('vision-caption-enabled');
  const visionCaptionBaseURLEl = document.getElementById('vision-caption-base-url');
  const visionCaptionModelEl = document.getElementById('vision-caption-model');
  const visionCaptionAPIKeyEl = document.getElementById('vision-caption-api-key');
  const visionClearCaptionAPIKeyEl = document.getElementById('vision-clear-caption-api-key');
  const saveVisionSettingsBtn = document.getElementById('save-vision-settings-btn');
  const visionSettingsStatusEl = document.getElementById('vision-settings-status');

  // enabledServerPrompts is populated once by loadChatEndpoint, then re-rendered on every prompt
  // textarea change, so the split updates live without needing a save.
  let enabledServerPrompts = [];

  // renderTokenUsageDonut is the settings-page counterpart of index.js's per-turn donut: with no
  // live turn to read, it estimates each piece from the form via admin.js's estimateTokensClient.
  // Prompts are shown against max conversation length as "remaining" budget; at 0 budget, it just
  // compares the two prompt pieces.
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

  // applyChatEndpoint mirrors admin_embedding_endpoint.js's API-key masking: the server never
  // echoes a stored key, so the field starts blank; has_api_key only drives the placeholder/
  // remove-key UI.
  function applyChatEndpoint(c) {
    chatEnabledEl.checked = !!c.enabled;
    chatBaseURLEl.value = c.base_url || '';
    chatModelEl.value = c.model || '';
    chatCompletionTimeoutSecondsEl.value = c.completion_timeout_seconds || 0;
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

  // loadAgentOptions populates "Default agent" from every configured agent, including disabled
  // ones (so one can be pre-selected before enabling). Best-effort: a failure just leaves the
  // built-in "(none)" option.
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

  // loadEnabledServerPrompts fetches the MCP servers list purely to feed the context-budget
  // preview -- best-effort: a failure leaves the server-prompt slice at 0 rather than blocking
  // the page.
  async function loadEnabledServerPrompts() {
    try {
      const servers = await getJSON('/admin/api/mcp-servers');
      enabledServerPrompts = servers.filter((s) => s.enabled && s.prompt).map((s) => s.prompt);
    } catch (err) {
      // Best-effort, per this function's doc comment above -- leave the
      // server-prompt slice at 0 rather than surfacing the error, but log
      // it for anyone debugging from the console.
      console.error('loading MCP server prompts for context-budget preview failed:', err);
      enabledServerPrompts = [];
    }
  }

  // The chat-endpoint fetch, loadEnabledServerPrompts, and loadAgentOptions hit disjoint endpoints
  // -- run concurrently, not three round-trips in sequence. loadAgentOptions must finish before
  // applyChatEndpoint sets the select (Promise.allSettled guarantees this).
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
        completion_timeout_seconds: Number.parseInt(chatCompletionTimeoutSecondsEl.value, 10) || 0,
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
      // Re-fetch so the API-key field reflects the masked state, not what was just typed --
      // same post-save refresh as admin_embedding_endpoint.js.
      await loadChatEndpoint();
    } catch (err) {
      chatSettingsStatusEl.style.color = 'var(--accent)';
      chatSettingsStatusEl.textContent = 'Could not save: ' + err.message;
    } finally {
      setButtonLoading(saveChatSettingsBtn, false);
    }
  }

  saveChatSettingsBtn.addEventListener('click', saveChatEndpoint);

  // loadEmbeddingProviderOptions populates "Embedding provider" from every configured HTTP
  // embedding endpoint, mirroring loadAgentOptions' pattern -- best-effort: a failure just leaves
  // the built-in "(none)" option.
  async function loadEmbeddingProviderOptions() {
    while (visionSimilarityProviderEl.options.length > 1) visionSimilarityProviderEl.remove(1);
    try {
      const endpoints = await getJSON('/admin/api/embeddings/endpoints');
      for (const e of endpoints) {
        const opt = document.createElement('option');
        opt.value = e.id;
        opt.textContent = e.name + ' (' + e.id + ')';
        visionSimilarityProviderEl.appendChild(opt);
      }
    } catch (err) {
      // Leave just the "(none)" option in place.
    }
  }

  // applyChatVision mirrors applyChatEndpoint's API-key masking convention exactly.
  function applyChatVision(v) {
    visionSimilarityEnabledEl.checked = !!v.similarity_enabled;
    visionSimilarityProviderEl.value = v.similarity_provider_id || '';
    visionCaptionEnabledEl.checked = !!v.caption_enabled;
    visionCaptionBaseURLEl.value = v.caption_base_url || '';
    visionCaptionModelEl.value = v.caption_model || '';
    visionCaptionAPIKeyEl.value = '';
    visionCaptionAPIKeyEl.placeholder = v.has_caption_api_key ? 'Leave blank to keep the current key' : '';
    visionClearCaptionAPIKeyEl.checked = false;
    visionClearCaptionAPIKeyEl.disabled = !v.has_caption_api_key;
  }

  // The chat-vision fetch and loadEmbeddingProviderOptions hit disjoint endpoints -- run
  // concurrently, same reasoning as loadChatEndpoint. loadEmbeddingProviderOptions must finish
  // before applyChatVision sets the select (Promise.allSettled guarantees this).
  async function loadChatVision() {
    const [visionResult] = await Promise.allSettled([
      getJSON('/admin/api/chat-vision'),
      loadEmbeddingProviderOptions(),
    ]);
    if (visionResult.status === 'fulfilled') {
      applyChatVision(visionResult.value);
    } else {
      visionSettingsStatusEl.style.color = 'var(--accent)';
      visionSettingsStatusEl.textContent = 'Could not load vision settings: ' + visionResult.reason.message;
    }
  }

  async function saveChatVision() {
    setButtonLoading(saveVisionSettingsBtn, true, 'Saving…');
    visionSettingsStatusEl.textContent = '';
    try {
      await patchJSON('/admin/api/chat-vision', {
        similarity_enabled: visionSimilarityEnabledEl.checked,
        similarity_provider_id: visionSimilarityProviderEl.value,
        caption_enabled: visionCaptionEnabledEl.checked,
        caption_base_url: visionCaptionBaseURLEl.value,
        caption_model: visionCaptionModelEl.value,
        caption_api_key: visionCaptionAPIKeyEl.value,
        clear_caption_api_key: visionClearCaptionAPIKeyEl.checked,
      });
      visionSettingsStatusEl.style.color = 'var(--ink-muted)';
      visionSettingsStatusEl.textContent = 'Saved.';
      // Re-fetch so the API-key field reflects the masked state, same post-save refresh as
      // saveChatEndpoint.
      await loadChatVision();
    } catch (err) {
      visionSettingsStatusEl.style.color = 'var(--accent)';
      visionSettingsStatusEl.textContent = 'Could not save: ' + err.message;
    } finally {
      setButtonLoading(saveVisionSettingsBtn, false);
    }
  }

  saveVisionSettingsBtn.addEventListener('click', saveChatVision);

  renderAdminNav();
  wireSignOut();
  loadChatEndpoint();
  loadChatVision();

  // Node test-runner export only; no-op in a browser <script> tag.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      applyChatEndpoint, loadChatEndpoint, saveChatEndpoint, renderTokenUsageDonut, loadAgentOptions,
      applyChatVision, loadChatVision, saveChatVision, loadEmbeddingProviderOptions,
    };
  }
