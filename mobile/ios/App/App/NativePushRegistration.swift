import CryptoKit
import Foundation
import UIKit
import UserNotifications
import WebKit

enum NativePushError: Error {
    case notPaired
    case denied
    case relayMismatch
    case unavailable
    case timeout
    case rejected
    case keychain
    case misconfigured
    case cancelled

    var code: String {
        switch self {
        case .notPaired: return "not_paired"
        case .denied: return "denied"
        case .relayMismatch: return "relay_mismatch"
        case .unavailable: return "unavailable"
        case .timeout: return "timeout"
        case .rejected: return "rejected"
        case .keychain: return "keychain"
        case .misconfigured: return "unsupported"
        case .cancelled: return "cancelled"
        }
    }
}

private final class NativePushSessionDelegate: NSObject, URLSessionTaskDelegate {
    // Neither the relay nor the paired server may move a request elsewhere:
    // both carry a capability (send key, device credential, push secret).
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

// Registers this device for native push (relay/PROTOCOL.md §1-2) and keeps
// the registration alive. Main-actor only: AppDelegate, the bridge and the
// notification delegate all hop here, so the state below needs no locks.
@MainActor
final class NativePushRegistration {
    static let shared = NativePushRegistration()

    private static let stateKey = "MoaNativePushState"
    private static let pendingDeleteKey = "MoaNativePushPendingDelete"
    // Set while a Keychain delete of S failed: S may still be readable by
    // the extension, so nothing may report push as off or reuse S.
    private static let secretDeletePendingKey = "MoaNativePushSecretDeletePending"
    private static let challengeWindow: TimeInterval = 300
    private static let tokenTimeout: TimeInterval = 20
    private static let refreshMargin: TimeInterval = 30 * 24 * 3600
    private static let refreshInterval: TimeInterval = 3600
    private static let maximumHandleLifetime: Int64 = 91 * 24 * 3600

    // Non-secret bookkeeping: which pairing push was enabled for, and what
    // the last successful registration used. S is only in PushSecretStore.
    private struct State: Codable {
        let origin: String
        var token: String?
        var env: String?
    }

    private struct PendingChallenge {
        let id: UUID
        let startedAt: Date
        let relay: String
        // The destination this registration is for. A confirmation is
        // accepted only for it (review I2): S, hence K_send, is reused on
        // renewal, so a challenge from an earlier register for another token
        // or env would otherwise verify too.
        let token: String
        let env: String
        let sendKey: SymmetricKey
        let continuation: CheckedContinuation<RelayHandle, Error>
    }

    private struct RelayHandle {
        let handle: String
        let expiresAt: Int64
        // The destination the relay issued the handle for.
        let token: String?
        let env: String?
    }

    private struct ServerStatus {
        let registered: Bool
        let relayURL: String?
        let expiresAt: Int64?
        let last: [String: Any]?
    }

    private struct HTTPResult {
        let data: Data
        let status: Int
    }

    private let credentialStore = DeviceCredentialStore()
    private let sessionDelegate = NativePushSessionDelegate()
    private lazy var session: URLSession = {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpCookieAcceptPolicy = .never
        configuration.httpShouldSetCookies = false
        configuration.requestCachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        configuration.timeoutIntervalForRequest = 15
        configuration.timeoutIntervalForResource = 20
        return URLSession(configuration: configuration, delegate: self.sessionDelegate, delegateQueue: nil)
    }()
    private var tokenWaiters: [CheckedContinuation<String, Error>] = []
    private var tokenRound = 0
    private var pending: PendingChallenge?
    // Orders every server/relay operation (POST and DELETE alike) and fences
    // the ones a disable, reset or pairing change made obsolete.
    private let operations = NativePushOperationQueue()
    private var lastRefresh: Date?
    private var started = false

    func start() {
        guard !started else { return }
        started = true
        NotificationCenter.default.addObserver(
            forName: UIApplication.didBecomeActiveNotification,
            object: nil,
            queue: .main
        ) { _ in
            Task { @MainActor in NativePushRegistration.shared.refreshIfNeeded() }
        }
    }

