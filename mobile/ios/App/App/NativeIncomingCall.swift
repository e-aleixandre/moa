import AVFoundation
import CallKit
import CryptoKit
import Foundation
import PushKit
import UIKit
import os

private let incomingLog = Logger(subsystem: "com.ealeixandre.moa.app", category: "incoming-call")

// The incoming-call destination's secret S_v and what it belongs to. It is
// read while the iPhone is locked (after the first unlock since boot), so it
// lives in its own item, in the app's private access group (never the
// extension's), AfterFirstUnlockThisDeviceOnly. The device credential stays
// WhenUnlocked: S_v yields only the narrow Moa-Voice key.
struct IncomingCallSecret: Codable, Equatable {
    let origin: String
    let deviceID: String
    let secret: Data
}

enum IncomingCallSecretStore {
    private static let service = (Bundle.main.bundleIdentifier ?? "moa") + ".voip"
    private static let account = "voip-secret-v1"

    static func load() -> IncomingCallSecret? {
        guard var query = baseQuery() else { return nil }
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess, let data = result as? Data else { return nil }
        guard let stored = try? JSONDecoder().decode(IncomingCallSecret.self, from: data), stored.secret.count == 32 else { return nil }
        return stored
    }

    static func save(_ stored: IncomingCallSecret) -> Bool {
        guard var item = baseQuery(), let data = try? JSONEncoder().encode(stored) else { return false }
        delete()
        item[kSecValueData as String] = data
        item[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        return SecItemAdd(item as CFDictionary, nil) == errSecSuccess
    }

    @discardableResult
    static func delete() -> Bool {
        guard let query = baseQuery() else { return false }
        let status = SecItemDelete(query as CFDictionary)
        return status == errSecSuccess || status == errSecItemNotFound
    }

    private static func baseQuery() -> [String: Any]? {
        guard let group = KeychainGroups.deviceCredential else { return nil }
        return [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrAccessGroup as String: group,
            kSecAttrSynchronizable as String: kCFBooleanFalse as Any
        ]
    }
}

// IncomingCallAuth is the one place that decides how a locked iPhone
// authenticates to answer: "Moa-Voice <device>.<HKDF(S_v)>", which the server
// accepts only for following, answering and declining invitations and for
// the call's own socket and hangup. Swapping the mechanism (e.g. for the full
// device credential, if the owner prefers) changes only this.
enum IncomingCallAuth {
    static func header(_ stored: IncomingCallSecret) -> String {
        let key = PushEnvelope.derive(secret: stored.secret, label: "moa-voice-auth-v1", length: 32)
        return "Moa-Voice \(stored.deviceID).\(PushEnvelope.base64URLEncode(key))"
    }
}

private final class IncomingSessionDelegate: NSObject, URLSessionTaskDelegate {
    // The relay and the paired server may not move a request elsewhere.
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

// Incoming calls: PushKit VoIP pushes that ring as CallKit calls even with
// the app closed and the iPhone locked. Everything runs on the main queue
// (PushKit, CallKit and the URLSession below deliver there).
//
// - Registration: in the foreground, the app registers its VoIP token with
//   the relay (type voip; the challenge is itself a VoIP push) and then with
//   its moa server (/api/push/voip, device credential).
// - A push: decrypted locally and reported to CallKit at once, before any
//   network; the invitation is then followed on the server, which is how a
//   call answered elsewhere, declined or cancelled stops ringing.
// - Answer: POST answer with Moa-Voice, then the call runs exactly like an
//   outgoing one in NativeVoiceCallManager (engine starts on didActivate).
final class NativeIncomingCalls: NSObject, PKPushRegistryDelegate {
    static let shared = NativeIncomingCalls()

    private struct Ringing {
        let uuid: UUID
        let invite: String
        let name: String
        let sessionId: String
        let stored: IncomingCallSecret
        let expires: Date
        var answering = false
    }

    private struct Pending {
        let token: String
        let env: String
        let relay: String
        let sendKey: SymmetricKey
        let startedAt: Date
        let done: ((handle: String, expiresAt: Int64)?) -> Void // nil: no handle
    }

