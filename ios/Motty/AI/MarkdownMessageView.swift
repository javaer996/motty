import SwiftUI
import UIKit

/// Draws the assistant's Markdown. The parser is deliberately small: headings,
/// paragraphs, lists, quotes, fenced code, rules, pipe tables and the inline
/// syntax AttributedString already knows. Answers stream in, so every block
/// falls back to plain text rather than failing on half-written Markdown.
struct MarkdownMessageView: View {
    let text: String

    var body: some View {
        let blocks = MarkdownParser.parse(text)
        VStack(alignment: .leading, spacing: 10) {
            ForEach(blocks.indices, id: \.self) { index in
                blockView(blocks[index])
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .textSelection(.enabled)
    }

    @ViewBuilder
    private func blockView(_ block: MarkdownBlock) -> some View {
        switch block {
        case .paragraph(let text):
            Text(MarkdownInline.attributed(text))
                .font(.body)
                .lineSpacing(2)
        case .heading(let level, let text):
            Text(MarkdownInline.attributed(text))
                .font(headingFont(level))
                .padding(.top, level <= 2 ? 2 : 0)
        case .code(let language, let code):
            MarkdownCodeBlock(language: language, code: code)
        case .listItem(let indent, let marker, let text):
            HStack(alignment: .firstTextBaseline, spacing: 7) {
                Text(verbatim: marker)
                    .font(.body.monospacedDigit())
                    .foregroundStyle(.secondary)
                Text(MarkdownInline.attributed(text))
                    .font(.body)
                    .lineSpacing(2)
            }
            .padding(.leading, CGFloat(min(indent, 4)) * 14)
        case .quote(let text):
            Text(MarkdownInline.attributed(text))
                .font(.callout)
                .foregroundStyle(.secondary)
                .lineSpacing(2)
                .padding(.leading, 12)
                .overlay(alignment: .leading) {
                    RoundedRectangle(cornerRadius: 1.5)
                        .fill(Color.secondary.opacity(0.35))
                        .frame(width: 3)
                }
        case .rule:
            Divider()
        case .table(let header, let rows):
            MarkdownTable(header: header, rows: rows)
        }
    }

    private func headingFont(_ level: Int) -> Font {
        switch level {
        case 1: return .title2.bold()
        case 2: return .title3.bold()
        case 3: return .headline
        default: return .subheadline.weight(.semibold)
        }
    }
}

// MARK: - Blocks

private enum MarkdownBlock {
    case paragraph(String)
    case heading(Int, String)
    case code(String?, String)
    case listItem(Int, String, String)
    case quote(String)
    case rule
    case table([String], [[String]])
}

private enum MarkdownParser {
    static func parse(_ source: String) -> [MarkdownBlock] {
        let lines = source
            .replacingOccurrences(of: "\r\n", with: "\n")
            .replacingOccurrences(of: "\r", with: "\n")
            .components(separatedBy: "\n")
        var blocks: [MarkdownBlock] = []
        var index = 0
        while index < lines.count {
            let line = lines[index]
            let trimmed = line.trimmingCharacters(in: .whitespaces)

            if trimmed.isEmpty {
                index += 1
                continue
            }

            if let fence = fenceStart(trimmed) {
                var code: [String] = []
                index += 1
                while index < lines.count {
                    if isFenceEnd(lines[index], marker: fence.marker, count: fence.count) {
                        index += 1
                        break
                    }
                    code.append(lines[index])
                    index += 1
                }
                blocks.append(.code(fence.language, code.joined(separator: "\n")))
                continue
            }

            if let heading = heading(trimmed) {
                blocks.append(.heading(heading.level, heading.text))
                index += 1
                continue
            }

            if isRule(trimmed) {
                blocks.append(.rule)
                index += 1
                continue
            }

            if trimmed.hasPrefix(">") {
                var quoted: [String] = []
                while index < lines.count {
                    let candidate = lines[index].trimmingCharacters(in: .whitespaces)
                    guard candidate.hasPrefix(">") else { break }
                    var content = String(candidate.dropFirst())
                    if content.hasPrefix(" ") { content.removeFirst() }
                    quoted.append(content)
                    index += 1
                }
                blocks.append(.quote(quoted.joined(separator: "\n")))
                continue
            }

            if let item = listItem(line) {
                var itemText = item.text
                index += 1
                // Indented continuation lines belong to the same item.
                while index < lines.count {
                    let next = lines[index]
                    let nextTrimmed = next.trimmingCharacters(in: .whitespaces)
                    guard !nextTrimmed.isEmpty,
                          listItem(next) == nil,
                          !startsBlock(next, nextLine: index + 1 < lines.count ? lines[index + 1] : nil),
                          next.hasPrefix("  ") || next.hasPrefix("\t") else { break }
                    if !itemText.isEmpty { itemText += " " }
                    itemText += nextTrimmed
                    index += 1
                }
                blocks.append(.listItem(item.indent, item.marker, itemText))
                continue
            }

            if trimmed.contains("|"),
               index + 1 < lines.count,
               isTableDelimiter(lines[index + 1]) {
                let header = tableCells(line)
                var rows: [[String]] = []
                index += 2
                while index < lines.count {
                    let row = lines[index]
                    let rowTrimmed = row.trimmingCharacters(in: .whitespaces)
                    guard !rowTrimmed.isEmpty, rowTrimmed.contains("|") else { break }
                    rows.append(tableCells(row))
                    index += 1
                }
                blocks.append(.table(header, rows))
                continue
            }

            var paragraph: [String] = [line]
            index += 1
            while index < lines.count {
                let next = lines[index]
                let nextTrimmed = next.trimmingCharacters(in: .whitespaces)
                guard !nextTrimmed.isEmpty,
                      !startsBlock(next, nextLine: index + 1 < lines.count ? lines[index + 1] : nil) else { break }
                paragraph.append(next)
                index += 1
            }
            blocks.append(.paragraph(paragraph.joined(separator: "\n")))
        }
        return blocks
    }

    /// Whether a line begins a block, which ends the paragraph or list item
    /// that came before it.
    private static func startsBlock(_ line: String, nextLine: String?) -> Bool {
        let trimmed = line.trimmingCharacters(in: .whitespaces)
        if trimmed.isEmpty { return true }
        if fenceStart(trimmed) != nil { return true }
        if heading(trimmed) != nil { return true }
        if isRule(trimmed) { return true }
        if trimmed.hasPrefix(">") { return true }
        if listItem(line) != nil { return true }
        if trimmed.contains("|"), let nextLine, isTableDelimiter(nextLine) { return true }
        return false
    }

    private static func heading(_ line: String) -> (level: Int, text: String)? {
        var level = 0
        var rest = Substring(line)
        while rest.first == "#" {
            level += 1
            rest = rest.dropFirst()
        }
        guard level >= 1, level <= 6, rest.isEmpty || rest.first == " " else { return nil }
        return (level, rest.trimmingCharacters(in: .whitespaces))
    }

    private static func isRule(_ line: String) -> Bool {
        let markers = line.filter { !$0.isWhitespace }
        guard markers.count >= 3, let first = markers.first,
              first == "-" || first == "*" || first == "_" else { return false }
        return markers.allSatisfy { $0 == first }
    }

    private static func fenceStart(_ line: String) -> (marker: Character, count: Int, language: String?)? {
        var rest = Substring(line)
        var marker: Character?
        var count = 0
        while let first = rest.first, first == "`" || first == "~" {
            if marker == nil { marker = first }
            guard first == marker else { return nil }
            count += 1
            rest = rest.dropFirst()
        }
        guard count >= 3, let marker else { return nil }
        let info = rest.trimmingCharacters(in: .whitespaces)
        guard !info.contains("`") else { return nil }
        let language = info.split(whereSeparator: { $0.isWhitespace }).first.map(String.init)
        return (marker, count, language?.isEmpty == true ? nil : language)
    }

    private static func isFenceEnd(_ line: String, marker: Character, count: Int) -> Bool {
        let trimmed = line.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty else { return false }
        var seen = 0
        for character in trimmed {
            guard character == marker else { return false }
            seen += 1
        }
        return seen >= count
    }

    private static func listItem(_ line: String) -> (indent: Int, marker: String, text: String)? {
        var prefix = 0
        var index = line.startIndex
        while index < line.endIndex, line[index] == " " || line[index] == "\t" {
            prefix += line[index] == "\t" ? 4 : 1
            index = line.index(after: index)
        }
        let rest = line[index...]
        guard !rest.isEmpty else { return nil }
        let indent = prefix / 2

        if let first = rest.first, first == "-" || first == "*" || first == "+" {
            let after = rest.dropFirst()
            guard after.first == " " else { return nil }
            return (indent, "•", after.trimmingCharacters(in: .whitespaces))
        }

        var digits = ""
        var cursor = rest.startIndex
        while cursor < rest.endIndex, rest[cursor].isNumber {
            digits.append(rest[cursor])
            cursor = rest.index(after: cursor)
        }
        guard !digits.isEmpty, cursor < rest.endIndex,
              rest[cursor] == "." || rest[cursor] == ")" else { return nil }
        let markerEnd = cursor
        cursor = rest.index(after: cursor)
        guard cursor < rest.endIndex, rest[cursor] == " " else { return nil }
        let marker = String(rest[rest.startIndex...markerEnd])
        return (indent, marker, rest[cursor...].trimmingCharacters(in: .whitespaces))
    }

    private static func tableCells(_ line: String) -> [String] {
        var trimmed = line.trimmingCharacters(in: .whitespaces)
        if trimmed.hasPrefix("|") { trimmed.removeFirst() }
        if trimmed.hasSuffix("|") { trimmed.removeLast() }
        return trimmed.split(separator: "|", omittingEmptySubsequences: false).map {
            String($0).trimmingCharacters(in: .whitespaces)
        }
    }

    private static func isTableDelimiter(_ line: String) -> Bool {
        let cells = tableCells(line)
        guard !cells.isEmpty else { return false }
        return cells.allSatisfy { cell in
            guard !cell.isEmpty else { return false }
            let dashes = cell.trimmingCharacters(in: CharacterSet(charactersIn: ":"))
            return !dashes.isEmpty && dashes.allSatisfy { $0 == "-" }
        }
    }
}

// MARK: - Inline text

private enum MarkdownInline {
    static func attributed(_ source: String) -> AttributedString {
        var result: AttributedString
        do {
            result = try AttributedString(
                markdown: source,
                options: .init(
                    interpretedSyntax: .inlineOnlyPreservingWhitespace,
                    failurePolicy: .returnPartiallyParsedIfPossible
                )
            )
        } catch {
            result = AttributedString(source)
        }
        for run in result.runs {
            guard run.inlinePresentationIntent?.contains(.code) == true else { continue }
            result[run.range].font = .system(.body, design: .monospaced)
            result[run.range].backgroundColor = Color.secondary.opacity(0.16)
        }
        return result
    }
}

// MARK: - Code blocks and tables

private struct MarkdownCodeBlock: View {
    let language: String?
    let code: String
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Text(language ?? "code")
                    .font(.caption2.monospaced())
                    .foregroundStyle(.secondary)
                Spacer(minLength: 12)
                Button {
                    UIPasteboard.general.string = code
                    copied = true
                    DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) {
                        copied = false
                    }
                } label: {
                    Image(systemName: copied ? "checkmark" : "doc.on.doc")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(copied ? Text("Copied") : Text("Copy"))
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 6)

            Divider()

            ScrollView(.horizontal, showsIndicators: false) {
                Text(verbatim: code)
                    .font(.system(.footnote, design: .monospaced))
                    .textSelection(.enabled)
                    .padding(10)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .background(Color(uiColor: .secondarySystemBackground), in: RoundedRectangle(cornerRadius: 9))
        .overlay {
            RoundedRectangle(cornerRadius: 9)
                .stroke(Color.secondary.opacity(0.18), lineWidth: 1)
        }
    }
}

private struct MarkdownTable: View {
    let header: [String]
    let rows: [[String]]

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            VStack(alignment: .leading, spacing: 0) {
                row(header, isHeader: true)
                Divider()
                ForEach(rows.indices, id: \.self) { index in
                    row(rows[index], isHeader: false)
                    if index != rows.count - 1 { Divider() }
                }
            }
            .background(Color(uiColor: .secondarySystemBackground), in: RoundedRectangle(cornerRadius: 8))
            .overlay {
                RoundedRectangle(cornerRadius: 8)
                    .stroke(Color.secondary.opacity(0.18), lineWidth: 1)
            }
        }
    }

    private func row(_ cells: [String], isHeader: Bool) -> some View {
        HStack(alignment: .top, spacing: 0) {
            ForEach(0..<max(header.count, cells.count), id: \.self) { column in
                Text(MarkdownInline.attributed(column < cells.count ? cells[column] : ""))
                    .font(isHeader ? .subheadline.weight(.semibold) : .subheadline)
                    .frame(minWidth: 80, maxWidth: 240, alignment: .leading)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 7)
            }
        }
    }
}