    // MARK: - Bridge entry points

    // A pending DELETE (offline disable) is kept until a new POST succeeds
    // in this same generation; failing here must not abandon it.
    func enable() async throws {
        let paired = try pairedCredential()
        let generation = operations.generation
        let granted: Bool
        do {
            granted = try await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound])
        } catch {
            throw NativePushError.unavailable
        }
        try ensureCurrent(generation, paired)
        guard granted else { throw NativePushError.denied }
        try await operations.enqueue {
            try await self.sync(paired: paired, generation: generation, allowNewSecret: true)
        }.value
    }

    // The local key goes first and unconditionally, so turning push off works
    // offline; the server is told best-effort and again on later launches.
    // The DELETE is queued behind any operation already running, so it always
    // lands after a POST that was in flight, and before any later enable.
    func disable() async throws {
        let paired = try? credentialStore.load()
        let removed = forgetLocal()
        if let paired {
            UserDefaults.standard.set(paired.origin, forKey: Self.pendingDeleteKey)
            _ = await operations.enqueue { await self.deleteServerRegistration(paired) }.result
        }
        guard removed else { throw NativePushError.keychain }
    }

    func status() async -> [String: Any] {
        let settings = await UNUserNotificationCenter.current().notificationSettings()
        var result: [String: Any] = [
            "enabled": false,
            "permission": Self.permissionName(settings.authorizationStatus)
        ]
        // Read first: an expired credential forgets here, and that delete
        // can fail too.
        let paired = try? pairedCredential()
        guard retrySecretDelete() else {
            // S may still be in the Keychain, so the extension may still
            // decrypt: never report that as off.
            result["enabled"] = true
            result["error"] = NativePushError.keychain.code
            return result
        }
        guard let paired, isEnabled(for: paired) else { return result }
        result["enabled"] = true
        if let status = try? await serverStatus(paired) {
            result["registered"] = status.registered
            if let last = status.last { result["last"] = last }
        }
        return result
    }

    // Deletes the push secret, local bookkeeping and every delivered or
    // pending notification. Called on disable, unpair, revocation, credential
    // expiry and any pairing change, with or without network. It also ends the
    // current generation: every operation started before it stops at its next
    // check, and waits for a token or a challenge end now.
    // Returns false if S could not be deleted; it stays marked and is retried.
    @discardableResult
    func forgetLocal() -> Bool {
        operations.invalidate()
        finishTokenWaiters(.failure(NativePushError.cancelled))
        finishPending(pending?.id, .failure(NativePushError.cancelled))
        let removed = deleteSecret()
        UserDefaults.standard.removeObject(forKey: Self.stateKey)
        lastRefresh = nil
        let center = UNUserNotificationCenter.current()
        center.removeAllDeliveredNotifications()
        center.removeAllPendingNotificationRequests()
        PushNavigator.shared.reset()
        return removed
    }

    // MARK: - AppDelegate entry points

    func didRegister(deviceToken: Data) {
        finishTokenWaiters(.success(deviceToken.map { String(format: "%02x", $0) }.joined()))
    }

    func didFailToRegister() {
        finishTokenWaiters(.failure(NativePushError.unavailable))
    }

    // Answers a challenge only for a registration this app started in the
    // last five minutes. A challenge caused by someone else's /v1/register
    // gets a proof under our K_send, which the relay rejects: we keep
    // waiting for our own until the window closes. A challenge from one of
    // our own earlier registrations for another token or env is confirmed by
    // the relay, but the destination it returns does not match the pending
    // one, so it is ignored and we keep waiting too (I2).
    // The pending one is cleared when its generation ends, so a challenge
    // never completes a registration that a disable or re-pair cancelled.
    func handleChallenge(_ userInfo: [AnyHashable: Any]) {
        guard
            let challenge = userInfo["r"] as? String,
            (1...2048).contains(challenge.utf8.count),
            PushEnvelope.base64URLDecode(challenge) != nil,
            let current = pending,
            Date().timeIntervalSince(current.startedAt) < Self.challengeWindow,
            let url = URL(string: current.relay + "/v1/confirm")
        else { return }
        let proof = PushEnvelope.confirmProof(challenge: challenge, sendKey: current.sendKey)
        Task { @MainActor in
            guard
                let response = try? await self.post(url, json: ["c": challenge, "p": proof]),
                response.status == 200,
                let handle = Self.parseHandle(response.data),
                RelayConfirmation.matches(handle.token, handle.env, token: current.token, env: current.env)
            else { return }
            self.finishPending(current.id, .success(handle))
        }
    }

    // MARK: - Registration

    private func refreshIfNeeded() {
        Task { @MainActor in
            self.retrySecretDelete()
            await self.retryPendingDelete()
            guard let paired = try? self.pairedCredential(), self.isEnabled(for: paired) else { return }
            if let last = self.lastRefresh, Date().timeIntervalSince(last) < Self.refreshInterval { return }
            self.lastRefresh = Date()
            let generation = self.operations.generation
            _ = await self.operations.enqueue {
                try await self.sync(paired: paired, generation: generation, allowNewSecret: false)
            }.result
        }
    }

    // Every await below is followed by ensureCurrent before S, state or a
    // registration is written: a generation that ended while this was
    // suspended must not save a key or reach a server.
    private func sync(paired: StoredDeviceCredential, generation: Int, allowNewSecret: Bool) async throws {
        try ensureCurrent(generation, paired)
        guard let relay = Self.relayURL else { throw NativePushError.misconfigured }
        let status = try await serverStatus(paired)
        try ensureCurrent(generation, paired)
        guard status.relayURL == relay else { throw NativePushError.relayMismatch }
        // A secret whose delete failed belongs to a disable or an old pairing.
        guard retrySecretDelete() else { throw NativePushError.keychain }

        let secret: Data
        var isNewSecret = false
        do {
            if let existing = try PushSecretStore.load() {
                secret = existing
            } else {
                guard allowNewSecret else { return }
                secret = SymmetricKey(size: .bits256).withUnsafeBytes { Data($0) }
                // Stored before anything is registered (review C3).
                try PushSecretStore.save(secret)
                isNewSecret = true
            }
        } catch {
            throw NativePushError.keychain
        }
        var state = isNewSecret ? State(origin: paired.origin) : (loadState() ?? State(origin: paired.origin))
        saveState(state)

        let token = try await deviceToken()
        try ensureCurrent(generation, paired)
        let env = Self.apsEnvironment
        let remaining = Double(status.expiresAt ?? 0) - Date().timeIntervalSince1970
        // The server reports the handle-expired signal in `result`; `reason`
        // is empty for it.
        let lastResult = status.last?["result"] as? String
        if !isNewSecret, status.registered, lastResult != "handle_expired",
           state.token == token, state.env == env, remaining > Self.refreshMargin {
            return
        }

        guard let keys = PushEnvelope.deriveKeys(secret: secret) else { throw NativePushError.keychain }
        let handle = try await relayHandle(relay: relay, token: token, env: env, keys: keys)
        try ensureCurrent(generation, paired)
        try await postServerRegistration(paired: paired, relay: relay, handle: handle, secret: secret, env: env)
        try ensureCurrent(generation, paired)
        state.token = token
        state.env = env
        saveState(state)
        // The new registration replaced whatever the pending DELETE targeted.
        if UserDefaults.standard.string(forKey: Self.pendingDeleteKey) == paired.origin {
            UserDefaults.standard.removeObject(forKey: Self.pendingDeleteKey)
        }
    }

    // Throws unless no forgetLocal ran since `generation` was read and the
    // stored credential is still the one the operation started with.
    private func ensureCurrent(_ generation: Int, _ paired: StoredDeviceCredential) throws {
        guard
            operations.isCurrent(generation),
            let stored = try? credentialStore.load(),
            stored.origin == paired.origin,
            stored.credential == paired.credential
        else { throw NativePushError.cancelled }
    }

    private func deviceToken() async throws -> String {
        try await withCheckedThrowingContinuation { continuation in
            tokenWaiters.append(continuation)
            guard tokenWaiters.count == 1 else { return }
            tokenRound += 1
            let round = tokenRound
            UIApplication.shared.registerForRemoteNotifications()
            Task { @MainActor in
                try? await Task.sleep(nanoseconds: UInt64(Self.tokenTimeout * 1_000_000_000))
                guard self.tokenRound == round else { return }
                self.finishTokenWaiters(.failure(NativePushError.timeout))
            }
        }
    }

    private func finishTokenWaiters(_ result: Result<String, Error>) {
        let waiters = tokenWaiters
        tokenWaiters = []
        tokenRound += 1
        waiters.forEach { $0.resume(with: result) }
    }

    private func relayHandle(relay: String, token: String, env: String, keys: PushEnvelope.Keys) async throws -> RelayHandle {
        guard let url = URL(string: relay + "/v1/register") else { throw NativePushError.misconfigured }
        let sendKey = PushEnvelope.base64URLEncode(keys.send.withUnsafeBytes { Data($0) })
        let id = UUID()
        return try await withCheckedThrowingContinuation { continuation in
            finishPending(pending?.id, .failure(NativePushError.timeout))
            // Armed before the request: the challenge may arrive before the
            // relay's 202 does.
            pending = PendingChallenge(
                id: id,
                startedAt: Date(),
                relay: relay,
                token: token,
                env: env,
                sendKey: keys.send,
                continuation: continuation
            )
            Task { @MainActor in
                do {
                    let response = try await self.post(url, json: ["token": token, "env": env, "send_key": sendKey])
                    if response.status != 202 {
                        self.finishPending(id, .failure(response.status == 429 ? NativePushError.unavailable : NativePushError.rejected))
                    }
                } catch {
                    self.finishPending(id, .failure(error))
                }
            }
            Task { @MainActor in
                try? await Task.sleep(nanoseconds: UInt64(Self.challengeWindow * 1_000_000_000))
                self.finishPending(id, .failure(NativePushError.timeout))
            }
        }
    }

    private func finishPending(_ id: UUID?, _ result: Result<RelayHandle, Error>) {
        guard let id, let current = pending, current.id == id else { return }
        pending = nil
        current.continuation.resume(with: result)
    }

    // MARK: - Paired server

    private func serverStatus(_ paired: StoredDeviceCredential) async throws -> ServerStatus {
        let response = try await server("GET", paired, path: "api/push/native/status", json: nil)
        switch response.status {
        case 200: break
        case 401, 403:
            forgetIfCurrent(paired)
            throw NativePushError.notPaired
        case 503: throw NativePushError.unavailable
        default: throw NativePushError.rejected
        }
        guard let object = try? JSONSerialization.jsonObject(with: response.data) as? [String: Any] else {
            throw NativePushError.rejected
        }
        var last: [String: Any]?
        if let raw = object["last"] as? [String: Any] {
            last = raw.filter { ["at", "result", "reason"].contains($0.key) && ($0.value is String || $0.value is NSNumber) }
        }
        return ServerStatus(
            registered: object["registered"] as? Bool ?? false,
            relayURL: object["relay_url"] as? String,
            expiresAt: (object["expires_at"] as? NSNumber)?.int64Value,
            last: last
        )
    }

    private func postServerRegistration(
        paired: StoredDeviceCredential,
        relay: String,
        handle: RelayHandle,
        secret: Data,
        env: String
    ) async throws {
        // Exactly the five fields the server accepts; it refuses unknown ones.
        let body: [String: Any] = [
            "relay_url": relay,
            "handle": handle.handle,
            "expires_at": handle.expiresAt,
            "secret": PushEnvelope.base64URLEncode(secret),
            "env": env
        ]
        let response = try await server("POST", paired, path: "api/push/native", json: body)
        switch response.status {
        case 200..<300:
            return
        case 400:
            let object = try? JSONSerialization.jsonObject(with: response.data) as? [String: Any]
            let code = object?["error"] as? String
            throw code == "relay_mismatch" ? NativePushError.relayMismatch : NativePushError.rejected
        case 401, 403:
            forgetIfCurrent(paired)
            throw NativePushError.notPaired
        case 503:
            throw NativePushError.unavailable
        default:
            throw NativePushError.rejected
        }
    }

    // The server said this credential is revoked or expired (review I4).
    // Only if it is still the stored one: a late answer for a previous
    // pairing must not wipe the current one. 503 and network errors never
    // get here.
    private func forgetIfCurrent(_ paired: StoredDeviceCredential) {
        guard (try? credentialStore.load())?.credential == paired.credential else { return }
        forgetLocal()
    }

    private func deleteServerRegistration(_ paired: StoredDeviceCredential) async {
        guard let response = try? await server("DELETE", paired, path: "api/push/native", json: nil) else { return }
        if (200..<300).contains(response.status) || response.status == 404 {
            UserDefaults.standard.removeObject(forKey: Self.pendingDeleteKey)
        }
    }

    // The mark is cleared only by a DELETE that reached the server or by a
    // later successful POST (sync), never by enabling again locally.
    private func retryPendingDelete() async {
        guard let origin = UserDefaults.standard.string(forKey: Self.pendingDeleteKey) else { return }
        guard let paired = try? credentialStore.load(), paired.origin == origin else {
            // Unpaired or paired elsewhere: there is nothing this credential
            // should delete.
            UserDefaults.standard.removeObject(forKey: Self.pendingDeleteKey)
            return
        }
        _ = await operations.enqueue {
            // A POST queued ahead of this one may have replaced it already.
            guard UserDefaults.standard.string(forKey: Self.pendingDeleteKey) == origin else { return }
            await self.deleteServerRegistration(paired)
        }.result
    }

    // Native header auth only: the ephemeral session never carries the
    // WebView cookie, so the server cannot fall back to it.
    private func server(_ method: String, _ paired: StoredDeviceCredential, path: String, json: [String: Any]?) async throws -> HTTPResult {
        guard let origin = Self.httpsOrigin(paired.origin) else { throw NativePushError.notPaired }
        var request = URLRequest(url: origin.appendingPathComponent(path))
        request.httpMethod = method
        request.setValue("Moa-Device \(paired.credential)", forHTTPHeaderField: "Authorization")
        if method != "GET" {
            request.setValue("1", forHTTPHeaderField: "X-Moa-Request")
        }
        if let json {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONSerialization.data(withJSONObject: json)
        }
        return try await send(request)
    }

    private func post(_ url: URL, json: [String: Any]) async throws -> HTTPResult {
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: json)
        return try await send(request)
    }

    private func send(_ request: URLRequest) async throws -> HTTPResult {
        do {
            let (data, response) = try await session.data(for: request)
            guard let http = response as? HTTPURLResponse else { throw NativePushError.unavailable }
            return HTTPResult(data: data, status: http.statusCode)
        } catch let error as NativePushError {
            throw error
        } catch {
            throw NativePushError.unavailable
        }
    }

    // MARK: - Local state

    private func pairedCredential() throws -> StoredDeviceCredential {
        let stored: StoredDeviceCredential?
        do {
            stored = try credentialStore.load()
        } catch {
            throw NativePushError.keychain
        }
        guard let stored, Self.httpsOrigin(stored.origin) != nil else {
            throw NativePushError.notPaired
        }
        guard stored.expiresAt > Date() else {
            // A credential known to be expired ends push here too (review I4).
            if hasLocalPushData() { forgetLocal() }
            throw NativePushError.notPaired
        }
        // A different pairing never inherits the previous S (review C3).
        if let state = loadState(), state.origin != stored.origin {
            forgetLocal()
        }
        return stored
    }

    private func isEnabled(for paired: StoredDeviceCredential) -> Bool {
        guard loadState()?.origin == paired.origin else { return false }
        return (try? PushSecretStore.load()) != nil
    }

    // A Keychain read error counts as data: forgetting is the safe side.
    private func hasLocalPushData() -> Bool {
        if loadState() != nil { return true }
        do {
            return try PushSecretStore.load() != nil
        } catch {
            return true
        }
    }

    // Deletes S. On failure the secret is marked and stays a hard stop for
    // registering, and status reports it, until a retry succeeds (review I5).
    private func deleteSecret() -> Bool {
        do {
            try PushSecretStore.delete()
        } catch {
            UserDefaults.standard.set(true, forKey: Self.secretDeletePendingKey)
            return false
        }
        UserDefaults.standard.removeObject(forKey: Self.secretDeletePendingKey)
        return true
    }

    // Returns false while a secret that should be gone may still be readable.
    @discardableResult
    private func retrySecretDelete() -> Bool {
        guard UserDefaults.standard.bool(forKey: Self.secretDeletePendingKey) else { return true }
        return deleteSecret()
    }

    private func loadState() -> State? {
        guard let data = UserDefaults.standard.data(forKey: Self.stateKey) else { return nil }
        return try? JSONDecoder().decode(State.self, from: data)
    }

    private func saveState(_ state: State) {
        if let data = try? JSONEncoder().encode(state) {
            UserDefaults.standard.set(data, forKey: Self.stateKey)
        }
    }

    // MARK: - Helpers

    static var relayURL: String? {
        guard
            let raw = Bundle.main.object(forInfoDictionaryKey: "MoaPushRelayURL") as? String,
            let components = URLComponents(string: raw),
            components.scheme == "https",
            components.host?.isEmpty == false,
            components.user == nil,
            components.password == nil,
            components.path.isEmpty,
            components.query == nil,
            components.fragment == nil
        else { return nil }
        return raw
    }

    // Xcode-installed builds carry a development profile and talk to the
    // APNs sandbox; TestFlight and App Store builds carry no development
    // profile and use production.
    static var apsEnvironment: String {
        guard
            let url = Bundle.main.url(forResource: "embedded", withExtension: "mobileprovision"),
            let data = try? Data(contentsOf: url),
            let text = String(data: data, encoding: .isoLatin1),
            text.range(
                of: "<key>aps-environment</key>\\s*<string>development</string>",
                options: .regularExpression
            ) != nil
        else { return "production" }
        return "sandbox"
    }

    private static func httpsOrigin(_ raw: String) -> URL? {
        guard
            let components = URLComponents(string: raw),
            components.scheme == "https",
            components.host?.isEmpty == false,
            components.user == nil,
            components.password == nil,
            components.path.isEmpty || components.path == "/",
            components.query == nil,
            components.fragment == nil
        else { return nil }
        return components.url
    }

    private static func parseHandle(_ data: Data) -> RelayHandle? {
        guard
            let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
            let handle = object["handle"] as? String,
            (40...1024).contains(handle.utf8.count),
            handle.utf8.allSatisfy({ byte in
                (byte >= 0x30 && byte <= 0x39) || (byte >= 0x41 && byte <= 0x5A)
                    || (byte >= 0x61 && byte <= 0x7A) || byte == 0x2D || byte == 0x5F
            }),
            let expiresAt = (object["expires_at"] as? NSNumber)?.int64Value
        else { return nil }
        let now = Int64(Date().timeIntervalSince1970)
        guard expiresAt > now, expiresAt <= now + maximumHandleLifetime else { return nil }
        return RelayHandle(handle: handle, expiresAt: expiresAt, token: object["token"] as? String, env: object["env"] as? String)
    }

    private static func permissionName(_ status: UNAuthorizationStatus) -> String {
        switch status {
        case .authorized: return "granted"
        case .denied: return "denied"
        case .notDetermined: return "not_determined"
        case .provisional: return "provisional"
        case .ephemeral: return "ephemeral"
        @unknown default: return "unknown"
        }
    }
}

