  const params = new URLSearchParams(window.location.search);
  const next = params.get('next') || '/admin';

  const form = document.getElementById('login-form');
  const username = document.getElementById('username');
  const password = document.getElementById('password');
  const error = document.getElementById('login-error');

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    error.textContent = '';
    try {
      const resp = await fetch('/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          username: username.value,
          password: password.value,
          next: next,
        }),
      });
      if (!resp.ok) {
        error.textContent = resp.status === 401
          ? 'Incorrect username or password.'
          : 'Sign-in failed: could not reach the server.';
        password.value = '';
        password.focus();
        return;
      }
      const data = await resp.json();
      window.location = data.redirect || '/admin';
    } catch (err) {
      error.textContent = 'Sign-in failed: could not reach the server.';
    }
  });

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag. See login.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { next };
  }
