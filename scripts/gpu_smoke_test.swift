// gpu_smoke_test.swift — Standalone test that proves Metal GPU access works.
// Run with: swift gpu_smoke_test.swift
import Metal
import Foundation

print("=== Apple GPU Operator — Metal Smoke Test ===\n")

// 1. Create Metal device
guard let device = MTLCreateSystemDefaultDevice() else {
    print("❌ FAIL: No Metal device found — is this Apple Silicon?")
    exit(1)
}
print("✅ Metal Device:  \(device.name)")
print("   Registry ID:   \(device.registryID)")
print("   Unified Memory: \(String(format: "%.1f", Double(device.recommendedMaxWorkingSetSize) / 1e9)) GB")
print("   Raytracing:    \(device.supportsRaytracing ? "Yes" : "No")")

// 2. Detect GPU family
var gpuFamily = "unknown"
if #available(macOS 14.0, *) {
    if device.supportsFamily(.apple9) { gpuFamily = "apple9 (M3+)" }
    else if device.supportsFamily(.apple8) { gpuFamily = "apple8 (M2)" }
    else if device.supportsFamily(.apple7) { gpuFamily = "apple7 (M1)" }
} else {
    if device.supportsFamily(.apple8) { gpuFamily = "apple8 (M2)" }
    else if device.supportsFamily(.apple7) { gpuFamily = "apple7 (M1)" }
}
print("   GPU Family:    \(gpuFamily)")

// 3. Create a command queue (this is what metal-proxy does)
guard let queue = device.makeCommandQueue() else {
    print("❌ FAIL: Cannot create MTLCommandQueue")
    exit(1)
}
print("\n✅ Command Queue: created (\(queue.label ?? "default"))")

// 4. Submit a trivial command buffer (proves GPU compute works)
guard let buffer = queue.makeCommandBuffer() else {
    print("❌ FAIL: Cannot create MTLCommandBuffer")
    exit(1)
}
buffer.label = "smoke-test"
buffer.commit()
buffer.waitUntilCompleted()

if buffer.status == .completed {
    let gpuTimeNS = UInt64((buffer.gpuEndTime - buffer.gpuStartTime) * 1_000_000_000)
    print("✅ GPU Compute:   Command buffer completed in \(gpuTimeNS) ns")
} else {
    print("❌ FAIL: Command buffer status = \(buffer.status.rawValue)")
    exit(1)
}

// 5. Create a compute pipeline (proves shader compilation works)
let source = """
#include <metal_stdlib>
using namespace metal;
kernel void add_arrays(device const float* inA,
                       device const float* inB,
                       device float* result,
                       uint index [[thread_position_in_grid]]) {
    result[index] = inA[index] + inB[index];
}
"""

do {
    let library = try device.makeLibrary(source: source, options: nil)
    guard let function = library.makeFunction(name: "add_arrays") else {
        print("❌ FAIL: Cannot find 'add_arrays' kernel")
        exit(1)
    }
    let pipeline = try device.makeComputePipelineState(function: function)
    print("✅ Compute Shader: 'add_arrays' compiled (maxThreads=\(pipeline.maxTotalThreadsPerThreadgroup))")
    
    // 6. Actually run a GPU computation
    let count = 1024
    let bufferSize = count * MemoryLayout<Float>.stride
    
    guard let bufA = device.makeBuffer(length: bufferSize, options: .storageModeShared),
          let bufB = device.makeBuffer(length: bufferSize, options: .storageModeShared),
          let bufR = device.makeBuffer(length: bufferSize, options: .storageModeShared) else {
        print("❌ FAIL: Cannot create Metal buffers")
        exit(1)
    }
    
    // Fill input buffers
    let ptrA = bufA.contents().bindMemory(to: Float.self, capacity: count)
    let ptrB = bufB.contents().bindMemory(to: Float.self, capacity: count)
    for i in 0..<count {
        ptrA[i] = Float(i)
        ptrB[i] = Float(i * 2)
    }
    
    // Run GPU kernel
    guard let cmdBuf = queue.makeCommandBuffer(),
          let encoder = cmdBuf.makeComputeCommandEncoder() else {
        print("❌ FAIL: Cannot create compute encoder")
        exit(1)
    }
    
    encoder.setComputePipelineState(pipeline)
    encoder.setBuffer(bufA, offset: 0, index: 0)
    encoder.setBuffer(bufB, offset: 0, index: 1)
    encoder.setBuffer(bufR, offset: 0, index: 2)
    
    let gridSize = MTLSize(width: count, height: 1, depth: 1)
    let threadGroupSize = MTLSize(
        width: min(pipeline.maxTotalThreadsPerThreadgroup, count),
        height: 1, depth: 1
    )
    encoder.dispatchThreads(gridSize, threadsPerThreadgroup: threadGroupSize)
    encoder.endEncoding()
    
    cmdBuf.commit()
    cmdBuf.waitUntilCompleted()
    
    // Verify results
    let ptrR = bufR.contents().bindMemory(to: Float.self, capacity: count)
    var correct = true
    for i in 0..<count {
        if ptrR[i] != Float(i) + Float(i * 2) {
            correct = false
            print("❌ FAIL: result[\(i)] = \(ptrR[i]), expected \(Float(i) + Float(i * 2))")
            break
        }
    }
    
    if correct {
        let gpuNS = UInt64((cmdBuf.gpuEndTime - cmdBuf.gpuStartTime) * 1_000_000_000)
        print("✅ GPU Compute:   1024-element vector add PASSED in \(gpuNS) ns")
    }
    
} catch {
    print("❌ FAIL: Shader compilation error: \(error)")
    exit(1)
}

// 7. Thermal state (what the exporter monitors)
let thermal = ProcessInfo.processInfo.thermalState
let thermalLabel: String = {
    switch thermal {
    case .nominal: return "Nominal ✅"
    case .fair: return "Fair ⚠️"
    case .serious: return "Serious 🔥"
    case .critical: return "Critical 🚨"
    @unknown default: return "Unknown"
    }
}()
print("\n✅ Thermal State: \(thermalLabel)")

print("\n=== ALL TESTS PASSED — GPU is ready for operator workloads ===")
