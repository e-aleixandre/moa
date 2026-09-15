import Foundation

struct SharedInboxFile: Codable {
    let name: String
    let mime: String
    let size: Int
    let storedName: String
}

struct SharedInboxManifest: Codable {
    let id: String
    let createdAt: TimeInterval
    let title: String
    let text: String
    let url: String
    let files: [SharedInboxFile]
}

struct SharedInboxEnvelope {
    let manifest: SharedInboxManifest
    let directory: URL
}

enum SharedInboxError: LocalizedError {
    case missingAppGroup
    case queueFull
    case tooManyFiles
    case tooLarge
    case textTooLarge
    case empty
    case corrupt

    var errorDescription: String? {
        switch self {
        case .missingAppGroup:
            return "moa's shared container is not configured."
        case .queueFull:
            return "Open moa and place the pending shares, then try again."
        case .tooManyFiles:
            return "Share no more than 8 files at once."
        case .tooLarge:
            return "This share is larger than 32 MB. Choose a smaller file."
        case .textTooLarge:
            return "The shared text is too large. Share a shorter selection."
        case .empty:
            return "No supported text, link, image, or file arrived."
        case .corrupt:
            return "A pending share could not be read."
        }
    }
}

enum SharedInbox {
    static let maximumFiles = 8
    static let maximumBytes = 32 * 1024 * 1024
    static let maximumTextBytes = 256 * 1024
    static let maximumPendingShares = 20

    private static let manifestName = "manifest.json"
    private static let inboxName = "ShareInbox"

    static func rootDirectory() throws -> URL {
        guard
            let group = Bundle.main.object(forInfoDictionaryKey: "MoaAppGroup") as? String,
            !group.isEmpty,
            !group.contains("YOUR_REVERSED_DOMAIN"),
            let container = FileManager.default.containerURL(
                forSecurityApplicationGroupIdentifier: group
            )
        else {
            throw SharedInboxError.missingAppGroup
        }
        let root = container.appendingPathComponent(inboxName, isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        return root
    }

    static func pendingCount() throws -> Int {
        try committedDirectories().count
    }

    static func next() throws -> SharedInboxEnvelope? {
        let decoder = JSONDecoder()
        let envelopes: [SharedInboxEnvelope] = try committedDirectories().compactMap { directory in
            let data = try Data(contentsOf: directory.appendingPathComponent(manifestName))
            let manifest = try decoder.decode(SharedInboxManifest.self, from: data)
            guard manifest.id == directory.lastPathComponent else {
                throw SharedInboxError.corrupt
            }
            return SharedInboxEnvelope(manifest: manifest, directory: directory)
        }
        return envelopes.min { $0.manifest.createdAt < $1.manifest.createdAt }
    }

    static func acknowledge(id: String) throws {
        guard UUID(uuidString: id) != nil else { throw SharedInboxError.corrupt }
        let directory = try rootDirectory().appendingPathComponent(id, isDirectory: true)
        if FileManager.default.fileExists(atPath: directory.path) {
            try FileManager.default.removeItem(at: directory)
        }
    }

    static func transaction() throws -> SharedInboxTransaction {
        guard try pendingCount() < maximumPendingShares else {
            throw SharedInboxError.queueFull
        }
        return try SharedInboxTransaction(root: rootDirectory())
    }

    private static func committedDirectories() throws -> [URL] {
        let root = try rootDirectory()
        return try FileManager.default.contentsOfDirectory(
            at: root,
            includingPropertiesForKeys: [.isDirectoryKey],
            options: [.skipsHiddenFiles]
        ).filter { url in
            guard UUID(uuidString: url.lastPathComponent) != nil else { return false }
            return (try? url.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true
        }
    }

    fileprivate static func manifestURL(in directory: URL) -> URL {
        directory.appendingPathComponent(manifestName)
    }
}

final class SharedInboxTransaction {
    private let root: URL
    private let id = UUID().uuidString.lowercased()
    private let staging: URL
    private var files: [SharedInboxFile] = []
    private var totalBytes = 0
    private var committed = false

    fileprivate init(root: URL) throws {
        self.root = root
        self.staging = root.appendingPathComponent(".\(id).tmp", isDirectory: true)
        try FileManager.default.createDirectory(at: staging, withIntermediateDirectories: false)
    }

    deinit {
        if !committed {
            try? FileManager.default.removeItem(at: staging)
        }
    }

    func addFile(at source: URL, name suggestedName: String?, mime: String?) throws {
        guard files.count < SharedInbox.maximumFiles else {
            throw SharedInboxError.tooManyFiles
        }
        let values = try source.resourceValues(forKeys: [.fileSizeKey, .nameKey])
        let attributes = try FileManager.default.attributesOfItem(atPath: source.path)
        guard let size = values.fileSize ?? (attributes[.size] as? NSNumber)?.intValue else {
            throw SharedInboxError.corrupt
        }
        guard size >= 0, size <= SharedInbox.maximumBytes - totalBytes else {
            throw SharedInboxError.tooLarge
        }

        let sourceName = suggestedName?.trimmingCharacters(in: .whitespacesAndNewlines)
        let safeName = URL(fileURLWithPath: sourceName?.isEmpty == false ? sourceName! : (values.name ?? "shared-file"))
            .lastPathComponent
        let storedName = UUID().uuidString.lowercased()
        let destination = staging.appendingPathComponent(storedName)
        try FileManager.default.copyItem(at: source, to: destination)
        files.append(SharedInboxFile(
            name: safeName.isEmpty ? "shared-file" : safeName,
            mime: mime?.isEmpty == false ? mime! : "application/octet-stream",
            size: size,
            storedName: storedName
        ))
        totalBytes += size
    }

    func commit(title: String, text: String, url: String) throws -> SharedInboxManifest {
        let cleanTitle = title.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanText = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanURL = url.trimmingCharacters(in: .whitespacesAndNewlines)
        let textBytes = cleanTitle.utf8.count + cleanText.utf8.count + cleanURL.utf8.count
        guard textBytes <= SharedInbox.maximumTextBytes else {
            throw SharedInboxError.textTooLarge
        }
        guard !cleanTitle.isEmpty || !cleanText.isEmpty || !cleanURL.isEmpty || !files.isEmpty else {
            throw SharedInboxError.empty
        }

        let manifest = SharedInboxManifest(
            id: id,
            createdAt: Date().timeIntervalSince1970,
            title: cleanTitle,
            text: cleanText,
            url: cleanURL,
            files: files
        )
        let data = try JSONEncoder().encode(manifest)
        try data.write(to: SharedInbox.manifestURL(in: staging), options: .atomic)
        try FileManager.default.moveItem(
            at: staging,
            to: root.appendingPathComponent(id, isDirectory: true)
        )
        committed = true
        return manifest
    }
}
