import Foundation

/// parseSignIn accepts only the exact line `acp web --print-url` writes:
/// `sign in: http://127.0.0.1:<port>/#code=<lowercase hex>`. Anything else is not a sign-in URL,
/// so a stray line on stdout can never point the web view somewhere else.
public func parseSignIn(_ line: String) -> (url: URL, port: Int)? {
    let prefix = "sign in: http://127.0.0.1:"
    guard line.hasPrefix(prefix) else { return nil }
    let rest = line.dropFirst(prefix.count)
    guard let slash = rest.range(of: "/#code=") else { return nil }
    let digits = rest[..<slash.lowerBound]
    let code = rest[slash.upperBound...]
    guard (1...5).contains(digits.count), digits.first != "0", digits.allSatisfy({ ("0"..."9").contains($0) }),
          let port = Int(digits), port <= 65535 else { return nil }
    guard !code.isEmpty, code.allSatisfy({ ("0"..."9").contains($0) || ("a"..."f").contains($0) }) else { return nil }
    guard let url = URL(string: String(line.dropFirst("sign in: ".count))) else { return nil }
    return (url, port)
}

/// withoutCode is the URL with its fragment dropped: the one-time code is never printed or logged.
public func withoutCode(_ url: URL) -> String {
    guard var c = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return "" }
    c.fragment = nil
    return c.string ?? ""
}
