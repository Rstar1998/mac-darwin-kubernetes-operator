import ArgumentParser
import Logging
import Foundation

// Starts the gRPC Unix socket server and the job dispatch loop.
@main
struct MetalProxy: AsyncParsableCommand {
    static var configuration = CommandConfiguration(
        commandName: "metal-proxy",
        abstract: "Apple GPU Operator — Metal Compute Proxy Daemon",
        version: "0.1.0"
    )

    @Option(name: .long, help: "Unix socket path to listen on.")
    var socketPath: String = "/var/run/metal-proxy/metal.sock"

    @Option(name: .long, help: "Maximum concurrent Metal command queues.")
    var maxQueues: Int = 4

    @Flag(name: .long, help: "Enable debug logging.")
    var debug: Bool = false

    mutating func run() async throws {
        var logger = Logger(label: "com.apple.metal-proxy")
        logger.logLevel = debug ? .debug : .info

        logger.info("Starting metal-proxy", metadata: [
            "socket": .string(socketPath),
            "maxQueues": .string("\(maxQueues)"),
            "os": .string(ProcessInfo.processInfo.operatingSystemVersionString)
        ])

        // Ensure socket directory exists.
        let socketDir = (socketPath as NSString).deletingLastPathComponent
        try FileManager.default.createDirectory(
            atPath: socketDir,
            withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o755]
        )

        // Remove stale socket from previous run.
        try? FileManager.default.removeItem(atPath: socketPath)

        let server = try MetalProxyServer(
            socketPath: socketPath,
            maxQueues: maxQueues,
            logger: logger
        )

        try await server.run()
    }
}
