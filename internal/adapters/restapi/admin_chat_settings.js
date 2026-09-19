  const chatEnabledEl = document.getElementById('chat-enabled');
  const chatBaseURLEl = document.getElementById('chat-base-url');
  const chatModelEl = document.getElementById('chat-model');
  const chatAPIKeyEl = document.getElementById('chat-api-key');
  const chatClearAPIKeyEl = document.getElementById('chat-clear-api-key');
  const chatRAGEnabledEl = document.getElementById('chat-rag-enabled');
  const chatRAGResultCountEl = document.getElementById('chat-rag-result-count');
  const chatMaxContextTokensEl = document.getElementById('chat-max-context-tokens');
  const chatWebSearchEnabledEl = document.getElementById('chat-web-search-enabled');
  const chatWebSearchBaseURLEl = document.getElementById('chat-web-search-base-url');
  const chatWebSearchResultCountEl = document.getElementById('chat-web-search-result-count');
  const chatSettingsStatusEl = document.getElementById('chat-settings-status');
  const saveChatSettingsBtn = document.getElementById('save-chat-settings-btn');

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
    chatRAGEnabledEl.checked = c.rag_enabled !== false;
    chatRAGResultCountEl.value = c.rag_result_count;
    chatMaxContextTokensEl.value = c.max_context_tokens || 0;
    chatWebSearchEnabledEl.checked = !!c.web_search_enabled;
    chatWebSearchBaseURLEl.value = c.web_search_base_url || '';
    chatWebSearchResultCountEl.value = c.web_search_result_count;
  }

  async function loadChatEndpoint() {
    try {
      applyChatEndpoint(await getJSON('/admin/api/chat-endpoint'));
    } catch (err) {
      chatSettingsStatusEl.style.color = 'var(--accent)';
      chatSettingsStatusEl.textContent = 'Could not load chat settings: ' + err.message;
    }
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
        rag_enabled: chatRAGEnabledEl.checked,
        rag_result_count: parseInt(chatRAGResultCountEl.value, 10),
        max_context_tokens: parseInt(chatMaxContextTokensEl.value, 10) || 0,
        web_search_enabled: chatWebSearchEnabledEl.checked,
        web_search_base_url: chatWebSearchBaseURLEl.value,
        web_search_result_count: parseInt(chatWebSearchResultCountEl.value, 10),
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
    module.exports = { applyChatEndpoint, loadChatEndpoint, saveChatEndpoint };
  }
