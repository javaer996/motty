import SwiftUI

/// Where the shared hosts, keys and assistant settings are backed up. The
/// passwords are written to the Keychain-backed vault and never shown back;
/// leaving a password field empty keeps the saved one.
struct SyncSettingsView: View {
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss

    @State private var kind = "webdav"
    @State private var savedKind = ""
    @State private var url = ""
    @State private var user = ""
    @State private var path = ""
    @State private var s3 = SyncS3Settings()
    @State private var gist = SyncGistSettings()
    @State private var secret = ""
    @State private var backupPassword = ""
    @State private var allowInsecureHTTP = false
    @State private var automatic = true
    @State private var loaded = false
    @State private var saving = false
    @State private var testing = false
    @State private var restoring = false
    @State private var message: String?
    @State private var confirmingRestore = false

    private var status: SyncStatus { bridge.syncStatus }

    var body: some View {
        Form {
            Section {
                Picker("Service", selection: $kind) {
                    Text("WebDAV").tag("webdav")
                    Text("S3-compatible").tag("s3")
                    Text("GitHub Gist").tag("gist")
                }
                .pickerStyle(.segmented)
            } footer: {
                Text("The server holds one encrypted file. Encrypt it with the backup password below; the desktop version of Motty reads the same file.")
            }

            switch kind {
            case "s3":
                Section("Bucket") {
                    TextField("Endpoint, such as https://s3.eu-west-1.amazonaws.com", text: $s3.endpoint)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                    TextField("Region (empty is us-east-1; R2 uses auto)", text: $s3.region)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    TextField("Bucket", text: $s3.bucket)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    TextField("Key", text: $s3.key)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    TextField("Access key", text: $s3.accessKey)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    Toggle("Path-style addresses", isOn: $s3.pathStyle)
                }
            case "gist":
                Section("Gist") {
                    TextField("Gist id (empty creates one)", text: $gist.id)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    TextField("File name", text: $gist.file)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    TextField("API address (optional)", text: $gist.api)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                }
            default:
                Section("Server") {
                    TextField("Address, such as https://dav.example.com/dav", text: $url)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                    TextField("User", text: $user)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    TextField("File path", text: $path)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                }
            }

            Section {
                SecureField(secretPrompt, text: $secret)
                SecureField(
                    status.hasBackupPassword == true ? "Backup password (saved)" : "Backup password",
                    text: $backupPassword
                )
                if kind != "gist" {
                    Toggle("Allow plain HTTP", isOn: $allowInsecureHTTP)
                }
            } header: {
                Text("Passwords")
            } footer: {
                Text("Leave a password empty to keep the one already saved. The backup password is also used to open the file with the desktop version.")
            }

            Section("Automatic sync") {
                Toggle("Sync when data changes", isOn: $automatic)
            }

            Section {
                Button {
                    save()
                } label: {
                    if saving {
                        ProgressView()
                    } else {
                        Text(LocalizedStringKey(status.isConfigured ? "Save changes" : "Turn on sync"))
                    }
                }
                .disabled(saving || !canSave || status.running == true)
                Button("Test connection") {
                    bridge.syncTestMessage = nil
                    testing = true
                    bridge.testSync()
                }
                .disabled(!status.isConfigured || testing)
                Button {
                    bridge.syncNow()
                } label: {
                    HStack {
                        Text("Sync now")
                        if status.running == true {
                            Spacer()
                            ProgressView()
                        }
                    }
                }
                .disabled(!status.isConfigured || status.running == true)
                Button("Restore from the server") {
                    confirmingRestore = true
                }
                .disabled(!status.isConfigured || restoring || status.running == true)
                .alert("Restore from the server?", isPresented: $confirmingRestore) {
                    Button("Cancel", role: .cancel) {}
                    Button("Restore", role: .destructive) { restore() }
                } message: {
                    Text("This replaces the hosts and keys on this iPhone with the server's copy. The current ones are kept in config.db.before-restore. Close every terminal tab first.")
                }
                Button("Forget this configuration", role: .destructive) {
                    bridge.clearSync { result in
                        if case .failure(let error) = result {
                            message = error.localizedDescription
                        }
                    }
                }
                .disabled(!status.isConfigured || status.running == true)
            }

            Section("Status") {
                LabeledContent("Service") {
                    if status.isConfigured {
                        Text(verbatim: status.kindText)
                    } else {
                        Text("Off")
                    }
                }
                if let last = status.lastSync, !last.isEmpty {
                    LabeledContent("Last sync", value: last)
                }
                if let error = status.error, !error.isEmpty {
                    Text(error)
                        .font(.caption)
                        .foregroundStyle(.orange)
                }
                if !status.isConfigured {
                    Text("Fill in the service and both passwords, then turn sync on.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
        }
        .navigationTitle("Sync")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear(perform: load)
        .onChange(of: bridge.syncStatus) { _, new in
            guard loaded, !saving else { return }
            automatic = !new.isManual
        }
        .onChange(of: bridge.syncTestMessage) { _, value in
            guard let value else { return }
            testing = false
            message = value
            bridge.syncTestMessage = nil
        }
        .onChange(of: bridge.syncStatus.running) { _, running in
            if running == false { testing = false }
        }
        .alert("Sync", isPresented: messageBinding) {
            Button("OK") { message = nil }
        } message: {
            Text(message ?? "")
        }
        .onDisappear {
            if testing { message = nil }
        }
    }

    private var messageBinding: Binding<Bool> {
        Binding(get: { message != nil }, set: { if !$0 { message = nil } })
    }

    private var secretPrompt: LocalizedStringKey {
        switch kind {
        case "s3":
            return status.hasSecret == true ? "Secret key (saved)" : "Secret key"
        case "gist":
            return status.hasSecret == true ? "Personal access token (saved)" : "Personal access token"
        default:
            return status.hasSecret == true ? "Password (saved)" : "Password"
        }
    }

    private var canSave: Bool {
        switch kind {
        case "s3":
            if s3.endpoint.isEmpty || s3.bucket.isEmpty { return false }
        case "gist":
            break
        default:
            if url.isEmpty || user.isEmpty { return false }
        }
        if (status.hasSecret != true || kind != savedKind) && secret.isEmpty { return false }
        if status.hasBackupPassword != true && backupPassword.count < 8 { return false }
        if !backupPassword.isEmpty && backupPassword.count < 8 { return false }
        return true
    }

    private func load() {
        guard !loaded else { return }
        loaded = true
        let current = bridge.syncStatus
        if let value = current.kind, !value.isEmpty { kind = value }
        savedKind = kind
        url = current.url ?? ""
        user = current.user ?? ""
        path = current.path ?? ""
        if let value = current.s3 { s3 = value }
        if let value = current.gist { gist = value }
        allowInsecureHTTP = current.allowInsecureHTTP ?? false
        automatic = !current.isManual
        if kind == "gist" { gist.api = current.gist?.api ?? "" }
    }

    private func save() {
        saving = true
        var params = MottyBridge.SyncConfigureParams(
            kind: kind,
            url: url,
            user: user,
            path: path,
            s3: kind == "s3" ? s3 : nil,
            gist: kind == "gist" ? gist : nil,
            secret: secret,
            backupPassword: backupPassword,
            allowInsecureHTTP: allowInsecureHTTP,
            manual: !automatic
        )
        if kind == "gist" {
            params.url = ""
            params.user = ""
            params.path = ""
        }
        if kind != "s3" { params.s3 = nil }
        if kind != "gist" { params.gist = nil }
        bridge.configureSync(params) { result in
            saving = false
            switch result {
            case .success(let updated):
                secret = ""
                backupPassword = ""
                savedKind = updated.kind ?? kind
                url = updated.url ?? url
                user = updated.user ?? user
                path = updated.path ?? path
                if let value = updated.s3 { s3 = value }
                if let value = updated.gist { gist = value }
                message = AppLanguage.localized("Sync is on. The first sync runs in a moment.")
            case .failure(let error):
                message = error.localizedDescription
            }
        }
    }

    private func restore() {
        restoring = true
        bridge.restoreFromSync { result in
            restoring = false
            switch result {
            case .success(let restored):
                message = AppLanguage.localized("Restored \(restored.hosts) hosts from the server.")
            case .failure(let error):
                message = error.localizedDescription
            }
        }
    }
}
