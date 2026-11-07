import SwiftUI

struct HostEditorView: View {
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss
    @State private var host: Host
    @State private var saving = false
    @State private var password = ""
    @State private var passwordSaved = false
    @State private var removePassword = false
    @State private var errorMessage: String?

    init(host: Host) {
        _host = State(initialValue: host)
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("General") {
                    TextField("Name", text: $host.label)
                    TextField("Group", text: optional(\.group))
                }
                Section("Connection") {
                    Picker("Protocol", selection: Binding(
                        get: { host.protocolName },
                        set: { selectProtocol($0) }
                    )) {
                        Text("SSH terminal").tag("ssh")
                        Text("VNC desktop").tag("vnc")
                        Text("RDP desktop").tag("rdp")
                    }
                    TextField("Host or IP", text: $host.address)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    TextField("Port", value: $host.port, format: .number)
                        .keyboardType(.numberPad)
                    if host.protocolName != "vnc" {
                        TextField(host.isDesktop ? "Account (optional)" : "User", text: $host.user)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                    }
                }
                if host.isDesktop {
                    Section {
                        Picker("Via SSH", selection: optional(\.viaSsh)) {
                            Text("Direct connection").tag("")
                            ForEach(bridge.hosts.filter { !$0.isDesktop }) { gateway in
                                Text(gateway.label).tag(gateway.id)
                            }
                        }
                        Toggle("Share clipboard", isOn: Binding(
                            get: { host.clipboardEnabled },
                            set: { host.clipboard = $0 }
                        ))
                        if host.protocolName == "rdp" {
                            Toggle("Console session", isOn: Binding(
                                get: { host.console ?? false },
                                set: { host.console = $0 }
                            ))
                        }
                    } header: {
                        Text("Remote desktop")
                    } footer: {
                        if host.viaSsh?.isEmpty == false {
                            Text("Open the SSH host first: the desktop is dialed from inside that server, so plain VNC or RDP need not be exposed.")
                        } else if host.protocolName == "vnc" {
                            Text("VNC has no account; the server asks only for its password.")
                        } else {
                            Text("The account may be left empty. The certificate is confirmed the first time you connect.")
                        }
                    }
                } else {
                    Section {
                        Picker("Mode", selection: optional(\.auth)) {
                            Text("Automatic").tag("")
                            Text("Password").tag("password")
                            Text("Private key").tag("key")
                            Text("Agent").tag("agent")
                        }
                        if wantsKey {
                            Picker("Private key", selection: optional(\.identity)) {
                                Text("None").tag("")
                                ForEach(bridge.keys) { key in
                                    Text(key.name).tag(key.path)
                                }
                                // A host imported from an SSH config may point at a
                                // path this device does not have yet.
                                if let identity = host.identity, !identity.isEmpty,
                                   !bridge.keys.contains(where: { $0.path == identity }) {
                                    Text(identity).tag(identity)
                                }
                            }
                        }
                        if wantsPassword {
                            SecureField(
                                passwordSaved ? "Saved password (type to replace)" : "Password (optional)",
                                text: $password
                            )
                            .textContentType(.password)
                            .onChange(of: password) { _, value in
                                if !value.isEmpty { removePassword = false }
                            }
                            if passwordSaved {
                                Button(
                                    removePassword ? "Keep the saved password" : "Remove the saved password",
                                    role: removePassword ? nil : .destructive
                                ) {
                                    removePassword.toggle()
                                    if removePassword { password = "" }
                                }
                            }
                        }
                    } header: {
                        Text("Authentication")
                    } footer: {
                        if authMode == "agent" {
                            Text("iOS has no ssh-agent. Choose a private key or Automatic instead; a password can still be typed when the server asks.")
                        } else if wantsKey && bridge.keys.isEmpty {
                            Text("Import a private key in Settings before choosing one.")
                        } else if wantsPassword {
                            if removePassword {
                                Text("The saved password is removed when you save.")
                            } else {
                                Text("Saved in the encrypted vault and used automatically when the server asks. Leave it empty to keep the current one.")
                            }
                        }
                    }
                    Section("Advanced") {
                        TextField("Proxy jump", text: optional(\.proxyJump))
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                        TextField("Login command", text: optional(\.loginCommand))
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                    }
                }
            }
            .navigationTitle(host.id.isEmpty ? "New host" : "Edit host")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { save() }
                        .disabled(!canSave || saving)
                }
            }
            .onAppear(perform: loadPasswordStatus)
            .alert("Could not save", isPresented: errorBinding) {
                Button("OK") { errorMessage = nil }
            } message: {
                Text(errorMessage ?? "")
            }
        }
    }

    private var authMode: String { host.auth ?? "" }

    /// A private key only belongs to Automatic and Private key.
    private var wantsKey: Bool {
        !host.isDesktop && (authMode.isEmpty || authMode == "key")
    }

    /// A saved password belongs to Automatic and Password; Agent signs in by
    /// itself, and a private key never asks for a password.
    private var wantsPassword: Bool {
        !host.isDesktop && (authMode.isEmpty || authMode == "password")
    }

    private var errorBinding: Binding<Bool> {
        Binding(get: { errorMessage != nil }, set: { if !$0 { errorMessage = nil } })
    }

    private var canSave: Bool {
        if host.address.trimmingCharacters(in: .whitespaces).isEmpty { return false }
        if host.isDesktop { return true }
        return !host.user.trimmingCharacters(in: .whitespaces).isEmpty
    }

    private func selectProtocol(_ value: String) {
        let previousDefault = Host.defaultPort(for: host.protocolName)
        if value == "ssh" {
            host.`protocol` = nil
            host.viaSsh = nil
            host.clipboard = nil
            host.console = nil
        } else {
            host.`protocol` = value
            if host.clipboard == nil { host.clipboard = true }
        }
        if host.port == 0 || host.port == previousDefault {
            host.port = Host.defaultPort(for: value)
        }
    }

    private func optional(_ keyPath: WritableKeyPath<Host, String?>) -> Binding<String> {
        Binding(
            get: { host[keyPath: keyPath] ?? "" },
            set: { host[keyPath: keyPath] = $0.isEmpty ? nil : $0 }
        )
    }

    private func loadPasswordStatus() {
        guard !host.id.isEmpty, wantsPassword else { return }
        bridge.hostPasswordStatus(id: host.id) { saved in
            passwordSaved = saved
        }
    }

    private func save() {
        saving = true
        bridge.saveHost(host) { result in
            switch result {
            case .success(let saved):
                savePassword(for: saved)
            case .failure(let error):
                saving = false
                errorMessage = error.localizedDescription
            }
        }
    }

    /// The host's row is written first so a new host has the id its password is
    /// kept under. The password itself never becomes part of the saved host.
    private func savePassword(for saved: Host) {
        if wantsPassword && !password.isEmpty {
            bridge.setHostPassword(id: saved.id, password: password) { error in
                saving = false
                if let error {
                    errorMessage = error.localizedDescription
                } else {
                    dismiss()
                }
            }
        } else if wantsPassword && removePassword && passwordSaved {
            bridge.setHostPassword(id: saved.id, password: "", remove: true) { error in
                saving = false
                if let error {
                    errorMessage = error.localizedDescription
                } else {
                    dismiss()
                }
            }
        } else {
            saving = false
            dismiss()
        }
    }
}
