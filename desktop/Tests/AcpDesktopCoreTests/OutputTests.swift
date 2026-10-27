import XCTest
@testable import AcpDesktopCore

final class OutputTests: XCTestCase {
    func testLineBufferJoinsChunks() {
        var b = LineBuffer()
        XCTAssertEqual(b.append(Data("acp web: http://127".utf8)), [])
        XCTAssertEqual(b.append(Data(".0.0.1:1\r\nsign in: x\nrest".utf8)), ["acp web: http://127.0.0.1:1", "sign in: x"])
        XCTAssertEqual(b.append(Data("\n".utf8)), ["rest"])
    }

    func testTailKeepsTheLastLines() {
        var t = Tail(maxLines: 2)
        t.append(Data("one\ntwo\n".utf8))
        t.append(Data("three\nfour".utf8))
        XCTAssertEqual(t.text, "three\nfour")
    }

    func testSparkleNeedsHTTPSFeedAndKey() {
        XCTAssertTrue(sparkleEnabled(feedURL: "https://example.com/appcast.xml", publicKey: "abc="))
        XCTAssertFalse(sparkleEnabled(feedURL: "http://example.com/appcast.xml", publicKey: "abc="))
        XCTAssertFalse(sparkleEnabled(feedURL: "@FEED_URL@", publicKey: "abc="))
        XCTAssertFalse(sparkleEnabled(feedURL: "https://example.com/appcast.xml", publicKey: "@PUBLIC_KEY@"))
        XCTAssertFalse(sparkleEnabled(feedURL: "https://example.com/appcast.xml", publicKey: ""))
        XCTAssertFalse(sparkleEnabled(feedURL: nil, publicKey: nil))
    }
}
