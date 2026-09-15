// Offline contract check using the real SDK/runtime. No provider credentials or
// Internet access. Reaching the expected model route is the success condition.
import http from 'node:http';
import fs from 'node:fs';
fs.mkdirSync('/review', { recursive: true });
fs.writeFileSync('/review/prompt.md', 'Reply only with reviewd-sdk-contract.');
// Muse loads a catalog before creating sessions. This is a local cache fixture,
// not an upstream catalog-response fixture; the gateway passes real catalogs on.
const model = process.env.REVIEWD_MODEL;
fs.mkdirSync(process.env.HOME + '/.local/share/muse/model-catalog', { recursive: true });
fs.writeFileSync(process.env.HOME + '/.local/share/muse/model-catalog/6d657461__p746268.json', JSON.stringify({
  schema_version: 1, provider_id: 'meta', profile_id: 'tbh', source: 'provider_catalog',
  rows: [{ model_id: model, display_label: model, provider_id: 'meta', profile_id: 'tbh',
    visibility: 'visible', release_date: null, display_order: null, is_current: true,
    is_default: true, roles: [], context_limit: 200000, output_limit: 8000,
    description: null, cost: null, reasoning_effort_variants: [], supports_video: false }],
}));
const routes = JSON.parse(process.env.REVIEWD_TEST_ROUTES);
const server = http.createServer((request, response) => {
  if (request.method !== 'POST') { response.writeHead(403); response.end(); return; }
  const url = new URL(request.url, 'http://fixture');
  if (!routes.some(route => route.Path === url.pathname && (!url.search || route.Query === url.search.slice(1)))) {
    console.error('SDK requested unexpected model route', request.url);
    process.exit(1);
  }
  for (const name of ['authorization', 'x-api-key']) {
    const value = request.headers[name];
    if (value && !value.endsWith('reviewd-placeholder')) {
      console.error('SDK supplied an unexpected credential'); process.exit(1);
    }
  }
  let body = '';
  request.on('data', chunk => { body += chunk; });
  request.on('end', () => {
    if (!body.includes('reviewd-sdk-contract')) { console.error('SDK omitted prompt'); process.exit(1); }
    console.log('SDK model route verified:', request.method, request.url);
    // Exit before returning a model result: this test verifies transport wiring,
    // not paid model behavior. Docker --init cleans up SDK child processes.
    process.exit(0);
  });
});
await new Promise(resolve => server.listen(39123, '127.0.0.1', resolve));
setTimeout(() => { console.error('SDK did not reach its model route'); process.exit(1); }, 45000);
try { await import('/driver.mjs'); } catch (error) { console.error(error.message); process.exit(1); }
