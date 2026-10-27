import Foundation

/// needsMove reports whether the app runs from somewhere it should not stay: anywhere outside
/// /Applications or ~/Applications, a mounted disk image, or an App Translocation copy. acp links
/// the CLI and registers MCP to the bundled binary, so that path must not vanish.
public func needsMove(bundlePath: String, home: String = NSHomeDirectory()) -> Bool {
    let path = (bundlePath as NSString).standardizingPath
    if path.hasPrefix("/Volumes/") || path.contains("/AppTranslocation/") {
        return true
    }
    let homeApps = (home as NSString).standardizingPath + "/Applications/"
    return !(path.hasPrefix("/Applications/") || path.hasPrefix(homeApps))
}

/// parseBundleVersion reads a CFBundleVersion as scripts/bundle-version.sh writes it: exactly four
/// dot-separated decimal numbers (0.1.0.2002 is 0.1.0-beta.2). Anything else is nil.
public func parseBundleVersion(_ s: String) -> [Int]? {
    let parts = s.split(separator: ".", omittingEmptySubsequences: false)
    guard parts.count == 4 else { return nil }
    var out: [Int] = []
    for p in parts {
        guard !p.isEmpty, p.count <= 9, p.allSatisfy({ ("0"..."9").contains($0) }), let n = Int(p) else { return nil }
        out.append(n)
    }
    return out
}

public enum InstalledCopy: Equatable {
    case older        // replace it with this copy
    case sameOrNewer  // keep it; offer to open it instead
    case unknown      // a version that cannot be read on either side
}

/// installedCopy compares the CFBundleVersion of an acp.app already in Applications with the one
/// running, so moving never silently swaps a newer install for an older download.
public func installedCopy(installed: String?, running: String?) -> InstalledCopy {
    guard let i = installed.flatMap(parseBundleVersion), let r = running.flatMap(parseBundleVersion) else { return .unknown }
    return i.lexicographicallyPrecedes(r) ? .older : .sameOrNewer
}
