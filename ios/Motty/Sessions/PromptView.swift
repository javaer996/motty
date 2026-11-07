import SwiftUI

/// The card that answers a connection waiting for the user: a host key, a
/// password, a key passphrase, or keyboard-interactive questions.
struct PromptView: View {
    let prompt: CoreEvent
    @EnvironmentObject private var bridge: MottyBridge
    @State private var value = ""
    @State private var answers: [String] = []
    @State private var saveKey = true
    @State private var saveSecret = false

    private var sessionID: UInt32 { prompt.sessionID ?? 0 }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            content
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 18))
        .shadow(color: .black.opacity(0.35), radius: 18, y: 8)
        .onAppear {
            let count = prompt.questions?.count ?? 0
            if answers.count < count {
                answers = Array(repeating: "", count: count)
            }
        }
    }

    @ViewBuilder
    private var content: some View {
        switch prompt.kind {
        case "hostkey":
            hostKeyContent
        case "passphrase", "password":
            secretContent
        default:
            Text("Waiting…").foregroundStyle(.secondary)
        }
    }

    @ViewBuilder
    private var hostKeyContent: some View {
        if let key = prompt.hostKey {
            Text(LocalizedStringKey(key.changed ? "Host key changed" : "Unknown host key"))
                .font(.headline)
                .foregroundStyle(key.changed ? .red : .primary)
            Text(key.address)
                .font(.subheadline.monospaced())
            Text("\(key.type)  \(key.fingerprint)")
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
            if let known = key.known, !known.isEmpty {
                Text("Previously trusted: \(known.joined(separator: ", "))")
                    .font(.caption2.monospaced())
                    .foregroundStyle(.secondary)
            }
            if !key.changed {
                Toggle("Remember this host", isOn: $saveKey)
                    .font(.subheadline)
            }
            HStack {
                Button("Reject", role: .destructive) {
                    bridge.answer(.init(id: sessionID, kind: "hostkey", reject: true))
                    bridge.dismissPrompt(prompt)
                }
                Spacer()
                Button("Trust") {
                    bridge.answer(.init(id: sessionID, kind: "hostkey", save: saveKey))
                    bridge.dismissPrompt(prompt)
                }
                .buttonStyle(.borderedProminent)
            }
        }
    }

    @ViewBuilder
    private var secretContent: some View {
        Text(LocalizedStringKey(prompt.kind == "passphrase" ? "Private key passphrase" : "Password"))
            .font(.headline)
        if let path = prompt.identityPath, !path.isEmpty {
            Text(path)
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
        if let questions = prompt.questions, !questions.isEmpty {
            ForEach(Array(questions.enumerated()), id: \.offset) { index, question in
                field(for: question, index: index)
            }
        } else {
            SecureField("Password", text: $value)
                .textFieldStyle(.roundedBorder)
                .textContentType(.password)
        }
        if let attempt = prompt.attempt, attempt > 1 {
            Text("Attempt \(attempt): the previous answer was refused")
                .font(.caption)
                .foregroundStyle(.red)
        }
        if canSaveSecret {
            Toggle("Save this password for the host", isOn: $saveSecret)
                .font(.subheadline)
        }
        HStack {
            Spacer()
            Button("Submit") { submitSecret() }
                .buttonStyle(.borderedProminent)
        }
    }

    @ViewBuilder
    private func field(for question: Question, index: Int) -> some View {
        if question.secret {
            SecureField(question.text.isEmpty ? "Password" : question.text, text: binding(index))
                .textFieldStyle(.roundedBorder)
                .textContentType(.password)
        } else {
            TextField(question.text.isEmpty ? "Answer" : question.text, text: binding(index))
                .textFieldStyle(.roundedBorder)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
        }
    }

    private func binding(_ index: Int) -> Binding<String> {
        Binding(
            get: { answers.indices.contains(index) ? answers[index] : "" },
            set: { newValue in
                while answers.count <= index {
                    answers.append("")
                }
                answers[index] = newValue
            }
        )
    }

    /// Only a plain password question can be kept for the host. A one-time
    /// code, or a server asking several questions, is not one.
    private var canSaveSecret: Bool {
        guard prompt.kind == "password", prompt.attempt ?? 1 <= 1 else { return false }
        if let questions = prompt.questions, !questions.isEmpty {
            guard questions.count == 1, questions[0].secret else { return false }
            let text = questions[0].text.lowercased()
            return text.isEmpty || text.contains("password") || text.contains("密码") || text.contains("密碼")
        }
        return true
    }

    private func submitSecret() {
        if let questions = prompt.questions, !questions.isEmpty {
            bridge.answer(.init(id: sessionID, kind: "questions", answers: answers))
        } else if prompt.kind == "passphrase" {
            bridge.answer(.init(id: sessionID, kind: "passphrase", value: value))
        } else {
            bridge.answer(.init(id: sessionID, kind: "password", value: value, save: saveSecret))
        }
        bridge.dismissPrompt(prompt)
    }
}
