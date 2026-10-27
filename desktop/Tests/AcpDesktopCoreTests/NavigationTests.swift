import XCTest
@testable import AcpDesktopCore

final class NavigationTests: XCTestCase {
    let port = 53124

    private func kind(_ s: String) -> NavigationKind {
        guard let url = URL(string: s) else { return .blocked }
        return classify(url, port: port)
    }

    func testDashboardIsSameOrigin() {
        XCTAssertEqual(kind("http://127.0.0.1:53124/"), .dashboard)
        XCTAssertEqual(kind("http://127.0.0.1:53124/#code=ab"), .dashboard)
        XCTAssertEqual(kind("http://localhost:53124/api/status"), .dashboard)
    }

    func testExternalIsHTTPSAgentKitOrGitHub() {
        XCTAssertEqual(kind("https://agentkit.best/x"), .external)
        XCTAssertEqual(kind("https://docs.agentkit.best/guide"), .external)
        XCTAssertEqual(kind("https://github.com/thieung/acp"), .external)
    }

    func testEverythingElseIsBlocked() {
        let blocked = [
            "http://agentkit.best",
            "http://github.com/thieung/acp",
            "https://evilagentkit.best/",
            "https://agentkit.best.evil.com/",
            "https://agentkit.best:8443/",
            "https://user@agentkit.best/",
            "https://gist.github.com/x",
            "file:///etc/passwd",
            "smb://x",
            "vscode://x",
            "javascript:alert(1)",
            "data:text/html,hi",
            "blob:http://127.0.0.1:53124/0e2f",
            "about:blank",
            "http://127.0.0.1:53125/",
            "http://127.0.0.1/",
            "https://127.0.0.1:53124/",
            "http://user@127.0.0.1:53124/",
            "http://10.0.0.5:53124/",
        ]
        for s in blocked {
            XCTAssertEqual(kind(s), .blocked, s)
        }
    }
}
