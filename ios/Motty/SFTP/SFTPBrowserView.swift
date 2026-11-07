import SwiftUI
import UniformTypeIdentifiers

/// A browser for the remote files of one SSH session. Downloads land in the
/// app's `downloads` directory first; sharing out to Files is explicit.
struct SFTPBrowserView: View {
    let sessionID: UInt32

    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss

    @State private var path = "."
    @State private var entries: [RemoteEntry] = []
    @State private var loading = false
    @State private var errorText: String?
    @State private var showHidden = false
    @State private var sortByName = true
    @State private var showingTransfers = false
    @State private var uploading = false
    @State private var creatingFolder = false
    @State private var newFolderName = ""
    @State private var renaming: RemoteEntry?
    @State private var renameText = ""
    @State private var deleting: RemoteEntry?

    private var visibleEntries: [RemoteEntry] {
        var list = entries.filter { $0.name != "." && $0.name != ".." }
        if !showHidden {
            list = list.filter { !$0.name.hasPrefix(".") }
        }
        list.sort { left, right in
            if left.isDir != right.isDir { return left.isDir }
            if sortByName {
                return left.name.localizedCaseInsensitiveCompare(right.name) == .orderedAscending
            }
            return (left.modified ?? .distantPast) > (right.modified ?? .distantPast)
        }
        return list
    }

    private var displayPath: String {
        path == "." ? "Home" : path
    }

