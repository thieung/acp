import AcpDesktopCore
import AppKit
import WebKit

/// WebController shows the dashboard in a WKWebView, a spinner while acp starts, and a native
/// error view (stderr tail + Retry) when acp or the page fails. It decides nothing about acp
/// itself: navigation goes through `classify`, and the page can only ask for "restart".
final class WebController: NSObject, WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandler {
    let view = NSView()
    /// Called once, after the dashboard's first successful load (smoke mode exits there).
    var onFirstLoad: ((URL) -> Void)?
    /// Called when the page fails to load (smoke mode exits there).
    var onLoadFailure: ((String) -> Void)?

    private let process: AcpProcess
    private let webView: WKWebView
    private let loading = NSStackView()
    private let errorView = NSStackView()
    private let errorTitle = NSTextField(wrappingLabelWithString: "")
    private let errorText = NSTextView()
    private var port = 0
    private var loaded = false

    init(process: AcpProcess, version: String) {
        self.process = process
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        // A suffix, not a replacement: the dashboard looks for "acp-desktop/" to show in-app copy.
        config.applicationNameForUserAgent = "acp-desktop/\(version)"
        let scripts = WKUserContentController()
        config.userContentController = scripts
        webView = WKWebView(frame: .zero, configuration: config)
        super.init()
        scripts.add(WeakScriptHandler(self), name: "acp")
        webView.navigationDelegate = self
        webView.uiDelegate = self
        #if DEBUG
        if #available(macOS 13.3, *) { webView.isInspectable = true }
        #endif
        buildViews()
        showLoading()
    }

    // MARK: - states

    func load(_ url: URL, port: Int) {
        self.port = port
        show(webView)
        webView.load(URLRequest(url: url))
    }

    func showLoading() {
        show(loading)
    }

    func showError(_ message: String, tail: String) {
        errorTitle.stringValue = message
        errorText.string = tail.isEmpty ? "(acp không ghi gì ra stderr)" : tail
        show(errorView)
    }

    /// reload reloads the dashboard, or retries acp when the error view is up.
    func reload() {
        if errorView.isHidden {
            webView.reload()
        } else {
            retry()
        }
    }

    @objc func retry() {
        showLoading()
        process.restart()
    }

    /// queryRunning asks acp web (`GET /api/running`, with the page's session token) whether a
    /// change or a sign-in is running. It answers "running", "idle" or why it could not tell; with
    /// no dashboard loaded it answers at once. A hung page may never answer: callers time out.
    func queryRunning(_ done: @escaping (String) -> Void) {
        guard port != 0, !webView.isHidden, let url = webView.url, classify(url, port: port) == .dashboard else {
            return done("no dashboard")
        }
        webView.callAsyncJavaScript(runningQueryBody, arguments: [:], in: nil, in: .defaultClient) { result in
            switch result {
            case .success(let v): done((v as? String) ?? "unexpected answer")
            case .failure(let e): done("script failed: \(e.localizedDescription)")
            }
        }
    }