// Decides what a delivered push does in the app. Destinations always come
// from re-authenticating `e` with the current key, never from fields the
// relay could set next to it (review M2).
@MainActor
final class PushNavigator {
    static let shared = PushNavigator()

    weak var webView: WKWebView?
    private var pending: (origin: String, url: URL)?
    private var visibleSession: String?

    func setVisibleSession(_ id: String?) {
        visibleSession = id.flatMap { PushEnvelope.isSessionID($0) ? $0 : nil }
    }

    func presentationOptions(for userInfo: [AnyHashable: Any]) -> UNNotificationPresentationOptions {
        if Self.isChallenge(userInfo) {
            NativePushRegistration.shared.handleChallenge(userInfo)
            return []
        }
        if case .session(let id)? = authenticatedContent(userInfo, now: Date())?.destination, id == visibleSession {
            return []
        }
        return [.banner, .list, .sound]
    }

    func handleTap(_ userInfo: [AnyHashable: Any]) {
        if Self.isChallenge(userInfo) {
            NativePushRegistration.shared.handleChallenge(userInfo)
            return
        }
        // Expiry is checked again at tap time: an envelope that is no longer
        // valid leads home, like the fallback it was shown as.
        open(authenticatedContent(userInfo, now: Date())?.destination ?? .home)
    }

