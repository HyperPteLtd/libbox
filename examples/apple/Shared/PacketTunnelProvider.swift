import Foundation
import Network
import NetworkExtension
import Libbox
import SwiftProtobuf

/// One raw-FD provider for iOS app extensions and macOS system extensions.
/// NE owns the original utun and settings; Go owns only its duplicated FD.
final class PacketTunnelProvider: NEPacketTunnelProvider, LibboxHostMessageHandlerProtocol {
    private let state=NSLock()
    private let startup=DispatchQueue(label:"libbox.apple.startup")
    private let shutdown=DispatchQueue(label:"libbox.apple.shutdown")
    private let messages=DispatchQueue(label:"libbox.apple.messages",attributes:.concurrent)
    private let networkQueue=DispatchQueue(label:"libbox.apple.network")
    private let memoryQueue=DispatchQueue(label:"libbox.apple.memory")
    private var lifecycle=AppleTunnelLifecycle()
    private var endpoint:LibboxMessageSession?
    private var sequence:UInt64=0
    private var settingsWait:SettingsWait?
    private let settingsFence=SettingsFence()
    private let startupActivity=DispatchGroup()
    private var networkReady:DispatchSemaphore?
    private var snapshot=LBXNetwork()
    private var pathMonitor:NWPathMonitor?
    private var pressure:DispatchSourceMemoryPressure?
    private var borrowedTun:AppleRawTunDescriptor?

    private func call(_ session:LibboxMessageSession,_ input:LBXRequest)throws->LBXResponse {
        var request=input
        state.lock();sequence &+= 1;request.id=sequence;state.unlock()
        request.protocolVersion=1;request.timeoutMs=30000
        request.deadlineUnixMs=Int64(Date().timeIntervalSince1970*1000)+30000
        guard let data=session.handleMessage(try request.serializedData()) else { throw HostError("No core response") }
        let response=try LBXResponse(serializedBytes:data)
        guard response.protocolVersion==1,response.id==request.id else { throw HostError("Core response version/id mismatch") }
        if case .error(let error)?=response.result { throw HostError(error.code+": "+error.message) }
        return response
    }

    override func startTunnel(options:[String:NSObject]?,completionHandler:@escaping(Error?)->Void) {
        guard let configuration=protocolConfiguration as? NETunnelProviderProtocol,
              let saved=configuration.providerConfiguration?["startup_request"] as? Data,
              saved.count<=2*1024*1024,
              let request=try? LBXRequest(serializedBytes:saved),request.protocolVersion==1,
              case .start(let config)?=request.method else {
            completionHandler(HostError("Missing or invalid startup protobuf message"));return
        }
        let ready=DispatchSemaphore(value:0)
        let monitor=NWPathMonitor()
        let ticket:UInt64
        state.lock()
        do { ticket=try lifecycle.beginStart() }
        catch { state.unlock();completionHandler(error);return }
        startupActivity.enter()
        let finishStart:(Error?)->Void={ error in
            completionHandler(error);self.startupActivity.leave()
        }
        snapshot=LBXNetwork();snapshot.defaultInterface = -1
        networkReady=ready;pathMonitor=monitor;borrowedTun=nil
        monitor.pathUpdateHandler={ [weak self] path in self?.pathChanged(path,ticket:ticket,ready:ready) }
        monitor.start(queue:networkQueue)
        state.unlock()

        startup.async {
            var opened:LibboxMessageSession?
            do {
                guard ready.wait(timeout:.now()+5) == .success else { throw HostError("Timed out waiting for the physical network path") }
                self.state.lock()
                let current=self.lifecycle.isCurrent(ticket)
                self.state.unlock()
                guard current else { throw HostError("Startup canceled") }
                let home=try FileManager.default.url(for:.applicationSupportDirectory,in:.userDomainMask,appropriateFor:nil,create:true)
                    .appendingPathComponent("Libbox",isDirectory:true)
                guard let session=LibboxNewMessageSession(self) else { throw HostError("Cannot allocate message endpoint") }
                opened=session
                self.state.lock()
                guard self.lifecycle.isCurrent(ticket) else { self.state.unlock();throw HostError("Startup canceled") }
                self.endpoint=session;self.state.unlock()
                var create=LBXRequest()
                create.createEngine=AppleHostPolicy.engineOptions(home:home,includeAllNetworks:configuration.includeAllNetworks)
                _ = try self.call(session,create)
                try self.networkQueue.sync {
                    self.state.lock()
                    let active=self.lifecycle.isCurrent(ticket),network=self.snapshot
                    self.state.unlock()
                    guard active else { throw HostError("Startup canceled") }
                    var update=LBXRequest();update.updateNetwork=network;_ = try self.call(session,update)
                }
                self.state.lock();let active=self.lifecycle.isCurrent(ticket);self.state.unlock()
                guard active else { throw HostError("Startup canceled") }
                var start=LBXRequest();start.start=config;_ = try self.call(session,start)
                self.state.lock()
                let committed=self.borrowedTun != nil && self.lifecycle.commitStart(ticket)
                self.state.unlock()
                guard committed else { throw HostError("Startup canceled or no raw TUN was attached") }
                self.installMemoryPressure(session,ticket:ticket)
                finishStart(nil)
            } catch {
                try? opened?.close()
                self.state.lock()
                if self.endpoint === opened { self.endpoint=nil;self.borrowedTun=nil }
                self.state.unlock();monitor.cancel()
                // A timed-out settings call may still be in the system. Drain it
                // before clearing settings or allowing a replacement start.
                self.settingsFence.notify(on:self.startup) {
                    self.state.lock()
                    guard self.lifecycle.isCurrent(ticket) else {
                        self.state.unlock();finishStart(error);return
                    }
                    let completion=self.settingsFence.begin()
                    self.setTunnelNetworkSettings(nil) { _ in
                        self.startup.async {
                            self.state.lock();self.lifecycle.failStart(ticket);self.state.unlock()
                            completion.finish();finishStart(error)
                        }
                    }
                    self.state.unlock()
                }
            }
        }
    }

