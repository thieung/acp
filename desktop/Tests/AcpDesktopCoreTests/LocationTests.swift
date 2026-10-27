import XCTest
@testable import AcpDesktopCore

final class LocationTests: XCTestCase {
    let home = "/Users/u"

    func testApplicationsFoldersStay() {
        XCTAssertFalse(needsMove(bundlePath: "/Applications/acp.app", home: home))
        XCTAssertFalse(needsMove(bundlePath: "/Applications/Utilities/acp.app", home: home))
        XCTAssertFalse(needsMove(bundlePath: "/Users/u/Applications/acp.app", home: home))
    }

    func testEverywhereElseMoves() {
        let paths = [
            "/Users/u/Downloads/acp.app",
            "/Volumes/acp/acp.app",
            "/private/var/folders/xy/abc/T/AppTranslocation/0F1E/d/acp.app",
            "/Applications/../tmp/acp.app",
            "/ApplicationsX/acp.app",
            "/Users/other/Applications/acp.app",
        ]
        for p in paths {
            XCTAssertTrue(needsMove(bundlePath: p, home: home), p)
        }
    }
}