    var body: some View {
        NavigationStack {
            List {
                if path != "." && path != "/" {
                    Button {
                        goUp()
                    } label: {
                        Label("Parent folder", systemImage: "arrow.up.left")
                            .font(.callout)
                    }
                }
                ForEach(visibleEntries) { entry in
                    entryRow(entry)
                }
                if visibleEntries.isEmpty && !loading {
                    ContentUnavailableView {
                        Label("Empty folder", systemImage: "folder")
                    } description: {
                        Text(displayPath)
                    }
                }
            }
            .listStyle(.plain)
            .overlay {
                if loading {
                    ProgressView()
                        .padding()
                        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 10))
                }
            }
            .navigationTitle(displayPath)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("Done") { dismiss() }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        Button {
                            newFolderName = ""
                            creatingFolder = true
                        } label: {
                            Label("New folder", systemImage: "folder.badge.plus")
                        }
                        Button {
                            uploading = true
                        } label: {
                            Label("Upload", systemImage: "arrow.up.doc")
                        }
                        Divider()
                        Toggle(isOn: $showHidden) {
                            Label("Show hidden files", systemImage: "eye")
                        }
                        Button {
                            sortByName.toggle()
                        } label: {
                            Label(
                                sortByName ? "Sort by date" : "Sort by name",
                                systemImage: "arrow.up.arrow.down"
                            )
                        }
                        Divider()
                        Button {
                            showingTransfers = true
                        } label: {
                            Label("Transfers", systemImage: "arrow.up.arrow.down.circle")
                        }
                        Button {
                            load()
                        } label: {
                            Label("Refresh", systemImage: "arrow.clockwise")
                        }
                    } label: {
                        Image(systemName: "ellipsis.circle")
                    }
                }
            }
            .refreshable { load() }
            .onAppear { load() }
            .fileImporter(
                isPresented: $uploading,
                allowedContentTypes: [.item],
                allowsMultipleSelection: false
            ) { handleUpload($0) }
            .alert("New folder", isPresented: $creatingFolder) {
                TextField("Folder name", text: $newFolderName)
                Button("Create") { createFolder() }
                Button("Cancel", role: .cancel) { newFolderName = "" }
            } message: {
                Text("Create inside \(displayPath)")
            }
            .alert("Rename", isPresented: renameBinding) {
                TextField("New name", text: $renameText)
                Button("Rename") { renameEntry() }
                Button("Cancel", role: .cancel) { renaming = nil }
            } message: {
                Text(renaming?.name ?? "")
            }
            .confirmationDialog(
                "Delete \(deleting?.name ?? "")?",
                isPresented: deleteBinding,
                titleVisibility: .visible
            ) {
                Button("Delete", role: .destructive) { deleteEntry() }
                Button("Cancel", role: .cancel) { deleting = nil }
            } message: {
                Text("Folders are deleted recursively.")
            }
            .alert("SFTP", isPresented: errorBinding) {
                Button("OK") { errorText = nil }
            } message: {
                Text(errorText ?? "")
            }
            .sheet(isPresented: $showingTransfers) {
                TransferCenterView(sessionID: sessionID)
                    .environmentObject(bridge)
            }
        }
    }

    private func entryRow(_ entry: RemoteEntry) -> some View {
        Button {
            open(entry)
        } label: {
            HStack(spacing: 12) {
                Image(systemName: iconName(entry))
                    .foregroundStyle(entry.isDir ? Color.accentColor : .secondary)
                    .frame(width: 22)
                VStack(alignment: .leading, spacing: 3) {
                    Text(entry.name)
                        .foregroundStyle(.primary)
                        .lineLimit(1)
                    Text(detailText(entry))
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Spacer()
                if entry.isDir {
                    Image(systemName: "chevron.right")
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                }
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
            Button(role: .destructive) {
                deleting = entry
            } label: {
                Label("Delete", systemImage: "trash")
            }
            Button {
                renameText = entry.name
                renaming = entry
            } label: {
                Label("Rename", systemImage: "pencil")
            }
            .tint(.blue)
        }
        .contextMenu {
            if entry.isDir {
                Button {
                    open(entry)
                } label: {
                    Label("Open", systemImage: "folder")
                }
            }
            Button {
                download(entry)
            } label: {
                Label("Download", systemImage: "arrow.down.circle")
            }
            Button {
                renameText = entry.name
                renaming = entry
            } label: {
                Label("Rename", systemImage: "pencil")
            }
            Button(role: .destructive) {
                deleting = entry
            } label: {
                Label("Delete", systemImage: "trash")
            }
        }
    }

    private func iconName(_ entry: RemoteEntry) -> String {
        if entry.isLink { return "link" }
        if entry.isDir { return "folder" }
        return "doc"
    }

    private func detailText(_ entry: RemoteEntry) -> String {
        var parts: [String] = []
        if entry.isLink {
            parts.append("→ \(entry.linkTarget ?? "?")")
            if entry.linkBroken == true {
                parts.append(AppLanguage.localized("broken"))
            }
        }
        if entry.isDir {
            parts.append(AppLanguage.localized("folder"))
        } else {
            parts.append(entry.sizeText)
        }
        parts.append(entry.permissions)
        if let owner = entry.owner, !owner.isEmpty { parts.append(owner) }
        return parts.joined(separator: "  ")
    }

    private func open(_ entry: RemoteEntry) {
        if entry.isDir {
            path = join(entry.name)
            load()
        } else {
            download(entry)
        }
    }

    private func goUp() {
        guard path != "." && path != "/" else { return }
        let absolute = path.hasPrefix("/")
        var parts = path.split(separator: "/").map(String.init)
        parts.removeLast()
        if parts.isEmpty {
            path = absolute ? "/" : "."
        } else {
            path = (absolute ? "/" : "") + parts.joined(separator: "/")
        }
        load()
    }

    private func join(_ name: String) -> String {
        if path == "." { return name }
        if path == "/" { return "/" + name }
        return path + "/" + name
    }

    private func load() {
        loading = true
        let requested = path
        bridge.listFiles(sessionID: sessionID, path: requested) { result in
            loading = false
            guard path == requested else { return }
            switch result {
            case .success(let listing):
                entries = listing.entries
            case .failure(let error):
                errorText = error.localizedDescription
            }
        }
    }

    private func download(_ entry: RemoteEntry) {
        guard let directory = bridge.dataDirectory?
            .appendingPathComponent("downloads", isDirectory: true) else {
            errorText = AppLanguage.localized("The app has no writable directory.")
            return
        }
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let destination = directory.appendingPathComponent(entry.name)
        bridge.downloadFile(
            sessionID: sessionID,
            remote: join(entry.name),
            local: destination,
            recursive: entry.isDir,
            policy: "resume"
        ) { result in
            switch result {
            case .success:
                showingTransfers = true
            case .failure(let error):
                errorText = error.localizedDescription
            }
        }
    }

    private func handleUpload(_ result: Result<[URL], Error>) {
        guard let source = try? result.get().first else {
            if case .failure(let error) = result { errorText = error.localizedDescription }
            return
        }
        guard let dataDirectory = bridge.dataDirectory else {
            errorText = AppLanguage.localized("The app has no writable directory.")
            return
        }
        let accessing = source.startAccessingSecurityScopedResource()
        defer { if accessing { source.stopAccessingSecurityScopedResource() } }
        let inbox = dataDirectory.appendingPathComponent("inbox", isDirectory: true)
        try? FileManager.default.createDirectory(at: inbox, withIntermediateDirectories: true)
        var staged = inbox.appendingPathComponent(source.lastPathComponent)
        if FileManager.default.fileExists(atPath: staged.path) {
            staged = inbox.appendingPathComponent(
                UUID().uuidString + "-" + source.lastPathComponent
            )
        }
        do {
            try FileManager.default.copyItem(at: source, to: staged)
        } catch {
            errorText = error.localizedDescription
            return
        }
        bridge.uploadFile(
            sessionID: sessionID,
            local: staged,
            remote: join(source.lastPathComponent),
            recursive: source.hasDirectoryPath,
            policy: "resume"
        ) { result in
            switch result {
            case .success:
                showingTransfers = true
                load()
            case .failure(let error):
                errorText = error.localizedDescription
            }
        }
    }

    private func createFolder() {
        let name = newFolderName.trimmingCharacters(in: .whitespacesAndNewlines)
        newFolderName = ""
        guard validName(name) else {
            errorText = AppLanguage.localized("Enter a name without slashes.")
            return
        }
        bridge.createDirectory(sessionID: sessionID, path: join(name)) { error in
            if let error { errorText = error.localizedDescription } else { load() }
        }
    }

    private func renameEntry() {
        guard let entry = renaming else { return }
        let name = renameText.trimmingCharacters(in: .whitespacesAndNewlines)
        renaming = nil
        renameText = ""
        guard validName(name), name != entry.name else {
            errorText = AppLanguage.localized("Enter a different name without slashes.")
            return
        }
        bridge.renameFile(sessionID: sessionID, oldPath: join(entry.name), newPath: join(name)) { error in
            if let error { errorText = error.localizedDescription } else { load() }
        }
    }

    private func deleteEntry() {
        guard let entry = deleting else { return }
        deleting = nil
        bridge.removeFile(
            sessionID: sessionID,
            path: join(entry.name),
            directory: entry.isDir
        ) { error in
            if let error { errorText = error.localizedDescription } else { load() }
        }
    }

    private func validName(_ name: String) -> Bool {
        !name.isEmpty && !name.contains("/") && name != "." && name != ".."
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { errorText != nil },
            set: { if !$0 { errorText = nil } }
        )
    }

    private var renameBinding: Binding<Bool> {
        Binding(
            get: { renaming != nil },
            set: { if !$0 { renaming = nil } }
        )
    }

    private var deleteBinding: Binding<Bool> {
        Binding(
            get: { deleting != nil },
            set: { if !$0 { deleting = nil } }
        )
    }
}

