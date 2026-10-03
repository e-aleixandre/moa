import CryptoKit
import Foundation
import Security

// Wire contract v1 of moa native push (relay/PROTOCOL.md). This file is
// compiled into the app, the Notification Service Extension and the vector
// tests, so the byte-level rules live in exactly one place.
enum PushEnvelope {
    static let paddedSize = 2048
    static let nonceSize = 12
    static let tagSize = 16
    static let kidSize = 8
    static let encodedCiphertextLength = 2768
    static let encodedKidLength = 11

    struct Keys {
        let enc: SymmetricKey
        let send: SymmetricKey
        let kid: Data
    }

    enum Level: String {
        case urgent, active, passive
    }

    enum Destination: Equatable {
        case session(String)
        case inbox
        case home

        // Only the paired origin plus one of three fixed query shapes: nothing
        // from the envelope can choose a host, path or arbitrary parameter.
        func url(origin: URL) -> URL? {
            guard var components = URLComponents(url: origin, resolvingAgainstBaseURL: false) else { return nil }
            components.path = "/"
            components.fragment = nil
            switch self {
            case .session(let id): components.queryItems = [URLQueryItem(name: "session", value: id)]
            case .inbox: components.queryItems = [URLQueryItem(name: "inbox", value: "1")]
            case .home: components.queryItems = nil
            }
            return components.url
        }
    }

    struct Content {
        let destination: Destination
        let level: Level
        let thread: String
        let title: String
        let body: String
    }

    private struct Plaintext: Decodable {
        let v: Int
        let exp: Int64
        let d: String
        let s: String?
        let lvl: String
        let th: String
        let t: String
        let b: String
    }

    // HKDF-SHA256 with an empty salt; the label is the info.
    static func derive(secret: Data, label: String, length: Int) -> Data {
        let key = HKDF<SHA256>.deriveKey(
            inputKeyMaterial: SymmetricKey(data: secret),
            salt: Data(),
            info: Data(label.utf8),
            outputByteCount: length
        )
        return key.withUnsafeBytes { Data($0) }
    }

    static func deriveKeys(secret: Data) -> Keys? {
        guard secret.count == 32 else { return nil }
        return Keys(
            enc: SymmetricKey(data: derive(secret: secret, label: "moa-push-enc-v1", length: 32)),
            send: SymmetricKey(data: derive(secret: secret, label: "moa-push-send-v1", length: 32)),
            kid: derive(secret: secret, label: "moa-push-kid-v1", length: kidSize)
        )
    }

    // Authenticates before interpreting anything. Any failure -- shape, kid,
    // tag, inner version, expiry -- returns nil and the caller falls back.
    static func open(_ raw: Any?, keys: Keys, now: Date) -> Content? {
        guard
            let envelope = raw as? [String: Any],
            let version = envelope["v"] as? Int, version == 1,
            let kidText = envelope["k"] as? String, kidText.utf8.count == encodedKidLength,
            let kid = base64URLDecode(kidText), kid == keys.kid,
            let sealedText = envelope["c"] as? String, sealedText.utf8.count == encodedCiphertextLength,
            let sealed = base64URLDecode(sealedText), sealed.count == nonceSize + paddedSize + tagSize,
            let nonce = try? AES.GCM.Nonce(data: sealed.prefix(nonceSize)),
            let box = try? AES.GCM.SealedBox(
                nonce: nonce,
                ciphertext: sealed.dropFirst(nonceSize).dropLast(tagSize),
                tag: sealed.suffix(tagSize)
            ),
            let padded = try? AES.GCM.open(box, using: keys.enc, authenticating: Data("moa-push-v1".utf8) + keys.kid),
            padded.count == paddedSize,
            let plaintext = try? JSONDecoder().decode(Plaintext.self, from: trimTrailingSpaces(padded)),
            plaintext.v == 1,
            plaintext.exp > Int64(now.timeIntervalSince1970)
        else { return nil }

        let destination: Destination
        switch plaintext.d {
        case "session":
            if let id = plaintext.s, isSessionID(id) {
                destination = .session(id)
            } else {
                destination = .home
            }
        case "inbox":
            destination = .inbox
        default:
            destination = .home
        }
        return Content(
            destination: destination,
            // An unknown level from a newer server still sounds rather than
            // being silently demoted.
            level: Level(rawValue: plaintext.lvl) ?? .active,
            thread: plaintext.th,
            title: plaintext.t,
            body: plaintext.b
        )
    }

    // p = b64u(HMAC(K_send, "moa-confirm-v1" || 0x00 || ascii(c)))
    static func confirmProof(challenge: String, sendKey: SymmetricKey) -> String {
        var message = Data("moa-confirm-v1".utf8)
        message.append(0)
        message.append(Data(challenge.utf8))
        return base64URLEncode(Data(HMAC<SHA256>.authenticationCode(for: message, using: sendKey)))
    }

