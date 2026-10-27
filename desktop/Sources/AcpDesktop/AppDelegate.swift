import AcpDesktopCore
import AppKit
import Sparkle

final class AppDelegate: NSObject, NSApplicationDelegate {
    private let env = ProcessInfo.processInfo.environment
    private lazy var smoke = env["ACP_DESKTOP_SMOKE"] == "1"
    private var window: NSWindow!
    private var process: AcpProcess!
    private var web: WebController!
    private var updater: SPUStandardUpdaterController?
    private var quitting = false

    func applicationDidFinishLaunching(_ note: Notification) {
        NSApp.setActivationPolicy(.regular)
        startUpdater()
        NSApp.mainMenu = buildMenu()
        if !smoke && MoveToApplications.offerIfNeeded() {
            return
        }

        process = AcpProcess(binary: acpBinary())
        web = WebController(process: process, version: version())
        process.onSignIn = { [weak self] url, port in self?.web.load(url, port: port) }
        process.onFailure = { [weak self] message, tail in
            self?.web.showError(message, tail: tail)
            self?.smokeExit(1, message)
        }
        web.onLoadFailure = { [weak self] message in self?.smokeExit(1, message) }
        web.onFirstLoad = { [weak self] url in self?.smokeExit(0, "smoke: loaded \(withoutCode(url))") }

        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1100, height: 760),
                          styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
        window.title = "acp"
        window.minSize = NSSize(width: 720, height: 480)
        window.contentView = web.view
        window.center()
        window.setFrameAutosaveName("acp.main")
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)

        if smoke {
            DispatchQueue.main.asyncAfter(deadline: .now() + 50) { [weak self] in
                self?.smokeExit(1, "smoke: timed out")
            }
        }
        process.start()
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ app: NSApplication) -> Bool {
        true
    }

    /// Quitting closes acp web's stdin and gives it up to 10 s to finish a running change; if the
    /// dashboard says one is running, the user decides first.
    func applicationShouldTerminate(_ app: NSApplication) -> NSApplication.TerminateReply {
        guard !quitting, let process, let web else { return .terminateNow }
        web.isChangeRunning { running in
            if running {
                let alert = NSAlert()
                alert.messageText = "Một thay đổi đang chạy"
                alert.informativeText = "Thoát bây giờ, acp sẽ chờ tối đa 10 giây cho thay đổi xong rồi mới dừng."
                alert.addButton(withTitle: "Ở lại")
                alert.addButton(withTitle: "Thoát")
                if alert.runModal() == .alertFirstButtonReturn {
                    NSApp.reply(toApplicationShouldTerminate: false)
                    return
                }
            }
            process.stop(timeout: 10) {
                self.quitting = true
                NSApp.reply(toApplicationShouldTerminate: true)
            }
        }
        return .terminateLater
    }

    // MARK: - actions

    @objc private func reload(_ sender: Any?) {
        web?.reload()
    }

    // MARK: - setup

    /// acpBinary is the acp bundled in Contents/Helpers. Debug builds may point elsewhere with
    /// ACP_DESKTOP_BIN, for running the shell from `swift run`.
    private func acpBinary() -> URL {
        #if DEBUG
        if let bin = env["ACP_DESKTOP_BIN"], !bin.isEmpty {
            return URL(fileURLWithPath: bin)
        }
        #endif
        return Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/acp")
    }

    private func version() -> String {
        let v = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""
        return v.isEmpty || v.hasPrefix("@") ? "dev" : v
    }

    /// Sparkle starts only when packaging filled in an https feed and a public key.
    private func startUpdater() {
        let info = Bundle.main.infoDictionary ?? [:]
        guard sparkleEnabled(feedURL: info["SUFeedURL"] as? String, publicKey: info["SUPublicEDKey"] as? String) else { return }
        updater = SPUStandardUpdaterController(startingUpdater: true, updaterDelegate: nil, userDriverDelegate: nil)
    }

    private func smokeExit(_ code: Int32, _ line: String) {
        guard smoke else { return }
        FileHandle.standardOutput.write(Data((line + "\n").utf8))
        let finish: () -> Void = { exit(code) }
        if let process {
            process.stop(timeout: 10, then: finish)
        } else {
            finish()
        }
    }

    private func buildMenu() -> NSMenu {
        let main = NSMenu()
        let appMenu = NSMenu(title: "acp")
        appMenu.addItem(withTitle: "Giới thiệu acp", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        let check = appMenu.addItem(withTitle: "Kiểm tra cập nhật…", action: nil, keyEquivalent: "")
        if let updater {
            check.target = updater
            check.action = #selector(SPUStandardUpdaterController.checkForUpdates(_:))
        }
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Ẩn acp", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Thoát acp", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")

        // Edit is what makes Cmd-V work in the web view (pasting the API key).
        let editMenu = NSMenu(title: "Sửa")
        editMenu.addItem(withTitle: "Hoàn tác", action: Selector(("undo:")), keyEquivalent: "z")
        editMenu.addItem(withTitle: "Làm lại", action: Selector(("redo:")), keyEquivalent: "Z")
        editMenu.addItem(.separator())
        editMenu.addItem(withTitle: "Cắt", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editMenu.addItem(withTitle: "Chép", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editMenu.addItem(withTitle: "Dán", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editMenu.addItem(withTitle: "Chọn tất cả", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")

        let viewMenu = NSMenu(title: "Xem")
        viewMenu.addItem(withTitle: "Tải lại", action: #selector(reload(_:)), keyEquivalent: "r").target = self

        let windowMenu = NSMenu(title: "Cửa sổ")
        windowMenu.addItem(withTitle: "Thu nhỏ", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
        windowMenu.addItem(withTitle: "Đóng", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        NSApp.windowsMenu = windowMenu

        for sub in [appMenu, editMenu, viewMenu, windowMenu] {
            main.addItem(withTitle: sub.title, action: nil, keyEquivalent: "").submenu = sub
        }
        return main
    }
}
