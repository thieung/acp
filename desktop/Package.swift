// swift-tools-version:5.9
// acp desktop: a thin macOS shell around the bundled acp binary. AcpDesktopCore holds the pure,
// tested logic; AcpDesktop is the AppKit launcher; Sparkle updates the app.
import PackageDescription

let package = Package(
    name: "AcpDesktop",
    platforms: [.macOS(.v13)],
    products: [
        .executable(name: "AcpDesktop", targets: ["AcpDesktop"]),
    ],
    dependencies: [
        .package(url: "https://github.com/sparkle-project/Sparkle", exact: "2.9.6"),
    ],
    targets: [
        .target(name: "AcpDesktopCore"),
        .executableTarget(
            name: "AcpDesktop",
            dependencies: [
                "AcpDesktopCore",
                .product(name: "Sparkle", package: "Sparkle"),
            ]
        ),
        .testTarget(name: "AcpDesktopCoreTests", dependencies: ["AcpDesktopCore"]),
    ]
)
