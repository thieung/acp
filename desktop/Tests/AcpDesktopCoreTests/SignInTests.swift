import XCTest
@testable import AcpDesktopCore

final class SignInTests: XCTestCase {
    func testAcceptsTheLineAcpWebPrints() throws {
        let got = try XCTUnwrap(parseSignIn("sign in: http://127.0.0.1:53124/#code=0123456789abcdef"))
        XCTAssertEqual(got.port, 53124)
        XCTAssertEqual(got.url.absoluteString, "http://127.0.0.1:53124/#code=0123456789abcdef")
    }

    func testRejectsAnythingElse() {
        let bad = [
            "sign in: http://localhost:53124/#code=ab",          // not the loopback literal acp prints
            "sign in: http://10.0.0.5:53124/#code=ab",           // not loopback
            "sign in: http://127.0.0.1.evil.com:53124/#code=ab",
            "sign in: https://127.0.0.1:53124/#code=ab",
            "sign in: http://127.0.0.1:53124/#code=",            // missing code
            "sign in: http://127.0.0.1:53124/",
            "sign in: http://127.0.0.1:53124/#code=ABCDEF",      // uppercase
            "sign in: http://127.0.0.1:53124/#code=abz",
            "sign in: http://127.0.0.1:53124/x#code=ab",         // extra path
            "sign in: http://127.0.0.1:53124/?q=1#code=ab",      // query
            "sign in: http://127.0.0.1:53124/#code=ab&x=1",
            "sign in: http://127.0.0.1/#code=ab",                // no port
            "sign in: http://127.0.0.1:0/#code=ab",
            "sign in: http://127.0.0.1:053124/#code=ab",
            "sign in: http://127.0.0.1:70000/#code=ab",
            "sign in: http://user@127.0.0.1:53124/#code=ab",
            "xsign in: http://127.0.0.1:53124/#code=ab",
            "sign in: http://127.0.0.1:53124/#code=ab ",
            "acp web: http://127.0.0.1:53124 (Ctrl-C to stop)",
            "",
        ]
        for line in bad {
            XCTAssertNil(parseSignIn(line), line)
        }
    }

    func testWithoutCodeDropsTheFragment() throws {
        let url = try XCTUnwrap(URL(string: "http://127.0.0.1:53124/#code=0123abcd"))
        XCTAssertEqual(withoutCode(url), "http://127.0.0.1:53124/")
    }
}
