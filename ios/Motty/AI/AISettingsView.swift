import SwiftUI

/// Providers of the in-app assistant. Several may be configured at once; the
/// picker chooses the one every session talks to.
struct AISettingsView: View {
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss
    @State private var settings = AISettings()
    @State private var saving = false
    @State private var loaded = false
    @State private var localError: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    if settings.providers.isEmpty {
                        Text("No assistant provider is configured yet.")
                            .foregroundStyle(.secondary)
                    } else {
                        Picker(selection: currentBinding) {
                            ForEach(settings.providers) { provider in
                                Text(verbatim: provider.displayName).tag(provider.id)
                            }
                        } label: {
                            Text("Active provider")
                        }
                    }
                } header: {
                    Text("Active provider")
                } footer: {
                    Text("The assistant uses the selected provider for every session.")
                }

                Section {
                    ForEach(settings.providers) { provider in
                        NavigationLink {
                            // The bindings, not a callback that captures this
                            // view: a destination closure capturing the
                            // enclosing @State makes SwiftUI re-create the
                            // destination on every pass and the app spins.
                            AIProviderEditorView(
                                provider: provider,
                                providers: $settings.providers,
                                currentId: $settings.currentId
                            )
                        } label: {
                            VStack(alignment: .leading, spacing: 3) {
                                HStack(spacing: 6) {
                                    Text(verbatim: provider.displayName)
                                    if provider.id == settings.current?.id {
                                        Image(systemName: "checkmark.circle.fill")
                                            .font(.caption)
                                            .foregroundStyle(.tint)
                                    }
                                }
                                Text(verbatim: provider.summary)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                    .onDelete(perform: delete)
                    NavigationLink {
                        AIProviderEditorView(
                            provider: AIProvider(id: "new"),
                            providers: $settings.providers,
                            currentId: $settings.currentId
                        )
                    } label: {
                        Label("Add provider", systemImage: "plus")
                    }
                    .accessibilityIdentifier("ai.addProvider")
                } header: {
                    Text("Providers")
                } footer: {
                    Text("Each provider keeps its own endpoint, key and model. The key is stored in the app's private container and only sent to that endpoint.")
                }

                Section {
                    NavigationLink {
                        AIPrivacyView()
                    } label: {
                        Label("Privacy protection", systemImage: "hand.raised.fill")
                    }
                    .accessibilityIdentifier("ai.privacy")
                } footer: {
                    Text("Reversible tokens hide saved secrets, addresses and account names before they are sent.")
                }

                Section {
                    Text("Commands rated low risk (reads, listings) run automatically. Anything that changes the host asks you first; critical commands require typing CONFIRM.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
            .navigationTitle("Assistant")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { save() }
                        .disabled(saving)
                        .accessibilityIdentifier("ai.save")
                }
            }
            .onAppear {
                guard !loaded else { return }
                settings = bridge.aiSettings
                loaded = true
            }
            .alert("Assistant", isPresented: errorBinding) {
                Button("OK") { localError = nil }
            } message: {
                Text(localError ?? "")
            }
        }
    }

    private var currentBinding: Binding<String> {
        Binding(
            get: { settings.current?.id ?? "" },
            set: { settings.currentId = $0 }
        )
    }

    private var errorBinding: Binding<Bool> {
        Binding(get: { localError != nil }, set: { if !$0 { localError = nil } })
    }

    private func delete(_ offsets: IndexSet) {
        let removed = offsets.map { settings.providers[$0] }
        settings.providers.remove(atOffsets: offsets)
        if let current = settings.currentId, removed.contains(where: { $0.id == current }) {
            settings.currentId = settings.providers.first?.id
        }
    }

    private func save() {
        saving = true
        bridge.saveAISettings(settings) { result in
            saving = false
            switch result {
            case .success:
                dismiss()
            case .failure(let error):
                localError = error.localizedDescription
            }
        }
    }
}

/// One provider's endpoint, key, model and behaviour. Edits go straight into
/// the list through bindings; backing out of a new provider leaves no trace.
private struct AIProviderEditorView: View {
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss
    @State private var draft: AIProvider
    @State private var models: [String] = []
    @State private var loadingModels = false
    @State private var localError: String?
    @Binding private var providers: [AIProvider]
    @Binding private var currentId: String?

