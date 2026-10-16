import AVFoundation
import CarPlay
import os

private let carLog = Logger(subsystem: "com.ealeixandre.moa.app", category: "carplay")

// A call from the car: moa's voice screen (CPVoiceControlTemplate) over the
// same native call engine as the iPhone, without CallKit, so the screen the
// guide requires while recording stays visible. End and Mute sit in the
// navigation bar. When the call ends it hands back to the list, with an
// alert if the end was not expected. If CarPlay goes away the call goes on
// (P5) and this object is kept, so a new connection shows the same call.
@available(iOS 26.4, *)
@MainActor
final class CarPlayCall: NSObject, NativeCallObserver {
    enum Finish {
        case alert(CarPlayAlert?)
        case reload
    }

    // The call left running when CarPlay went away, for the next connection.
    private(set) static var surviving: CarPlayCall?

    private(set) var template: CPVoiceControlTemplate!
    private let row: CarPlayRow
    private let client: CarPlayServer
    private let authorization: String
    private let image: UIImage?
    // nil while no car screen shows this call.
    private var onFinish: ((CarPlayCall, Finish) -> Void)?
    private var machine = CarPlayCallMachine()
    private var callId: String?
    private var userEnded = false
    private var closed = false
    private var muteButton: CPBarButton!

    // Nothing is opened, and the microphone is not touched, until start().
    init(row: CarPlayRow, client: CarPlayServer, authorization: String, image: UIImage?,
         onFinish: @escaping (CarPlayCall, Finish) -> Void) {
        self.row = row
        self.client = client
        self.authorization = authorization
        self.image = image
        self.onFinish = onFinish
        super.init()
        template = makeTemplate()
    }

    private func makeTemplate() -> CPVoiceControlTemplate {
        let states = CarPlayCallPhase.allCases.map { phase in
            CPVoiceControlState(identifier: phase.rawValue, titleVariants: [Self.title(phase, row.name)], image: image, repeats: false)
        }
        let template = CPVoiceControlTemplate(voiceControlStates: states)
        let end = CPBarButton(title: CarPlayText.end) { [weak self] _ in self?.end() }
        muteButton = CPBarButton(title: machine.muted ? CarPlayText.unmute : CarPlayText.mute) { [weak self] _ in self?.toggleMute() }
        template.leadingNavigationBarButtons = [end]
        template.trailingNavigationBarButtons = [muteButton]
        // The voice screen has no way back but End: leaving it would leave a
        // call running with no screen to end it from.
        template.backButton = CPBarButton(title: CarPlayText.end) { [weak self] _ in self?.end() }
        return template
    }

    static func title(_ phase: CarPlayCallPhase, _ name: String) -> String {
        switch phase {
        case .connecting: return CarPlayText.connecting
        case .listening: return CarPlayText.listening
        case .speaking: return CarPlayText.speaking(name)
        case .muted: return CarPlayText.muted
        }
    }

    // start creates the call on the server, then opens it here. The voice
    // screen is already showing "Connecting…".
    func start() {
        // Held until the answer arrives, even if the screen has gone: a call
        // the server created must still be opened or hung up.
        client.call(owner: row.id) { status, data in
            MainActor.assumeIsolated { self.created(status: status, data: data) }
        }
    }

    // Runs even when the screen is gone: a call the server created is
    // opened (P5: it goes on on the iPhone) or, after End, hung up on the
    // server before anything here (session, microphone) is touched.
    private func created(status: Int?, data: Data?) {
        let outcome = CarPlayCallStartModel.outcome(status: status, data: data)
        if let discarded = CarPlayCallStartModel.discarded(outcome, userEnded: userEnded) {
            client.hangUp(call: discarded)
            return
        }
        switch outcome {
        case .alert(let alert):
            finish(.alert(alert))
        case .reload:
            finish(.reload)
        case .started(let wsPath, let sessionId):
            guard
                let url = client.socketURL(wsPath),
                let id = NativeVoiceCallManager.shared.startNative(name: row.name, sessionId: sessionId, origin: client.origin,
                                                                   url: url, authorization: authorization, observer: self)
            else {
                carLog.error("carplay call created but not opened")
                finish(.alert(.unreachable))
                return
            }
            callId = id
        }
    }

    // end is the user's End (or the voice screen going away).
    func end() {
        guard !closed, !userEnded else { return }
        userEnded = true
        if let callId {
            NativeVoiceCallManager.shared.hangUpNative(id: callId)
        }
        // Still being created: created() hangs it up on the server. The
        // screen goes now.
        if callId == nil { finish(.alert(nil)) }
    }

    // detach: CarPlay went away. The call goes on on the iPhone (P5); only
    // the car screen is forgotten, and the call is kept for the next one.
    func detach() {
        onFinish = nil
        if !closed && !userEnded { Self.surviving = self }
    }

    // reattach gives a new car screen this call's voice screen, with End and
    // Mute on the same call. Show it, then call showPhase().
    func reattach(onFinish: @escaping (CarPlayCall, Finish) -> Void) -> CPVoiceControlTemplate? {
        if Self.surviving === self { Self.surviving = nil }
        guard !closed, !userEnded else { return nil }
        self.onFinish = onFinish
        template = makeTemplate()
        return template
    }

    // showPhase puts the voice screen in the call's current state; a
    // template only switches state once it is on screen.
    func showPhase() {
        guard onFinish != nil, !closed else { return }
        template.activateVoiceControlState(withIdentifier: machine.phase.rawValue)
    }

    private func toggleMute() {
        guard let callId else { return }
        NativeVoiceCallManager.shared.setMutedNative(id: callId, muted: !machine.muted)
    }

    nonisolated func nativeCall(_ id: String, didReceive event: NativeCallEvent) {
        MainActor.assumeIsolated { self.handle(id, event) }
    }

    private func handle(_ id: String, _ event: NativeCallEvent) {
        guard id == callId, !closed else { return }
        let mapped: CarPlayCallEvent
        switch event {
        case .open: mapped = .open
        case .playback(let state): mapped = .playback(state)
        case .mute(let muted):
            mapped = .mute(muted)
            muteButton.title = muted ? CarPlayText.unmute : CarPlayText.mute
        case .close(let reason, let serverEnded):
            mapped = .close(reason: reason, serverEnded: serverEnded, userEnded: userEnded)
        }
        switch machine.apply(mapped) {
        case .show(let phase)?:
            if onFinish != nil { template.activateVoiceControlState(withIdentifier: phase.rawValue) }
        case .end(let alert)?:
            finish(.alert(alert))
        case nil:
            break
        }
    }

    private func finish(_ how: Finish) {
        guard !closed else { return }
        closed = true
        if Self.surviving === self { Self.surviving = nil }
        onFinish?(self, how)
    }
}
