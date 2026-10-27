import XCTest
@testable import AcpDesktopCore

final class ChildEnvTests: XCTestCase {
    let appEnv = [
        "HOME": "/Users/u", "USER": "u", "LOGNAME": "u", "LANG": "vi_VN.UTF-8", "TMPDIR": "/var/folders/x/T/",
        "SHELL": "/opt/homebrew/bin/fish", "PATH": "/usr/bin:/bin",
        // Planted by launchd or a parent process: none of these may reach acp.
        "ACP_AK": "/tmp/evil-ak", "AGENTKIT_REGISTRY_URL": "https://evil.example", "DYLD_INSERT_LIBRARIES": "/tmp/x.dylib",
        "ACP_INSTALLER_URL": "https://evil.example/install.sh", "AK_API_KEY": "ak_live_FAKEKEY0000000000",
        "ACP_ROOT": "/tmp/launchd-root", "XPC_SERVICE_NAME": "x",
    ]

    func testDropsEverythingOutsideTheAllowList() {
        let env = childEnvironment(appEnv: appEnv, loginShell: [:])
        for k in ["ACP_AK", "AGENTKIT_REGISTRY_URL", "DYLD_INSERT_LIBRARIES", "ACP_INSTALLER_URL", "AK_API_KEY",
                  "ACP_ROOT", "XPC_SERVICE_NAME", "XDG_CONFIG_HOME"] {
            XCTAssertNil(env[k], k)
        }
        XCTAssertEqual(env["HOME"], "/Users/u")
        XCTAssertEqual(env["LANG"], "vi_VN.UTF-8")
        XCTAssertEqual(env["SHELL"], "/opt/homebrew/bin/fish")
        XCTAssertEqual(env["ACP_DESKTOP"], "1")
        XCTAssertFalse(env.values.contains { $0.contains("FAKEKEY") })
    }

    func testTakesPathRootAndConfigFromTheLoginShell() {
        let shell = ["PATH": "/Users/u/.local/bin:/usr/bin", "ACP_ROOT": "/Users/u/acp-root",
                     "XDG_CONFIG_HOME": "/Users/u/.cfg", "ACP_AK": "/tmp/evil-ak"]
        let env = childEnvironment(appEnv: appEnv, loginShell: shell)
        XCTAssertEqual(env["ACP_ROOT"], "/Users/u/acp-root")
        XCTAssertEqual(env["XDG_CONFIG_HOME"], "/Users/u/.cfg")
        XCTAssertEqual(env["PATH"], "/Users/u/.local/bin:/usr/bin")
        XCTAssertNil(env["ACP_AK"])
    }

    func testPathFallbackWhenTheProbeIsEmpty() {
        let env = childEnvironment(appEnv: appEnv, loginShell: ["PATH": "", "ACP_ROOT": ""])
        XCTAssertEqual(env["PATH"], "/Users/u/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
        XCTAssertNil(env["ACP_ROOT"])
    }

    func testParseShellProbeSkipsStartupNoise() {
        let rest = String(repeating: "\0", count: shellKeys.count - 2) // empty XDG_CONFIG_HOME and agent homes
        let out = Data(("Welcome to fish\n__acp_env__\0/a:/b\0/r\0" + rest).utf8)
        XCTAssertEqual(parseShellProbe(out), ["PATH": "/a:/b", "ACP_ROOT": "/r"])
    }

    func testParseShellProbeWithoutMarkerIsEmpty() {
        XCTAssertEqual(parseShellProbe(Data("/a:/b\0/r\0\0".utf8)), [:])
        XCTAssertEqual(parseShellProbe(Data()), [:])
        XCTAssertEqual(parseShellProbe(Data("__acp_env__\0/a".utf8)), [:]) // cut short by the timeout
    }

    /// acp and the claude and codex it starts read these to find the user's agent configs; without
    /// them the app would read and write ~/.claude.json while the terminal's claude uses another.
    let agentHomes = ["CLAUDE_CONFIG_DIR", "CODEX_HOME", "PI_CODING_AGENT_DIR", "AGENTKIT_OMP_HOME", "OMP_HOME",
                      "PI_AK_AGENT_DIR", "PI_OMP_AGENT_DIR"]

    func testTakesAgentHomesFromTheLoginShell() {
        var shell: [String: String] = [:]
        for k in agentHomes { shell[k] = "/Users/u/homes/\(k)" }
        let env = childEnvironment(appEnv: appEnv.merging(["CLAUDE_CONFIG_DIR": "/tmp/launchd"]) { $1 }, loginShell: shell)
        for k in agentHomes { XCTAssertEqual(env[k], "/Users/u/homes/\(k)", k) }
        XCTAssertNil(childEnvironment(appEnv: ["CLAUDE_CONFIG_DIR": "/tmp/launchd"], loginShell: [:])["CLAUDE_CONFIG_DIR"])
    }

    func testShellProbeAsksForEveryKey() {
        for k in shellKeys { XCTAssertTrue(shellProbeCommand.contains("\"$\(k)\""), k) }
        XCTAssertEqual(shellProbeCommand.components(separatedBy: "%s").count - 1, shellKeys.count + 1)
        for k in agentHomes { XCTAssertTrue(shellKeys.contains(k), k) }
    }

    func testParseShellProbeReadsAgentHomes() {
        var values = shellKeys.map { _ in "" }
        values[shellKeys.firstIndex(of: "PATH")!] = "/a"
        values[shellKeys.firstIndex(of: "CLAUDE_CONFIG_DIR")!] = "/Users/u/.ccs/instances/w"
        values[shellKeys.firstIndex(of: "CODEX_HOME")!] = "/Users/u/.codex-w"
        let out = Data(("__acp_env__\0" + values.map { $0 + "\0" }.joined()).utf8)
        XCTAssertEqual(parseShellProbe(out), ["PATH": "/a", "CLAUDE_CONFIG_DIR": "/Users/u/.ccs/instances/w",
                                              "CODEX_HOME": "/Users/u/.codex-w"])
    }
}
