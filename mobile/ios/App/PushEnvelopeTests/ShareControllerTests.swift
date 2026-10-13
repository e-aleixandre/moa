import XCTest
import UIKit

// Drives the real ShareViewController with a stubbed transport. Adapted from the
// independent review (review-b1cda28d) whose RED cases it keeps.
private final class ShareTransport: URLProtocol {
    enum Mode: Equatable { case http(Int), nonHTTP, otherError, ok, acceptedTimeout, acceptedConnectionLoss, offline, pending, oldServer404 }
    struct Record { let method: String; let path: String; let body: Data }
    private static let lock = NSLock()
    nonisolated(unsafe) private static var mode: Mode = .ok
    nonisolated(unsafe) private static var records: [Record] = []
    nonisolated(unsafe) private static var accepted = 0
    nonisolated(unsafe) private static var pending: [ShareTransport] = []

    static func reset(_ newMode: Mode) {
        lock.lock(); defer { lock.unlock() }
        mode = newMode; records = []; accepted = 0; pending = []
    }
    static var seen: [Record] { lock.lock(); defer { lock.unlock() }; return records }
    static var acceptedCount: Int { lock.lock(); defer { lock.unlock() }; return accepted }
    static func releasePending() {
        lock.lock(); let work = pending; pending = []; accepted += work.count; lock.unlock()
        work.forEach { $0.finish(status: 200) }
    }
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        var body = request.httpBody ?? Data()
        if let stream = request.httpBodyStream {
            stream.open(); defer { stream.close() }
            var buffer = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let n = stream.read(&buffer, maxLength: buffer.count)
                if n <= 0 { break }
                body.append(buffer, count: n)
            }
        }
        Self.lock.lock()
        let mode = Self.mode
        Self.records.append(Record(method: request.httpMethod ?? "", path: request.url!.path, body: body))
        if mode != .offline && mode != .pending && mode != .oldServer404 {
            Self.accepted += 1
        }
        if mode == .pending { Self.pending.append(self) }
        Self.lock.unlock()
        switch mode {
        case .ok: finish(status: 200)
        case .acceptedTimeout: client?.urlProtocol(self, didFailWithError: URLError(.timedOut))
        case .acceptedConnectionLoss: client?.urlProtocol(self, didFailWithError: URLError(.networkConnectionLost))
        case .offline: client?.urlProtocol(self, didFailWithError: URLError(.notConnectedToInternet))
        case .pending: break
        case .oldServer404: finish(status: 404)
        case .http(let status): finish(status: status)
        case .nonHTTP:
            client?.urlProtocol(self, didReceive: URLResponse(url: request.url!, mimeType: nil, expectedContentLength: 0, textEncodingName: nil), cacheStoragePolicy: .notAllowed)
            client?.urlProtocolDidFinishLoading(self)
        case .otherError: client?.urlProtocol(self, didFailWithError: NSError(domain: "fixture", code: 1))
        }
    }
    func finish(status: Int) {
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data("{}".utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private final class ShareExtensionContextSpy: NSExtensionContext {
    var completions = 0
    var cancellations = 0
    override func completeRequest(returningItems items: [Any]?, completionHandler: ((Bool) -> Void)? = nil) {
        completions += 1
        completionHandler?(true)
    }
    override func cancelRequest(withError error: Error) { cancellations += 1 }
}

private final class ShareControllerSpy: ShareViewController {
    let context = ShareExtensionContextSpy()
    override var extensionContext: NSExtensionContext? { context }
}


final class ShareControllerTests: XCTestCase {
    private var root: URL!
    private var sessions: [URLSession] = []
    private let destination = ShareDestination(kind: .owner, sessionID: "dest1", title: "Owner", project: "p", state: "idle")

    override func setUpWithError() throws {
        root = FileManager.default.temporaryDirectory.appendingPathComponent("moa-share-controller-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        ShareTransport.reset(.ok)
    }

    override func tearDown() {
        sessions.forEach { $0.invalidateAndCancel() }
        sessions = []
        try? FileManager.default.removeItem(at: root)
    }

    private func client() -> ShareClient {
        let session = ShareClient.makeSession(protocolClasses: [ShareTransport.self])
        sessions.append(session)
        return ShareClient(origin: URL(string: "https://share.invalid")!, credential: "fixture-only", session: session)
    }

    @MainActor private func controller(
        note: String = "", text: String = "shared payload", title: String = "", url: String = "",
        emptyDestinations: Bool = false
    ) throws -> ShareControllerSpy {
        let controller = ShareControllerSpy()
        controller.transaction = try SharedInboxTransaction(root: root)
        controller.client = client()
        controller.sharedText = text
        controller.sharedTitle = title
        controller.sharedURL = url
        controller.loadViewIfNeeded()
        controller.destinations = emptyDestinations ? [] : [destination]
        controller.selectedID = emptyDestinations ? nil : destination.sessionID
        controller.messageField.text = note
        return controller
    }

    @MainActor private func waitFor(_ predicate: () -> Bool, file: StaticString = #filePath, line: UInt = #line) async throws {
        for _ in 0..<200 {
            if predicate() { return }
            try await Task.sleep(nanoseconds: 20_000_000)
        }
        XCTFail("Timed out waiting for the controller", file: file, line: line)
    }

    private func button(_ controller: ShareViewController, _ title: String) -> UIButton? {
        controller.messageStack.arrangedSubviews.compactMap { $0 as? UIButton }
            .first { $0.configuration?.title == title }
    }

    private func press(_ controller: ShareViewController, _ title: String) -> Bool {
        guard let button = button(controller, title) else { return false }
        button.sendActions(for: .primaryActionTriggered)
        return true
    }

    private func manifests() throws -> [SharedInboxManifest] {
        try FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil, options: .skipsHiddenFiles)
            .filter { UUID(uuidString: $0.lastPathComponent) != nil }
            .map { try JSONDecoder().decode(SharedInboxManifest.self, from: Data(contentsOf: $0.appendingPathComponent("manifest.json"))) }
    }

    private func json(_ record: ShareTransport.Record) throws -> [String: Any] {
        try XCTUnwrap(JSONSerialization.jsonObject(with: record.body) as? [String: Any])
    }

    // MARK: Certain vs uncertain failures

    @MainActor func testUncertainSendOffersOnlyCloseAndSavesNothing() async throws {
        for mode in [ShareTransport.Mode.acceptedTimeout, .acceptedConnectionLoss, .nonHTTP, .otherError] {
            ShareTransport.reset(mode)
            let controller = try controller()
            controller.sendTapped()
            try await waitFor { !controller.sending }
            XCTAssertEqual(ShareTransport.seen.count, 1)
            XCTAssertNil(button(controller, "Save for later"))
            XCTAssertNil(button(controller, "Try again"))
            XCTAssertNil(button(controller, "Back"))
            XCTAssertNotNil(button(controller, "Close"))
            XCTAssertTrue(controller.tableView.isHidden)
            XCTAssertTrue(try manifests().isEmpty)
        }
    }

    @MainActor func testAnswersAfterThePostAreUncertainEvenWhenTheServerAccepted() async throws {
        // 502 is the case the reviewer reproduced: the upstream accepted, the proxy answered 502.
        for status in [301, 302, 307, 402, 409, 422, 500, 502, 503, 504] {
            ShareTransport.reset(.http(status))
            let controller = try controller(note: "typed note")
            controller.sendTapped()
            try await waitFor { !controller.sending }
            XCTAssertEqual(ShareTransport.seen.count, 1, "HTTP \(status)")
            XCTAssertNil(button(controller, "Save for later"), "HTTP \(status)")
            XCTAssertNil(button(controller, "Try again"), "HTTP \(status)")
            XCTAssertNotNil(button(controller, "Close"))
            XCTAssertTrue(try manifests().isEmpty)
        }
    }

    @MainActor func testCertainFailuresOfferSaveForLater() async throws {
        let modes: [ShareTransport.Mode] = [.offline, .http(401), .http(403), .http(400), .http(413)]
        for mode in modes {
            ShareTransport.reset(mode)
            let controller = try controller()
            controller.sendTapped()
            try await waitFor { !controller.sending }
            XCTAssertNotNil(button(controller, "Save for later"), "\(mode)")
            XCTAssertTrue(press(controller, "Save for later"))
        }
        XCTAssertEqual(try manifests().count, modes.count)
    }

    @MainActor func testDoubleTargetActionProducesOnlyOneSend() async throws {
        let controller = try controller()
        controller.sendTapped()
        controller.sendTapped()
        try await waitFor { controller.context.completions >= 1 }
        try await Task.sleep(nanoseconds: 200_000_000)
        XCTAssertEqual(ShareTransport.acceptedCount, 1)
        XCTAssertEqual(controller.context.completions, 1)
    }

    // MARK: Note kept when saving

    @MainActor func testSaveForLaterPreservesTypedNote() throws {
        let controller = try controller(note: "Read this carefully")
        controller.saveForLater()
        let manifest = try XCTUnwrap(manifests().first)
        XCTAssertEqual(manifest.text, "Read this carefully\nshared payload")
        XCTAssertEqual(try manifests().count, 1)
        XCTAssertEqual(ShareTransport.seen.count, 0)
    }

    @MainActor func testSaveForLaterWithoutNoteKeepsTextUntouched() throws {
        let controller = try controller(note: "  ")
        controller.saveForLater()
        XCTAssertEqual(try manifests().first?.text, "shared payload")
    }

    // MARK: Server without the endpoint

    @MainActor func testOldServer404OffersOnlySaveAndClose() async throws {
        ShareTransport.reset(.oldServer404)
        let controller = try controller(emptyDestinations: true)
        do { _ = try await client().destinations(); XCTFail("Expected missing endpoint") }
        catch {
            XCTAssertEqual(error as? ShareError, .notFound)
            controller.showFailure(error, whileSending: false)
        }
        XCTAssertNotNil(button(controller, "Save for later"))
        XCTAssertNotNil(button(controller, "Close"))
        XCTAssertNil(button(controller, "Choose another"))
        XCTAssertTrue(controller.tableView.isHidden)
        XCTAssertTrue(press(controller, "Save for later"))
        XCTAssertEqual(try manifests().count, 1)
        XCTAssertEqual(ShareTransport.seen.map(\.method), ["GET"])
    }

    @MainActor func testSendNotFoundAfterResumeKeepsAPopulatedPickerAndSave() throws {
        let controller = try controller()
        controller.showFailure(ShareError.notFound, whileSending: true)
        XCTAssertNotNil(button(controller, "Save for later"))
        XCTAssertTrue(press(controller, "Choose another"))
        XCTAssertFalse(controller.tableView.isHidden)
        XCTAssertFalse(controller.destinations.isEmpty)
    }

    // MARK: Text limit

    private func uploadedText() throws -> String? {
        try ShareTransport.seen.first.flatMap { try json($0)["text"] as? String }
    }

    @MainActor func testTextOverTheLimitIsRejectedWithoutUpload() async throws {
        let controller = try controller(text: String(repeating: "x", count: SharedInbox.maximumTextBytes + 1))
        controller.sendTapped()
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertEqual(ShareTransport.seen.count, 0)
        XCTAssertFalse(controller.sending)
        XCTAssertEqual(controller.context.completions, 0)
        XCTAssertNotNil(button(controller, "Back"))
    }

    @MainActor func testTextAtTheLimitIsSentExactly() async throws {
        let controller = try controller(text: String(repeating: "x", count: SharedInbox.maximumTextBytes))
        controller.sendTapped()
        try await waitFor { controller.context.completions == 1 }
        XCTAssertEqual(try uploadedText()?.utf8.count, SharedInbox.maximumTextBytes)
    }

    @MainActor func testNoteSeparatorCountsTowardTheLimit() async throws {
        // "n" + "\n" + 262143 bytes is 262145 uploaded: one over.
        let over = try controller(note: "n", text: String(repeating: "x", count: SharedInbox.maximumTextBytes - 1))
        over.sendTapped()
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertEqual(ShareTransport.seen.count, 0)

        // One byte less is exactly the limit and goes through.
        ShareTransport.reset(.ok)
        let exact = try controller(note: "n", text: String(repeating: "x", count: SharedInbox.maximumTextBytes - 2))
        exact.sendTapped()
        try await waitFor { exact.context.completions == 1 }
        XCTAssertEqual(try uploadedText()?.utf8.count, SharedInbox.maximumTextBytes)
    }

    @MainActor func testTextLimitCountsNoteTitleTextAndURL() async throws {
        let half = String(repeating: "x", count: SharedInbox.maximumTextBytes / 2)
        let controller = try controller(note: half, text: "", title: "ttt", url: half)
        controller.sendTapped()
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertEqual(ShareTransport.seen.count, 0)
    }

    @MainActor func testSavePersistsTheComposedTextOnly() throws {
        let controller = try controller(note: "n", text: "body", title: "Page", url: "https://a.example")
        controller.saveForLater()
        let manifest = try XCTUnwrap(manifests().first)
        XCTAssertEqual(manifest.text, ShareText.message(note: "n", title: "Page", text: "body", url: "https://a.example"))
        XCTAssertEqual(manifest.title, "")
        XCTAssertEqual(manifest.url, "")
    }

    @MainActor func testSaveAndSendAgreeByteForByteAtTheBoundary() async throws {
        // note "n" + title "t" + 262141 bytes: composed 262145, one over for both paths.
        let over = try controller(note: "n", text: String(repeating: "x", count: SharedInbox.maximumTextBytes - 3), title: "t")
        XCTAssertEqual(ShareText.message(note: "n", title: "t", text: over.sharedText, url: "").utf8.count, SharedInbox.maximumTextBytes + 1)
        over.sendTapped()
        try await Task.sleep(nanoseconds: 250_000_000)
        XCTAssertEqual(ShareTransport.seen.count, 0)
        over.saveForLater()
        XCTAssertTrue(try manifests().isEmpty)

        // One byte shorter: exactly the limit, accepted by Save and Send alike.
        let exact = try controller(note: "n", text: String(repeating: "x", count: SharedInbox.maximumTextBytes - 4), title: "t")
        exact.saveForLater()
        XCTAssertEqual(try manifests().first?.text.utf8.count, SharedInbox.maximumTextBytes)
        let sender = try controller(note: "n", text: String(repeating: "x", count: SharedInbox.maximumTextBytes - 4), title: "t")
        sender.sendTapped()
        try await waitFor { sender.context.completions == 1 }
        XCTAssertEqual(try uploadedText()?.utf8.count, SharedInbox.maximumTextBytes)
    }

    @MainActor func testUTF8NoteCountsInBytes() async throws {
        let over = try controller(note: "é", text: String(repeating: "x", count: SharedInbox.maximumTextBytes - 2))
        over.sendTapped()
        try await Task.sleep(nanoseconds: 250_000_000)
        XCTAssertEqual(ShareTransport.seen.count, 0)
        let exact = try controller(note: "é", text: String(repeating: "x", count: SharedInbox.maximumTextBytes - 3))
        exact.sendTapped()
        try await waitFor { exact.context.completions == 1 }
        XCTAssertEqual(try uploadedText()?.utf8.count, SharedInbox.maximumTextBytes)
    }

    @MainActor func testSaveTooLargeGoesBackToEditingWithTheNoteIntact() throws {
        let controller = try controller(note: "keep me", text: String(repeating: "x", count: SharedInbox.maximumTextBytes))
        controller.saveForLater()
        XCTAssertTrue(try manifests().isEmpty)
        XCTAssertNotNil(button(controller, "Back"))
        XCTAssertNil(button(controller, "Save for later"))
        XCTAssertTrue(press(controller, "Back"))
        XCTAssertFalse(controller.tableView.isHidden)
        XCTAssertEqual(controller.messageField.text, "keep me")
    }

    @MainActor func testSaveTooLargeWithoutAPickerOffersOnlyClose() throws {
        let controller = try controller(text: String(repeating: "x", count: SharedInbox.maximumTextBytes + 1), emptyDestinations: true)
        controller.saveForLater()
        XCTAssertNil(button(controller, "Back"))
        XCTAssertNotNil(button(controller, "Close"))
    }

    @MainActor func testPostFailureBeforeTheRequestIsSafeToSave() async throws {
        let controller = try controller()
        let source = root.appendingPathComponent("source.bin")
        try Data([1, 2, 3]).write(to: source)
        try controller.transaction?.addFile(at: source, name: "source.bin", mime: "application/octet-stream")
        try FileManager.default.removeItem(at: try XCTUnwrap(controller.transaction?.stagedFiles.first?.url))
        controller.sendTapped()
        try await waitFor { !controller.sending }
        XCTAssertEqual(ShareTransport.seen.count, 0)
        XCTAssertNotNil(button(controller, "Save for later"))
    }

    // MARK: Kept from the review (already green)

    @MainActor func testCancelBeforeSendLeavesNoQueueOrRequest() throws {
        let controller = try controller()
        controller.close()
        XCTAssertEqual(controller.context.cancellations, 1)
        XCTAssertEqual(controller.context.completions, 0)
        XCTAssertEqual(ShareTransport.seen.count, 0)
        XCTAssertTrue(try FileManager.default.contentsOfDirectory(atPath: root.path).isEmpty)
    }

    @MainActor func testNoCompletionBeforeAcceptance() async throws {
        ShareTransport.reset(.pending)
        let controller = try controller()
        controller.sendTapped()
        try await waitFor { ShareTransport.seen.count == 1 }
        XCTAssertEqual(controller.context.completions, 0)
        XCTAssertTrue(controller.sending)
        ShareTransport.releasePending()
        try await waitFor { controller.context.completions == 1 }
        XCTAssertTrue(try FileManager.default.contentsOfDirectory(atPath: root.path).isEmpty)
    }

    @MainActor func testSuccessSendsNoteThenSharedText() async throws {
        let controller = try controller(note: "my note")
        controller.sendTapped()
        try await waitFor { controller.context.completions == 1 }
        XCTAssertEqual(try json(XCTUnwrap(ShareTransport.seen.first))["text"] as? String, "my note\nshared payload")
        XCTAssertTrue(try manifests().isEmpty)
    }

    @MainActor func testOfflineThenSaveProducesOnePendingShare() async throws {
        ShareTransport.reset(.offline)
        let controller = try controller()
        controller.sendTapped()
        try await waitFor { !controller.sending }
        XCTAssertTrue(press(controller, "Save for later"))
        XCTAssertEqual(try manifests().count, 1)
    }
}
