import SwiftUI
import UniformTypeIdentifiers

/// Exports the shared data as a `.mottybak` file the desktop version can open,
/// and restores one picked from Files. Both sides ask for the same backup
/// password used by cloud sync.
struct BackupView: View {
    @EnvironmentObject private var bridge: MottyBridge

    @State private var exportPassword = ""
    @State private var exportConfirm = ""
    @State private var exporting = false
    @State private var exported: BackupExportResult?
    @State private var exportURL: URL?

    @State private var importing = false
    @State private var selectedPath: String?
    @State private var selectedName = ""
    @State private var restorePassword = ""
    @State private var restoring = false
    @State private var confirmingRestore = false

    @State private var message: String?

    var body: some View {
        Form {
            Section {
                SecureField("Backup password", text: $exportPassword)
                SecureField("Repeat password", text: $exportConfirm)
                Button {
                    export()
                } label: {
                    if exporting {
                        ProgressView()
                    } else {
                        Text("Export backup")
                    }
                }
                .disabled(exporting || exportPassword.count < 8 || exportPassword != exportConfirm)
                if let exported {
                    ShareLink(item: exportURL ?? URL(fileURLWithPath: exported.path)) {
                        LabeledContent("Share \(exported.name)", value: bytesText(exported.bytes))
                    }
                }
            } header: {
                Text("Export")
            } footer: {
                Text("Contains the hosts, private keys, known hosts and assistant settings. The password has at least eight characters, and the desktop version opens the same file.")
            }

            Section {
                Button {
                    importing = true
                } label: {
                    Label("Choose a backup file", systemImage: "doc.badge.plus")
                }
                if selectedPath != nil {
                    LabeledContent("File", value: selectedName)
                    SecureField("Backup password", text: $restorePassword)
                    Button {
                        confirmingRestore = true
                    } label: {
                        if restoring {
                            ProgressView()
                        } else {
                            Text("Restore this backup")
                        }
                    }
                    .disabled(
                        restoring || restorePassword.isEmpty || !bridge.sessions.isEmpty ||
                        bridge.syncStatus.running == true
                    )
                    .alert("Restore this backup?", isPresented: $confirmingRestore) {
                        Button("Cancel", role: .cancel) {}
                        Button("Restore", role: .destructive) { restore() }
                    } message: {
                        Text("The hosts and keys currently on this iPhone will be replaced.")
                    }
                    if !bridge.sessions.isEmpty {
                        Text("Close every terminal tab before restoring.")
                            .font(.caption)
                            .foregroundStyle(.orange)
                    } else if bridge.syncStatus.running == true {
                        Text("Wait for the current sync to finish.")
                            .font(.caption)
                            .foregroundStyle(.orange)
                    }
                }
            } header: {
                Text("Restore")
            } footer: {
                Text("Restoring replaces the hosts and keys on this iPhone with the file's copy. The current data is kept as config.db.before-restore.")
            }

            Section {
                Text("Backups stay in the app's private storage. Use Share to save one in Files, or choose a backup file to restore it here.")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                Text("Cloud sync and backups use the desktop version's formats, so hosts, keys and settings carry over.")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            } header: {
                Text("Storage")
            }
        }
        .navigationTitle("Backup")
        .navigationBarTitleDisplayMode(.inline)
        .fileImporter(
            isPresented: $importing,
            allowedContentTypes: backupTypes,
            allowsMultipleSelection: false
        ) { result in
            handlePicked(result)
        }
        .alert("Backup", isPresented: messageBinding) {
            Button("OK") { message = nil }
        } message: {
            Text(message ?? "")
        }
    }

    private var backupTypes: [UTType] {
        var types: [UTType] = [.data]
        if let motty = UTType(filenameExtension: "mottybak") {
            types.insert(motty, at: 0)
        }
        return types
    }

    private var messageBinding: Binding<Bool> {
        Binding(get: { message != nil }, set: { if !$0 { message = nil } })
    }

    private func bytesText(_ count: Int) -> String {
        ByteCountFormatter.string(fromByteCount: Int64(count), countStyle: .file)
    }

    private func export() {
        exporting = true
        bridge.exportBackup(password: exportPassword) { result in
            exporting = false
            switch result {
            case .success(let exported):
                self.exported = exported
                exportURL = URL(fileURLWithPath: exported.path)
                exportPassword = ""
                exportConfirm = ""
                message = AppLanguage.localized(
                    "Saved \(exported.name) (\(bytesText(exported.bytes))). Use Share to copy it out of the app."
                )
            case .failure(let error):
                message = error.localizedDescription
            }
        }
    }

    private func handlePicked(_ result: Result<[URL], Error>) {
        switch result {
        case .failure(let error):
            message = error.localizedDescription
        case .success(let urls):
            guard let url = urls.first else { return }
            guard let directory = bridge.dataDirectory else {
                message = AppLanguage.localized("The app's data directory is unavailable.")
                return
            }
            let inbox = directory.appendingPathComponent("inbox", isDirectory: true)
            let access = url.startAccessingSecurityScopedResource()
            defer { if access { url.stopAccessingSecurityScopedResource() } }
            let destination = uniqueDestination(inbox: inbox, name: url.lastPathComponent)
            do {
                try FileManager.default.createDirectory(at: inbox, withIntermediateDirectories: true)
                try FileManager.default.copyItem(at: url, to: destination)
            } catch {
                message = AppLanguage.localized("Could not copy the file: \(error.localizedDescription)")
                return
            }
            selectedPath = destination.path
            selectedName = destination.lastPathComponent
            restorePassword = ""
            bridge.inspectBackup(path: destination.path) { inspected in
                switch inspected {
                case .success(let info):
                    if !info.backup {
                        selectedPath = nil
                        message = AppLanguage.localized("This file is not a Motty backup.")
                    }
                case .failure(let error):
                    selectedPath = nil
                    message = error.localizedDescription
                }
            }
        }
    }

    private func uniqueDestination(inbox: URL, name: String) -> URL {
        let base = (name as NSString).deletingPathExtension
        let ext = (name as NSString).pathExtension
        var candidate = inbox.appendingPathComponent(name)
        var index = 2
        while FileManager.default.fileExists(atPath: candidate.path) {
            let fileName = ext.isEmpty ? "\(base)-\(index)" : "\(base)-\(index).\(ext)"
            candidate = inbox.appendingPathComponent(fileName)
            index += 1
        }
        return candidate
    }

    private func restore() {
        guard let path = selectedPath else { return }
        restoring = true
        bridge.restoreBackup(path: path, password: restorePassword) { result in
            restoring = false
            switch result {
            case .success(let restored):
                selectedPath = nil
                restorePassword = ""
                message = AppLanguage.localized("Restored \(restored.hosts) hosts.")
            case .failure(let error):
                message = error.localizedDescription
            }
        }
    }
}
