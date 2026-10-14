import Security
import XCTest

/// In-memory Keychain that records every copyMatching by service. Items of the
/// secret service answer like a physical device would to an unauthenticated
/// lookup: they need user interaction.
private final class RecordingKeychain: KeychainClient {
    var items: [String: [String: Any]] = [:]
    private(set) var copyServices: [String] = []
    private(set) var deletes: [(service: String, account: String?)] = []

    private func service(_ q: CFDictionary) -> String {
        (q as! [String: Any])[kSecAttrService as String] as! String
    }
    private func key(_ q: [String: Any]) -> String {
        "\(q[kSecAttrService as String]!)|\(q[kSecAttrAccount as String]!)"
    }
    func secretCopies() -> Int {
        copyServices.filter { $0 == SavedSecretsStore.secretService }.count
    }

    func copyMatching(_ query: CFDictionary, _ result: UnsafeMutablePointer<CFTypeRef?>?) -> OSStatus {
        let q = query as! [String: Any]
        let svc = service(query)
        copyServices.append(svc)
        let rows = items.values.filter {
            $0[kSecAttrService as String] as? String == svc
                && (q[kSecAttrAccount as String] == nil
                    || $0[kSecAttrAccount as String] as? String == q[kSecAttrAccount as String] as? String)
        }
        if rows.isEmpty { return errSecItemNotFound }
        if q[kSecMatchLimit as String] as? String == kSecMatchLimitAll as String {
            result?.pointee = rows as CFArray
        } else if q[kSecReturnData as String] as? Bool == true {
            result?.pointee = rows[0][kSecValueData as String] as? Data as CFTypeRef?
        }
        return errSecSuccess
    }
    func add(_ attributes: CFDictionary) -> OSStatus {
        let a = attributes as! [String: Any]
        items[key(a)] = a
        return errSecSuccess
    }
    func update(_ query: CFDictionary, _ attributes: CFDictionary) -> OSStatus {
        let q = query as! [String: Any]
        guard var item = items[key(q)] else { return errSecItemNotFound }
        (attributes as! [String: Any]).forEach { item[$0] = $1 }
        items[key(q)] = item
        return errSecSuccess
    }
    func delete(_ query: CFDictionary) -> OSStatus {
        let q = query as! [String: Any]
        deletes.append((q[kSecAttrService as String] as! String, q[kSecAttrAccount as String] as? String))
        if q[kSecAttrAccount as String] != nil {
            return items.removeValue(forKey: key(q)) == nil ? errSecItemNotFound : errSecSuccess
        }
        let svc = service(query)
        items = items.filter { $0.value[kSecAttrService as String] as? String != svc }
        return errSecSuccess
    }
}

final class SavedSecretsStoreKeychainTests: XCTestCase {
    private let server = "https://moa.example"
    private let other = "https://other.example"

    private func makeStore() -> (SavedSecretsStore, RecordingKeychain) {
        let kc = RecordingKeychain()
        return (SavedSecretsStore(accessGroup: "grp", keychain: kc), kc)
    }

    private func save(_ store: SavedSecretsStore, _ serverID: String, _ alias: String) throws {
        var v = Data("pw".utf8)
        try store.save(serverID: serverID, alias: alias, label: alias, summary: "s",
                       fingerprint: "fp", value: &v)
    }

    func testSettingsPathsNeverQueryTheSecretItems() throws {
        let (store, kc) = makeStore()
        try save(store, server, "a")
        try save(store, other, "b")
        let afterSave = kc.secretCopies()
        XCTAssertEqual(afterSave, 0, "save must not look the secret up")

        XCTAssertEqual(try store.list(serverID: server).map(\.alias), ["a"])
        XCTAssertEqual(try store.status(serverID: server, alias: "a", fingerprint: "fp")["saved"] as? Bool, true)
        try store.touch(serverID: server, alias: "a", fingerprint: "fp2")
        try store.forget(serverID: server, alias: "a")
        try save(store, server, "a")
        try store.forgetAll(serverID: server)
        XCTAssertEqual(kc.secretCopies(), 0)
        XCTAssertEqual(try store.list(serverID: other).map(\.alias), ["b"], "other server untouched")
    }

    func testForgetAllForServerDeletesOnlyThatServersSecretAndMetadata() throws {
        let (store, kc) = makeStore()
        try save(store, server, "a")
        try save(store, server, "b")
        try save(store, other, "c")
        try store.forgetAll(serverID: server)
        XCTAssertEqual(kc.items.count, 2)
        XCTAssertTrue(kc.items.keys.allSatisfy { $0.contains(other) })
    }

    func testUnpairDeletesServiceWideWithoutQueryingSecrets() throws {
        let (store, kc) = makeStore()
        try save(store, server, "a")
        try save(store, other, "c")
        try store.forgetAll()
        XCTAssertTrue(kc.items.isEmpty)
        XCTAssertEqual(kc.secretCopies(), 0)
        XCTAssertTrue(kc.deletes.contains { $0.service == SavedSecretsStore.secretService && $0.account == nil })
    }

    func testOnlyUseReadsTheSecretAndDropsStaleMetadataWhenGone() throws {
        let (store, kc) = makeStore()
        try save(store, server, "a")
        XCTAssertEqual(try store.use(serverID: server, alias: "a", reason: "r"), Data("pw".utf8))
        XCTAssertEqual(kc.secretCopies(), 1)

        // Secret vanishes (e.g. Face ID enrolment change) but metadata remains:
        // list/status still say saved, `use` reports notFound and cleans up.
        kc.items = kc.items.filter { $0.value[kSecAttrService as String] as? String != SavedSecretsStore.secretService }
        XCTAssertEqual(try store.list(serverID: server).count, 1)
        XCTAssertThrowsError(try store.use(serverID: server, alias: "a", reason: "r")) {
            XCTAssertEqual($0 as? SavedSecretsError, .notFound)
        }
        XCTAssertTrue(try store.list(serverID: server).isEmpty)
    }
}
