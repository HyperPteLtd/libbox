import Foundation
import SystemExtensions

final class SystemExtensionActivator:NSObject,OSSystemExtensionRequestDelegate {
    private var pending:OSSystemExtensionRequest?
    private var completion:((Error?)->Void)?
    private var approval:(()->Void)?
    private var timeout:DispatchWorkItem?

    func activate(identifier:String,onApproval:@escaping()->Void,completion:@escaping(Error?)->Void) {
        guard pending==nil else { completion(HostControllerError("A system extension request is already pending"));return }
        let request=OSSystemExtensionRequest.activationRequest(forExtensionWithIdentifier:identifier,queue:.main)
        pending=request;self.completion=completion;approval=onApproval;request.delegate=self
        let timeout=DispatchWorkItem { [weak self,weak request] in
            guard let self,let request,self.pending === request else { return }
            self.finish(HostControllerError("System extension activation timed out; retry after system approval"))
        }
        self.timeout=timeout
        DispatchQueue.main.asyncAfter(deadline:.now()+120,execute:timeout)
        OSSystemExtensionManager.shared.submitRequest(request)
    }
    func requestNeedsUserApproval(_ request:OSSystemExtensionRequest) {
        guard pending === request else { return };approval?()
    }
    func request(_ request:OSSystemExtensionRequest,didFinishWithResult result:OSSystemExtensionRequest.Result) {
        guard pending === request else { return }
        if result == .completed { finish(nil) }
        else { finish(HostControllerError("The system extension needs a reboot before it can start")) }
    }
    func request(_ request:OSSystemExtensionRequest,didFailWithError error:Error) {
        guard pending === request else { return };finish(error)
    }
    func request(_ request:OSSystemExtensionRequest,actionForReplacingExtension existing:OSSystemExtensionProperties,withExtension ext:OSSystemExtensionProperties)->OSSystemExtensionRequest.ReplacementAction {
        pending === request ? .replace:.cancel
    }
    private func finish(_ error:Error?) {
        timeout?.cancel();timeout=nil
        let callback=completion;pending=nil;completion=nil;approval=nil
        callback?(error)
    }
}