    /// whenSignedIn polls the page (signedInCheck) until it has a session and shows the dashboard,
    /// for up to `timeout`. It calls `done` once, with nil on success or what was still missing.
    func whenSignedIn(timeout: TimeInterval, _ done: @escaping (String?) -> Void) {
        let deadline = Date().addingTimeInterval(timeout)
        var finished = false
        var last = "the page never answered"
        func finish(_ missing: String?) {
            guard !finished else { return }
            finished = true
            done(missing)
        }
        func poll() {
            guard !finished else { return }
            webView.evaluateJavaScript(signedInCheck, in: nil, in: .defaultClient) { result in
                switch result {
                case .success(let v): last = (v as? String) ?? "unexpected answer"
                case .failure(let e): last = "script failed: \(e.localizedDescription)"
                }
                if last == "ok" { return finish(nil) }
                if Date() >= deadline { return finish(last) }
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.25, execute: poll)
            }
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + timeout + 1) { finish(last) }
        poll()
    }

    // MARK: - navigation

    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard let url = action.request.url else { return decisionHandler(.cancel) }
        switch classify(url, port: port) {
        case .dashboard:
            decisionHandler(.allow)
        case .external:
            NSWorkspace.shared.open(url)
            decisionHandler(.cancel)
        case .blocked:
            decisionHandler(.cancel)
        }
    }

    func webView(_ webView: WKWebView, decidePolicyFor response: WKNavigationResponse,
                 decisionHandler: @escaping (WKNavigationResponsePolicy) -> Void) {
        if response.isForMainFrame, let http = response.response as? HTTPURLResponse, http.statusCode >= 400 {
            decisionHandler(.cancel)
            pageFailed("Trang điều khiển trả về HTTP \(http.statusCode).")
            return
        }
        decisionHandler(.allow)
    }

    /// window.open and target=_blank links get the same rules as a normal navigation; no second
    /// web view is ever created.
    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for action: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        if let url = action.request.url {
            switch classify(url, port: port) {
            case .dashboard: webView.load(action.request)
            case .external: NSWorkspace.shared.open(url)
            case .blocked: break
            }
        }
        return nil
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        guard !loaded, let url = webView.url, classify(url, port: port) == .dashboard else { return }
        loaded = true
        onFirstLoad?(url)
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        loadFailed(error)
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        loadFailed(error)
    }

    /// The page's process died (memory pressure, a crash): its session is gone, so acp web
    /// starts again with a fresh sign-in URL.
    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        retry()
    }

    private func loadFailed(_ error: Error) {
        let e = error as NSError
        // Cancelled loads and navigations we refused in decidePolicyFor are not failures.
        if e.domain == NSURLErrorDomain && e.code == NSURLErrorCancelled { return }
        if e.domain == WKErrorDomain && e.code == 102 { return } // frame load interrupted by policy change
        pageFailed("Không mở được trang điều khiển: \(e.localizedDescription)")
    }

    private func pageFailed(_ message: String) {
        showError(message, tail: "")
        onLoadFailure?(message)
    }

    // MARK: - messages from the page

    /// The page may only ask for "restart" (after its session expired), and only from the
    /// dashboard's own origin.
    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        let origin = message.frameInfo.securityOrigin
        guard message.name == "acp", message.frameInfo.isMainFrame, (message.body as? String) == "restart",
              origin.protocol == "http", origin.host == "127.0.0.1" || origin.host == "localhost",
              origin.port == port else { return }
        retry()
    }

    // MARK: - views

    private func show(_ shown: NSView) {
        for v in [webView, loading, errorView] as [NSView] {
            v.isHidden = v !== shown
        }
    }

    private func buildViews() {
        let spinner = NSProgressIndicator()
        spinner.style = .spinning
        spinner.startAnimation(nil)
        loading.orientation = .vertical
        loading.alignment = .centerX
        loading.spacing = 12
        loading.addArrangedSubview(spinner)
        loading.addArrangedSubview(NSTextField(labelWithString: "Đang khởi động acp…"))

        errorTitle.font = .boldSystemFont(ofSize: 15)
        errorText.isEditable = false
        errorText.font = .monospacedSystemFont(ofSize: 11, weight: .regular)
        errorText.textContainerInset = NSSize(width: 6, height: 6)
        let scroll = NSScrollView()
        scroll.documentView = errorText
        scroll.hasVerticalScroller = true
        scroll.borderType = .bezelBorder
        errorText.autoresizingMask = [.width]
        scroll.heightAnchor.constraint(greaterThanOrEqualToConstant: 160).isActive = true
        let retryButton = NSButton(title: "Thử lại", target: self, action: #selector(retry))
        retryButton.keyEquivalent = "\r"
        errorView.orientation = .vertical
        errorView.alignment = .leading
        errorView.spacing = 12
        errorView.edgeInsets = NSEdgeInsets(top: 32, left: 32, bottom: 32, right: 32)
        errorView.addArrangedSubview(errorTitle)
        errorView.addArrangedSubview(scroll)
        errorView.addArrangedSubview(retryButton)
        scroll.widthAnchor.constraint(equalTo: errorView.widthAnchor, constant: -64).isActive = true

        for v in [webView, loading, errorView] as [NSView] {
            v.translatesAutoresizingMaskIntoConstraints = false
            view.addSubview(v)
        }
        NSLayoutConstraint.activate([
            webView.topAnchor.constraint(equalTo: view.topAnchor),
            webView.bottomAnchor.constraint(equalTo: view.bottomAnchor),
            webView.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            webView.trailingAnchor.constraint(equalTo: view.trailingAnchor),
            loading.centerXAnchor.constraint(equalTo: view.centerXAnchor),
            loading.centerYAnchor.constraint(equalTo: view.centerYAnchor),
            errorView.topAnchor.constraint(equalTo: view.topAnchor),
            errorView.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            errorView.trailingAnchor.constraint(equalTo: view.trailingAnchor),
            errorView.bottomAnchor.constraint(lessThanOrEqualTo: view.bottomAnchor),
        ])
    }
}

/// WKUserContentController keeps its handlers alive; this breaks the cycle with WebController.
private final class WeakScriptHandler: NSObject, WKScriptMessageHandler {
    weak var target: WKScriptMessageHandler?

    init(_ target: WKScriptMessageHandler) {
        self.target = target
    }

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        target?.userContentController(controller, didReceive: message)
    }
}