    // A cold-start tap is held until the paired page has loaded, which the
    // local page only allows after `authorize` succeeded.
    func navigationFinished(at url: URL) {
        guard
            let next = pending,
            NativeServerBinding.origin == next.origin,
            NativeServerBinding.matches(url),
            let webView
        else { return }
        pending = nil
        webView.load(URLRequest(url: next.url))
    }

    func reset() {
        pending = nil
        visibleSession = nil
    }

    private func open(_ destination: PushEnvelope.Destination) {
        guard
            let rawOrigin = NativeServerBinding.origin,
            let origin = URL(string: rawOrigin),
            let url = destination.url(origin: origin),
            NativeServerBinding.matches(url)
        else { return }
        guard let webView, !webView.isLoading, let current = webView.url, NativeServerBinding.matches(current) else {
            // Not showing the paired page yet (cold start, pairing, loading):
            // hold the deep link until it has loaded.
            pending = (rawOrigin, url)
            return
        }
        // The paired page is loaded: open the destination in place so nothing
        // being typed is lost. Reload with the deep link only if the page
        // cannot (an older frontend, a session it does not know).
        Task { @MainActor in
            if await Self.openInPlace(destination, in: webView) { return }
            guard let current = webView.url, NativeServerBinding.matches(current) else {
                self.pending = (rawOrigin, url)
                return
            }
            webView.load(URLRequest(url: url))
        }
    }

