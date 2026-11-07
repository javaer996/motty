import SwiftUI

@main
struct MottyApp: App {
    @StateObject private var bridge = MottyBridge.shared
    @Environment(\.scenePhase) private var scenePhase
    @AppStorage(AppLanguage.storageKey) private var language = AppLanguage.system.rawValue

    var body: some Scene {
        WindowGroup {
            rootView
        }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active {
                bridge.pollEvents()
                bridge.syncOnForeground()
            }
        }
    }

    @ViewBuilder
    private var rootView: some View {
        let content = RootView()
            .environmentObject(bridge)
            .preferredColorScheme(.dark)
            .onAppear { bridge.start() }
        if let locale = (AppLanguage(rawValue: language) ?? .system).locale {
            content.environment(\.locale, locale)
        } else {
            content
        }
    }
}
