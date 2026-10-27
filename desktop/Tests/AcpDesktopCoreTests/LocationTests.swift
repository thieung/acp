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

final class InstalledCopyTests: XCTestCase {
    func testParsesTheFourNumbersBundleVersionMakes() {
        XCTAssertEqual(parseBundleVersion("0.1.0.2002"), [0, 1, 0, 2002])
        XCTAssertEqual(parseBundleVersion("1.0.0.9000"), [1, 0, 0, 9000])
        for bad in ["", "0.1.0", "0.1.0.2002.1", "0.1.0-beta.2", "@VERSION@", "0.1..2", "0.1.0.x", " 0.1.0.1", "-1.0.0.1"] {
            XCTAssertNil(parseBundleVersion(bad), bad)
        }
    }

    func testNewerOrSameInstalledCopyIsKept() {
        XCTAssertEqual(installedCopy(installed: "0.1.0.2002", running: "0.1.0.2002"), .sameOrNewer)
        XCTAssertEqual(installedCopy(installed: "0.1.0.9000", running: "0.1.0.2002"), .sameOrNewer)
        XCTAssertEqual(installedCopy(installed: "0.2.0.1001", running: "0.1.9.9000"), .sameOrNewer)
        // Numbers, not strings: beta.10 (2010) is newer than beta.9 (2009).
        XCTAssertEqual(installedCopy(installed: "0.1.0.2010", running: "0.1.0.2009"), .sameOrNewer)
    }

    func testOlderInstalledCopyIsReplaced() {
        XCTAssertEqual(installedCopy(installed: "0.1.0.2002", running: "0.1.0.2003"), .older)
        XCTAssertEqual(installedCopy(installed: "0.1.0.2009", running: "0.1.0.2010"), .older)
        XCTAssertEqual(installedCopy(installed: "0.9.9.9000", running: "1.0.0.0"), .older)
    }

    func testUnreadableVersionIsUnknown() {
        XCTAssertEqual(installedCopy(installed: nil, running: "0.1.0.2002"), .unknown)
        XCTAssertEqual(installedCopy(installed: "@VERSION@", running: "0.1.0.2002"), .unknown)
        XCTAssertEqual(installedCopy(installed: "0.1.0.2002", running: nil), .unknown)
        XCTAssertEqual(installedCopy(installed: "0.1.0.2002", running: "0.1.0"), .unknown)
    }
}
