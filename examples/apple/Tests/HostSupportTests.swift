import Foundation

@main enum HostSupportTests {
    static func check(_ value:Bool,_ message:String) throws { if !value { throw HostError(message) } }
    static func rejects(_ work:() throws -> Void) throws {
        do { try work() } catch { return }
        throw HostError("Expected rejection")
    }
    static func main() throws {
        let fd=try AppleRawTunDescriptor.select([.init(fileDescriptor:0,interfaceName:"utun7")])
        try check(fd.fileDescriptor==0,"borrowed fd zero must remain representable")
        for name in ["utun","utun-1","utun+1","en0","utun١"] { try check(!AppleRawTunDescriptor.validName(name),"invalid utun name accepted") }
        try rejects { _ = try AppleRawTunDescriptor.select([]) }
        try rejects { _ = try AppleRawTunDescriptor.select([.init(fileDescriptor:5,interfaceName:"utun1"),.init(fileDescriptor:6,interfaceName:"utun1")]) }
        try rejects { _ = try AppleRawTunDescriptor.select([.init(fileDescriptor:5,interfaceName:"utun1"),.init(fileDescriptor:6,interfaceName:"utun2")]) }
        var lifecycle=AppleTunnelLifecycle()
        let first=try lifecycle.beginStart()
        try rejects { _ = try lifecycle.beginStart() }
        let stop=lifecycle.beginStop()
        try check(!lifecycle.commitStart(first),"late start committed after stop")
        try rejects { _ = try lifecycle.beginStart() }
        lifecycle.finishStop(stop)
        let second=try lifecycle.beginStart()
        lifecycle.failStart(first)
        try check(lifecycle.commitStart(second),"old completion damaged new start")
        let end=lifecycle.beginStop();lifecycle.finishStop(end)
        try check(lifecycle.phase == .idle,"stop did not return to idle")
        let canceled=SettingsWait();canceled.finish(HostError("canceled"))
        try rejects { try canceled.wait(timeout:0.01) }
        try check(!canceled.finish(nil),"late settings callback reopened canceled operation")
        let timeout=SettingsWait()
        try rejects { try timeout.wait(timeout:0.001) }
        try check(!timeout.finish(nil),"late callback reopened timeout")
        let fence=SettingsFence(),queue=DispatchQueue(label:"settings-fence-test")
        let firstSetting=fence.begin(),secondSetting=fence.begin()
        let drained=DispatchSemaphore(value:0)
        fence.notify(on:queue) { drained.signal() }
        firstSetting.finish();firstSetting.finish()
        try check(drained.wait(timeout:.now()+0.02) == .timedOut,"settings fence released an outstanding OS operation")
        // Cancellation of the synchronous waiter must not release this fence.
        secondSetting.finish()
        try check(drained.wait(timeout:.now()+1) == .success,"settings callbacks did not release stop")
        print("Apple raw-FD/lifecycle unit tests passed (mock descriptors; no VPN or simulator)")
    }
}