    private static let stateKey = "MoaVoIPState" // last registered token/origin, not secret
    private static let refreshInterval: TimeInterval = 3600
    private static let refreshMargin: Int64 = 30 * 24 * 3600
    private static let challengeWindow: TimeInterval = 300

    private var registry: PKPushRegistry?
    private var token: String?
    private var ringing: Ringing?
    private var pending: Pending?
    private var registering = false
    private var lastRefresh: Date?
    private let delegate = IncomingSessionDelegate()
    private lazy var session: URLSession = {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpCookieAcceptPolicy = .never
        configuration.httpShouldSetCookies = false
        configuration.timeoutIntervalForRequest = 15
        return URLSession(configuration: configuration, delegate: delegate, delegateQueue: .main)
    }()

    // Called from didFinishLaunching: a VoIP push that launched the app is
    // delivered as soon as the registry exists, so it must exist before
    // anything else waits on the page.
    func start() {
        guard registry == nil else { return }
        _ = NativeVoiceCallManager.shared // the CXProvider, before any push
        let registry = PKPushRegistry(queue: .main)
        registry.delegate = self
        registry.desiredPushTypes = [.voIP]
        self.registry = registry
        NotificationCenter.default.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { [weak self] _ in
            self?.refreshRegistration(force: false)
        }
    }

    // Unpairing, a re-pair, a revoked credential: the secret goes, so this
    // device cannot decrypt invitations or answer any more. A ringing
    // invitation goes with it: its copy of the secret is never used again,
    // and an answer in flight finds it gone (see answer).
    func forgetLocal() {
        if let call = ringing { endRinging(call, reason: .failed) }
        IncomingCallSecretStore.delete()
        UserDefaults.standard.removeObject(forKey: Self.stateKey)
        pending?.done(nil)
        pending = nil
        lastRefresh = nil
    }

    // MARK: PKPushRegistryDelegate

    func pushRegistry(_ registry: PKPushRegistry, didUpdate pushCredentials: PKPushCredentials, for type: PKPushType) {
        guard type == .voIP else { return }
        let hex = pushCredentials.token.map { String(format: "%02x", $0) }.joined()
        let changed = hex != token
        token = hex
        if changed { refreshRegistration(force: true) }
    }

    func pushRegistry(_ registry: PKPushRegistry, didInvalidatePushTokenFor type: PKPushType) {
        guard type == .voIP else { return }
        token = nil
    }

    // Before iOS 26.4 every VoIP push must be reported to CallKit.
    func pushRegistry(_ registry: PKPushRegistry, didReceiveIncomingPushWith payload: PKPushPayload, for type: PKPushType,
                      completion: @escaping () -> Void) {
        receive(payload.dictionaryPayload, mustReport: true, completion: completion)
    }

    // From iOS 26.4 the system says when a report is required (not in the
    // foreground, during another call or for a very late push).
    @available(iOS 26.4, *)
    func pushRegistry(_ registry: PKPushRegistry, didReceiveIncomingVoIPPushWith payload: PKPushPayload, metadata: PKVoIPPushMetadata,
                      withCompletionHandler completion: @escaping () -> Void) {
        receive(payload.dictionaryPayload, mustReport: metadata.mustReport, completion: completion)
    }

    // MARK: Receiving