    private func pathChanged(_ path:Network.NWPath,ticket:UInt64,ready:DispatchSemaphore) {
        guard let value=try? NetworkSnapshot.message(path:path) else { return }
        state.lock()
        guard lifecycle.isCurrent(ticket) else { state.unlock();ready.signal();return }
        let changed=snapshot != value
        snapshot=value;let session=endpoint
        state.unlock();ready.signal()
        if changed,let session {
            var request=LBXRequest();request.updateNetwork=value
            _ = try? call(session,request)
        }
    }

    func handleHostMessage(_ data:Data?)throws->Data {
        guard let data,data.count<=4*1024*1024 else { throw HostError("Invalid host message") }
        let request=try LBXHostRequest(serializedBytes:data)
        var response=LBXHostResponse();response.protocolVersion=1;response.id=request.id
        do {
            guard request.protocolVersion==1 else { throw HostError("Unsupported host protocol") }
            switch request.method {
            case .openTun(let options):
                var descriptor=LBXTunDescriptor()
                descriptor.fd=try openTun(options);response.tun=descriptor
            default:throw HostError("Host capability is not installed")
            }
        } catch {
            var failure=LBXError();failure.code="host_error";failure.message=error.localizedDescription
            response.error=failure
        }
        return try response.serializedData()
    }

    private func openTun(_ options:LBXTunOptions)throws->Int32 {
        let settings=try TunnelSettings(options).networkSettings()
        let wait=SettingsWait()
        state.lock()
        guard lifecycle.active else { state.unlock();throw HostError("Tunnel is stopping") }
        let ticket=lifecycle.generation,completion=settingsFence.begin()
        settingsWait=wait
        // Register and submit under the same lock as stop admission. The callback
        // never acquires this lock, including if the API invokes it inline.
        setTunnelNetworkSettings(settings) { wait.finish($0);completion.finish() }
        state.unlock()
        defer { state.lock();if settingsWait === wait { settingsWait=nil };state.unlock() }
        try wait.wait()
        let descriptor=try AppleRawTunDescriptor.locate()
        state.lock();defer { state.unlock() }
        guard lifecycle.isCurrent(ticket) else { throw HostError("Tunnel is stopping") }
        borrowedTun=descriptor
        return descriptor.fileDescriptor
    }

    override func stopTunnel(with reason:NEProviderStopReason,completionHandler:@escaping()->Void) {
        state.lock()
        let ticket=lifecycle.beginStop()
        let session=endpoint,wait=settingsWait,ready=networkReady,monitor=pathMonitor,source=pressure
        pressure=nil;state.unlock()
        ready?.signal();wait?.finish(HostError("Tunnel is stopping"));monitor?.cancel();source?.cancel()
        shutdown.async {
            if let session {
                var destroy=LBXRequest();destroy.destroyEngine=LBXEmpty()
                _ = try? self.call(session,destroy)
                try? session.close()
            }
            self.settingsFence.notify(on:self.shutdown) {
                self.startupActivity.notify(queue:self.shutdown) {
                    self.state.lock()
                    if self.lifecycle.generation==ticket {
                        if self.endpoint === session { self.endpoint=nil;self.borrowedTun=nil }
                        self.pathMonitor=nil;self.networkReady=nil
                        self.lifecycle.finishStop(ticket)
                    }
                    self.state.unlock()
                    // Both Go and the outstanding OS settings callbacks are done.
                    // NE can now close its original descriptor.
                    completionHandler()
                }
            }
        }
    }

    override func handleAppMessage(_ data:Data,completionHandler:((Data?)->Void)?) {
        messages.async {
            guard data.count<=4*1024*1024,let request=try? LBXRequest(serializedBytes:data) else {
                completionHandler?(Self.failure(id:0,code:"invalid_argument",message:"Invalid provider message"));return
            }
            switch request.method {
            case .createEngine,.destroyEngine,.start,.reload,.stop,.updateNetwork:
                completionHandler?(Self.failure(id:request.id,code:"unsupported",message:"Use NETunnelProviderManager for lifecycle/configuration"));return
            default:break
            }
            self.state.lock();let session=self.lifecycle.active ? self.endpoint:nil;self.state.unlock()
            guard let session else { completionHandler?(Self.failure(id:request.id,code:"not_running",message:"Tunnel is not running"));return }
            completionHandler?(session.handleMessage(data))
        }
    }

    private func installMemoryPressure(_ session:LibboxMessageSession,ticket:UInt64) {
        let source=DispatchSource.makeMemoryPressureSource(eventMask:[.warning,.critical],queue:memoryQueue)
        source.setEventHandler { [weak self,weak session,weak source] in
            guard let self,let session,let source else { return }
            var pressure=LBXMemoryPressure();pressure.level=source.data.contains(.critical) ? .critical:.warning
            var request=LBXRequest();request.memoryPressure=pressure
            _ = try? self.call(session,request)
        }
        source.resume()
        state.lock()
        guard lifecycle.isCurrent(ticket) else { state.unlock();source.cancel();return }
        pressure=source;state.unlock()
    }
    private static func failure(id:UInt64,code:String,message:String)->Data? {
        var response=LBXResponse();response.protocolVersion=1;response.id=id
        var error=LBXError();error.code=code;error.message=message;response.error=error
        return try? response.serializedData()
    }
}
