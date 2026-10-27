import AcpDesktopCore
import Foundation

/// AcpProcess runs the bundled acp: `acp init --json` once, then `acp web` until the app quits.
/// acp web gets a stdin pipe that stays open for its whole life; with --exit-with-parent it shuts
/// down when that pipe closes, so a crashed or killed app never leaves it behind.
/// All state lives on `queue`; callbacks run on the main queue.
final class AcpProcess {
    /// Called with the sign-in URL and the dashboard's port.
    var onSignIn: ((URL, Int) -> Void)?
    /// Called with a message and the tail of acp's stderr.
    var onFailure: ((String, String) -> Void)?

    private let binary: URL
    private let queue = DispatchQueue(label: "acp.process")
    private var env: [String: String]?
    private var web: Process?
    private var stdinPipe: Pipe?
    private var lines = LineBuffer()
    private var tail = Tail()
    private var signedIn = false
    // Bumped by every start and stop, so callbacks from an older child are ignored.
    private var generation = 0

    init(binary: URL) {
        self.binary = binary
    }

    func start() {
        queue.async { self.launch() }
    }

    /// restart stops the running acp web and starts a fresh one, which prints a new sign-in URL.
    func restart() {
        stop(timeout: 10) { self.start() }
    }

    /// stop closes acp web's stdin, waits up to `timeout` for it to finish what it is doing, then
    /// terminates it.
    func stop(timeout: TimeInterval, then done: @escaping () -> Void) {
        queue.async {
            self.generation += 1
            let p = self.web, pipe = self.stdinPipe
            self.web = nil
            self.stdinPipe = nil
            try? pipe?.fileHandleForWriting.close()
            if let p {
                let deadline = Date().addingTimeInterval(timeout)
                while p.isRunning, Date() < deadline { usleep(100_000) }
                if p.isRunning {
                    p.terminate()
                    let hard = Date().addingTimeInterval(3)
                    while p.isRunning, Date() < hard { usleep(100_000) }
                    if p.isRunning { kill(p.processIdentifier, SIGKILL) }
                }
            }
            DispatchQueue.main.async(execute: done)
        }
    }

    // MARK: - on queue

    private func launch() {
        generation += 1
        let gen = generation
        lines = LineBuffer()
        tail = Tail()
        signedIn = false
        guard FileManager.default.isExecutableFile(atPath: binary.path) else {
            return fail("Không tìm thấy acp trong ứng dụng (\(binary.path)).")
        }
        if env == nil {
            let appEnv = ProcessInfo.processInfo.environment
            env = childEnvironment(appEnv: appEnv, loginShell: probeLoginShell(appEnv))
        }

        let initRun = runToEnd(binary, ["init", "--json"], env: env, timeout: 60)
        guard gen == generation else { return }
        tail.append(initRun.err)
        guard initRun.status == 0 else {
            let why = envelopeError(initRun.out) ?? (initRun.status == nil ? "quá thời gian" : "mã thoát \(initRun.status!)")
            return fail("acp init không chạy được: \(why)")
        }

        let p = Process()
        p.executableURL = binary
        p.arguments = ["web", "--port", "0", "--no-open", "--print-url", "--exit-with-parent"]
        p.environment = env
        p.currentDirectoryURL = URL(fileURLWithPath: env?["HOME"] ?? NSHomeDirectory())
        let inPipe = Pipe(), out = Pipe(), err = Pipe()
        p.standardInput = inPipe
        p.standardOutput = out
        p.standardError = err
        // Drain both pipes for the child's whole life: a full pipe would block acp web.
        out.fileHandleForReading.readabilityHandler = { [weak self] h in
            let d = h.availableData
            if d.isEmpty { h.readabilityHandler = nil; return }
            self?.queue.async { self?.readStdout(d, gen) }
        }
        err.fileHandleForReading.readabilityHandler = { [weak self] h in
            let d = h.availableData
            if d.isEmpty { h.readabilityHandler = nil; return }
            self?.queue.async { if gen == self?.generation { self?.tail.append(d) } }
        }
        p.terminationHandler = { [weak self] p in
            // Give the stderr drain a moment so the error view shows acp's last words.
            self?.queue.asyncAfter(deadline: .now() + 0.3) {
                guard let self, gen == self.generation else { return }
                self.web = nil
                self.stdinPipe = nil
                self.fail("acp web đã dừng (mã thoát \(p.terminationStatus)).")
            }
        }
        do {
            try p.run()
        } catch {
            return fail("Không chạy được acp web: \(error.localizedDescription)")
        }
        web = p
        stdinPipe = inPipe
        queue.asyncAfter(deadline: .now() + 20) { [weak self] in
            guard let self, gen == self.generation, !self.signedIn else { return }
            self.generation += 1
            p.terminate()
            self.fail("acp web không đưa ra địa chỉ đăng nhập sau 20 giây.")
        }
    }

