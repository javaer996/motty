import SwiftUI
import UniformTypeIdentifiers

/// Private keys imported into the app. iOS cannot read ~/.ssh, so a key is
/// copied in once and hosts point at the copy.
struct KeysView: View {
    @EnvironmentObject private var bridge: MottyBridge
    @State private var importing = false
    @State private var pasting = false
    @State private var pastedName = ""
    @State private var pastedText = ""
    @State private var pendingReplace: PendingImport?
    @State private var importMessage: String?

    private struct PendingImport: Identifiable {
        let id = UUID()
        let name: String
        let data: Data
    }

    var body: some View {
        List {
            if bridge.keys.isEmpty {
                Section {
                    ContentUnavailableView {
                        Label("No keys", systemImage: "key")
                    } description: {
                        Text("Import an OpenSSH or PEM private key. Encrypted keys ask for their passphrase when used.")
                    }
                }
            } else {
                Section {
                    ForEach(bridge.keys) { key in
                        VStack(alignment: .leading, spacing: 4) {
                            HStack(spacing: 6) {
                                Text(key.name)
                                    .font(.headline)
                                if key.encrypted {
                                    Text("passphrase")
                                        .font(.caption2)
                                        .padding(.horizontal, 6)
                                        .padding(.vertical, 2)
                                        .background(.quaternary, in: Capsule())
                                }
                            }
                            Text(key.shortPublicKey)
                                .font(.caption.monospaced())
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                        }
                    }
                    .onDelete { indexSet in
                        for index in indexSet {
                            bridge.deleteKey(bridge.keys[index])
                        }
                    }
                } footer: {
                    Text("Keys are stored encrypted in the same format as the desktop app. Remove a key here when no host uses it.")
                }
            }

            Section {
                Button {
                    importing = true
                } label: {
                    Label("Import private key", systemImage: "square.and.arrow.down")
                }
                Button {
                    pastedName = ""
                    pastedText = ""
                    pasting = true
                } label: {
                    Label("Paste private key", systemImage: "doc.on.clipboard")
                }
            }
        }
        .navigationTitle("Private keys")
        .sheet(isPresented: $pasting) {
            pasteKeySheet
        }
        .fileImporter(
            isPresented: $importing,
            allowedContentTypes: [.item],
            allowsMultipleSelection: false
        ) { result in
            handleImport(result)
        }
        .alert("Replace key?", isPresented: replaceBinding) {
            Button("Cancel", role: .cancel) { pendingReplace = nil }
            Button("Replace", role: .destructive) { replacePending() }
        } message: {
            Text("A key named \(pendingReplace?.name ?? "") already exists. Replacing it affects every host that uses it.")
        }
        .alert("Import", isPresented: messageBinding) {
            Button("OK") { importMessage = nil }
        } message: {
            Text(importMessage ?? "")
        }
    }

    /// A key can be pasted as text instead of picked from Files, which is how
    /// most people keep a key on a phone after copying it from a password
    /// manager or a chat message.
    private var pasteKeySheet: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("Key name", text: $pastedName)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                } footer: {
                    Text("The name identifies the key in the host editor.")
                }
                Section {
                    TextEditor(text: $pastedText)
                        .font(.caption.monospaced())
                        .frame(minHeight: 220)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                } header: {
                    Text("Private key")
                } footer: {
                    Text("Paste the whole key, including the BEGIN and END lines.")
                }
            }
            .navigationTitle("Paste private key")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { pasting = false }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Import") { importPastedKey() }
                        .disabled(pastedText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
        }
    }

    private func importPastedKey() {
        let text = pastedText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        let name = pastedName.trimmingCharacters(in: .whitespaces)
        pasting = false
        importKey(name: name.isEmpty ? "pasted-key" : name, data: Data((text + "\n").utf8))
    }

    private var replaceBinding: Binding<Bool> {
        Binding(get: { pendingReplace != nil }, set: { if !$0 { pendingReplace = nil } })
    }

    private var messageBinding: Binding<Bool> {
        Binding(get: { importMessage != nil }, set: { if !$0 { importMessage = nil } })
    }

    private func handleImport(_ result: Result<[URL], Error>) {
        switch result {
        case .failure(let error):
            importMessage = error.localizedDescription
        case .success(let urls):
            guard let url = urls.first else { return }
            let accessing = url.startAccessingSecurityScopedResource()
            defer { if accessing { url.stopAccessingSecurityScopedResource() } }
            do {
                let data = try Data(contentsOf: url)
                importKey(name: url.lastPathComponent, data: data)
            } catch {
                importMessage = error.localizedDescription
            }
        }
    }

    private func importKey(name: String, data: Data, overwrite: Bool = false) {
        bridge.importKey(name: name, data: data, overwrite: overwrite) { result in
            switch result {
            case .success(let key):
                importMessage = AppLanguage.localized("Imported \(key.name).")
                pendingReplace = nil
            case .failure(let error):
                if let rpc = error as? RPCError, rpc.code == "conflict" {
                    pendingReplace = PendingImport(name: name, data: data)
                } else {
                    importMessage = error.localizedDescription
                }
            }
        }
    }

    private func replacePending() {
        guard let pending = pendingReplace else { return }
        importKey(name: pending.name, data: pending.data, overwrite: true)
    }
}