    // Calls window.MoaNavigation in the page's own world. The destination is
    // passed as an argument, never spliced into the script.
    private static func openInPlace(_ destination: PushEnvelope.Destination, in webView: WKWebView) async -> Bool {
        let script: String
        var arguments: [String: Any] = [:]
        switch destination {
        case .session(let id):
            script = "const nav = window.MoaNavigation; return !!(nav && nav.version >= 1 && await nav.openSession(id));"
            arguments["id"] = id
        case .inbox:
            script = "const nav = window.MoaNavigation; return !!(nav && nav.version >= 1 && nav.openInbox());"
        case .home:
            return true // the paired page is already on screen
        }
        do {
            let result = try await webView.callAsyncJavaScript(script, arguments: arguments, in: nil, contentWorld: .page)
            return (result as? Bool) == true
        } catch {
            return false
        }
    }

    private func authenticatedContent(_ userInfo: [AnyHashable: Any], now: Date) -> PushEnvelope.Content? {
        guard
            let secret = try? PushSecretStore.load(),
            let keys = PushEnvelope.deriveKeys(secret: secret)
        else { return nil }
        return PushEnvelope.open(userInfo["e"], keys: keys, now: now)
    }

    private static func isChallenge(_ userInfo: [AnyHashable: Any]) -> Bool {
        userInfo["r"] is String && userInfo["e"] == nil
    }
}