/// Live transfer list for one session. Polls while visible; the Go worker owns
/// the transfers themselves, so closing this sheet never cancels anything.
struct TransferCenterView: View {
    let sessionID: UInt32

    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss
    @State private var transfers: [FileTransfer] = []

    private let timer = Timer.publish(every: 0.5, on: .main, in: .common).autoconnect()

    var body: some View {
        NavigationStack {
            List {
                if transfers.isEmpty {
                    ContentUnavailableView {
                        Label("No transfers", systemImage: "arrow.up.arrow.down")
                    } description: {
                        Text("Uploads and downloads appear here.")
                    }
                }
                ForEach(transfers) { transfer in
                    transferRow(transfer)
                }
            }
            .navigationTitle("Transfers")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                Button("Done") { dismiss() }
            }
            .onAppear { refresh() }
            .onReceive(timer) { _ in refresh() }
        }
    }

    private func transferRow(_ transfer: FileTransfer) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                Image(systemName: transfer.isDownload ? "arrow.down.circle" : "arrow.up.circle")
                    .foregroundStyle(.secondary)
                Text(transfer.displayName)
                    .font(.callout)
                    .lineLimit(1)
                Spacer()
                TransferActions(transfer: transfer, sessionID: sessionID)
            }
            if transfer.isActive || transfer.state == "completed" {
                ProgressView(value: transfer.progress)
                    .tint(transfer.state == "failed" ? .red : .accentColor)
            }
            HStack {
                Text(statusText(transfer))
                    .font(.caption)
                    .foregroundStyle(transfer.state == "failed" ? .red : .secondary)
                Spacer()
                if !transfer.speedText.isEmpty {
                    Text(transfer.speedText)
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                }
            }
            if let error = transfer.error, !error.isEmpty {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(.red)
                    .lineLimit(2)
            }
        }
        .padding(.vertical, 2)
    }

    private func statusText(_ transfer: FileTransfer) -> LocalizedStringKey {
        switch transfer.state {
        case "queued": return "Queued"
        case "running":
            if transfer.bytesTotal > 0 {
                let done = ByteCountFormatter.string(fromByteCount: transfer.bytesDone, countStyle: .file)
                let total = ByteCountFormatter.string(fromByteCount: transfer.bytesTotal, countStyle: .file)
                return "\(done) of \(total)"
            }
            return "Running"
        case "paused": return "Paused"
        case "completed": return "Completed"
        case "failed": return "Failed"
        case "canceled": return "Canceled"
        default: return LocalizedStringKey(transfer.state)
        }
    }

    private func refresh() {
        bridge.fileTransfers(sessionID: sessionID) { result in
            if case .success(let list) = result {
                transfers = list.reversed()
            }
        }
    }
}