    private func receive(_ payload: [AnyHashable: Any], mustReport: Bool, completion: @escaping () -> Void) {
        if let challenge = payload["r"] as? String, payload["e"] == nil {
            handleChallenge(challenge)
            // A registration challenge is not a call; when the system still
            // demands a report, a call is reported and ended at once.
            if mustReport { NativeVoiceCallManager.shared.reportFailedIncoming(name: "moa", completion: completion) } else { completion() }
            return
        }
        guard
            let stored = IncomingCallSecretStore.load(),
            let keys = PushEnvelope.deriveKeys(secret: stored.secret),
            let content = PushEnvelope.open(payload["e"], keys: keys, now: Date()),
            content.kind == "call",
            let invite = content.invite, let uuid = UUID(uuidString: invite),
            case .session(let sessionId) = content.destination
        else {
            incomingLog.error("incoming push could not be opened")
            if mustReport { NativeVoiceCallManager.shared.reportFailedIncoming(name: "moa", completion: completion) } else { completion() }
            return
        }
        if ringing?.uuid == uuid {
            // The same invitation twice (APNs may repeat): reporting the same
            // UUID again is refused by CallKit and leaves the ringing call be.
            NativeVoiceCallManager.shared.reportIncoming(uuid: uuid, name: content.title) { _ in completion() }
            return
        }
        let name = content.title.isEmpty ? "moa" : String(content.title.prefix(80))
        let call = Ringing(uuid: uuid, invite: invite, name: name, sessionId: sessionId, stored: stored,
                           expires: content.expires ?? Date().addingTimeInterval(45))
        NativeVoiceCallManager.shared.reportIncoming(uuid: uuid, name: name) { [weak self] error in
            defer { completion() }
            guard let self else { return }
            if let error {
                // Focus, another call, or a duplicate: the system declined to
                // ring. Nothing to follow.
                incomingLog.error("incoming call not shown: \(NativeVoiceCallManager.code(error), privacy: .public)")
                return
            }
            self.ringing = call
            self.follow(call)
        }
    }

    // follow is the signalling channel while it rings: a long poll on the
    // invitation. Anything but "ringing" ends the ringing call with the
    // matching reason; failing to reach the server only ends it at expiry.
    private func follow(_ call: Ringing) {
        guard let current = ringing, current.uuid == call.uuid, !current.answering else { return }
        if Date() >= call.expires {
            endRinging(call, reason: .unanswered)
            return
        }
        guard let url = URL(string: call.stored.origin + "/api/voice/invites/" + call.invite + "?wait=ringing") else { return }
        var request = URLRequest(url: url)
        request.timeoutInterval = 30
        request.setValue(IncomingCallAuth.header(call.stored), forHTTPHeaderField: "Authorization")
        session.dataTask(with: request) { [weak self] data, response, _ in
            guard let self, let current = self.ringing, current.uuid == call.uuid, !current.answering else { return }
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            let object = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
            let state = object?["state"] as? String
            switch (status, state) {
            case (200, "ringing"?):
                self.follow(call)
            case (200, "answering"?), (200, "answered"?), (200, "failed"?):
                self.endRinging(call, reason: .answeredElsewhere)
            case (200, "declined"?):
                self.endRinging(call, reason: .declinedElsewhere)
            case (200, _), (404, _):
                self.endRinging(call, reason: .remoteEnded)
            case (401, _), (403, _):
                self.endRinging(call, reason: .failed)
            default:
                DispatchQueue.main.asyncAfter(deadline: .now() + 2) { self.follow(call) }
            }
        }.resume()
        // Ends at expiry even if the server never answers.
        let left = call.expires.timeIntervalSinceNow + 2
        DispatchQueue.main.asyncAfter(deadline: .now() + max(0, left)) { [weak self] in
            guard let self, let current = self.ringing, current.uuid == call.uuid, !current.answering else { return }
            self.endRinging(call, reason: .unanswered)
        }
    }

    private func endRinging(_ call: Ringing, reason: CXCallEndedReason) {
        guard ringing?.uuid == call.uuid else { return }
        ringing = nil
        NativeVoiceCallManager.shared.provider.reportCall(with: call.uuid, endedAt: Date(), reason: reason)
    }

    // MARK: CallKit actions (from NativeVoiceCallManager)

    // answer returns false when the action is not about a ringing invitation.
    func answer(_ action: CXAnswerCallAction) -> Bool {
        guard var call = ringing, call.uuid == action.callUUID else { return false }
        guard !call.answering else {
            action.fail()
            return true
        }
        call.answering = true
        ringing = call
        NativeVoiceCallManager.configureSessionForCall()
        post(call, "invites/" + call.invite + "/answer") { [weak self] status, object in
            guard let self, self.ringing?.uuid == call.uuid else {
                // Ended or forgotten while the answer was in flight: the
                // call the server opened for it is hung up, not joined.
                action.fail()
                if status == 201, let id = object?["call_id"] as? String, PushEnvelope.base64URLDecode(id) != nil {
                    self?.post(call, "calls/" + id + "/hangup") { _, _ in }
                }
                return
            }
            self.ringing = nil
            guard
                status == 201,
                let wsPath = object?["ws_url"] as? String,
                let url = URL(string: call.stored.origin.replacingOccurrences(of: "https://", with: "wss://") + wsPath),
                NativeVoiceCallManager.shared.startIncoming(uuid: call.uuid, name: call.name, sessionId: call.sessionId,
                                                            origin: call.stored.origin, url: url,
                                                            authorization: IncomingCallAuth.header(call.stored))
            else {
                incomingLog.error("incoming answer refused: \(status, privacy: .public) \((object?["error"] as? String) ?? "", privacy: .public)")
                action.fail()
                return
            }
            action.fulfill()
        }
        return true
    }

