import AppKit

// acp desktop: launches the bundled acp and shows its dashboard. Everything acp does lives in Go.
let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()
