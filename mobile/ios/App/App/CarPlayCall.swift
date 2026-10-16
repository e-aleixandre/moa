import AVFoundation
import CarPlay
import os

private let carLog = Logger(subsystem: "com.ealeixandre.moa.app", category: "carplay")

// A call from the car: moa's voice screen (CPVoiceControlTemplate) over the
// same native call engine as the iPhone, without CallKit, so the screen the
// guide requires while recording stays visible. End and Mute sit in the
// navigation bar. When the call ends it hands back to the list, with an
// alert if the end was not expected.
@available(iOS 26.4, *)
@MainActor
final class CarPlayCall: NSObject, NativeCallObserver {
    enum Finish {
        case alert(CarPlayAlert?)
        case reload
    }

    let template: CPVoiceControlTemplate
    private let row: CarPlayRow
    private let client: CarPlayServer
    private let authorization: String
    private let onFinish: (CarPlayCall, Finish) -> Void
    private var machine = CarPlayCallMachine()
    private var callId: String?
    private var userEnded = false
    private var done = false
    private var muteButton: CPBarButton!

    // Nothing is opened, and the microphone is not touched, until start().
    init(row: CarPlayRow, client: CarPlayServer, authorization: String, image: UIImage?,
         onFinish: @escaping (CarPlayCall, Finish) -> Void) {
        self.row = row
        self.client = client
        self.authorization = authorization
        self.onFinish = onFinish
        let states = CarPlayCallPhase.allCases.map { phase in
            CPVoiceControlState(identifier: phase.rawValue, titleVariants: [Self.title(phase, row.name)], image: image, repeats: false)
        }
        template = CPVoiceControlTemplate(voiceControlStates: states)
        super.init()
        let end = CPBarButton(title: CarPlayText.end) { [weak self] _ in self?.end() }
        muteButton = CPBarButton(title: CarPlayText.mute) { [weak self] _ in self?.toggleMute() }
        template.leadingNavigationBarButtons = [end]
        template.trailingNavigationBarButtons = [muteButton]
        // The voice screen has no way back but End: leaving it would leave a
        // call running with no screen to end it from.
        template.backButton = CPBarButton(title: CarPlayText.end) { [weak self] _ in self?.end() }
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
    // opened (P5: it goes on on the iPhone) or, after End, hung up.
    private func created(status: Int?, data: Data?) {
        switch CarPlayCallStartModel.outcome(status: status, data: data) {
        case .alert(let alert):
            finish(.alert(alert))
        case .reload:
            finish(.reload)
        case .started(let wsPath):
            guard
                let url = client.socketURL(wsPath),
                let id = NativeVoiceCallManager.shared.startNative(name: row.name, sessionId: "", origin: client.origin,
                                                                   url: url, authorization: authorization, observer: self)
            else {
                carLog.error("carplay call created but not opened")
                finish(.alert(.unreachable))
                return
            }
            callId = id
            // End tapped while the call was being created: hang it up now.
            if userEnded { NativeVoiceCallManager.shared.hangUpNative(id: id) }
        }
    }

    // end is the user's End (or the voice screen going away).
    func end() {
        guard !done, !userEnded else { return }
        userEnded = true
        if let callId {
            NativeVoiceCallManager.shared.hangUpNative(id: callId)
        }
        // Still being created: created() hangs it up. The screen goes now.
        if callId == nil { finish(.alert(nil)) }
    }

    // detach: CarPlay went away. The call goes on on the iPhone (P5); only
    // the car screen is forgotten.
    func detach() {
        done = true
    }

    private func toggleMute() {
        guard let callId else { return }
        NativeVoiceCallManager.shared.setMutedNative(id: callId, muted: !machine.muted)
    }

    nonisolated func nativeCall(_ id: String, didReceive event: NativeCallEvent) {
        MainActor.assumeIsolated { self.handle(id, event) }
    }

    private func handle(_ id: String, _ event: NativeCallEvent) {
        guard id == callId, !done else { return }
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
            template.activateVoiceControlState(withIdentifier: phase.rawValue)
        case .end(let alert)?:
            finish(.alert(alert))
        case nil:
            break
        }
    }

    private func finish(_ how: Finish) {
        guard !done else { return }
        done = true
        onFinish(self, how)
    }
}
