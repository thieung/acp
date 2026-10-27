import Foundation

/// LineBuffer turns pipe chunks into complete lines (without "\n" or "\r\n"); a partial line waits
/// for the next chunk.
public struct LineBuffer {
    private var pending = Data()

    public init() {}

    public mutating func append(_ chunk: Data) -> [String] {
        pending.append(chunk)
        var lines: [String] = []
        while let nl = pending.firstIndex(of: UInt8(ascii: "\n")) {
            var line = pending[pending.startIndex..<nl]
            if line.last == UInt8(ascii: "\r") { line = line.dropLast() }
            lines.append(String(decoding: line, as: UTF8.self))
            pending = Data(pending[(nl + 1)...])
        }
        return lines
    }
}

/// Tail keeps the last lines of a stream (acp's stderr) for the error view.
public struct Tail {
    public let maxLines: Int
    private var data = Data()

    public init(maxLines: Int = 40) {
        self.maxLines = maxLines
    }

    public mutating func append(_ chunk: Data) {
        data.append(chunk)
        if data.count > maxLines * 512 {
            data = Data(data.suffix(maxLines * 512))
        }
    }

    public var text: String {
        let lines = String(decoding: data, as: UTF8.self).split(separator: "\n", omittingEmptySubsequences: false)
        let kept = lines.last == "" ? lines.dropLast() : lines[...]
        return kept.suffix(maxLines).joined(separator: "\n")
    }
}

/// sparkleEnabled is true only when packaging filled in an https feed and a public EdDSA key;
/// with half a configuration Sparkle stays off rather than failing at every check.
public func sparkleEnabled(feedURL: String?, publicKey: String?) -> Bool {
    guard let feed = feedURL, let key = publicKey?.trimmingCharacters(in: .whitespaces),
          !key.isEmpty, !key.hasPrefix("@"),
          let url = URL(string: feed), url.scheme == "https", url.host?.isEmpty == false else { return false }
    return true
}