    private func readStdout(_ chunk: Data, _ gen: Int) {
        guard gen == generation else { return }
        // stdout carries the one-time code: it is parsed, never logged.
        for line in lines.append(chunk) where !signedIn {
            if let s = parseSignIn(line) {
                signedIn = true
                DispatchQueue.main.async { self.onSignIn?(s.url, s.port) }
            }
        }
    }

    private func fail(_ message: String) {
        let text = tail.text
        DispatchQueue.main.async { self.onFailure?(message, text) }
    }

    /// probeLoginShell asks the user's login shell for shellKeys (PATH, ACP_ROOT, the agent homes), so acp
    /// sees what it sees in a terminal. The shell itself gets only the allow-listed variables: a
    /// value planted in the app's environment must not come back through it.
    private func probeLoginShell(_ appEnv: [String: String]) -> [String: String] {
        var shell = appEnv["SHELL"] ?? ""
        if shell.isEmpty, let pw = getpwuid(getuid()), let sh = pw.pointee.pw_shell {
            shell = String(cString: sh)
        }
        if !shell.hasPrefix("/") || !FileManager.default.isExecutableFile(atPath: shell) {
            shell = "/bin/zsh"
        }
        var env = childEnvironment(appEnv: appEnv, loginShell: [:])
        env["TERM"] = "dumb"
        let run = runToEnd(URL(fileURLWithPath: shell), ["-lic", shellProbeCommand], env: env, timeout: 3)
        return parseShellProbe(run.out)
    }

    private func envelopeError(_ out: Data) -> String? {
        let obj = try? JSONSerialization.jsonObject(with: out) as? [String: Any]
        return obj?["error"] as? String
    }
}

/// runToEnd runs a short command with stdin from /dev/null and collects its output. Past the
/// deadline the command is killed and `status` is nil.
func runToEnd(_ exe: URL, _ args: [String], env: [String: String]?, timeout: TimeInterval)
    -> (status: Int32?, out: Data, err: Data)
{
    let p = Process()
    p.executableURL = exe
    p.arguments = args
    p.environment = env
    p.standardInput = FileHandle.nullDevice
    let out = Pipe(), err = Pipe()
    p.standardOutput = out
    p.standardError = err
    let lock = NSLock()
    var outData = Data(), errData = Data()
    let drained = DispatchGroup()
    for (pipe, isOut) in [(out, true), (err, false)] {
        drained.enter()
        pipe.fileHandleForReading.readabilityHandler = { h in
            let d = h.availableData
            if d.isEmpty {
                h.readabilityHandler = nil
                drained.leave()
                return
            }
            lock.lock()
            if isOut { outData.append(d) } else { errData.append(d) }
            lock.unlock()
        }
    }
    let exited = DispatchSemaphore(value: 0)
    p.terminationHandler = { _ in exited.signal() }
    do {
        try p.run()
    } catch {
        return (nil, Data(), Data(error.localizedDescription.utf8))
    }
    var status: Int32?
    if exited.wait(timeout: .now() + timeout) == .timedOut {
        p.terminate()
        _ = exited.wait(timeout: .now() + 1)
    } else {
        status = p.terminationStatus
    }
    // A grandchild may still hold the pipes open; do not wait on it for long.
    _ = drained.wait(timeout: .now() + 1)
    lock.lock()
    defer { lock.unlock() }
    return (status, outData, errData)
}
