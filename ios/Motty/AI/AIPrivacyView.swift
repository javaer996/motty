import SwiftUI

/// What the assistant hides before a message leaves the device, the words the
/// user listed by hand, a dry run, and the last few exchanges with the exact
/// replacements made.
struct AIPrivacyView: View {
    /// When the screen was opened from a session, the preview also uses that
    /// host's name and address as known values.
    var sessionID: UInt32? = nil

    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss

    @State private var settings = AIPrivacySettings()
    @State private var customStrings: [String] = []
    @State private var newWord = ""
    @State private var draft = ""
    @State private var preview: AIPrivacyPreviewResult?
    @State private var entries: [AIPrivacyAuditEntry] = []
    @State private var loaded = false
    @State private var previewing = false

    var body: some View {
        Form {
            Section {
                Toggle("Protect messages", isOn: $settings.enabled)
                    .accessibilityIdentifier("ai.privacy.enabled")
            } footer: {
                Text("Messages are replaced with reversible tokens before they leave the device. The assistant's reply is restored here.")
            }

            if settings.enabled {
                detectedSection
                if settings.custom {
                    customSection
                }
                previewSection
                logSection
            }
        }
        .navigationTitle("Privacy protection")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Done") { dismiss() }
                    .accessibilityIdentifier("ai.privacy.done")
            }
        }
        .task { reload() }
        .onChange(of: settings) { _, value in
            guard loaded else { return }
            bridge.saveAIPrivacy(value, customStrings: customStrings)
        }
    }

    // MARK: Detected values

    private var detectedSection: some View {
        Section {
            Toggle("Saved secrets and the current host", isOn: $settings.knownValues)
            Toggle("Passwords, keys and certificates", isOn: $settings.secrets)
            Toggle("IP addresses", isOn: $settings.ips)
            Toggle("Accounts and home folders", isOn: $settings.accounts)
            Toggle("Host names", isOn: $settings.hosts)
            Toggle("E-mail addresses", isOn: $settings.emails)
            Toggle("Custom words", isOn: $settings.custom)
        } header: {
            Text("Detected automatically")
        } footer: {
            Text("Hiding host names can make answers less useful.")
        }
    }

    // MARK: Custom words

    private var customSection: some View {
        Section {
            ForEach(customStrings, id: \.self) { word in
                Text(verbatim: word)
                    .font(.callout.monospaced())
            }
            .onDelete { offsets in
                customStrings.remove(atOffsets: offsets)
                save()
            }

            HStack {
                TextField("Word or path to hide", text: $newWord)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .onSubmit(addWord)
                    .accessibilityIdentifier("ai.privacy.word")
                Button("Add") { addWord() }
                    .disabled(newWord.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    .accessibilityIdentifier("ai.privacy.add")
            }

            if customStrings.isEmpty {
                Text("No custom words yet.")
                    .foregroundStyle(.secondary)
            }
        } header: {
            Text("Custom words")
        } footer: {
            Text("These words are stored with your saved secrets and sync with them.")
        }
    }

    // MARK: Preview

    private var previewSection: some View {
        Section {
            TextEditor(text: $draft)
                .frame(minHeight: 90)
                .font(.callout.monospaced())
                .accessibilityIdentifier("ai.privacy.draft")

            Button {
                runPreview()
            } label: {
                HStack {
                    Text("See what the assistant receives")
                    Spacer()
                    if previewing {
                        ProgressView().controlSize(.small)
                    }
                }
            }
            .disabled(previewing || draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            .accessibilityIdentifier("ai.privacy.preview")

            if let preview {
                VStack(alignment: .leading, spacing: 6) {
                    Text("The assistant will receive")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .accessibilityIdentifier("ai.privacy.preview.result")
                    Text(verbatim: preview.text)
                        .font(.caption.monospaced())
                        .textSelection(.enabled)
                }
                if let replacements = preview.replacements, !replacements.isEmpty {
                    ForEach(replacements) { replacement in
                        replacementRow(replacement, inbound: false)
                    }
                } else {
                    Text("Nothing was replaced.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
        } header: {
            Text("Preview")
        } footer: {
            Text("Send a draft to see what the assistant would receive.")
        }
    }

    // MARK: Log

    private var logSection: some View {
        Section {
            if entries.isEmpty {
                Text("No exchanges recorded yet.")
                    .foregroundStyle(.secondary)
            } else {
                ForEach(entries) { entry in
                    NavigationLink {
                        AIPrivacyEntryView(entry: entry)
                    } label: {
                        logRow(entry)
                    }
                }
                Button("Clear log", role: .destructive) {
                    bridge.clearAIPrivacyLog { entries = [] }
                }
            }
        } header: {
            Text("Recent exchanges")
        } footer: {
            Text("Only the newest 10 exchanges are shown. Passwords and keys stay masked here too.")
        }
    }

    private func logRow(_ entry: AIPrivacyAuditEntry) -> some View {
        HStack(spacing: 10) {
            Image(systemName: entry.direction == "in" ? "arrow.down.left.circle.fill" : "arrow.up.right.circle.fill")
                .foregroundStyle(entry.direction == "in" ? Color.green : Color.accentColor)
            VStack(alignment: .leading, spacing: 2) {
                Text(entry.direction == "in" ? "Received" : "Sent")
                Text(Date(timeIntervalSince1970: Double(entry.time) / 1000), format: .dateTime.hour().minute().second())
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            Text(String(format: AppLanguage.localized("%ld replaced"), entry.count ?? 0))
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    private func replacementRow(_ replacement: AIPrivacyReplacement, inbound: Bool) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(categoryTitle(replacement.category))
                .font(.caption2)
                .foregroundStyle(.secondary)
                .accessibilityIdentifier("ai.privacy.preview.replacement")
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(verbatim: inbound ? replacement.token : replacement.value)
                    .font(.caption.monospaced())
                    .textSelection(.enabled)
                Image(systemName: "arrow.right")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Text(verbatim: inbound ? replacement.value : replacement.token)
                    .font(.caption.monospaced())
                    .textSelection(.enabled)
            }
        }
    }

    private func categoryTitle(_ category: String) -> String {
        switch category {
        case "IP": return AppLanguage.localized("IP")
        case "USER": return AppLanguage.localized("Account")
        case "HOST": return AppLanguage.localized("Host")
        case "SECRET": return AppLanguage.localized("Password or token")
        case "KEY": return AppLanguage.localized("Key")
        case "CERT": return AppLanguage.localized("Certificate")
        case "EMAIL": return AppLanguage.localized("E-mail")
        case "CUSTOM": return AppLanguage.localized("Custom")
        default: return category
        }
    }

    // MARK: Actions

    private func reload() {
        bridge.refreshAIPrivacy { info in
            settings = info.settings
            customStrings = info.customStrings ?? []
            loaded = true
        }
        loadLog()
    }

    private func loadLog() {
        bridge.loadAIPrivacyLog(limit: 10) { result in
            if case .success(let list) = result {
                entries = list
            }
        }
    }

    private func save() {
        bridge.saveAIPrivacy(settings, customStrings: customStrings)
    }

    private func addWord() {
        let word = newWord.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !word.isEmpty, !customStrings.contains(word) else { return }
        customStrings.append(word)
        newWord = ""
        save()
    }

    private func runPreview() {
        previewing = true
        preview = nil
        bridge.previewAIPrivacy(sessionID: sessionID, text: draft) { result in
            previewing = false
            if case .success(let value) = result {
                preview = value
            }
        }
    }
}

/// One exchange in full: the tokenized message plus every value that was
/// replaced, in the direction it travelled.
private struct AIPrivacyEntryView: View {
    let entry: AIPrivacyAuditEntry

    var body: some View {
        List {
            if let preview = entry.preview, !preview.isEmpty {
                Section {
                    Text(verbatim: preview)
                        .font(.caption.monospaced())
                        .textSelection(.enabled)
                } header: {
                    Text("Message preview")
                }
            }
            Section {
                if let replacements = entry.replacements, !replacements.isEmpty {
                    ForEach(replacements) { replacement in
                        VStack(alignment: .leading, spacing: 3) {
                            Text(categoryTitle(replacement.category))
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                            HStack(alignment: .firstTextBaseline, spacing: 6) {
                                Text(verbatim: entry.direction == "in" ? replacement.token : replacement.value)
                                    .font(.caption.monospaced())
                                    .textSelection(.enabled)
                                Image(systemName: "arrow.right")
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                                Text(verbatim: entry.direction == "in" ? replacement.value : replacement.token)
                                    .font(.caption.monospaced())
                                    .textSelection(.enabled)
                            }
                        }
                    }
                } else {
                    Text("Nothing was replaced.")
                        .foregroundStyle(.secondary)
                }
            } header: {
                Text("Replaced values")
            }
        }
        .navigationTitle(entry.direction == "in" ? "Received" : "Sent")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func categoryTitle(_ category: String) -> String {
        switch category {
        case "IP": return AppLanguage.localized("IP")
        case "USER": return AppLanguage.localized("Account")
        case "HOST": return AppLanguage.localized("Host")
        case "SECRET": return AppLanguage.localized("Password or token")
        case "KEY": return AppLanguage.localized("Key")
        case "CERT": return AppLanguage.localized("Certificate")
        case "EMAIL": return AppLanguage.localized("E-mail")
        case "CUSTOM": return AppLanguage.localized("Custom")
        default: return category
        }
    }
}
