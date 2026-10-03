import Foundation
import Security

struct StoredDeviceCredential: Codable, Equatable {
    let origin: String
    let credential: String
    let expiresAt: Date
}

enum DeviceCredentialStoreError: Error {
    case invalidData
    case missingAccessGroup
    case keychain(OSStatus)
}

final class DeviceCredentialStore {
    private let service: String
    private let account = "paired-device"
    private let accessGroup: String?

    // The credential lives in the app's own access group, named on every
    // query. Once the entitlements list a shared push group, an add without
    // a group would land in the first listed group and a lookup without one
    // would search all of them (review R1).
    init(
        service: String = (Bundle.main.bundleIdentifier ?? "moa") + ".device-auth",
        accessGroup: String? = KeychainGroups.deviceCredential
    ) {
        self.service = service
        self.accessGroup = accessGroup
    }

    func save(_ credential: StoredDeviceCredential) throws {
        let data = try JSONEncoder().encode(credential)
        let query = try baseQuery()
        let attributes: [String: Any] = [
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        ]
        let updateStatus = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if updateStatus == errSecSuccess { return }
        guard updateStatus == errSecItemNotFound else {
            throw DeviceCredentialStoreError.keychain(updateStatus)
        }

        var item = query
        attributes.forEach { item[$0] = $1 }
        let addStatus = SecItemAdd(item as CFDictionary, nil)
        guard addStatus == errSecSuccess else {
            throw DeviceCredentialStoreError.keychain(addStatus)
        }
    }

    func load() throws -> StoredDeviceCredential? {
        var query = try baseQuery()
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess else {
            throw DeviceCredentialStoreError.keychain(status)
        }
        guard
            let data = result as? Data,
            let credential = try? JSONDecoder().decode(StoredDeviceCredential.self, from: data)
        else {
            throw DeviceCredentialStoreError.invalidData
        }
        return credential
    }

    func delete() throws {
        let query = try baseQuery()
        let status = SecItemDelete(query as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw DeviceCredentialStoreError.keychain(status)
        }
    }

    private func baseQuery() throws -> [String: Any] {
        guard let accessGroup else { throw DeviceCredentialStoreError.missingAccessGroup }
        return [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrAccessGroup as String: accessGroup,
            kSecAttrSynchronizable as String: kCFBooleanFalse as Any
        ]
    }
}