private struct TransferActions: View {
    let transfer: FileTransfer
    let sessionID: UInt32
    @EnvironmentObject private var bridge: MottyBridge

    var body: some View {
        Menu {
            switch transfer.state {
            case "running":
                Button {
                    bridge.controlTransfer("files.pause", sessionID: sessionID, id: transfer.id)
                } label: {
                    Label("Pause", systemImage: "pause.circle")
                }
                Button(role: .destructive) {
                    bridge.controlTransfer("files.cancel", sessionID: sessionID, id: transfer.id)
                } label: {
                    Label("Cancel", systemImage: "xmark.circle")
                }
            case "paused":
                Button {
                    bridge.controlTransfer("files.resume", sessionID: sessionID, id: transfer.id)
                } label: {
                    Label("Resume", systemImage: "play.circle")
                }
                Button(role: .destructive) {
                    bridge.controlTransfer("files.cancel", sessionID: sessionID, id: transfer.id)
                } label: {
                    Label("Cancel", systemImage: "xmark.circle")
                }
            case "failed", "canceled":
                Button {
                    bridge.controlTransfer(
                        "files.retry",
                        sessionID: sessionID,
                        id: transfer.id,
                        policy: "resume"
                    )
                } label: {
                    Label("Retry", systemImage: "arrow.clockwise")
                }
                Button(role: .destructive) {
                    bridge.controlTransfer("files.forget", sessionID: sessionID, id: transfer.id)
                } label: {
                    Label("Remove", systemImage: "trash")
                }
            default:
                if let url = transfer.localURL {
                    ShareLink(item: url) {
                        Label("Share", systemImage: "square.and.arrow.up")
                    }
                }
                Button(role: .destructive) {
                    bridge.controlTransfer("files.forget", sessionID: sessionID, id: transfer.id)
                } label: {
                    Label("Remove", systemImage: "trash")
                }
            }
        } label: {
            Image(systemName: "ellipsis")
                .font(.body)
                .foregroundStyle(.secondary)
                .frame(width: 30, height: 30)
                .contentShape(Rectangle())
        }
    }
}
