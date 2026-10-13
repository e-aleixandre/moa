import Foundation

// Pure parts of the "Save on this iPhone" store. Kept free of Keychain and
// WebKit so the PushEnvelopeTests target can compile and test them directly.

enum SavedSecretLimits {
    static let maxAlias = 128
    static let maxLabel = 80
    static let maxSummary = 500
    static let maxFingerprint = 256
    static let maxValueBytes = 4096
    static let maxReason = 120
}

// Secrets are scoped by the paired server origin: a secret saved for server A
// is never listed or released for server B. The origin has no path, so the
// "<origin>/" prefix of one server can never match another server's accounts.
enum SavedSecretKey {
    static func account(serverID: String, alias: String) -> String {
        "\(serverID)/\(alias)"
    }

    static func alias(fromAccount account: String, serverID: String) -> String? {
        let prefix = serverID + "/"
        guard account.hasPrefix(prefix), account.count > prefix.count else { return nil }
        return String(account.dropFirst(prefix.count))
    }
}

struct SavedSecretMetadata: Codable, Equatable {
    var alias: String
    var label: String
    var actionSummary: String
    var actionFingerprint: String
    var savedAt: Int64
    var lastUsedAt: Int64?

    static func epochMs(_ date: Date) -> Int64 {
        Int64((date.timeIntervalSince1970 * 1000).rounded())
    }

    func encoded() throws -> Data { try JSONEncoder().encode(self) }

    static func decode(_ data: Data) -> SavedSecretMetadata? {
        try? JSONDecoder().decode(SavedSecretMetadata.self, from: data)
    }

    // Never carries the fingerprint or any value: this is what `list` returns.
    var listEntry: [String: Any] {
        [
            "alias": alias,
            "label": label,
            "actionSummary": actionSummary,
            "savedAt": savedAt,
            "lastUsedAt": lastUsedAt.map { $0 as Any } ?? NSNull()
        ]
    }
}

enum SavedSecretInput {
    // Returns nil when absent, empty, over the limit or containing control
    // characters; the bridge answers `invalid`.
    static func text(_ raw: Any?, max: Int, allowEmpty: Bool = false) -> String? {
        guard let value = raw as? String, value.count <= max else { return nil }
        if value.isEmpty { return allowEmpty ? value : nil }
        if value.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) { return nil }
        return value
    }

    // Kept as-is (not trimmed): it is exactly what the user typed.
    static func secretValue(_ raw: Any?) -> Data? {
        guard let value = raw as? String, !value.isEmpty else { return nil }
        let data = Data(value.utf8)
        return data.count <= SavedSecretLimits.maxValueBytes ? data : nil
    }
}

// Replies from the native bridge go only to the document that asked. Every
// document commit, pairing bind/clear and unpair bumps the generation; work
// admitted under an older one, or for another origin, is dropped unanswered.
final class DocumentGeneration {
    static let shared = DocumentGeneration()

    private let lock = NSLock()
    private var value = 0

    var current: Int {
        lock.lock(); defer { lock.unlock() }
        return value
    }

    func bump() {
        lock.lock(); defer { lock.unlock() }
        value += 1
    }
}

struct ReplyFence: Equatable {
    let generation: Int
    let origin: String

    // `documentMatchesPairing`: the web view's current URL is on the paired origin.
    func permits(generation current: Int, boundOrigin: String?, documentMatchesPairing: Bool) -> Bool {
        generation == current && boundOrigin == origin && documentMatchesPairing
    }
}
