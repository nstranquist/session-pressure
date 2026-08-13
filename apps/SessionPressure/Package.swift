// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "NDevPressure",
    platforms: [.macOS(.v14)],
    targets: [
        .target(
            name: "NDevPressureCore",
            path: "Sources/NDevPressureCore"
        ),
        .executableTarget(
            name: "NDevPressure",
            dependencies: ["NDevPressureCore"],
            path: "Sources/NDevPressure"
        ),
        .executableTarget(
            name: "NDevPressureTraceHelper",
            dependencies: ["NDevPressureCore"],
            path: "Sources/NDevPressureTraceHelper"
        ),
        .testTarget(
            name: "NDevPressureCoreTests",
            dependencies: ["NDevPressureCore", "NDevPressure"],
            path: "Tests/NDevPressureCoreTests",
            // Real captured CLI payloads. Hand-written fixtures twice diverged
            // from what ndev actually emits, so the contract tests decode a
            // scrubbed recording of the live command instead.
            resources: [.copy("Fixtures")]
        ),
    ]
)
