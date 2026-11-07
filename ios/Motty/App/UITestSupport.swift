#if DEBUG
import Foundation

/// Seeds the simulator with a disposable SSH host for UI tests. The app only
/// changes when launched with `-MottyUITestSeed`; release builds do not
/// compile this file's contents.
enum UITestSupport {
    /// Arguments: `-MottyUITestSeed <label> <address> <port> <user> <password>`.
    static func seedIfRequested() {
        let arguments = ProcessInfo.processInfo.arguments
        guard let index = arguments.firstIndex(of: "-MottyUITestSeed"),
              index + 5 < arguments.count else { return }
        let label = arguments[index + 1]
        let address = arguments[index + 2]
        guard let port = UInt16(arguments[index + 3]) else { return }
        let user = arguments[index + 4]
        let password = arguments[index + 5]

        var host = Host.empty()
        host.label = label
        host.address = address
        host.port = port
        host.user = user
        host.auth = "password"
        // `hosts.save` derives the id from address/user/port, so seeding on
        // every launch replaces the previous fixture instead of duplicating it.
        MottyBridge.shared.saveHost(host) { result in
            guard case .success(let saved) = result else { return }
            MottyBridge.shared.setHostPassword(id: saved.id, password: password)
        }
    }
}
#endif
