import SwiftUI

/// Provider, model and behaviour of the in-app assistant.
struct AISettingsView: View {
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss
    @State private var settings = AISettings()
    @State private var models: [String] = []
    @State private var loadingModels = false
    @State private var saving = false
    @State private var loaded = false
    @State private var localError: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Picker("Protocol", selection: $settings.provider) {
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
                    TextField("Model", text: $settings.model)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
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
                                Button(model) { settings.model = model }
                            }
                        } label: {
                            Label("Choose from \(models.count) models", systemImage: "list.bullet")
                        }
                    }
                    TextField("Max tokens", value: $settings.maxTokens, format: .number)
                        .keyboardType(.numberPad)
                } header: {
                    Text("Model")
                } footer: {
                    Text("Leave max tokens empty to use the provider default.")
                }

                Section {
                    Picker("Mode", selection: $settings.mode) {
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
                        .disabled(saving || settings.model.trimmingCharacters(in: .whitespaces).isEmpty)
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

    private var errorBinding: Binding<Bool> {
        Binding(get: { localError != nil }, set: { if !$0 { localError = nil } })
    }

    private func optional(_ keyPath: WritableKeyPath<AISettings, String?>) -> Binding<String> {
        Binding(
            get: { settings[keyPath: keyPath] ?? "" },
            set: { settings[keyPath: keyPath] = $0.isEmpty ? nil : $0 }
        )
    }

    private func loadModels() {
        loadingModels = true
        bridge.loadAIModels(settings) { result in
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
