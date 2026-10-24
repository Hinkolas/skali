// swift-tools-version: 6.2

import PackageDescription

// SkaliKit is everything the Apple apps share below the UI: the API client,
// the wire models, the browser sign-in flow, credential storage, and the
// presentation rollups that keep every screen agreeing with the Studio.
// It builds for every Apple platform so `swift test` runs on the Mac.
let package = Package(
    name: "SkaliKit",
    platforms: [
        .iOS(.v26),
        .macOS(.v26),
        .watchOS(.v26),
    ],
    products: [
        .library(name: "SkaliKit", targets: ["SkaliKit"]),
    ],
    targets: [
        .target(name: "SkaliKit"),
        .testTarget(name: "SkaliKitTests", dependencies: ["SkaliKit"]),
    ]
)
