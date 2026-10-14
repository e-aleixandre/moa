import Foundation
import LocalAuthentication
import Security

enum SavedSecretsError: Error, Equatable {
    case missingAccessGroup
    case biometryUnavailable
    case cancelled
    case authFailed
    case notFound
    case invalidData
    case keychain(OSStatus)
}

/// The only door to the Keychain, so tests can observe which queries run.
protocol KeychainClient {
    func copyMatching(_ query: CFDictionary, _ result: UnsafeMutablePointer<CFTypeRef?>?) -> OSStatus
    func add(_ attributes: CFDictionary) -> OSStatus
    func update(_ query: CFDictionary, _ attributes: CFDictionary) -> OSStatus
    func delete(_ query: CFDictionary) -> OSStatus
}

struct SystemKeychain: KeychainClient {
    func copyMatching(_ query: CFDictionary, _ result: UnsafeMutablePointer<CFTypeRef?>?) -> OSStatus {
        SecItemCopyMatching(query, result)
    }
    func add(_ attributes: CFDictionary) -> OSStatus { SecItemAdd(attributes, nil) }
    func update(_ query: CFDictionary, _ attributes: CFDictionary) -> OSStatus {
        SecItemUpdate(query, attributes)
    }
    func delete(_ query: CFDictionary) -> OSStatus { SecItemDelete(query) }
}

// The value lives in one Keychain item guarded by an access control
// (biometryCurrentSet, passcode set, this device only): the Keychain itself
// demands Face ID on every read and drops the item if enrolment changes.
// Metadata lives in a second, unprotected item so status/list never prompt.
// Invariant: SecItemCopyMatching is NEVER run on the secret item outside `use`
// -- not even attributes-only. On a physical device that lookup either prompts
// for Face ID (no context) or fails with -25308 (no-interaction context), so
// existence and listing come from the metadata items alone. A secret that is
// gone while its metadata remains surfaces as notFound in `use`, which then
// drops the stale metadata.
final class SavedSecretsStore {
    static let secretService = "com.ealeixandre.moa.saved-secrets"
    static let metaService = "com.ealeixandre.moa.saved-secrets.meta"

    private let accessGroup: String?
    private let keychain: KeychainClient

    init(accessGroup: String? = KeychainGroups.deviceCredential,
         keychain: KeychainClient = SystemKeychain()) {
        self.accessGroup = accessGroup
        self.keychain = keychain
    }

    func status(serverID: String, alias: String, fingerprint: String) throws -> [String: Any] {
        guard let meta = try metadata(serverID: serverID, alias: alias) else {
            return ["saved": false, "sameAction": false]
        }
        return [
            "saved": true,
            "sameAction": meta.actionFingerprint == fingerprint,
            "savedAt": meta.savedAt
        ]
    }

    func save(serverID: String, alias: String, label: String, summary: String,
              fingerprint: String, value: inout Data,
              stillValid: () -> Bool = { true }) throws {
        defer { zero(&value) }
        let account = SavedSecretKey.account(serverID: serverID, alias: alias)
        guard stillValid() else { throw SavedSecretsError.cancelled }
        try deleteSecret(account: account)

        var cfError: Unmanaged<CFError>?
        guard let control = SecAccessControlCreateWithFlags(
            nil,
            kSecAttrAccessibleWhenPasscodeSetThisDeviceOnly,
            .biometryCurrentSet,
            &cfError
        ) else {
            throw SavedSecretsError.biometryUnavailable
        }
        var item = try query(service: Self.secretService, account: account)
        item[kSecAttrAccessControl as String] = control
        item[kSecValueData as String] = value
        let status = keychain.add(item as CFDictionary)
        if status == errSecParam || status == errSecNotAvailable || status == errSecAuthFailed {
            throw SavedSecretsError.biometryUnavailable
        }
        guard status == errSecSuccess else { throw SavedSecretsError.keychain(status) }

        do {
            try writeMetadata(SavedSecretMetadata(
                alias: alias, label: label, actionSummary: summary,
                actionFingerprint: fingerprint,
                savedAt: SavedSecretMetadata.epochMs(Date()), lastUsedAt: nil
            ), serverID: serverID)
        } catch {
            try? deleteSecret(account: account)
            throw error
        }
        // Unpair (or a navigation) may have run its cleanup while this write
        // was in flight: whatever landed after it must not survive it.
        if !stillValid() {
            try? deleteSecret(account: account)
            try? deleteMetadata(account: account)
            throw SavedSecretsError.cancelled
        }
    }

    // Blocking: shows the Face ID sheet. Call off the main thread.
    func use(serverID: String, alias: String, reason: String) throws -> Data {
        let account = SavedSecretKey.account(serverID: serverID, alias: alias)
        let context = LAContext()
        context.localizedReason = reason
        var q = try query(service: Self.secretService, account: account)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        q[kSecUseAuthenticationContext as String] = context
        var result: CFTypeRef?
        let status = keychain.copyMatching(q as CFDictionary, &result)
        switch status {
        case errSecSuccess:
            guard let data = result as? Data else { throw SavedSecretsError.invalidData }
            return data
        case errSecItemNotFound:
            // Gone, e.g. invalidated by a Face ID enrolment change.
            try? deleteMetadata(account: account)
            throw SavedSecretsError.notFound
        case errSecUserCanceled:
            throw SavedSecretsError.cancelled
        case errSecAuthFailed:
            throw SavedSecretsError.authFailed
        default:
            throw SavedSecretsError.keychain(status)
        }
    }

