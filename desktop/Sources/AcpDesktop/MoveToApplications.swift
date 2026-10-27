import AcpDesktopCore
import AppKit

/// MoveToApplications offers, on launch, to copy an app that runs from outside /Applications or
/// ~/Applications (Downloads, a disk image, an App Translocation copy) into /Applications, because
/// acp links the CLI, installs hooks and registers MCP only to a bundled binary that lives there.
/// Cancel keeps the app running; the dashboard still works. A copy already in /Applications is
/// replaced only when it is older, and the alert says so; a newer or equal one is opened instead.
enum MoveToApplications {
    /// offerIfNeeded returns true when the app was copied (or the installed copy opened) and is
    /// relaunching; the caller then starts nothing.
    static func offerIfNeeded() -> Bool {
        let bundle = Bundle.main.bundleURL
        guard bundle.pathExtension == "app", needsMove(bundlePath: bundle.path) else { return false }
        NSApp.activate(ignoringOtherApps: true)
        let fm = FileManager.default
        let dest = URL(fileURLWithPath: "/Applications").appendingPathComponent(bundle.lastPathComponent)
        let here = "acp đang chạy từ \(bundle.deletingLastPathComponent().path). Để lệnh acp và kết nối với "
            + "Claude Code, Codex không bị mất khi thư mục này biến mất, acp cần nằm trong Applications."
        let mine = Bundle.main.infoDictionary ?? [:]
        var replacing = ""
        if fm.fileExists(atPath: dest.path) {
            let theirs = NSDictionary(contentsOf: dest.appendingPathComponent("Contents/Info.plist")) as? [String: Any] ?? [:]
            switch installedCopy(installed: theirs["CFBundleVersion"] as? String, running: mine["CFBundleVersion"] as? String) {
            case .sameOrNewer:
                return offerInstalled(dest, installed: shortVersion(theirs), running: shortVersion(mine), here: here)
            case .older:
                replacing = "\n\nBản cũ hơn đang có ở \(dest.path) (\(shortVersion(theirs))) sẽ được chuyển vào Thùng rác "
                    + "và thay bằng bản này (\(shortVersion(mine)))."
            case .unknown:
                replacing = "\n\nĐã có \(dest.path) nhưng không đọc được phiên bản của nó; bản đó sẽ được chuyển vào "
                    + "Thùng rác và thay bằng bản này (\(shortVersion(mine)))."
            }
        }
        let alert = NSAlert()
        alert.messageText = "Chuyển acp vào Applications"
        alert.informativeText = here + " Hãy chép acp vào /Applications rồi mở lại." + replacing
        alert.addButton(withTitle: replacing.isEmpty ? "Chuyển và mở lại" : "Thay và mở lại")
        alert.addButton(withTitle: "Để sau")
        guard alert.runModal() == .alertFirstButtonReturn else { return false }

        do {
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

    /// offerInstalled handles an acp.app in /Applications that is as new as this one or newer: it
    /// is never replaced; the user may open it instead of this copy.
    private static func offerInstalled(_ dest: URL, installed: String, running: String, here: String) -> Bool {
        let alert = NSAlert()
        alert.messageText = "acp đã có trong Applications"
        alert.informativeText = here + " \(dest.path) (\(installed)) mới bằng hoặc hơn bản đang chạy (\(running)), "
            + "nên acp không thay nó. Mở bản trong Applications thay cho bản này?"
        alert.addButton(withTitle: "Mở bản trong Applications")
        alert.addButton(withTitle: "Để sau")
        guard alert.runModal() == .alertFirstButtonReturn else { return false }
        relaunch(dest)
        NSApp.terminate(nil)
        return true
    }

    /// shortVersion is the version people read (CFBundleShortVersionString) from an Info.plist.
    private static func shortVersion(_ info: [String: Any]) -> String {
        let v = info["CFBundleShortVersionString"] as? String ?? ""
        return v.isEmpty || v.hasPrefix("@") ? "không rõ phiên bản" : v
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
