import Foundation

enum ShareError: Error, Equatable {
    case notPaired
    case unreachable
    // The request may have reached the server: never retried automatically.
    case outcomeUnknown
    case notFound
    case server(Int)
    case invalidResponse
}

private final class NoRedirectDelegate: NSObject, URLSessionTaskDelegate {
    func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        willPerformHTTPRedirection response: HTTPURLResponse,
        newRequest request: URLRequest,
        completionHandler: @escaping (URLRequest?) -> Void
    ) {
        completionHandler(nil)
    }
}

// Talks to the paired moa server with the device credential, the same way
// DeviceAuthBridge does: ephemeral session, no cookies, redirects refused.
struct ShareClient {
    let origin: URL
    let credential: String
    let session: URLSession

    static func makeSession(
        protocolClasses: [AnyClass]? = nil,
        requestTimeout: TimeInterval = 30
    ) -> URLSession {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpCookieAcceptPolicy = .never
        configuration.httpShouldSetCookies = false
        configuration.requestCachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        configuration.timeoutIntervalForRequest = requestTimeout
        configuration.timeoutIntervalForResource = 300
        if let protocolClasses { configuration.protocolClasses = protocolClasses }
        return URLSession(configuration: configuration, delegate: NoRedirectDelegate(), delegateQueue: nil)
    }

    // Only the https origin the app stored with the credential is trusted.
    static func validatedOrigin(_ raw: String) -> URL? {
        guard
            let components = URLComponents(string: raw),
            components.scheme == "https",
            components.host != nil,
            components.user == nil,
            components.password == nil,
            components.path.isEmpty || components.path == "/",
            components.query == nil,
            components.fragment == nil
        else { return nil }
        return components.url
    }

    func destinations() async throws -> [ShareDestination] {
        var request = makeRequest("api/share/destinations", method: "GET")
        request.timeoutInterval = 15
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch {
            throw ShareError.unreachable
        }
        try check(response)
        return try ShareDestinations.decode(data)
    }

    // 404 means the conversation is closed: resume it (409 = already active)
    // and send once more. Nothing else is retried.
    func send(to sessionID: String, bodyFile: URL) async throws {
        do {
            try await post(sessionID, "send", bodyFile: bodyFile)
        } catch ShareError.notFound {
            do {
                try await post(sessionID, "resume", bodyFile: nil)
            } catch ShareError.server(409) {
            }
            try await post(sessionID, "send", bodyFile: bodyFile)
        }
    }

    private func post(_ sessionID: String, _ action: String, bodyFile: URL?) async throws {
        var request = makeRequest("api/sessions/\(sessionID)/\(action)", method: "POST")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let response: URLResponse
        do {
            if let bodyFile {
                (_, response) = try await session.upload(for: request, fromFile: bodyFile)
            } else {
                request.httpBody = Data("{}".utf8)
                (_, response) = try await session.data(for: request)
            }
        } catch let error as URLError {
            switch error.code {
            case .cannotFindHost, .cannotConnectToHost, .notConnectedToInternet, .dnsLookupFailed,
                 .secureConnectionFailed, .serverCertificateUntrusted:
                throw ShareError.unreachable
            default:
                throw ShareError.outcomeUnknown
            }
        }
        try check(response)
    }

    private func makeRequest(_ path: String, method: String) -> URLRequest {
        var request = URLRequest(url: origin.appendingPathComponent(path))
        request.httpMethod = method
        request.setValue("1", forHTTPHeaderField: "X-Moa-Request")
        request.setValue("Moa-Device \(credential)", forHTTPHeaderField: "Authorization")
        return request
    }

    private func check(_ response: URLResponse) throws {
        guard let http = response as? HTTPURLResponse else { throw ShareError.invalidResponse }
        switch http.statusCode {
        case 200..<300: return
        case 401, 403: throw ShareError.notPaired
        case 404: throw ShareError.notFound
        default: throw ShareError.server(http.statusCode)
        }
    }
}

// Read-only view of the credential the app stored. The extension never
// saves or deletes it: pairing and unpairing belong to the app.
enum ShareCredential {
    static func load() throws -> ShareClient? {
        guard
            let service = Bundle.main.object(forInfoDictionaryKey: "MoaDeviceAuthService") as? String,
            !service.isEmpty, !service.contains("$(")
        else { return nil }
        let store = DeviceCredentialStore(service: service, accessGroup: KeychainGroups.deviceCredential)
        guard
            let stored = try store.load(),
            stored.expiresAt > Date(),
            let origin = ShareClient.validatedOrigin(stored.origin)
        else { return nil }
        return ShareClient(origin: origin, credential: stored.credential, session: ShareClient.makeSession())
    }
}
