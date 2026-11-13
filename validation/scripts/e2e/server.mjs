// Server contract: retired routes and missing assets are 404, the app shell
// is served for SPA routes, and ES modules use a JavaScript MIME type.
import assert from 'node:assert/strict';

export async function runServer(h) {
  await h.check('retired legacy/v2/shared routes and missing /ui assets return 404', async () => {
    for (const route of ['/legacy/', '/legacy', '/v2-assets/x', '/shared-assets/x', '/ui/missing.js']) {
      const result = await h.api('GET', route);
      assert.equal(result.status, 404, route + ' -> ' + result.status);
      assert.doesNotMatch(result.text, /<div id="app"/, route + ' must not fall back to the app shell');
    }
  });
  await h.check('ES modules and styles are served with the right MIME types', async () => {
    for (const asset of ['/ui/src/main.js', '/ui/vendor/preact.module.js', '/ui/vendor/htm.module.js', '/ui/src/lib/html.js']) {
      const result = await h.api('GET', asset);
      assert.equal(result.status, 200, asset);
      assert.match(result.headers.get('content-type') || '', /javascript/, asset + ' content-type');
    }
    const css = await h.api('GET', '/ui/styles/app.css');
    assert.equal(css.status, 200);
    assert.match(css.headers.get('content-type') || '', /text\/css/);
  });
  await h.check('app shell for / and SPA routes; CSP blocks inline scripts; no external CDN', async () => {
    for (const route of ['/', '/s/does-not-exist', '/sessions', '/skills', '/workspace', '/settings', '/not/a/route']) {
      const result = await h.api('GET', route);
      assert.equal(result.status, 200, route);
      assert.match(result.text, /<script type="module" src="\/ui\/src\/main\.js"/, route + ' serves index.html');
      assert.match(result.headers.get('content-security-policy') || '', /script-src 'self'/);
      assert.doesNotMatch(result.text, /https?:\/\/(?!127\.0\.0\.1)/, route + ' has no external URLs');
    }
    const api404 = await h.api('GET', '/api/not-a-route');
    assert.equal(api404.status, 404, '/api/* never falls back to the shell');
  });
}
