import Foundation

/// Variables copied from the app's own environment. launchd hands a Finder-started app an
/// environment the user never sees, so everything else (ACP_*, AK_*, AGENTKIT_*, DYLD_*, ...) is dropped.
let appAllowList = ["HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "SHELL"]

/// Variables taken from the user's login shell, so acp sees the same control plane, tools and agent
/// homes as in a terminal. The agent homes are the ones core.GlobalHomes reads; claude and codex,
/// which acp starts, read CLAUDE_CONFIG_DIR and CODEX_HOME too, so without them the app would write
/// a config the user's own claude never reads. The proxy and CA variables let ak, claude and codex
/// reach the network behind a corporate proxy or TLS inspection, as they do in a terminal.
let shellKeys = ["PATH", "ACP_ROOT", "XDG_CONFIG_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "PI_CODING_AGENT_DIR",
                 "AGENTKIT_OMP_HOME", "OMP_HOME", "PI_AK_AGENT_DIR", "PI_OMP_AGENT_DIR",
                 "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "no_proxy", "all_proxy",
                 "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"]

/// shellProbeMarker starts the probe's output; anything a shell's startup files print before it is noise.
public let shellProbeMarker = "__acp_env__"

/// shellProbeCommand runs under `$SHELL -lic` and prints the marker, then each of shellKeys, each
/// closed by a NUL. It is valid in sh, bash, zsh and fish (fish joins a quoted $PATH with colons).
public let shellProbeCommand = "printf \"" + String(repeating: #"%s\0"#, count: shellKeys.count + 1) + "\" "
    + shellProbeMarker + shellKeys.map { " \"$\($0)\"" }.joined()

/// parseShellProbe reads the output of shellProbeCommand. Empty values are left out, and output
/// cut short (a slow shell killed by the timeout) yields nothing.
public func parseShellProbe(_ data: Data) -> [String: String] {
    let marker = Data((shellProbeMarker + "\0").utf8)
    guard let found = data.range(of: marker, options: .backwards) else { return [:] }
    let fields = data[found.upperBound...].split(separator: 0, omittingEmptySubsequences: false)
    // Every value is closed by a NUL, which leaves one more empty piece after the last.
    guard fields.count > shellKeys.count else { return [:] }
    var out: [String: String] = [:]
    for (key, raw) in zip(shellKeys, fields) {
        if let v = String(data: Data(raw), encoding: .utf8), !v.isEmpty {
            out[key] = v
        }
    }
    return out
}

/// childEnvironment is the whole environment acp runs with: the allow-listed app variables, shellKeys
/// from the login shell, and ACP_DESKTOP=1.
public func childEnvironment(appEnv: [String: String], loginShell: [String: String]) -> [String: String] {
    var env: [String: String] = [:]
    for k in appAllowList {
        if let v = appEnv[k], !v.isEmpty { env[k] = v }
    }
    for k in shellKeys {
        if let v = loginShell[k], !v.isEmpty { env[k] = v }
    }
    if env["PATH"] == nil {
        let home = env["HOME"] ?? NSHomeDirectory()
        env["PATH"] = "\(home)/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
    }
    env["ACP_DESKTOP"] = "1"
    return env
}
