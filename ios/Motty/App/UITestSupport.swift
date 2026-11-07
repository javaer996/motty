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

        // A stable id replaces the fixture on every launch. Without it the
        // store derives a new id per launch and leaves a stale, passwordless
        // host behind that older tests would match first.
        var host = Host.empty()
        host.id = "motty-ui-fixture"
        host.label = label
        host.address = address
        host.port = port
        host.user = user
        host.auth = "password"
        MottyBridge.shared.refreshHosts {
            // Drop fixtures left by earlier launches before saving the new
            // one. The serial bridge queue keeps these ordered ahead of the
            // save below.
            for stale in MottyBridge.shared.hosts where stale.label.hasPrefix("motty-ui-fixture") {
                MottyBridge.shared.deleteHost(stale)
            }
            MottyBridge.shared.saveHost(host) { result in
                guard case .success(let saved) = result else { return }
                MottyBridge.shared.setHostPassword(id: saved.id, password: password)
            }
        }
    }
}
#endif
