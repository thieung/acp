import Foundation

public enum NavigationKind: Equatable {
    case dashboard  // load in the web view
    case external   // hand to the default browser
    case blocked    // drop
}

/// classify decides where a navigation may go. Only the dashboard's own origin loads in the web
/// view; https links to AgentKit or GitHub open in the browser; every other scheme, host or port
/// (file, javascript, data, blob, smb, custom schemes, other loopback ports) is blocked.
public func classify(_ url: URL, port: Int) -> NavigationKind {
    guard let c = URLComponents(url: url, resolvingAgainstBaseURL: false),
          let scheme = c.scheme?.lowercased(), let host = c.host?.lowercased(), !host.isEmpty,
          c.user == nil, c.password == nil else { return .blocked }
    if scheme == "http", host == "127.0.0.1" || host == "localhost", c.port == port {
        return .dashboard
    }
    if scheme == "https", c.port == nil || c.port == 443,
       host == "agentkit.best" || host.hasSuffix(".agentkit.best") || host == "github.com" {
        return .external
    }
    return .blocked
}
