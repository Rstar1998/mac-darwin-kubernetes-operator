// swift-tools-version: 5.9
// Swift Package for metal-proxy
import PackageDescription

let package = Package(
    name: "MetalProxy",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "metal-proxy", targets: ["MetalProxy"]),
    ],
    dependencies: [
        .package(url: "https://github.com/grpc/grpc-swift.git", from: "1.23.0"),
        .package(url: "https://github.com/apple/swift-log.git", from: "1.5.4"),
        .package(url: "https://github.com/apple/swift-argument-parser.git", from: "1.3.0"),
    ],
    targets: [
        .executableTarget(
            name: "MetalProxy",
            dependencies: [
                .product(name: "GRPC", package: "grpc-swift"),
                .product(name: "Logging", package: "swift-log"),
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
            ],
            path: "Sources/MetalProxy"
        ),
        .testTarget(
            name: "MetalProxyTests",
            dependencies: ["MetalProxy"],
            path: "Tests/MetalProxyTests"
        ),
    ]
)
