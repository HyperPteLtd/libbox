import Darwin
import Foundation

/// Borrowed from NetworkExtension. The core duplicates it; this type never closes it.
struct AppleRawTunDescriptor {
    let fileDescriptor: Int32
    let interfaceName: String

    static func validName(_ name: String) -> Bool {
        guard name.hasPrefix("utun") else { return false }
        let suffix=name.utf8.dropFirst(4)
        return !suffix.isEmpty && suffix.allSatisfy { $0>=48 && $0<=57 }
    }
    static func select(_ candidates: [AppleRawTunDescriptor]) throws -> AppleRawTunDescriptor {
        guard candidates.count==1,let candidate=candidates.first,candidate.fileDescriptor>=0,
              validName(candidate.interfaceName) else { throw HostError("Unable to identify one unambiguous NetworkExtension utun descriptor") }
        return candidate
    }
    static func locate() throws -> AppleRawTunDescriptor {
        var limit=rlimit()
        guard getrlimit(RLIMIT_NOFILE,&limit)==0 else { throw HostError("Cannot inspect descriptor limit") }
        let upper=Int32(min(limit.rlim_cur,65_536))
        var candidates=[AppleRawTunDescriptor]()
        for descriptor in 0..<upper {
            if let name=interfaceName(descriptor) {
                candidates.append(AppleRawTunDescriptor(fileDescriptor:descriptor,interfaceName:name))
                // Even two aliases are ambiguous to this provider. Never choose the first.
                if candidates.count>1 { break }
            }
        }
        return try select(candidates)
    }
    private static func interfaceName(_ descriptor: Int32) -> String? {
        var buffer=[UInt8](repeating:0,count:Int(IFNAMSIZ))
        var length=socklen_t(buffer.count)
        let result=buffer.withUnsafeMutableBytes { getsockopt(descriptor,2,2,$0.baseAddress,&length) }
        guard result==0,length>0,length<=buffer.count else { return nil }
        let bytes=buffer.prefix(Int(length)).prefix { $0 != 0 }
        guard let name=String(bytes:bytes,encoding:.utf8),validName(name) else { return nil }
        return name
    }
}
