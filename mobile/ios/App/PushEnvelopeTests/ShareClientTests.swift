import XCTest

private final class StubProtocol: URLProtocol {
    struct Seen { let method: String; let path: String; let headers: [String: String]; let body: Data }
    nonisolated(unsafe) static var seen: [Seen] = []
    nonisolated(unsafe) static var responses: [Result<(Int, String), Error>] = []

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        var body = request.httpBody ?? Data()
        if let stream = request.httpBodyStream {
            stream.open()
            var buffer = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let n = stream.read(&buffer, maxLength: buffer.count)
                if n <= 0 { break }
                body.append(buffer, count: n)
            }
            stream.close()
        }
        Self.seen.append(Seen(
            method: request.httpMethod ?? "", path: request.url?.path ?? "",
            headers: request.allHTTPHeaderFields ?? [:], body: body))
        switch Self.responses.removeFirst() {
        case .success(let (status, text)):
            let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Data(text.utf8))
            client?.urlProtocolDidFinishLoading(self)
        case .failure(let error):
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}
}

final class ShareClientTests: XCTestCase {
    private var body: URL!
    private var client: ShareClient!

    override func setUpWithError() throws {
        StubProtocol.seen = []
        StubProtocol.responses = []
        body = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try Data(#"{"text":"hi","attachments":[]}"#.utf8).write(to: body)
        client = ShareClient(
            origin: URL(string: "https://moa.example")!, credential: "dev.secret",
            session: ShareClient.makeSession(protocolClasses: [StubProtocol.self]))
    }

    override func tearDown() { try? FileManager.default.removeItem(at: body) }

    func testSendSuccess() async throws {
        StubProtocol.responses = [.success((200, "{}"))]
        try await client.send(to: "abc", bodyFile: body)
        XCTAssertEqual(StubProtocol.seen.count, 1)
        let seen = StubProtocol.seen[0]
        XCTAssertEqual(seen.method, "POST")
        XCTAssertEqual(seen.path, "/api/sessions/abc/send")
        XCTAssertEqual(seen.headers["Authorization"], "Moa-Device dev.secret")
        XCTAssertEqual(seen.headers["X-Moa-Request"], "1")
        XCTAssertEqual(String(decoding: seen.body, as: UTF8.self), #"{"text":"hi","attachments":[]}"#)
    }

    func testClosedConversationResumesThenSends() async throws {
        StubProtocol.responses = [.success((404, "")), .success((409, "")), .success((200, "{}"))]
        try await client.send(to: "abc", bodyFile: body)
        XCTAssertEqual(StubProtocol.seen.map(\.path), ["/api/sessions/abc/send", "/api/sessions/abc/resume", "/api/sessions/abc/send"])
        XCTAssertEqual(StubProtocol.seen[2].body, StubProtocol.seen[0].body)
    }

    func testStillNotFoundAfterResumeStops() async {
        StubProtocol.responses = [.success((404, "")), .success((200, "{}")), .success((404, ""))]
        do { try await client.send(to: "abc", bodyFile: body); XCTFail("expected error") }
        catch { XCTAssertEqual(error as? ShareError, .notFound) }
        XCTAssertEqual(StubProtocol.seen.count, 3)
    }

    func testUnauthorizedIsNotPairedAndNotRetried() async {
        StubProtocol.responses = [.success((401, ""))]
        do { try await client.send(to: "abc", bodyFile: body); XCTFail("expected error") }
        catch { XCTAssertEqual(error as? ShareError, .notPaired) }
        XCTAssertEqual(StubProtocol.seen.count, 1)
    }

    func testTimeoutAfterUploadIsAmbiguousAndNotRetried() async {
        StubProtocol.responses = [.failure(URLError(.timedOut))]
        do { try await client.send(to: "abc", bodyFile: body); XCTFail("expected error") }
        catch { XCTAssertEqual(error as? ShareError, .outcomeUnknown) }
        XCTAssertEqual(StubProtocol.seen.count, 1)
    }

    func testOfflineIsUnreachable() async {
        StubProtocol.responses = [.failure(URLError(.notConnectedToInternet))]
        do { try await client.send(to: "abc", bodyFile: body); XCTFail("expected error") }
        catch { XCTAssertEqual(error as? ShareError, .unreachable) }
    }

    func testExplicitRejectionsOfSendCarryTheirStatus() async {
        for status in [400, 413, 426] {
            StubProtocol.responses = [.success((status, ""))]
            do { try await client.send(to: "abc", bodyFile: body); XCTFail("expected error") }
            catch { XCTAssertEqual(error as? ShareError, .server(status)) }
        }
    }

    func testAnyOtherAnswerToSendIsUncertain() async {
        for status in [301, 302, 307, 402, 409, 422, 429, 500, 502, 503, 504] {
            StubProtocol.responses = [.success((status, ""))]
            do { try await client.send(to: "abc", bodyFile: body); XCTFail("expected error") }
            catch { XCTAssertEqual(error as? ShareError, .outcomeUnknown, "HTTP \(status)") }
            XCTAssertEqual(StubProtocol.seen.count, 1)
            StubProtocol.seen = []
        }
    }

    func testEveryConnectionEstablishmentFailureIsSafe() async {
        let codes: [URLError.Code] = [.cannotFindHost, .cannotConnectToHost, .dnsLookupFailed,
                                      .notConnectedToInternet, .secureConnectionFailed, .serverCertificateUntrusted]
        for code in codes {
            StubProtocol.responses = [.failure(URLError(code))]
            do { try await client.send(to: "abc", bodyFile: body); XCTFail("expected error") }
            catch { XCTAssertEqual(error as? ShareError, .unreachable, "\(code)") }
        }
    }

    func testOtherTransportErrorsAreUncertain() async {
        let errors: [Error] = [URLError(.networkConnectionLost), URLError(.cancelled), URLError(.fileDoesNotExist),
                               NSError(domain: "fixture", code: 1)]
        for error in errors {
            StubProtocol.responses = [.failure(error)]
            do { try await client.send(to: "abc", bodyFile: body); XCTFail("expected error") }
            catch let caught { XCTAssertEqual(caught as? ShareError, .outcomeUnknown, "\(error)") }
        }
    }

    func testResumeFailuresStaySafe() async {
        StubProtocol.responses = [.success((404, "")), .success((503, ""))]
        do { try await client.send(to: "abc", bodyFile: body); XCTFail("expected error") }
        catch { XCTAssertEqual(error as? ShareError, .server(503)) }
        XCTAssertEqual(StubProtocol.seen.count, 2)
    }

    func testDestinationsRequestAndDecode() async throws {
        StubProtocol.responses = [.success((200, #"{"destinations":[{"kind":"owner","session_id":"o1","title":"moa"}]}"#))]
        let list = try await client.destinations()
        XCTAssertEqual(list.map(\.sessionID), ["o1"])
        XCTAssertEqual(StubProtocol.seen[0].method, "GET")
        XCTAssertEqual(StubProtocol.seen[0].path, "/api/share/destinations")
        XCTAssertEqual(StubProtocol.seen[0].headers["Authorization"], "Moa-Device dev.secret")
    }

    func testDestinationsMapsFailures() async {
        StubProtocol.responses = [.success((401, ""))]
        do { _ = try await client.destinations(); XCTFail() } catch { XCTAssertEqual(error as? ShareError, .notPaired) }
        StubProtocol.responses = [.failure(URLError(.cannotConnectToHost))]
        do { _ = try await client.destinations(); XCTFail() } catch { XCTAssertEqual(error as? ShareError, .unreachable) }
    }

    func testOriginValidation() {
        XCTAssertNotNil(ShareClient.validatedOrigin("https://moa.example:8443"))
        XCTAssertNil(ShareClient.validatedOrigin("http://moa.example"))
        XCTAssertNil(ShareClient.validatedOrigin("https://u:p@moa.example"))
        XCTAssertNil(ShareClient.validatedOrigin("https://moa.example/x"))
        XCTAssertNil(ShareClient.validatedOrigin("https://moa.example?q=1"))
    }

    func testLostResumeIsNotAnUncertainSend() async {
        StubProtocol.responses = [.success((404, "")), .failure(URLError(.timedOut))]
        do { try await client.send(to: "abc", bodyFile: body); XCTFail("expected error") }
        catch { XCTAssertEqual(error as? ShareError, .unreachable) }
        XCTAssertEqual(StubProtocol.seen.map(\.path), ["/api/sessions/abc/send", "/api/sessions/abc/resume"])
    }
}
