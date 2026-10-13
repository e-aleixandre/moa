import Foundation

enum ShareDestinationKind: String {
    case owner
    case session
}

struct ShareDestination: Equatable {
    let kind: ShareDestinationKind
    let sessionID: String
    let title: String
    let project: String
    let state: String

    var displayTitle: String { title.isEmpty ? "Untitled" : title }

    var subtitle: String {
        let status = state == "saved" ? "closed" : "working"
        return project.isEmpty ? status : "\(project) · \(status)"
    }
}

enum ShareDestinations {
    // Unknown kinds and unknown fields are ignored so the server can add both
    // without breaking an installed extension.
    static func decode(_ data: Data) throws -> [ShareDestination] {
        guard
            let root = try JSONSerialization.jsonObject(with: data) as? [String: Any],
            let rows = root["destinations"] as? [[String: Any]]
        else { throw ShareError.invalidResponse }
        return rows.compactMap { row in
            guard
                let rawKind = row["kind"] as? String,
                let kind = ShareDestinationKind(rawValue: rawKind),
                let id = row["session_id"] as? String,
                isPathSafe(id)
            else { return nil }
            return ShareDestination(
                kind: kind,
                sessionID: id,
                title: row["title"] as? String ?? "",
                project: row["project"] as? String ?? "",
                state: row["state"] as? String ?? ""
            )
        }
    }

    static func filter(_ destinations: [ShareDestination], query: String) -> [ShareDestination] {
        let needle = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !needle.isEmpty else { return destinations }
        return destinations.filter {
            $0.title.localizedCaseInsensitiveContains(needle)
                || $0.project.localizedCaseInsensitiveContains(needle)
        }
    }

    // The id ends up in a URL path: refuse anything that could change it.
    private static func isPathSafe(_ id: String) -> Bool {
        !id.isEmpty && id.unicodeScalars.allSatisfy {
            CharacterSet.alphanumerics.contains($0) || $0 == "-" || $0 == "_" || $0 == "."
        } && id != "." && id != ".."
    }
}

// Port of shareComposerText / shareSummary in the web frontend
// (data/share-target.js, CommandPalette.jsx).
enum ShareText {
    static func composed(title: String, text: String, url: String) -> String {
        var lines: [String] = []
        for value in [text, url, title] {
            let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
            if trimmed.isEmpty || lines.contains(where: { $0.contains(trimmed) }) { continue }
            lines.append(trimmed)
        }
        return lines.joined(separator: "\n")
    }

    static func message(note: String, title: String, text: String, url: String) -> String {
        [note.trimmingCharacters(in: .whitespacesAndNewlines), composed(title: title, text: text, url: url)]
            .filter { !$0.isEmpty }
            .joined(separator: "\n")
    }

    static func summary(fileNames: [String], title: String, text: String, url: String) -> String {
        if fileNames.count == 1 { return fileNames[0] }
        if fileNames.count > 1 { return "\(fileNames.count) files · \(fileNames.joined(separator: ", "))" }
        let flat = composed(title: title, text: text, url: url)
            .split(whereSeparator: { $0.isWhitespace }).joined(separator: " ")
        return flat.count > 80 ? String(flat.prefix(79)) + "…" : flat
    }
}