    // decline returns false when the action is not about a ringing invitation.
    // While its answer is in flight, End only drops it: the answer's reply
    // then hangs up whatever call it got.
    func decline(_ action: CXEndCallAction) -> Bool {
        guard let call = ringing, call.uuid == action.callUUID else { return false }
        ringing = nil
        action.fulfill()
        if !call.answering { post(call, "invites/" + call.invite + "/decline") { _, _ in } }
        return true
    }

    func reset() {
        ringing = nil
    }

    // post sends to /api/voice/<path> with the invitation's Moa-Voice key.
    private func post(_ call: Ringing, _ path: String, done: @escaping (Int, [String: Any]?) -> Void) {
        guard let url = URL(string: call.stored.origin + "/api/voice/" + path) else {
            done(0, nil)
            return
        }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue(IncomingCallAuth.header(call.stored), forHTTPHeaderField: "Authorization")
        request.setValue("1", forHTTPHeaderField: "X-Moa-Request")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = Data("{}".utf8)
        session.dataTask(with: request) { data, response, _ in
            let object = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
            done((response as? HTTPURLResponse)?.statusCode ?? 0, object)
        }.resume()
    }

    // MARK: Registration

    private struct State: Codable {
        let origin: String
        let token: String
        let env: String
    }

    // refreshRegistration keeps this device's VoIP destination registered,
    // only in the foreground (the challenge must not ring a locked phone)
    // and only for the paired server.
    private func refreshRegistration(force: Bool) {
        guard
            UIApplication.shared.applicationState == .active,
            !registering,
            let token,
            let paired = try? DeviceCredentialStore().load(),
            paired.expiresAt > Date(),
            paired.origin.hasPrefix("https://"),
            let deviceID = paired.credential.split(separator: ".").first.map(String.init)
        else { return }
        if !force, let last = lastRefresh, Date().timeIntervalSince(last) < Self.refreshInterval { return }
        lastRefresh = Date()
        registering = true
        let env = NativePushRegistration.apsEnvironment
        server("GET", paired, "api/push/voip/status", nil) { [weak self] status, object in
            guard let self else { return }
            guard status == 200, let object else {
                // An older server without incoming calls (404), or offline.
                self.registering = false
                return
            }
            var stored = IncomingCallSecretStore.load()
            if stored?.origin != paired.origin || stored?.deviceID != deviceID { stored = nil }
            let state = self.loadState()
            let remaining = ((object["expires_at"] as? NSNumber)?.int64Value ?? 0) - Int64(Date().timeIntervalSince1970)
            if let stored, object["registered"] as? Bool == true, (object["last"] as? [String: Any])?["result"] as? String != "handle_expired",
               state?.origin == stored.origin, state?.token == token, state?.env == env, remaining > Self.refreshMargin {
                self.registering = false
                return
            }
            guard case .relay(let relay) = NativePushRegistration.relayAllowlist.select(reported: object["relay_url"] as? String) else {
                self.registering = false
                return
            }
            let secret = stored ?? IncomingCallSecret(origin: paired.origin, deviceID: deviceID,
                                                      secret: SymmetricKey(size: .bits256).withUnsafeBytes { Data($0) })
            // Kept before anything is registered with it.
            guard stored != nil || IncomingCallSecretStore.save(secret) else {
                self.registering = false
                return
            }
            self.register(secret: secret, paired: paired, relay: relay, token: token, env: env)
        }
    }

