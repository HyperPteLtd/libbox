import Foundation

struct HostError: LocalizedError {
    let errorDescription: String?
    init(_ message: String) { errorDescription=message }
}

/// Mutated only under the provider's state lock. Same lifecycle on both Apple OSes.
struct AppleTunnelLifecycle {
    enum Phase { case idle,starting,running,stopping }
    private(set) var phase=Phase.idle
    private(set) var generation:UInt64=0
    var active:Bool { phase == .starting || phase == .running }
    mutating func beginStart() throws -> UInt64 {
        guard phase == .idle else { throw HostError("Tunnel lifecycle is busy") }
        generation &+= 1;phase = .starting;return generation
    }
    func isCurrent(_ token:UInt64)->Bool { active && generation==token }
    mutating func commitStart(_ token:UInt64)->Bool {
        guard phase == .starting,generation==token else { return false }
        phase = .running;return true
    }
    mutating func failStart(_ token:UInt64) {
        if phase == .starting && generation==token { phase = .idle }
    }
    mutating func beginStop()->UInt64 {
        generation &+= 1;phase = .stopping;return generation
    }
    mutating func finishStop(_ token:UInt64) {
        if phase == .stopping && generation==token { phase = .idle }
    }
}

/// A completed/canceled/timed-out settings operation cannot be reopened by a late callback.
final class SettingsWait {
    private let condition=NSCondition()
    private var finished=false
    private var failure:Error?
    @discardableResult func finish(_ error:Error?)->Bool {
        condition.lock();defer { condition.unlock() }
        guard !finished else { return false }
        finished=true;failure=error;condition.broadcast();return true
    }
    func wait(timeout:TimeInterval=25) throws {
        condition.lock();defer { condition.unlock() }
        let deadline=Date().addingTimeInterval(timeout)
        while !finished {
            if !condition.wait(until:deadline) && !finished {
                finished=true;failure=HostError("Timed out applying tunnel settings")
            }
        }
        if let failure { throw failure }
    }
}

/// Tracks actual system callbacks, independently of a canceled SettingsWait.
/// Admission is protected by the provider's state lock; stop closes admission
/// before registering its drain notification.
final class SettingsFence {
    private let group=DispatchGroup()
    func begin()->Completion {
        group.enter();return Completion(group)
    }
    func notify(on queue:DispatchQueue,execute:@escaping()->Void) {
        group.notify(queue:queue,execute:execute)
    }
    final class Completion {
        private let lock=NSLock()
        private var group:DispatchGroup?
        fileprivate init(_ group:DispatchGroup) { self.group=group }
        func finish() {
            lock.lock();let pending=group;group=nil;lock.unlock()
            pending?.leave()
        }
    }
}
