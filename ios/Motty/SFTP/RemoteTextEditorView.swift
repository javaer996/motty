import SwiftUI
import UIKit

/// A small UTF-8 editor for a remote file. The core refuses binary and
/// oversized files, and saving is revision-checked so a concurrent edit on the
/// server is never overwritten silently.
struct RemoteTextEditorView: View {
    let sessionID: UInt32
    let path: String
    let name: String
    var onSaved: () -> Void = {}
    var onDownload: () -> Void = {}

    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss

    @State private var text = ""
    @State private var original = ""
    @State private var revision = ""
    @State private var bom = false

    @State private var loading = true
    @State private var saving = false
    @State private var loadFailure: LoadFailure?
    @State private var saveError: String?
    @State private var saveConflict = false
    @State private var confirmingReload = false
    @State private var confirmingDiscard = false
    @State private var confirmingDownload = false
    @State private var savedNotice = false

    private var dirty: Bool {
        !loading && loadFailure == nil && text != original
    }

    private var canSave: Bool {
        dirty && !saving && loadFailure == nil
    }

    var body: some View {
        NavigationStack {
            Group {
                if loading {
                    ProgressView("Loading…")
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if let failure = loadFailure {
                    failureView(failure)
                } else {
                    editor
                }
            }
            .navigationTitle(name)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("Done") { done() }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Save") { save() }
                        .disabled(!canSave)
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        Button {
                            requestReload()
                        } label: {
                            Label("Reload", systemImage: "arrow.clockwise")
                        }
                        .disabled(loading || saving)
                        Button {
                            requestDownload()
                        } label: {
                            Label("Download", systemImage: "arrow.down.circle")
                        }
                    } label: {
                        Image(systemName: "ellipsis.circle")
                    }
                }
            }
            .interactiveDismissDisabled(dirty || saving)
            .task { load() }
            .alert("Save failed", isPresented: saveErrorBinding) {
                if saveConflict {
                    Button("Reload") {
                        saveError = nil
                        saveConflict = false
                        load()
                    }
                }
                Button("OK", role: .cancel) {
                    saveError = nil
                    saveConflict = false
                }
            } message: {
                Text(saveError ?? "")
            }
            .confirmationDialog(
                "Reload from server?",
                isPresented: $confirmingReload,
                titleVisibility: .visible
            ) {
                Button("Reload", role: .destructive) { load() }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("This discards your unsaved changes.")
            }
            .confirmationDialog(
                "Discard changes?",
                isPresented: $confirmingDiscard,
                titleVisibility: .visible
            ) {
                Button("Discard", role: .destructive) { dismiss() }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("Your changes have not been saved.")
            }
            .confirmationDialog(
                "Download and discard changes?",
                isPresented: $confirmingDownload,
                titleVisibility: .visible
            ) {
                Button("Download", role: .destructive) { onDownload() }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("Your changes have not been saved.")
            }
        }
    }

    private var editor: some View {
        VStack(spacing: 0) {
            CodeTextEditor(text: $text)
            Divider()
            HStack(spacing: 8) {
                Text("UTF-8")
                    .font(.caption2.monospaced())
                    .foregroundStyle(.secondary)
                if bom {
                    Text("BOM")
                        .font(.caption2.monospaced())
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if saving {
                    ProgressView()
                        .controlSize(.small)
                    Text("Saving…")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                } else if dirty {
                    Text("Unsaved changes")
                        .font(.caption)
                        .foregroundStyle(.orange)
                } else if savedNotice {
                    Text("Saved")
                        .font(.caption)
                        .foregroundStyle(.green)
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 7)
            .background(.bar)
        }
    }

    @ViewBuilder
    private func failureView(_ failure: LoadFailure) -> some View {
        ContentUnavailableView {
            Label(failure.title, systemImage: failure.icon)
        } description: {
            Text(failure.message)
        } actions: {
            Button("Retry") { load() }
            Button("Download") { onDownload() }
                .buttonStyle(.borderedProminent)
        }
    }

    private func load() {
        guard !saving else { return }
        loading = true
        loadFailure = nil
        savedNotice = false
        bridge.openRemoteText(sessionID: sessionID, path: path) { result in
            loading = false
            switch result {
            case .success(let document):
                text = document.text
                original = document.text
                revision = document.revision
                bom = document.bom
            case .failure(let error):
                loadFailure = LoadFailure(error: error)
            }
        }
    }

    private func save() {
        guard canSave else { return }
        saving = true
        savedNotice = false
        let value = text
        bridge.saveRemoteText(
            sessionID: sessionID,
            path: path,
            text: value,
            revision: revision,
            bom: bom
        ) { result in
            saving = false
            switch result {
            case .success(let document):
                text = document.text
                original = document.text
                revision = document.revision
                bom = document.bom
                savedNotice = true
                onSaved()
            case .failure(let error):
                let code = (error as? RPCError)?.code
                saveConflict = code == "conflict"
                switch code {
                case "conflict":
                    saveError = AppLanguage.localized(
                        "The file changed on the server. Reload it before saving again."
                    )
                case "unsupported":
                    saveError = AppLanguage.localized(
                        "This server does not support editing files here."
                    )
                case "too_large":
                    saveError = AppLanguage.localized("Files larger than 8 MiB cannot be edited.")
                case "not_text":
                    saveError = AppLanguage.localized("This file is not UTF-8 text.")
                default:
                    saveError = error.localizedDescription
                }
            }
        }
    }

    private func requestReload() {
        if dirty {
            confirmingReload = true
        } else {
            load()
        }
    }

    private func requestDownload() {
        if dirty {
            confirmingDownload = true
        } else {
            onDownload()
        }
    }

    private func done() {
        if dirty {
            confirmingDiscard = true
        } else {
            dismiss()
        }
    }

    private var saveErrorBinding: Binding<Bool> {
        Binding(
            get: { saveError != nil },
            set: { if !$0 { saveError = nil } }
        )
    }
}

private struct LoadFailure {
    enum Kind {
        case notText
        case tooLarge
        case unsupported
        case other
    }

    let kind: Kind
    let detail: String

    init(error: Error) {
        if let rpc = error as? RPCError {
            switch rpc.code {
            case "not_text":
                kind = .notText
            case "too_large":
                kind = .tooLarge
            case "unsupported":
                kind = .unsupported
            default:
                kind = .other
            }
            detail = rpc.message
        } else {
            kind = .other
            detail = error.localizedDescription
        }
    }

    var title: LocalizedStringKey {
        switch kind {
        case .notText, .tooLarge, .unsupported:
            return "Cannot edit this file"
        case .other:
            return "Could not load the file"
        }
    }

    var message: LocalizedStringKey {
        switch kind {
        case .notText:
            return "This file is not UTF-8 text."
        case .tooLarge:
            return "Files larger than 8 MiB cannot be edited."
        case .unsupported:
            return "This server does not support editing files here."
        case .other:
            return LocalizedStringKey(detail)
        }
    }

    var icon: String {
        switch kind {
        case .notText, .tooLarge:
            return "doc.text"
        case .unsupported:
            return "pencil.slash"
        case .other:
            return "exclamationmark.triangle"
        }
    }
}

/// UITextView configured for source files: no smart quotes, dashes or
/// autocorrection, all of which would corrupt configuration syntax.
private struct CodeTextEditor: UIViewRepresentable {
    @Binding var text: String

    func makeCoordinator() -> Coordinator {
        Coordinator(text: $text)
    }

    func makeUIView(context: Context) -> UITextView {
        let view = UITextView()
        view.delegate = context.coordinator
        view.font = UIFont.monospacedSystemFont(ofSize: 14, weight: .regular)
        view.textColor = .label
        view.backgroundColor = .systemBackground
        view.textContainerInset = UIEdgeInsets(top: 12, left: 12, bottom: 12, right: 12)
        view.alwaysBounceVertical = true
        view.keyboardDismissMode = .interactive
        view.autocorrectionType = .no
        view.autocapitalizationType = .none
        view.smartQuotesType = .no
        view.smartDashesType = .no
        view.smartInsertDeleteType = .no
        view.spellCheckingType = .no
        view.dataDetectorTypes = []
        view.text = text
        view.accessibilityLabel = AppLanguage.localized("File editor")
        return view
    }

    func updateUIView(_ view: UITextView, context: Context) {
        if view.text != text {
            view.text = text
        }
    }

    final class Coordinator: NSObject, UITextViewDelegate {
        private let text: Binding<String>

        init(text: Binding<String>) {
            self.text = text
        }

        func textViewDidChange(_ textView: UITextView) {
            text.wrappedValue = textView.text
        }
    }
}
