import SwiftUI

/// Command completion for the line currently being edited. The core reads the
/// line from the terminal screen and merges the host's history with the
/// built-in syntax catalog; picking a row types its suffix into the shell.
struct CompletionView: View {
    let sessionID: UInt32

    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss

    @State private var line = ""
    @State private var typedLine = ""
    @State private var candidates: [CompletionCandidate] = []
    @State private var suppressed = false
    @State private var loading = true
    @State private var errorText: String?

    var body: some View {
        NavigationStack {
            List {
                Section {
                    TextField("Command line", text: $typedLine)
                        .font(.body.monospaced())
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .onSubmit { complete() }
                    HStack {
                        Text(line.isEmpty
                             ? LocalizedStringKey("Nothing is being edited.")
                             : LocalizedStringKey("Typing after: \(line)"))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        Spacer()
                        Button("Complete") { complete() }
                            .buttonStyle(.borderless)
                    }
                } header: {
                    Text("Line")
                } footer: {
                    if suppressed {
                        Text("This line uses syntax the catalog does not understand, so only history is offered.")
                    }
                }
                Section("Suggestions") {
                    if loading {
                        ProgressView()
                    } else if candidates.isEmpty {
                        Text("No suggestions.")
                            .foregroundStyle(.secondary)
                    }
                    ForEach(candidates) { candidate in
                        Button {
                            apply(candidate)
                        } label: {
                            HStack(spacing: 10) {
                                Image(systemName: candidate.iconName)
                                    .foregroundStyle(.secondary)
                                    .frame(width: 20)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(candidate.text)
                                        .font(.body.monospaced())
                                        .foregroundStyle(.primary)
                                        .lineLimit(1)
                                    if let desc = candidate.desc, !desc.isEmpty {
                                        Text(desc)
                                            .font(.caption)
                                            .foregroundStyle(.secondary)
                                            .lineLimit(2)
                                    }
                                }
                                Spacer()
                                Text("+" + candidate.insert)
                                    .font(.caption.monospaced())
                                    .foregroundStyle(.tertiary)
                                    .lineLimit(1)
                            }
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
            .navigationTitle("Complete")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("Close") { dismiss() }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        complete(derive: true)
                    } label: {
                        Image(systemName: "arrow.clockwise")
                    }
                }
            }
            .onAppear { complete(derive: true) }
            .alert("Completion", isPresented: errorBinding) {
                Button("OK") { errorText = nil }
            } message: {
                Text(errorText ?? "")
            }
        }
    }

    private func complete(derive: Bool = false) {
        loading = true
        let provided = typedLine.isEmpty ? nil : typedLine
        bridge.complete(
            sessionID: sessionID,
            line: derive ? nil : provided,
            locale: AppLanguage.effectiveLocale.identifier
        ) { result in
            loading = false
            switch result {
            case .success(let completion):
                line = completion.line
                candidates = completion.candidates
                suppressed = completion.suppressed
            case .failure(let error):
                errorText = error.localizedDescription
            }
        }
    }

    private func apply(_ candidate: CompletionCandidate) {
        guard !candidate.insert.isEmpty else { return }
        bridge.input(sessionID: sessionID, data: Data(candidate.insert.utf8))
        dismiss()
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { errorText != nil },
            set: { if !$0 { errorText = nil } }
        )
    }
}
