import SwiftUI

struct HostEditorView: View {
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss
    @State private var host: Host
    @State private var saving = false
    @State private var password = ""
    @State private var passwordSaved = false
    @State private var removePassword = false
    @State private var proxyPassword = ""
    @State private var proxyPasswordSaved = false
    @State private var removeProxyPassword = false
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
                    Section {
                        passwordFields
                    } header: {
                        Text("Authentication")
                    } footer: {
                        if removePassword {
                            Text("The saved password is removed when you save.")
                        } else {
                            Text("Saved in the encrypted vault and used automatically when the server asks. Leave it empty to keep the current one.")
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
                            passwordFields
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
                Section {
                    Picker("Proxy", selection: proxyKindBinding) {
                        Text("Direct connection").tag("")
                        Text("SOCKS5").tag(HostProxy.socks5)
                        Text("HTTP CONNECT").tag(HostProxy.http)
                    }
                    if let proxy = host.proxy, proxy.enabled {
                        TextField("Proxy address", text: proxyBinding(\.address))
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                        TextField("Proxy user (optional)", text: proxyOptionalBinding(\.user))
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                        if proxy.user?.isEmpty == false {
                            SecureField(
                                proxyPasswordSaved ? "Saved proxy password (type to replace)" : "Proxy password (optional)",
                                text: $proxyPassword
                            )
                            .textContentType(.password)
                            .onChange(of: proxyPassword) { _, value in
                                if !value.isEmpty { removeProxyPassword = false }
                            }
                            if proxyPasswordSaved {
                                Button(
                                    removeProxyPassword ? "Keep the saved proxy password" : "Remove the saved proxy password",
                                    role: removeProxyPassword ? nil : .destructive
                                ) {
                                    removeProxyPassword.toggle()
                                    if removeProxyPassword { proxyPassword = "" }
                                }
                            }
                        }
                    }
                } header: {
                    Text("Outbound proxy")
                } footer: {
                    if host.proxy?.enabled == true {
                        if host.proxy?.user?.isEmpty != false {
                            Text("Set a proxy user to be able to save its password.")
                        } else if removeProxyPassword {
                            Text("The saved proxy password is removed when you save.")
                        } else {
                            Text("The proxy carries this host's first connection. Its password is kept in the vault and used automatically.")
                        }
                    } else {
                        Text("A direct connection does not use a proxy.")
                    }
                }
                .onChange(of: host.proxy) { _, _ in
                    // The saved password belongs to a kind, address and user;
                    // editing them makes the old one unreachable.
                    proxyPasswordSaved = false
                    removeProxyPassword = false
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

    /// A saved password belongs to Automatic and Password, and to every remote
    /// desktop (VNC and RDP ask for one); Agent signs in by itself, and a
    /// private key never asks for a password.
    private var wantsPassword: Bool {
        if host.isDesktop { return true }
        return authMode.isEmpty || authMode == "password"
    }

    /// The password field and its remove button, shared by the terminal and
    /// desktop forms.
    @ViewBuilder
    private var passwordFields: some View {
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

    private var errorBinding: Binding<Bool> {
        Binding(get: { errorMessage != nil }, set: { if !$0 { errorMessage = nil } })
    }

    private var canSave: Bool {
        if host.address.trimmingCharacters(in: .whitespaces).isEmpty { return false }
        if let proxy = host.proxy, proxy.enabled,
           proxy.address.trimmingCharacters(in: .whitespaces).isEmpty {
            return false
        }
        if host.isDesktop { return true }
        return !host.user.trimmingCharacters(in: .whitespaces).isEmpty
    }

    /// Picking a kind turns the proxy on or off; the other fields stay when the
    /// user switches between SOCKS5 and HTTP.
    private var proxyKindBinding: Binding<String> {
        Binding(
            get: { host.proxy?.kind ?? "" },
            set: { kind in
                if kind.isEmpty {
                    host.proxy = nil
                } else if var proxy = host.proxy {
                    proxy.kind = kind
                    host.proxy = proxy
                } else {
                    host.proxy = HostProxy(kind: kind)
                }
            }
        )
    }

    private func proxyBinding(_ keyPath: WritableKeyPath<HostProxy, String>) -> Binding<String> {
        Binding(
            get: { host.proxy?[keyPath: keyPath] ?? "" },
            set: { value in
                guard var proxy = host.proxy else { return }
                proxy[keyPath: keyPath] = value
                host.proxy = proxy
            }
        )
    }

    private func proxyOptionalBinding(_ keyPath: WritableKeyPath<HostProxy, String?>) -> Binding<String> {
        Binding(
            get: { host.proxy?[keyPath: keyPath] ?? "" },
            set: { value in
                guard var proxy = host.proxy else { return }
                proxy[keyPath: keyPath] = value.isEmpty ? nil : value
                host.proxy = proxy
            }
        )
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
        guard !host.id.isEmpty else { return }
        if wantsPassword {
            bridge.hostPasswordStatus(id: host.id) { saved in
                passwordSaved = saved
            }
        }
        if host.proxy?.enabled == true, host.proxy?.user?.isEmpty == false {
            bridge.hostPasswordStatus(id: host.id, kind: "proxy") { saved in
                proxyPasswordSaved = saved
            }
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

    /// The host's row is written first so a new host has the id its passwords
    /// are kept under. The passwords themselves never become part of the saved
    /// host.
    private func savePassword(for saved: Host) {
        saveLoginPassword(for: saved) { loginError in
            if let loginError {
                self.saving = false
                self.errorMessage = loginError.localizedDescription
                return
            }
            self.saveProxyPassword(for: saved) { proxyError in
                self.saving = false
                if let proxyError {
                    self.errorMessage = proxyError.localizedDescription
                } else {
                    self.dismiss()
                }
            }
        }
    }

    private func saveLoginPassword(for saved: Host, completion: @escaping (Error?) -> Void) {
        if wantsPassword && !password.isEmpty {
            bridge.setHostPassword(id: saved.id, password: password, completion: completion)
        } else if wantsPassword && removePassword && passwordSaved {
            bridge.setHostPassword(id: saved.id, password: "", remove: true, completion: completion)
        } else {
            completion(nil)
        }
    }

    private func saveProxyPassword(for saved: Host, completion: @escaping (Error?) -> Void) {
        guard saved.proxy?.enabled == true, saved.proxy?.user?.isEmpty == false else {
            completion(nil)
            return
        }
        if !proxyPassword.isEmpty {
            bridge.setHostPassword(
                id: saved.id, password: proxyPassword, kind: "proxy", completion: completion
            )
        } else if removeProxyPassword && proxyPasswordSaved {
            bridge.setHostPassword(
                id: saved.id, password: "", remove: true, kind: "proxy", completion: completion
            )
        } else {
            completion(nil)
        }
    }
}
