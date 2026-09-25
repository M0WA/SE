function documentJobIDFromPath() {
  const parts = window.location.pathname.split('/');
  return decodeURIComponent(parts[parts.length - 1]);
}

function renderJobMeta(job) {
  const container = document.getElementById('job-meta');
  clear(container);
  kvRow(container, 'Filename', job.filename);
  kvRow(container, 'Content type', job.content_type);
  kvRow(container, 'Size', String(job.size) + ' bytes');
  kvRow(container, 'Source', job.source === 's3' ? 'S3 import' : 'Upload');
  kvRow(container, 'Vocabulary indexed', job.index_vocabulary ? 'Yes' : 'No');
  kvRow(container, 'Status', job.status);
  if (job.error) kvRow(container, 'Error', job.error);
  if (job.doc_id) kvRow(container, 'Document ID', job.doc_id);
  kvRow(container, 'Created', formatTimestamp(job.created_at));
  if (job.started_at) kvRow(container, 'Started', formatTimestamp(job.started_at));
  if (job.finished_at) kvRow(container, 'Finished', formatTimestamp(job.finished_at));
}

async function loadJobDetail(id) {
  const statusEl = document.getElementById('job-status');
  try {
    const job = await getJSON('/admin/api/document-jobs/' + encodeURIComponent(id));
    document.getElementById('job-title').textContent = job.filename;
    statusEl.textContent = '';
    renderJobMeta(job);

    const isImage = job.content_type && job.content_type.startsWith('image/');
    if (isImage) {
      const wrap = document.getElementById('image-preview-wrap');
      wrap.hidden = false;
      document.getElementById('image-preview').src = '/admin/api/document-jobs/' + encodeURIComponent(id) + '/data';
    }

    // An image job's resulting document has no text (see the backend's
    // classifyDocumentContent) -- skip the request rather than fetching it
    // just to find that out.
    if (!isImage && job.status === 'done' && job.doc_id) {
      try {
        const doc = await getJSON('/admin/api/documents/' + encodeURIComponent(job.doc_id));
        const wrap = document.getElementById('text-preview-wrap');
        if (doc.text) {
          wrap.hidden = false;
          document.getElementById('text-preview').textContent = doc.text;
        }
      } catch (err) {
        // The indexed document may have since been deleted independently --
        // not fatal to showing the job's own metadata above.
      }
    }

    document.getElementById('delete-btn').addEventListener('click', async () => {
      if (!window.confirm('Delete "' + job.filename + '"? This also removes its indexed document, if any.')) return;
      try {
        await deleteRequest('/admin/api/document-jobs/' + encodeURIComponent(id));
        window.location.href = '/admin/document-upload';
      } catch (err) {
        window.alert('Could not delete: ' + err.message);
      }
    });
  } catch (err) {
    statusEl.textContent = 'Could not load document job: ' + err.message;
  }
}

renderAdminNav();
wireSignOut();
loadJobDetail(documentJobIDFromPath());

// Node test-runner export only; no-op in a browser <script> tag.
if (typeof module !== 'undefined' && module.exports) {
  module.exports = { documentJobIDFromPath, renderJobMeta, loadJobDetail };
}
