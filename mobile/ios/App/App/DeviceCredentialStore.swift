import Foundation
import Security

struct StoredDeviceCredential: Codable, Equatable {
    let origin: String
    let credential: String
    let expiresAt: Date
}

enum DeviceCredentialStoreError: Error {
    case invalidData
    case keychain(OSStatus)
}

final class DeviceCredentialStore {
    private let service: String
    private let account = "paired-device"

    init(service: String = (Bundle.main.bundleIdentifier ?? "moa") + ".device-auth") {
        self.service = service
    }

    func save(_ credential: StoredDeviceCredential) throws {
        let data = try JSONEncoder().encode(credential)
        let query = baseQuery()
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
        var query = baseQuery()
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
        let status = SecItemDelete(baseQuery() as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw DeviceCredentialStoreError.keychain(status)
        }
    }

    private func baseQuery() -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrSynchronizable as String: kCFBooleanFalse as Any
        ]
    }
}
