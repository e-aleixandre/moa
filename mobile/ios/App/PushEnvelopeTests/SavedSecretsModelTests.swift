import Security
import XCTest

final class SavedSecretsModelTests: XCTestCase {
    func testAccountKeyIsScopedByServer() {
        let a = SavedSecretKey.account(serverID: "https://a.example", alias: "sudo")
        let b = SavedSecretKey.account(serverID: "https://a.example:8443", alias: "sudo")
        XCTAssertEqual(a, "https://a.example/sudo")
        XCTAssertNotEqual(a, b)
        XCTAssertEqual(SavedSecretKey.alias(fromAccount: a, serverID: "https://a.example"), "sudo")
        XCTAssertNil(SavedSecretKey.alias(fromAccount: b, serverID: "https://a.example"))
        XCTAssertNil(SavedSecretKey.alias(fromAccount: "https://a.example/", serverID: "https://a.example"))
    }

    func testAliasMayContainSlash() {
        let account = SavedSecretKey.account(serverID: "https://a.example", alias: "ssh/host")
        XCTAssertEqual(SavedSecretKey.alias(fromAccount: account, serverID: "https://a.example"), "ssh/host")
    }

    func testMetadataRoundTripAndListEntryHasNoFingerprint() throws {
        let meta = SavedSecretMetadata(
            alias: "sudo", label: "sudo", actionSummary: "sudo apt update",
            actionFingerprint: "fp1", savedAt: 1_790_000_000_000, lastUsedAt: nil
        )
        XCTAssertEqual(SavedSecretMetadata.decode(try meta.encoded()), meta)
        XCTAssertNil(SavedSecretMetadata.decode(Data("nope".utf8)))
        let entry = meta.listEntry
        XCTAssertNil(entry["actionFingerprint"])
        XCTAssertEqual(entry["savedAt"] as? Int64, 1_790_000_000_000)
        XCTAssertTrue(entry["lastUsedAt"] is NSNull)
    }

    func testEpochMs() {
        XCTAssertEqual(SavedSecretMetadata.epochMs(Date(timeIntervalSince1970: 1.5)), 1500)
    }

    func testInputValidation() {
        XCTAssertEqual(SavedSecretInput.text("ok", max: 5), "ok")
        XCTAssertNil(SavedSecretInput.text("", max: 5))
        XCTAssertEqual(SavedSecretInput.text("", max: 5, allowEmpty: true), "")
        XCTAssertNil(SavedSecretInput.text("toolong", max: 5))
        XCTAssertNil(SavedSecretInput.text("a\nb", max: 5))
        XCTAssertNil(SavedSecretInput.text(3, max: 5))
        XCTAssertEqual(SavedSecretInput.secretValue(" p w "), Data(" p w ".utf8))
        XCTAssertNil(SavedSecretInput.secretValue(""))
        XCTAssertNil(SavedSecretInput.secretValue(String(repeating: "x", count: 4097)))
    }

    func testReplyFenceDropsStaleOrForeignDocuments() {
        let fence = ReplyFence(generation: 3, origin: "https://a.example")
        XCTAssertTrue(fence.permits(generation: 3, boundOrigin: "https://a.example", documentMatchesPairing: true))
        XCTAssertFalse(fence.permits(generation: 4, boundOrigin: "https://a.example", documentMatchesPairing: true))
        XCTAssertFalse(fence.permits(generation: 3, boundOrigin: "https://b.example", documentMatchesPairing: true))
        XCTAssertFalse(fence.permits(generation: 3, boundOrigin: nil, documentMatchesPairing: true))
        XCTAssertFalse(fence.permits(generation: 3, boundOrigin: "https://a.example", documentMatchesPairing: false))
    }

    func testDocumentGenerationBumps() {
        let g = DocumentGeneration()
        let before = g.current
        g.bump()
        XCTAssertEqual(g.current, before + 1)
    }
}
