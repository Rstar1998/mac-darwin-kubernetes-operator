// Unit tests for MetalProxyServer (non-GPU paths)
import XCTest
@testable import MetalProxy

final class MetalProxyServerTests: XCTestCase {
    func testErrorDescriptions() {
        XCTAssertFalse(MetalProxyError.noMetalDevice.description.isEmpty)
        XCTAssertTrue(MetalProxyError.unsupportedBackend("foo").description.contains("foo"))
        XCTAssertTrue(MetalProxyError.jobTimeout("job-1").description.contains("job-1"))
    }

    func testJobPriorityOrdering() {
        XCTAssertTrue(MetalProxyServer.JobPriority.high < .normal)
        XCTAssertTrue(MetalProxyServer.JobPriority.normal < .batch)
        XCTAssertFalse(MetalProxyServer.JobPriority.batch < .high)
    }
}
