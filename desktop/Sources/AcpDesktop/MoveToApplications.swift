import AcpDesktopCore
import AppKit

/// MoveToApplications offers, on launch, to copy an app that runs from Downloads, a disk image or
/// an App Translocation copy into /Applications, because acp links the CLI and registers MCP to
/// the bundled binary's path. Cancel keeps the app running; the dashboard still works.
enum MoveToApplications {
    /// offerIfNeeded returns true when the app was copied and is relaunching; the caller then
    /// starts nothing.
    static func offerIfNeeded() -> Bool {
        let bundle = Bundle.main.bundleURL
        guard bundle.pathExtension == "app", needsMove(bundlePath: bundle.path) else { return false }
        NSApp.activate(ignoringOtherApps: true)
        let alert = NSAlert()
        alert.messageText = "Chuyển acp vào Applications"
        alert.informativeText = "acp đang chạy từ \(bundle.deletingLastPathComponent().path). Để lệnh acp và kết nối với "
            + "Claude Code, Codex không bị mất khi thư mục này biến mất, hãy chép acp vào /Applications rồi mở lại."
        alert.addButton(withTitle: "Chuyển và mở lại")
        alert.addButton(withTitle: "Để sau")
        guard alert.runModal() == .alertFirstButtonReturn else { return false }

        let dest = URL(fileURLWithPath: "/Applications").appendingPathComponent(bundle.lastPathComponent)
        do {
            let fm = FileManager.default
            if fm.fileExists(atPath: dest.path) {
                try fm.trashItem(at: dest, resultingItemURL: nil)
            }
            try fm.copyItem(at: bundle, to: dest)
        } catch {
            let failed = NSAlert()
            failed.messageText = "Không chép được acp vào /Applications"
            failed.informativeText = "\(error.localizedDescription)\n\nHãy tự kéo acp vào thư mục Applications trong Finder."
            failed.runModal()
            return false
        }
        // The user already opened this copy; without this the new copy would be translocated again.
        _ = runToEnd(URL(fileURLWithPath: "/usr/bin/xattr"), ["-d", "-r", "com.apple.quarantine", dest.path], env: nil, timeout: 10)
        relaunch(dest)
        NSApp.terminate(nil)
        return true
    }

    /// relaunch opens `app` once this process has exited, so LaunchServices starts the new copy
    /// instead of activating this one.
    private static func relaunch(_ app: URL) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/sh")
        p.arguments = ["-c", #"while /bin/kill -0 "$1" 2>/dev/null; do /bin/sleep 0.2; done; exec /usr/bin/open "$2""#,
                       "sh", String(ProcessInfo.processInfo.processIdentifier), app.path]
        p.environment = ["PATH": "/usr/bin:/bin"]
        try? p.run()
    }
}
