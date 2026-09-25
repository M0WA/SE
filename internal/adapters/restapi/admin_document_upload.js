const uploadForm = document.getElementById('upload-form');
const uploadFileInput = document.getElementById('upload-file');
const uploadIndexVocabulary = document.getElementById('upload-index-vocabulary');
const uploadSubmitBtn = document.getElementById('upload-submit');
const uploadStatusEl = document.getElementById('upload-status');

const s3Form = document.getElementById('s3-form');
const s3SubmitBtn = document.getElementById('s3-submit');
const s3StatusEl = document.getElementById('s3-status');

const jobsStatusEl = document.getElementById('jobs-status');
const jobsTableEl = document.getElementById('jobs-table');

function jobSourceLabel(source) {
  return source === 's3' ? 'S3 import' : 'Upload';
}

function jobActionsCell(job) {
  const td = document.createElement('td');
  td.className = 'actions';
  const viewLink = document.createElement('a');
  viewLink.className = 'text-button';
  viewLink.href = '/admin/document-upload/' + encodeURIComponent(job.id);
  setIconLabel(viewLink, 'view', 'View');
  td.appendChild(viewLink);
  const delBtn = document.createElement('button');
  delBtn.type = 'button';
  delBtn.className = 'text-button';
  setIconLabel(delBtn, 'delete', 'Delete');
  delBtn.addEventListener('click', () => deleteJob(job));
  td.appendChild(delBtn);
  return td;
}

function renderJobs(jobs) {
  clear(jobsTableEl);
  if (jobs.length === 0) {
    jobsStatusEl.textContent = 'No uploads yet -- use the form above to index one.';
    return;
  }
  jobsStatusEl.textContent = '';
  const table = buildTable(
    [{ label: 'filename' }, { label: 'content type' }, { label: 'size', num: true }, { label: 'source' }, { label: 'status' }, { label: 'created' }, { label: '' }],
    jobs,
    (job) => [
      textCell(job.filename),
      textCell(job.content_type),
      textCell(String(job.size), { num: true }),
      textCell(jobSourceLabel(job.source)),
      textCell(job.status),
      textCell(formatTimestamp(job.created_at)),
      jobActionsCell(job),
    ],
  );
  jobsTableEl.appendChild(table);
}

async function loadJobs() {
  try {
    renderJobs(await getJSON('/admin/api/document-jobs'));
  } catch (err) {
    jobsStatusEl.textContent = 'Could not load uploads: ' + err.message;
  }
}

async function deleteJob(job) {
  if (!window.confirm('Delete "' + job.filename + '"? This also removes its indexed document, if any.')) return;
  try {
    await deleteRequest('/admin/api/document-jobs/' + encodeURIComponent(job.id));
    await loadJobs();
  } catch (err) {
    window.alert('Could not delete: ' + err.message);
  }
}

uploadForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  const file = uploadFileInput.files[0];
  if (!file) return;
  setButtonLoading(uploadSubmitBtn, true, 'Uploading…');
  uploadStatusEl.textContent = '';
  try {
    const body = new FormData();
    body.append('file', file);
    body.append('index_vocabulary', uploadIndexVocabulary.checked ? 'true' : 'false');
    await checkResponse(await fetch('/admin/api/document-jobs', { method: 'POST', body }));
    uploadForm.reset();
    uploadIndexVocabulary.checked = true;
    uploadStatusEl.textContent = 'Uploaded -- indexing in the background.';
    await loadJobs();
  } catch (err) {
    uploadStatusEl.textContent = 'Could not upload: ' + err.message;
  } finally {
    setButtonLoading(uploadSubmitBtn, false);
  }
});

s3Form.addEventListener('submit', async (e) => {
  e.preventDefault();
  setButtonLoading(s3SubmitBtn, true, 'Importing…');
  s3StatusEl.textContent = '';
  try {
    await postJSON('/admin/api/document-jobs/import-s3', {
      endpoint: document.getElementById('s3-endpoint').value.trim(),
      region: document.getElementById('s3-region').value.trim(),
      bucket: document.getElementById('s3-bucket').value.trim(),
      key: document.getElementById('s3-key').value.trim(),
      access_key_id: document.getElementById('s3-access-key-id').value.trim(),
      secret_access_key: document.getElementById('s3-secret-access-key').value,
      session_token: document.getElementById('s3-session-token').value,
      index_vocabulary: document.getElementById('s3-index-vocabulary').checked,
    });
    s3Form.reset();
    document.getElementById('s3-index-vocabulary').checked = true;
    s3StatusEl.textContent = 'Imported -- indexing in the background.';
    await loadJobs();
  } catch (err) {
    s3StatusEl.textContent = 'Could not import: ' + err.message;
  } finally {
    setButtonLoading(s3SubmitBtn, false);
  }
});

renderAdminNav();
wireSignOut();
loadJobs();

// Node test-runner export only; no-op in a browser <script> tag.
if (typeof module !== 'undefined' && module.exports) {
  module.exports = { renderJobs, loadJobs, deleteJob, jobSourceLabel };
}