    init(provider: AIProvider, providers: Binding<[AIProvider]>, currentId: Binding<String?>) {
        _draft = State(initialValue: provider)
        _providers = providers
        _currentId = currentId
    }

    var body: some View {
        Form {
            Section {
                TextField("Name", text: optional(\.name), prompt: Text("Optional label"))
                    .textInputAutocapitalization(.words)
                    .accessibilityIdentifier("ai.name")
                Picker("Protocol", selection: $draft.provider) {
                    Text("OpenAI compatible").tag("openai")
                    Text("Anthropic").tag("anthropic")
                    Text("OpenAI Responses").tag("responses")
                }
                TextField("Base URL", text: optional(\.baseUrl), prompt: Text("Use the provider default"))
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                SecureField("API key", text: optional(\.apiKey))
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
            } header: {
                Text("Provider")
            } footer: {
                Text("The key is stored in the app's private container and only sent to this endpoint.")
            }

            Section {
                TextField("Model", text: $draft.model)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .accessibilityIdentifier("ai.model")
                Button {
                    loadModels()
                } label: {
                    HStack {
                        Text("Load available models")
                        Spacer()
                        if loadingModels {
                            ProgressView().controlSize(.small)
                        }
                    }
                }
                .disabled(loadingModels)
                if !models.isEmpty {
                    Menu {
                        ForEach(models, id: \.self) { model in
                            Button(model) { draft.model = model }
                        }
                    } label: {
                        Label("Choose from \(models.count) models", systemImage: "list.bullet")
                    }
                }
                TextField("Max tokens", value: $draft.maxTokens, format: .number)
                    .keyboardType(.numberPad)
            } header: {
                Text("Model")
            } footer: {
                Text("Leave max tokens empty to use the provider default.")
            }

            Section {
                Picker("Mode", selection: $draft.mode) {
                    Text("Agent").tag("agent")
                    Text("Ask").tag("ask")
                }
                .pickerStyle(.segmented)
            } header: {
                Text("Behaviour")
            } footer: {
                Text("Agent reads the terminal and can run commands after approval. Ask answers without tools and offers to switch when a task needs one.")
            }

            Section("System prompt") {
                TextEditor(text: optional(\.system))
                    .frame(minHeight: 110)
                    .font(.callout)
            }
        }
        .navigationTitle(draft.displayName)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Done") {
                    commit()
                    dismiss()
                }
                .disabled(draft.model.trimmingCharacters(in: .whitespaces).isEmpty)
                .accessibilityIdentifier("ai.done")
            }
        }
        .alert("Assistant", isPresented: errorBinding) {
            Button("OK") { localError = nil }
        } message: {
            Text(localError ?? "")
        }
    }

    private var errorBinding: Binding<Bool> {
        Binding(get: { localError != nil }, set: { if !$0 { localError = nil } })
    }

    private func optional(_ keyPath: WritableKeyPath<AIProvider, String?>) -> Binding<String> {
        Binding(
            get: { draft[keyPath: keyPath] ?? "" },
            set: { draft[keyPath: keyPath] = $0.isEmpty ? nil : $0 }
        )
    }

    /// Writes the draft into the shared list. A provider the user just
    /// configured also becomes the active one.
    private func commit() {
        var edited = draft
        let isNew = edited.id.isEmpty || edited.id == "new"
        if isNew {
            edited.id = "p" + UUID().uuidString
                .replacingOccurrences(of: "-", with: "")
                .lowercased()
        }
        if let index = providers.firstIndex(where: { $0.id == draft.id }) {
            providers[index] = edited
        } else {
            providers.append(edited)
        }
        if isNew || currentId == nil || currentId?.isEmpty == true {
            currentId = edited.id
        }
    }

    private func loadModels() {
        loadingModels = true
        bridge.loadAIModels(draft) { result in
            loadingModels = false
            switch result {
            case .success(let list):
                models = list
                if list.isEmpty {
                    localError = AppLanguage.localized("The provider returned no models.")
                }
            case .failure(let error):
                localError = error.localizedDescription
            }
        }
    }
}
