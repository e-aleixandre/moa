import Foundation
import WebKit

private final class NativeAuthSessionDelegate: NSObject, URLSessionTaskDelegate {
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

enum NativeServerBinding {
    private static let key = "MoaShareServerOrigin"

    static var origin: String? {
        UserDefaults.standard.string(forKey: key)
    }

    static func bind(_ origin: String) {
        UserDefaults.standard.set(origin, forKey: key)
    }

    static func clear() {
        UserDefaults.standard.removeObject(forKey: key)
    }

    static func matches(_ securityOrigin: WKSecurityOrigin) -> Bool {
        origin == originString(securityOrigin)
    }

    static func originString(_ origin: WKSecurityOrigin) -> String {
        let defaultPort = (origin.protocol == "https" && origin.port == 443)
            || (origin.protocol == "http" && origin.port == 80)
        let port = origin.port > 0 && !defaultPort ? ":\(origin.port)" : ""
        let host = origin.host.contains(":") ? "[\(origin.host)]" : origin.host
        return "\(origin.protocol)://\(host)\(port)"
    }
}

final class DeviceAuthMessageHandler: NSObject, WKScriptMessageHandler {
    static let name = "moaNativeAuth"
    static let sessionCookieName = "__Host-moa_device"
    static let javaScript = #"""
    (() => {
      if (window.MoaNativeAuth) return;
      let nextId = 1;
      const calls = new Map();
      const call = (method, options = {}) => new Promise((resolve, reject) => {
        const id = String(nextId++);
        calls.set(id, { resolve, reject });
        window.webkit.messageHandlers.moaNativeAuth.postMessage({ id, method, options });
      });
      window.MoaNativeAuth = {
        claim: (origin, payload, deviceLabel) => call("claim", { origin, payload, deviceLabel }),
        authorize: (origin) => call("authorize", { origin }),
        reauthorize: () => call("reauthorize"),
        reset: () => call("reset"),
        __receive(message) {
          const pending = calls.get(message.id);
          if (!pending) return;
          calls.delete(message.id);
          if (message.ok) {
            pending.resolve(message.value);
            return;
          }
          const error = new Error(message.error || "Native authentication failed.");
          error.code = message.code || "unavailable";
          pending.reject(error);
        }
      };
    })();
    """#

    weak var webView: WKWebView?

    private let credentialStore: DeviceCredentialStore
    private let sessionDelegate = NativeAuthSessionDelegate()
    private lazy var session: URLSession = {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpCookieAcceptPolicy = .never
        configuration.httpShouldSetCookies = false
        configuration.requestCachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        configuration.timeoutIntervalForRequest = 15
        configuration.timeoutIntervalForResource = 20
        return URLSession(configuration: configuration, delegate: sessionDelegate, delegateQueue: nil)
    }()

    init(credentialStore: DeviceCredentialStore = DeviceCredentialStore()) {
        self.credentialStore = credentialStore
    }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard
            message.frameInfo.isMainFrame,
            let body = message.body as? [String: Any],
            let id = body["id"] as? String,
            let method = body["method"] as? String,
            let options = body["options"] as? [String: Any]
        else { return }

        switch method {
        case "claim":
            guard isLocal(message.frameInfo.securityOrigin) else {
                respond(id: id, error: .notAuthorized)
                return
            }
            Task {
                do {
                    let origin = try validatedOrigin(options["origin"] as? String)
                    try await claim(
                        origin: origin,
                        payload: options["payload"] as? String,
                        deviceLabel: options["deviceLabel"] as? String
                    )
                    respond(id: id, value: [:])
                } catch {
                    respond(id: id, error: bridgeError(error))
                }
            }
        case "authorize":
            guard isLocal(message.frameInfo.securityOrigin) else {
                respond(id: id, error: .notAuthorized)
                return
            }
            Task {
                do {
                    let origin = try validatedOrigin(options["origin"] as? String)
                    try await authorize(origin: origin)
                    respond(id: id, value: [:])
                } catch {
                    respond(id: id, error: bridgeError(error))
                }
            }
        case "reauthorize":
            guard NativeServerBinding.matches(message.frameInfo.securityOrigin) else {
                respond(id: id, error: .notAuthorized)
                return
            }
            Task {
                do {
                    guard let bound = NativeServerBinding.origin else { throw AuthBridgeError.notPaired }
                    try await authorize(origin: try validatedOrigin(bound))
                    respond(id: id, value: [:])
                } catch {
                    respond(id: id, error: bridgeError(error))
                }
            }
        case "reset":
            guard isLocal(message.frameInfo.securityOrigin) || NativeServerBinding.matches(message.frameInfo.securityOrigin) else {
                respond(id: id, error: .notAuthorized)
                return
            }
            Task {
                do {
                    try await reset()
                    respond(id: id, value: [:])
                } catch {
                    respond(id: id, error: bridgeError(error))
                }
            }
        default:
            respond(id: id, error: .unknownMethod)
        }
    }

    private func claim(origin: URL, payload: String?, deviceLabel: String?) async throws {
        guard
            let payload,
            let deviceLabel = deviceLabel?.trimmingCharacters(in: .whitespacesAndNewlines),
            !deviceLabel.isEmpty,
            deviceLabel.count <= 80
        else { throw AuthBridgeError.malformed }
        let parts = payload.split(separator: ":", omittingEmptySubsequences: false)
        guard parts.count == 3, parts[0] == "moa-pair-v1", !parts[1].isEmpty, !parts[2].isEmpty else {
            throw AuthBridgeError.malformed
        }

        var request = URLRequest(url: origin.appendingPathComponent("api/pulse/pairings/claim"))
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("1", forHTTPHeaderField: "X-Moa-Request")
        request.httpBody = try JSONSerialization.data(withJSONObject: [
            "pairing_id": String(parts[1]),
            "pairing_secret": String(parts[2]),
            "device_label": deviceLabel
        ])
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw AuthBridgeError.unavailable }
        guard http.statusCode == 201 else {
            if http.statusCode == 400 || http.statusCode == 401 || http.statusCode == 403 || http.statusCode == 429 {
                throw AuthBridgeError.claimRejected
            }
            throw AuthBridgeError.unavailable
        }

        let decoder = JSONDecoder()
        guard
            let result = try? decoder.decode(DeviceClaimResponse.self, from: data),
            let expiresAt = parseDate(result.expiresAt),
            !result.deviceID.isEmpty,
            result.credential.hasPrefix(result.deviceID + "."),
            expiresAt > Date()
        else { throw AuthBridgeError.malformedResponse }
        try credentialStore.save(StoredDeviceCredential(
            origin: normalizedOrigin(origin),
            credential: result.credential,
            expiresAt: expiresAt
        ))
    }

    private func authorize(origin: URL) async throws {
        guard
            let stored = try credentialStore.load(),
            stored.origin == normalizedOrigin(origin),
            stored.expiresAt > Date()
        else {
            throw AuthBridgeError.notPaired
        }

        var request = URLRequest(url: origin.appendingPathComponent("api/pulse/device-session"))
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("1", forHTTPHeaderField: "X-Moa-Request")
        request.setValue("Moa-Device \(stored.credential)", forHTTPHeaderField: "Authorization")
        request.httpBody = Data("{}".utf8)
        let (_, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw AuthBridgeError.unavailable }
        if http.statusCode == 401 || http.statusCode == 403 {
            try credentialStore.delete()
            NativeServerBinding.clear()
            await clearSessionCookie(origin: origin)
            throw AuthBridgeError.notPaired
        }
        guard http.statusCode == 204 else { throw AuthBridgeError.unavailable }
        try await installSessionCookie(from: http, origin: origin)
    }

    private func installSessionCookie(from response: HTTPURLResponse, origin: URL) async throws {
        let headers = response.allHeaderFields.reduce(into: [String: String]()) { fields, entry in
            guard let key = entry.key as? String else { return }
            fields[key] = String(describing: entry.value)
        }
        let host = origin.host?.lowercased()
        guard let cookie = HTTPCookie.cookies(withResponseHeaderFields: headers, for: origin).first(where: {
            $0.name == Self.sessionCookieName
                && $0.domain.trimmingCharacters(in: CharacterSet(charactersIn: ".")).lowercased() == host
                && $0.path == "/"
                && $0.isSecure
                && $0.isHTTPOnly
                && ($0.expiresDate?.timeIntervalSinceNow ?? 0) > 0
        }) else {
            throw AuthBridgeError.malformedResponse
        }
        guard let webView else { throw AuthBridgeError.unavailable }
        await withCheckedContinuation { continuation in
            webView.configuration.websiteDataStore.httpCookieStore.setCookie(cookie) {
                continuation.resume()
            }
        }
    }

    private func reset() async throws {
        let storedOrigin = try credentialStore.load()?.origin
        try credentialStore.delete()
        let origin = storedOrigin ?? NativeServerBinding.origin
        NativeServerBinding.clear()
        if let origin, let url = try? validatedOrigin(origin) {
            await clearSessionCookie(origin: url)
        }
    }

    private func clearSessionCookie(origin: URL) async {
        guard let webView else { return }
        let cookieStore = webView.configuration.websiteDataStore.httpCookieStore
        let cookies = await withCheckedContinuation { continuation in
            cookieStore.getAllCookies { continuation.resume(returning: $0) }
        }
        let host = origin.host?.lowercased()
        for cookie in cookies where cookie.name == Self.sessionCookieName
            && cookie.domain.trimmingCharacters(in: CharacterSet(charactersIn: ".")).lowercased() == host {
            await withCheckedContinuation { continuation in
                cookieStore.delete(cookie) { continuation.resume() }
            }
        }
    }

    private func validatedOrigin(_ raw: String?) throws -> URL {
        guard
            let raw,
            let components = URLComponents(string: raw),
            components.scheme == "https",
            components.host != nil,
            components.user == nil,
            components.password == nil,
            components.path.isEmpty || components.path == "/",
            components.query == nil,
            components.fragment == nil,
            let url = components.url
        else { throw AuthBridgeError.invalidOrigin }
        return url
    }

    private func parseDate(_ value: String) -> Date? {
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = fractional.date(from: value) { return date }
        return ISO8601DateFormatter().date(from: value)
    }

    private func normalizedOrigin(_ url: URL) -> String {
        url.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
    }

    private func isLocal(_ origin: WKSecurityOrigin) -> Bool {
        origin.protocol == "capacitor" && origin.host == "localhost"
    }

    private func bridgeError(_ error: Error) -> AuthBridgeError {
        if let error = error as? AuthBridgeError { return error }
        if error is DeviceCredentialStoreError { return .keychain }
        return .unavailable
    }

    private func respond(id: String, value: Any) {
        send(["id": id, "ok": true, "value": value])
    }

    private func respond(id: String, error: AuthBridgeError) {
        send(["id": id, "ok": false, "error": error.localizedDescription, "code": error.code])
    }

    private func send(_ message: [String: Any]) {
        guard
            JSONSerialization.isValidJSONObject(message),
            let data = try? JSONSerialization.data(withJSONObject: message),
            let json = String(data: data, encoding: .utf8)
        else { return }
        DispatchQueue.main.async { [weak self] in
            self?.webView?.evaluateJavaScript("window.MoaNativeAuth?.__receive(\(json));")
        }
    }

    private struct DeviceClaimResponse: Decodable {
        let deviceID: String
        let credential: String
        let expiresAt: String

        enum CodingKeys: String, CodingKey {
            case deviceID = "device_id"
            case credential
            case expiresAt = "expires_at"
        }
    }

    private enum AuthBridgeError: LocalizedError {
        case invalidOrigin
        case malformed
        case malformedResponse
        case claimRejected
        case notPaired
        case notAuthorized
        case keychain
        case unavailable
        case unknownMethod

        var code: String {
            switch self {
            case .claimRejected: return "claim_rejected"
            case .notPaired: return "not_paired"
            case .notAuthorized: return "not_authorized"
            case .keychain: return "keychain"
            case .unavailable: return "unavailable"
            case .invalidOrigin, .malformed, .malformedResponse: return "invalid"
            case .unknownMethod: return "unsupported"
            }
        }

        var errorDescription: String? {
            switch self {
            case .invalidOrigin: return "The paired server address is invalid."
            case .malformed: return "The pairing code is malformed."
            case .malformedResponse: return "The paired server returned an invalid response."
            case .claimRejected: return "The pairing code was rejected."
            case .notPaired: return "This device is no longer paired."
            case .notAuthorized: return "This page cannot use moa device authentication."
            case .keychain: return "The device credential could not be secured."
            case .unavailable: return "The paired server could not be reached."
            case .unknownMethod: return "The native authentication request is not supported."
            }
        }
    }
}
