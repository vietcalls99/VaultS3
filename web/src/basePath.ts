// Runtime base path for hosting the dashboard under a reverse-proxy subpath
// (issue #36). The Go server injects `window.__VAULTS3_BASE__` into index.html
// from `server.base_path` / `VAULTS3_BASE_PATH` or the `X-Forwarded-Prefix`
// header. Empty by default, so a normal deployment is unchanged.
function normalize(raw: unknown): string {
  let p = typeof raw === 'string' ? raw.trim() : ''
  if (!p || p === '/') return ''
  if (!p.startsWith('/')) p = '/' + p
  return p.replace(/\/+$/, '') // no trailing slash
}

export const BASE_PATH = normalize(
  typeof window !== 'undefined' ? (window as { __VAULTS3_BASE__?: string }).__VAULTS3_BASE__ : '',
)

// The dashboard SPA lives at <base>/dashboard, the admin API at <base>/api/v1.
export const DASHBOARD_BASE = `${BASE_PATH}/dashboard`
export const API_BASE = `${BASE_PATH}/api/v1`
