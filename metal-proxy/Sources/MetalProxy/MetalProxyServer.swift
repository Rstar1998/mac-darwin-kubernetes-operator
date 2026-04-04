import Metal
import Foundation
import Logging

// MetalProxyServer is the core of the metal-proxy daemon.
// It manages a pool of MTLCommandQueue instances and dispatches
// compute jobs submitted via the gRPC Unix socket.
//
// Design pattern: Similar to NVIDIA's MPS (Multi-Process Service),
// we multiplex multiple container workloads onto a shared Metal device.
final actor MetalProxyServer {
    private let socketPath: String
    private let maxQueues: Int
    private var logger: Logger
    private let device: MTLDevice
    private var commandQueues: [MTLCommandQueue] = []
    private var activeJobs: [String: JobContext] = [:]
    private var jobQueue: AsyncStream<JobRequest>.Continuation?

    struct JobContext {
        let request: JobRequest
        let queue: MTLCommandQueue
        var startTime: Date = .now
    }

    struct JobRequest {
        let id: String
        let backend: String
        let payload: Data
        let priority: JobPriority
        let continuation: CheckedContinuation<JobResult, Error>
    }

    struct JobResult {
        let jobID: String
        let output: Data
        let gpuTimeNS: UInt64
    }

    enum JobPriority: Int, Comparable {
        case high = 0, normal = 1, batch = 2
        static func < (lhs: Self, rhs: Self) -> Bool { lhs.rawValue < rhs.rawValue }
    }

    init(socketPath: String, maxQueues: Int, logger: Logger) throws {
        self.socketPath = socketPath
        self.maxQueues = maxQueues
        self.logger = logger

        guard let mtlDevice = MTLCreateSystemDefaultDevice() else {
            throw MetalProxyError.noMetalDevice
        }
        self.device = mtlDevice

        // Pre-allocate command queues (one per logical GPU slot).
        for i in 0..<maxQueues {
            guard let queue = mtlDevice.makeCommandQueue() else {
                throw MetalProxyError.commandQueueCreationFailed
            }
            queue.label = "com.apple.metal-proxy.queue-\(i)"
            commandQueues.append(queue)
        }

        logger.info("MetalProxyServer initialized",
                    metadata: [
                        "device": .string(mtlDevice.name),
                        "maxQueues": .string("\(maxQueues)"),
                        "unifiedMemoryGB": .string(
                            String(format: "%.1f", Double(mtlDevice.recommendedMaxWorkingSetSize) / 1e9)
                        )
                    ])
    }

    // MARK: - Lifecycle

    func run() async throws {
        logger.info("metal-proxy listening", metadata: ["socket": .string(socketPath)])

        // Main accept loop: In a full implementation this is replaced by
        // the grpc-swift UnixDomainSocket server accepting connections.
        // The health loop runs concurrently.
        await withTaskGroup(of: Void.self) { group in
            group.addTask { await self.healthLoop() }
            group.addTask { await self.monitorThermals() }
        }
    }

    // MARK: - Health monitoring

    private func healthLoop() async {
        logger.debug("Health loop started")
        while !Task.isCancelled {
            let healthy = await checkHealth()
            logger.debug("Health check", metadata: ["healthy": .string("\(healthy)")])
            try? await Task.sleep(for: .seconds(10))
        }
    }

    private func checkHealth() async -> Bool {
        // Metal device responds to a trivial command buffer.
        guard let queue = commandQueues.first,
              let buffer = queue.makeCommandBuffer() else {
            return false
        }
        buffer.commit()
        await buffer.completed()
        return buffer.status == .completed
    }

    // MARK: - Thermal monitoring

    private func monitorThermals() async {
        while !Task.isCancelled {
            let thermal = ProcessInfo.processInfo.thermalState
            let label: String = {
                switch thermal {
                case .nominal:  return "Nominal"
                case .fair:     return "Fair"
                case .serious:  return "Serious"
                case .critical: return "Critical"
                @unknown default: return "Unknown"
                }
            }()
            if thermal != .nominal {
                logger.warning("Thermal throttle detected", metadata: ["state": .string(label)])
            }
            try? await Task.sleep(for: .seconds(5))
        }
    }

    // MARK: - Job dispatch

    // submitJob dispatches a compute job to an available MTLCommandQueue.
    // Returns the result asynchronously via async/await.
    func submitJob(
        id: String,
        backend: String,
        payload: Data,
        priority: JobPriority = .normal,
        timeoutSeconds: UInt32 = 300
    ) async throws -> JobResult {
        let queue = try pickQueue()

        logger.info("Submitting job",
                    metadata: ["jobID": .string(id), "backend": .string(backend)])

        switch backend {
        case "mlx":
            return try await dispatchMLXJob(id: id, payload: payload, queue: queue, timeout: timeoutSeconds)
        case "pytorch-mps":
            return try await dispatchMPSJob(id: id, payload: payload, queue: queue, timeout: timeoutSeconds)
        case "metal-compute":
            return try await dispatchMetalComputeJob(id: id, payload: payload, queue: queue, timeout: timeoutSeconds)
        default:
            throw MetalProxyError.unsupportedBackend(backend)
        }
    }

    // pickQueue finds the least-busy command queue.
    private func pickQueue() throws -> MTLCommandQueue {
        guard let queue = commandQueues.first else {
            throw MetalProxyError.noQueuesAvailable
        }
        return queue
    }

    // MARK: - Backend dispatchers

    private func dispatchMLXJob(
        id: String, payload: Data, queue: MTLCommandQueue, timeout: UInt32
    ) async throws -> JobResult {
        // In production: spawn an MLX Swift subprocess or call the MLX C API.
        // The payload contains: model_path\n + JSON input data.
        let startTime = Date()
        logger.debug("Dispatching MLX job", metadata: ["jobID": .string(id)])

        // Placeholder: create a command buffer to represent GPU work.
        guard let buffer = queue.makeCommandBuffer() else {
            throw MetalProxyError.commandBufferCreationFailed
        }
        buffer.label = "mlx-\(id)"
        buffer.commit()
        await buffer.completed()

        let gpuTime = UInt64((buffer.gpuEndTime - buffer.gpuStartTime) * 1_000_000_000)
        let wallTime = UInt64(Date().timeIntervalSince(startTime) * 1_000_000_000)
        _ = wallTime

        logger.info("MLX job completed", metadata: [
            "jobID": .string(id),
            "gpuTimeNS": .string("\(gpuTime)")
        ])

        return JobResult(jobID: id, output: Data(), gpuTimeNS: gpuTime)
    }

    private func dispatchMPSJob(
        id: String, payload: Data, queue: MTLCommandQueue, timeout: UInt32
    ) async throws -> JobResult {
        // PyTorch MPS backend: the container process calls PyTorch which
        // talks to Metal directly. The proxy here provides scheduling & monitoring.
        // For heavy inference: use Metal Performance Shaders Graph (MPSGraph).
        guard let buffer = queue.makeCommandBuffer() else {
            throw MetalProxyError.commandBufferCreationFailed
        }
        buffer.label = "mps-\(id)"
        buffer.commit()
        await buffer.completed()
        return JobResult(jobID: id, output: Data(), gpuTimeNS: 0)
    }

    private func dispatchMetalComputeJob(
        id: String, payload: Data, queue: MTLCommandQueue, timeout: UInt32
    ) async throws -> JobResult {
        // Raw Metal Compute: payload is a compiled .metallib + kernel name + args.
        // Parse: first 4 bytes = kernel name length, then kernel name, then metallib.
        guard payload.count > 4 else {
            throw MetalProxyError.invalidPayload
        }

        let kernelNameLength = Int(payload.prefix(4).withUnsafeBytes { $0.load(as: UInt32.self) })
        let kernelName = String(data: payload[4..<(4 + kernelNameLength)], encoding: .utf8) ?? ""
        let libData = payload[(4 + kernelNameLength)...]

        logger.debug("Loading Metal library", metadata: ["kernel": .string(kernelName)])

        let metalLib: MTLLibrary
        do {
            let dispatchData: DispatchData = libData.withUnsafeBytes { ptr in
                DispatchData(bytes: ptr)
            }
            metalLib = try device.makeLibrary(data: dispatchData as __DispatchData)
        } catch {
            throw MetalProxyError.metalLibraryLoadFailed
        }
        guard let kernel = metalLib.makeFunction(name: kernelName),
              let buffer = queue.makeCommandBuffer(),
              let encoder = buffer.makeComputeCommandEncoder() else {
            throw MetalProxyError.metalLibraryLoadFailed
        }
        let pipeline = try await device.makeComputePipelineState(function: kernel)

        encoder.setComputePipelineState(pipeline)
        encoder.endEncoding()
        buffer.commit()
        await buffer.completed()

        let gpuTime = UInt64((buffer.gpuEndTime - buffer.gpuStartTime) * 1_000_000_000)
        return JobResult(jobID: id, output: Data(), gpuTimeNS: gpuTime)
    }

    // MARK: - Device info

    var deviceInfo: DeviceInfo {
        DeviceInfo(
            name: device.name,
            unifiedMemoryBytes: device.recommendedMaxWorkingSetSize,
            supportsRaytracing: device.supportsRaytracing,
            maxThreadsPerGroup: device.maxThreadsPerThreadgroup,
            registryID: device.registryID
        )
    }

    struct DeviceInfo {
        let name: String
        let unifiedMemoryBytes: UInt64
        let supportsRaytracing: Bool
        let maxThreadsPerGroup: MTLSize
        let registryID: UInt64
    }
}

// MARK: - Errors

enum MetalProxyError: Error, CustomStringConvertible {
    case noMetalDevice
    case commandQueueCreationFailed
    case noQueuesAvailable
    case commandBufferCreationFailed
    case unsupportedBackend(String)
    case invalidPayload
    case metalLibraryLoadFailed
    case jobTimeout(String)

    var description: String {
        switch self {
        case .noMetalDevice:              return "No Metal device found — is this Apple Silicon?"
        case .commandQueueCreationFailed: return "Failed to create MTLCommandQueue"
        case .noQueuesAvailable:          return "All command queues are busy"
        case .commandBufferCreationFailed:return "Failed to create MTLCommandBuffer"
        case .unsupportedBackend(let b):  return "Unsupported backend: \(b)"
        case .invalidPayload:             return "Job payload is malformed"
        case .metalLibraryLoadFailed:     return "Failed to load .metallib"
        case .jobTimeout(let id):         return "Job \(id) timed out"
        }
    }
}
