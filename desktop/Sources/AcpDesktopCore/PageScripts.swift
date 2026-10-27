import Foundation

// Scripts the app runs inside the dashboard page, in its own content world: the page's code
// cannot see or replace what they call. They only read; they never change the page.

/// runningQueryBody is the body of an async function (WKWebView.callAsyncJavaScript) that asks acp
/// web, with the page's own session token, whether a change or a sign-in is running
/// (`GET /api/running`). It resolves to "running", "idle", or a short reason it could not tell.
/// It gives up after 1.5 s.
public let runningQueryBody = """
let token = null;
try { token = sessionStorage.getItem('acp.token'); } catch (_) { /* storage blocked */ }
if (!token) return 'no session token';
const ctl = new AbortController();
const timer = setTimeout(() => ctl.abort(), 1500);
try {
  const res = await fetch('/api/running', {
    headers: { Authorization: 'Bearer ' + token }, cache: 'no-store', signal: ctl.signal,
  });
  if (!res.ok) return 'HTTP ' + res.status;
  const body = await res.json();
  if (!body || typeof body.running !== 'boolean') return 'unexpected response';
  return body.running ? 'running' : 'idle';
} catch (e) {
  return 'request failed: ' + ((e && e.name) || 'error');
} finally {
  clearTimeout(timer);
}
"""

/// isRunning reads runningQueryBody's answer. Anything but a clear "running" (an error, a timeout,
/// no session) counts as not running, so quitting is never blocked by a page that cannot answer.
public func isRunning(_ answer: Any?) -> Bool {
    (answer as? String) == "running"
}

/// signedInCheck is an expression that tells whether the page exchanged its one-time code for a
/// session and drew the signed-in dashboard: a token in sessionStorage and the section tabs that
/// only render() draws. It is "ok", or what is missing. Smoke mode polls it.
public let signedInCheck = """
(() => {
  let token = null;
  try { token = sessionStorage.getItem('acp.token'); } catch (_) { /* storage blocked */ }
  if (document.querySelector('#app .card.signin')) return 'the sign-in card is shown';
  if (!token) return 'no session token in sessionStorage';
  if (/code=/.test(location.hash)) return 'the one-time code is still in the address';
  if (!document.querySelector('#app .shell nav.tabs')) return 'the dashboard has not rendered';
  return 'ok';
})()
"""
