import SwiftUI

struct SettingsView: View {
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss
    @AppStorage(AppLanguage.storageKey) private var language = AppLanguage.system.rawValue

    var body: some View {
        NavigationStack {
            Form {
                Section("Language") {
                    Picker("Language", selection: $language) {
                        ForEach(AppLanguage.allCases) { option in
                            Text(option.title).tag(option.rawValue)
                        }
                    }
                }
                Section("Terminal") {
                    LabeledContent("Font", value: "SF Mono 13")
                    LabeledContent("Scrollback") {
                        Text("5,000 lines")
                    }
                }
                Section("Authentication") {
                    NavigationLink {
                        KeysView()
                    } label: {
                        LabeledContent("Private keys", value: "\(bridge.keys.count)")
                    }
                }
                Section("Assistant") {
                    NavigationLink {
                        AISettingsView()
                    } label: {
                        LabeledContent("Provider & model") {
                            if bridge.aiSettings.model.isEmpty {
                                Text("Not configured")
                            } else {
                                Text(verbatim: bridge.aiSettings.model)
                            }
                        }
                    }
                }
                Section("Sync & Backup") {
                    NavigationLink {
                        SyncSettingsView()
                    } label: {
                        LabeledContent("Cloud sync") {
                            if bridge.syncStatus.isConfigured {
                                Text(verbatim: bridge.syncStatus.kindText)
                            } else {
                                Text("Off")
                            }
                        }
                    }
                    NavigationLink {
                        BackupView()
                    } label: {
                        Label("Backup & restore", systemImage: "externaldrive.badge.timemachine")
                    }
                }
                Section("Journals") {
                    NavigationLink {
                        LogsView()
                    } label: {
                        Label("Session log", systemImage: "clock.arrow.circlepath")
                    }
                    NavigationLink {
                        HistoryView()
                    } label: {
                        Label("Command history", systemImage: "text.append")
                    }
                }
                Section("About") {
                    LabeledContent("Version", value: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "—")
                    LabeledContent("Core", value: "Go")
                }
            }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                Button("Done") { dismiss() }
            }
        }
    }
}
