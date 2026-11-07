import SwiftUI

/// SwiftUI wrapper around the UIKit terminal. The UIKit view owns the display
/// link and the input accessory; SwiftUI only gives it a size and a session.
struct TerminalView: UIViewRepresentable {
    let sessionID: UInt32
    @EnvironmentObject private var bridge: MottyBridge

    func makeUIView(context: Context) -> TerminalUIView {
        let view = TerminalUIView(sessionID: sessionID, bridge: bridge)
        view.start()
        DispatchQueue.main.async { view.becomeFirstResponder() }
        return view
    }

    func updateUIView(_ uiView: TerminalUIView, context: Context) {}

    static func dismantleUIView(_ uiView: TerminalUIView, coordinator: ()) {
        uiView.stop()
    }
}
