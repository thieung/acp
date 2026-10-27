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