    func list(serverID: String) throws -> [SavedSecretMetadata] {
        try allMetadata(serverID: serverID).sorted { ($0.lastUsedAt ?? $0.savedAt) > ($1.lastUsedAt ?? $1.savedAt) }
    }

    func touch(serverID: String, alias: String, fingerprint: String) throws {
        guard var meta = try metadata(serverID: serverID, alias: alias) else {
            throw SavedSecretsError.notFound
        }
        meta.actionFingerprint = fingerprint
        meta.lastUsedAt = SavedSecretMetadata.epochMs(Date())
        try writeMetadata(meta, serverID: serverID)
    }

    func forget(serverID: String, alias: String) throws {
        let account = SavedSecretKey.account(serverID: serverID, alias: alias)
        try deleteSecret(account: account)
        try deleteMetadata(account: account)
    }

    // serverID nil wipes every server (unpairing) with service-wide deletes:
    // no enumeration, so the secret items are never queried. Otherwise the
    // accounts of that server come from the metadata items.
    func forgetAll(serverID: String? = nil) throws {
        var firstError: Error?
        func attempt(_ body: () throws -> Void) {
            do { try body() } catch { firstError = firstError ?? error }
        }
        if let serverID {
            for account in try metadataAccounts(serverID: serverID) {
                attempt { try deleteSecret(account: account) }
                attempt { try deleteMetadata(account: account) }
            }
        } else {
            attempt { try delete(service: Self.secretService, account: nil) }
            attempt { try delete(service: Self.metaService, account: nil) }
        }
        if let firstError { throw firstError }
    }

    // MARK: - Keychain plumbing

    private func metadata(serverID: String, alias: String) throws -> SavedSecretMetadata? {
        let account = SavedSecretKey.account(serverID: serverID, alias: alias)
        var q = try query(service: Self.metaService, account: account)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = keychain.copyMatching(q as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess else { throw SavedSecretsError.keychain(status) }
        guard let data = result as? Data, let meta = SavedSecretMetadata.decode(data) else { return nil }
        return meta
    }

    private func allMetadata(serverID: String) throws -> [SavedSecretMetadata] {
        var q = try query(service: Self.metaService, account: nil)
        q[kSecReturnAttributes as String] = true
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitAll
        var result: CFTypeRef?
        let status = keychain.copyMatching(q as CFDictionary, &result)
        if status == errSecItemNotFound { return [] }
        guard status == errSecSuccess, let rows = result as? [[String: Any]] else {
            throw SavedSecretsError.keychain(status)
        }
        return rows.compactMap { row in
            guard
                let account = row[kSecAttrAccount as String] as? String,
                SavedSecretKey.alias(fromAccount: account, serverID: serverID) != nil,
                let data = row[kSecValueData as String] as? Data
            else { return nil }
            return SavedSecretMetadata.decode(data)
        }
    }

    private func metadataAccounts(serverID: String) throws -> [String] {
        var q = try query(service: Self.metaService, account: nil)
        q[kSecReturnAttributes as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitAll
        var result: CFTypeRef?
        let status = keychain.copyMatching(q as CFDictionary, &result)
        if status == errSecItemNotFound { return [] }
        guard status == errSecSuccess, let rows = result as? [[String: Any]] else {
            throw SavedSecretsError.keychain(status)
        }
        return rows.compactMap { $0[kSecAttrAccount as String] as? String }
            .filter { SavedSecretKey.alias(fromAccount: $0, serverID: serverID) != nil }
    }

    private func writeMetadata(_ meta: SavedSecretMetadata, serverID: String) throws {
        let account = SavedSecretKey.account(serverID: serverID, alias: meta.alias)
        let data = try meta.encoded()
        let q = try query(service: Self.metaService, account: account)
        let attrs: [String: Any] = [
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        ]
        let updated = keychain.update(q as CFDictionary, attrs as CFDictionary)
        if updated == errSecSuccess { return }
        guard updated == errSecItemNotFound else { throw SavedSecretsError.keychain(updated) }
        var item = q
        attrs.forEach { item[$0] = $1 }
        let added = keychain.add(item as CFDictionary)
        guard added == errSecSuccess else { throw SavedSecretsError.keychain(added) }
    }

    private func deleteSecret(account: String) throws { try delete(service: Self.secretService, account: account) }
    private func deleteMetadata(account: String) throws { try delete(service: Self.metaService, account: account) }

    private func delete(service: String, account: String?) throws {
        let status = keychain.delete(try query(service: service, account: account) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw SavedSecretsError.keychain(status)
        }
    }

    private func query(service: String, account: String?) throws -> [String: Any] {
        guard let accessGroup else { throw SavedSecretsError.missingAccessGroup }
        var q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccessGroup as String: accessGroup,
            kSecAttrSynchronizable as String: kCFBooleanFalse as Any
        ]
        if let account { q[kSecAttrAccount as String] = account }
        return q
    }

    private func zero(_ data: inout Data) {
        data.resetBytes(in: 0..<data.count)
    }
}
