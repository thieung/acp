import JavaScriptCore
import XCTest
@testable import AcpDesktopCore

/// Runs the page scripts in JavaScriptCore against a stubbed page: sessionStorage, fetch,
/// document and location are plain objects the test sets up.
final class PageScriptsTests: XCTestCase {
    private func page(token: String?, setup: String = "") -> JSContext {
        let ctx = JSContext()!
        ctx.exceptionHandler = { _, e in XCTFail("JS exception: \(e?.toString() ?? "?")") }
        let tokenJS = token.map { "'\($0)'" } ?? "null"
        ctx.evaluateScript("""
        var sessionStorage = { getItem: (k) => (k === 'acp.token' ? \(tokenJS) : null) };
        var timers = 0, cleared = 0;
        var setTimeout = () => ++timers, clearTimeout = () => { cleared++; };
        var AbortController = function () { this.signal = {}; this.abort = () => {}; };
        var requests = [];
        var reply = { ok: true, status: 200, body: { running: false } };
        var fetch = async (url, init) => {
          requests.push({ url, auth: init.headers.Authorization, cache: init.cache });
          if (reply.throws) throw { name: reply.throws };
          return { ok: reply.ok, status: reply.status, json: async () => reply.body };
        };
        var selectors = {};
        var document = { querySelector: (s) => (selectors[s] ? {} : null) };
        var location = { hash: '' };
        \(setup)
        """)
        return ctx
    }

    /// running resolves runningQueryBody the way callAsyncJavaScript does: as an async function.
    private func running(_ ctx: JSContext) -> String? {
        ctx.evaluateScript("var answer; (async () => { \(runningQueryBody) })().then((v) => { answer = v; });")
        return ctx.objectForKeyedSubscript("answer").toString()
    }

    func testRunningSendsThePagesTokenAndReadsTheAnswer() {
        let ctx = page(token: "t0k", setup: "reply.body = { running: true };")
        XCTAssertEqual(running(ctx), "running")
        XCTAssertEqual(ctx.evaluateScript("JSON.stringify(requests)").toString(),
                       #"[{"url":"/api/running","auth":"Bearer t0k","cache":"no-store"}]"#)
        XCTAssertEqual(ctx.evaluateScript("timers === 1 && cleared === 1").toBool(), true)

        let idle = page(token: "t0k")
        XCTAssertEqual(running(idle), "idle")
    }

    func testRunningWithoutASessionAsksNothing() {
        let ctx = page(token: nil)
        XCTAssertEqual(running(ctx), "no session token")
        XCTAssertEqual(ctx.evaluateScript("requests.length").toInt32(), 0)
    }

    func testRunningErrorsAreNotRunning() {
        let cases = [
            ("reply.ok = false; reply.status = 401;", "HTTP 401"),
            ("reply.ok = false; reply.status = 404;", "HTTP 404"),
            ("reply.throws = 'AbortError';", "request failed: AbortError"),
            ("reply.throws = 'TypeError';", "request failed: TypeError"),
            ("reply.body = { running: 'yes' };", "unexpected response"),
            ("reply.body = null;", "unexpected response"),
        ]
        for (setup, want) in cases {
            let answer = running(page(token: "t0k", setup: setup))
            XCTAssertEqual(answer, want, setup)
            XCTAssertFalse(isRunning(answer), setup)
        }
    }

    func testIsRunningOnlyForAClearYes() {
        XCTAssertTrue(isRunning("running"))
        for v: Any? in ["idle", "no session token", true, 1, nil, NSNull()] {
            XCTAssertFalse(isRunning(v), String(describing: v))
        }
    }

    private func signedIn(token: String?, setup: String) -> String? {
        page(token: token, setup: setup).evaluateScript(signedInCheck).toString()
    }

    func testSignedInNeedsTheTokenAndTheDashboard() {
        XCTAssertEqual(signedIn(token: "t0k", setup: "selectors['#app .shell nav.tabs'] = true;"), "ok")
    }

    func testNotSignedIn() {
        XCTAssertEqual(signedIn(token: nil, setup: "selectors['#app .card.signin'] = true;"), "the sign-in card is shown")
        XCTAssertEqual(signedIn(token: nil, setup: ""), "no session token in sessionStorage")
        XCTAssertEqual(signedIn(token: "", setup: "selectors['#app .shell nav.tabs'] = true;"),
                       "no session token in sessionStorage")
        XCTAssertEqual(signedIn(token: "t0k", setup: "location.hash = '#code=abc';"),
                       "the one-time code is still in the address")
        XCTAssertEqual(signedIn(token: "t0k", setup: ""), "the dashboard has not rendered") // still "Đang tải…"
    }
}