    static func isSessionID(_ value: String) -> Bool {
        (1...128).contains(value.utf8.count) && value.utf8.allSatisfy { byte in
            (byte >= 0x30 && byte <= 0x39) || (byte >= 0x41 && byte <= 0x5A)
                || (byte >= 0x61 && byte <= 0x7A) || byte == 0x2D || byte == 0x5F
        }
    }

    static func base64URLEncode(_ data: Data) -> String {
        data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }

    // Unpadded base64url only; standard-alphabet or padded input is refused.
    static func base64URLDecode(_ text: String) -> Data? {
        guard !text.contains("="), !text.contains("+"), !text.contains("/") else { return nil }
        var standard = text
            .replacingOccurrences(of: "-", with: "+")
            .replacingOccurrences(of: "_", with: "/")
        switch standard.utf8.count % 4 {
        case 0: break
        case 2: standard += "=="
        case 3: standard += "="
        default: return nil
        }
        return Data(base64Encoded: standard)
    }

    private static func trimTrailingSpaces(_ data: Data) -> Data {
        var end = data.endIndex
        while end > data.startIndex, data[data.index(before: end)] == 0x20 {
            end = data.index(before: end)
        }
        return data[data.startIndex..<end]
    }
}

// Keychain access groups come from Info.plist so the team prefix is never
// hard-coded. A missing or unexpanded value is a build error surfaced at
// runtime as a Keychain failure: falling back to "no group" would let a new
// item land in whichever group the entitlements list first.
enum KeychainGroups {
    static var push: String? { resolved("MoaKeychainPushGroup") }
    static var deviceCredential: String? { resolved("MoaKeychainPrivateGroup") }

    private static func resolved(_ key: String) -> String? {
        guard
            let value = Bundle.main.object(forInfoDictionaryKey: key) as? String,
            !value.isEmpty,
            !value.contains("$("),
            !value.hasPrefix(".")
        else { return nil }
        return value
    }
}

enum PushSecretStoreError: Error {
    case missingAccessGroup
    case invalidData
    case keychain(OSStatus)
}

// The device push secret S. Shared with the Notification Service Extension
// through a push-only access group; the device credential is never here.
enum PushSecretStore {
    static let service = "com.ealeixandre.moa.push"
    static let account = "device-secret-v1"

    static func load() throws -> Data? {
        var query = try baseQuery()
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess else { throw PushSecretStoreError.keychain(status) }
        guard let data = result as? Data, data.count == 32 else { throw PushSecretStoreError.invalidData }
        return data
    }

    static func save(_ secret: Data) throws {
        guard secret.count == 32 else { throw PushSecretStoreError.invalidData }
        try delete()
        var item = try baseQuery()
        item[kSecValueData as String] = secret
        item[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(item as CFDictionary, nil)
        guard status == errSecSuccess else { throw PushSecretStoreError.keychain(status) }
    }

    static func delete() throws {
        let query = try baseQuery()
        let status = SecItemDelete(query as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw PushSecretStoreError.keychain(status)
        }
    }

    private static func baseQuery() throws -> [String: Any] {
        guard let group = KeychainGroups.push else { throw PushSecretStoreError.missingAccessGroup }
        return [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrAccessGroup as String: group,
            kSecAttrSynchronizable as String: kCFBooleanFalse as Any
        ]
    }
}

// The app's registration bookkeeping that the vector tests can exercise
// without a device. The extension compiles it but does not use it.

// Orders the app's push operations and fences the obsolete ones. Not
// thread-safe: NativePushRegistration uses it from the main actor only.
final class NativePushOperationQueue {
    private(set) var generation = 0
    private var last: Task<Void, Error>?

    // Ends the current generation; operations that read it stop at their
    // next isCurrent check.
    func invalidate() {
        generation += 1
    }

    func isCurrent(_ generation: Int) -> Bool {
        generation == self.generation
    }

    // FIFO: an operation starts only after the one enqueued before it has
    // finished, whatever its outcome. Enqueueing itself does not suspend, so
    // the order is the order of the calls.
    func enqueue(_ work: @escaping @MainActor () async throws -> Void) -> Task<Void, Error> {
        let previous = last
        let task = Task { @MainActor in
            _ = await previous?.result
            try await work()
        }
        last = task
        return task
    }
}

// Binds a relay confirmation to the registration the app is waiting on: the
// relay returns the token and env the challenge was sealed for, and only the
// pending ones are accepted. A relay that does not return them (older than
// this contract) is refused, never trusted.
enum RelayConfirmation {
    static func matches(_ confirmedToken: String?, _ confirmedEnv: String?, token: String, env: String) -> Bool {
        guard let confirmedToken, let confirmedEnv else { return false }
        return confirmedToken == token && confirmedEnv == env
    }
}