    private func register(secret: IncomingCallSecret, paired: StoredDeviceCredential, relay: String, token: String, env: String) {
        guard let keys = PushEnvelope.deriveKeys(secret: secret.secret), let url = URL(string: relay + "/v1/register") else {
            registering = false
            return
        }
        pending?.done(nil)
        pending = Pending(token: token, env: env, relay: relay, sendKey: keys.send, startedAt: Date()) { [weak self] handle in
            guard let self else { return }
            self.pending = nil
            guard let handle else {
                self.registering = false
                return
            }
            let body: [String: Any] = [
                "relay_url": relay, "handle": handle.handle, "expires_at": handle.expiresAt,
                "secret": PushEnvelope.base64URLEncode(secret.secret), "env": env
            ]
            self.server("POST", paired, "api/push/voip", body) { status, _ in
                self.registering = false
                if (200..<300).contains(status) {
                    self.saveState(State(origin: secret.origin, token: token, env: env))
                    incomingLog.info("incoming calls registered")
                } else {
                    incomingLog.error("incoming calls registration refused: \(status, privacy: .public)")
                }
            }
        }
        let startedAt = pending?.startedAt
        let sendKey = PushEnvelope.base64URLEncode(keys.send.withUnsafeBytes { Data($0) })
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try? JSONSerialization.data(withJSONObject: ["token": token, "env": env, "send_key": sendKey, "type": "voip"])
        session.dataTask(with: request) { [weak self] _, response, _ in
            guard let self, (response as? HTTPURLResponse)?.statusCode != 202,
                  let current = self.pending, current.startedAt == startedAt else { return }
            current.done(nil)
        }.resume()
        DispatchQueue.main.asyncAfter(deadline: .now() + Self.challengeWindow) { [weak self] in
            guard let self, let current = self.pending, current.startedAt == startedAt else { return }
            current.done(nil)
        }
    }

    // Answers only a challenge for the registration this app started, and
    // accepts the handle only for that token, env and type.
    private func handleChallenge(_ challenge: String) {
        guard
            let current = pending,
            Date().timeIntervalSince(current.startedAt) < Self.challengeWindow,
            (1...2048).contains(challenge.utf8.count),
            PushEnvelope.base64URLDecode(challenge) != nil,
            let url = URL(string: current.relay + "/v1/confirm")
        else { return }
        let proof = PushEnvelope.confirmProof(challenge: challenge, sendKey: current.sendKey)
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try? JSONSerialization.data(withJSONObject: ["c": challenge, "p": proof])
        session.dataTask(with: request) { [weak self] data, response, _ in
            guard
                let self, let still = self.pending, still.startedAt == current.startedAt,
                (response as? HTTPURLResponse)?.statusCode == 200,
                let object = data.flatMap({ try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }),
                let handle = object["handle"] as? String, (40...1024).contains(handle.utf8.count),
                let expires = (object["expires_at"] as? NSNumber)?.int64Value,
                object["type"] as? String == "voip",
                RelayConfirmation.matches(object["token"] as? String, object["env"] as? String, token: current.token, env: current.env)
            else { return }
            still.done((handle: handle, expiresAt: expires))
        }.resume()
    }

    private func server(_ method: String, _ paired: StoredDeviceCredential, _ path: String, _ json: [String: Any]?,
                        done: @escaping (Int, [String: Any]?) -> Void) {
        guard let url = URL(string: paired.origin)?.appendingPathComponent(path) else {
            done(0, nil)
            return
        }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.setValue("Moa-Device \(paired.credential)", forHTTPHeaderField: "Authorization")
        if method != "GET" { request.setValue("1", forHTTPHeaderField: "X-Moa-Request") }
        if let json {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try? JSONSerialization.data(withJSONObject: json)
        }
        session.dataTask(with: request) { data, response, _ in
            let object = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
            done((response as? HTTPURLResponse)?.statusCode ?? 0, object)
        }.resume()
    }

    private func loadState() -> State? {
        UserDefaults.standard.data(forKey: Self.stateKey).flatMap { try? JSONDecoder().decode(State.self, from: $0) }
    }

    private func saveState(_ state: State) {
        if let data = try? JSONEncoder().encode(state) { UserDefaults.standard.set(data, forKey: Self.stateKey) }
    }
}
