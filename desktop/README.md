# acp desktop app (macOS)

`acp.app` is a thin Swift shell around the acp binary it bundles. It lets someone who never
opens a terminal sign in to AgentKit, put `acp` on their PATH and connect `acp mcp` to Claude
Code and Codex. The shell holds no business logic:
1. It runs the bundled `Contents/Helpers/acp init --json`.
2. It runs `acp web --port 0 --no-open --print-url --exit-with-parent` and reads the `sign in:` line.
3. It loads that URL in a WKWebView. Everything you see is the same dashboard as `acp web`.

Children get an allow-listed environment: `HOME`, `USER`, `LANG`… from the app, and `PATH`,
`ACP_ROOT`, `XDG_CONFIG_HOME` from your login shell. `ACP_*`, `AK_*`, `AGENTKIT_*` and `DYLD_*`
from launchd never reach acp. acp web stops when the app quits or crashes, because the app holds
its stdin.

## Build

```bash
cd desktop && swift test                                   # AcpDesktopCore unit tests
sh scripts/build-desktop.sh v0.0.0-dev                     # dist/desktop/acp.app + acp-0.0.0-dev.dmg
sh scripts/tests/desktop-smoke.sh dist/desktop/acp.app v0.0.0-dev
```

`build-desktop.sh` writes only under `dist/desktop/`:
1. It builds acp for amd64 and arm64 and joins them with `lipo`.
2. It builds the Swift shell universal with Sparkle 2.9.6.
3. It assembles `acp.app` (`MacOS/AcpDesktop`, `Helpers/acp`, `Frameworks/Sparkle.framework`), fills `Info.plist` and adds the `../Frameworks` rpath.
4. It signs inside out and builds the DMG with an `/Applications` link.
5. It appends the DMG's sha256 to `checksums.txt`.

Without secrets the app is signed ad hoc. That runs on the machine that built it and in CI, but
Gatekeeper blocks it elsewhere, so releases attach only a signed and notarized DMG.

The smoke test checks both architectures, the rpath, the signature and `acp --version`. It also
drives the Go contract in a throwaway home (init, web, `GET /` with its CSP, `POST /api/session`).
Then it launches the app with `ACP_DESKTOP_SMOKE=1`, which prints `smoke: loaded <url>` after the
dashboard's first load and exits 0.

## Run during development

```bash
go build -o /tmp/acp ./cmd/acp
cd desktop && ACP_DESKTOP_BIN=/tmp/acp swift run AcpDesktop
```

`ACP_DESKTOP_BIN` works only in debug builds; a release app always runs its own `Helpers/acp`.
`swift run` has no Sparkle feed, so the updater stays off.

## What the maintainer supplies

None of these exist in the repository. Without them CI and releases still build the app ad hoc,
and the release job skips the DMG with a notice.

| What | Where it goes | Used for |
|------|---------------|----------|
| Developer ID Application certificate, as a base64 `.p12`, and its password | secrets `ACP_DEVELOPER_ID_P12_BASE64`, `ACP_DEVELOPER_ID_P12_PASSWORD` | signing (imported into a temp keychain) |
| The identity name, e.g. `Developer ID Application: Name (TEAMID)` | secret `ACP_DEVELOPER_ID` | `codesign -s` |
| App Store Connect API key (`.p8` text), key id, issuer id | secrets `ACP_NOTARY_KEY`, `ACP_NOTARY_KEY_ID`, `ACP_NOTARY_ISSUER` | `notarytool` + `stapler` |
| Sparkle EdDSA keypair from `generate_keys` | private key (`generate_keys -x file`) in secret `ACP_SPARKLE_PRIVATE_KEY`; public key in variable `ACP_SPARKLE_PUBLIC_KEY` | signing the appcast; the app checks updates with it |
| An https URL where `appcast.xml` is hosted | variable `ACP_SPARKLE_FEED_URL` | Sparkle's feed |

`generate_keys` lives in `desktop/.build/artifacts/sparkle/Sparkle/bin/` after a build. The
secrets belong to the `release` environment.

Locally, the same build takes `ACP_DEVELOPER_ID`, `ACP_NOTARY_KEY_FILE`, `ACP_NOTARY_KEY_ID`,
`ACP_NOTARY_ISSUER`, `ACP_SPARKLE_FEED_URL`, `ACP_SPARKLE_PUBLIC_KEY` and
`ACP_SPARKLE_PRIVATE_KEY_FILE`. Set the feed and public key together or not at all. Sparkle runs
only when both are set and the feed is https.

When the private key is set, `generate_appcast` writes `dist/desktop/appcast/appcast.xml` for
this release. Its download links point at the tag's GitHub release
(`ACP_SPARKLE_DOWNLOAD_URL_PREFIX` overrides that). The release job attaches `appcast.xml` to the
release; host it, merged with the earlier entries, at the feed URL.

## Install and remove

Open the DMG and drag `acp.app` to Applications. Run from Downloads, a disk image or a
translocated copy, the app offers to move itself there first. acp refuses to link the CLI or
register MCP to a path that vanishes.

The dashboard can then do three things, each previewed first:
- Sign in to AgentKit with an API key. The key goes to `ak` on stdin only.
- Point `~/.local/bin/acp` at the bundled binary (`acp setup link-cli`), so the terminal runs the same acp.
- Register `acp mcp` with Claude Code and Codex (`acp connect`).

`install.sh` leaves a link it finds to `acp.app` alone.

To remove the app, quit it and move it to the Trash. The link `~/.local/bin/acp` and the MCP
entries still point into the deleted app:
- Remove the link with `rm ~/.local/bin/acp`, or reinstall the CLI with `install.sh` once the link is gone.
- Remove the MCP entries with `claude mcp remove --scope user acp` and `codex mcp remove acp`.

## Updates

Sparkle updates `acp.app`, and with it the bundled acp. ak and the AgentKit kit are updated by
`acp upgrade`, as from a terminal. AgentKit's own app updates ak too while its `updates.enabled`
setting is on. With both installed, two updaters manage the same ak, so keep one: turn
`updates.enabled` off, or leave ak upgrades to that app.
