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
            BackgroundSessionKeeper.shared.setBackground(phase == .background)
        }
    }

    @ViewBuilder
    private var rootView: some View {
        let content = RootView()
            .environmentObject(bridge)
            .preferredColorScheme(.dark)
            .onAppear {
                bridge.start()
                SessionLiveActivityManager.shared.start()
                BackgroundSessionKeeper.shared.start()
                #if DEBUG
                UITestSupport.seedIfRequested()
                #endif
            }
        if let locale = (AppLanguage(rawValue: language) ?? .system).locale {
            content.environment(\.locale, locale)
        } else {
            content
        }
    }
}
