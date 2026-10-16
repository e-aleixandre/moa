import AVFoundation
import CarPlay
import UIKit

// moa on the car screen: a list of the owners ("moa"); tapping one calls it
// on moa's voice screen (CarPlayCall). Everything here works with the iPhone
// locked and with the app launched only in the car: the list and the call
// use the narrow Moa-Voice credential (readable after the first unlock), not
// the device credential, and no UIWindowScene is needed.
@available(iOS 26.4, *)
@MainActor
final class CarPlaySceneDelegate: UIResponder, CPTemplateApplicationSceneDelegate, CPInterfaceControllerDelegate {
    private var interface: CPInterfaceController?
    private var list: CPListTemplate?
    private var server: CarPlayServer?
    private var authorization = ""
    private var call: CarPlayCall?
    private var generation = 0
    private var avatars: [String: UIImage] = [:]

    func templateApplicationScene(_ templateApplicationScene: CPTemplateApplicationScene,
                                  didConnect interfaceController: CPInterfaceController) {
        interface = interfaceController
        interfaceController.delegate = self
        let list = CPListTemplate(title: CarPlayText.title, sections: [])
        list.showsSpinnerWhileEmpty = true
        self.list = list
        interfaceController.setRootTemplate(list, animated: false, completion: nil)
        reload()
        // A call still on from the last connection: its voice screen again.
        if let surviving = CarPlayCall.surviving,
           let template = surviving.reattach(onFinish: { [weak self] call, how in self?.ended(call, how) }) {
            call = surviving
            interfaceController.pushTemplate(template, animated: false) { _, _ in
                MainActor.assumeIsolated { surviving.showPhase() }
            }
        }
    }

    // P5: a call goes on on the iPhone; only the car screen is let go.
    func templateApplicationScene(_ templateApplicationScene: CPTemplateApplicationScene,
                                  didDisconnectInterfaceController interfaceController: CPInterfaceController) {
        call?.detach()
        call = nil
        server?.invalidate()
        server = nil
        list = nil
        interface = nil
        avatars = [:]
    }

    // MARK: CPInterfaceControllerDelegate

    nonisolated func templateDidAppear(_ aTemplate: CPTemplate, animated: Bool) {
        MainActor.assumeIsolated {
            if aTemplate === list, call == nil { reload() }
        }
    }

    // The voice screen left without End (the system's back): it is End.
    nonisolated func templateDidDisappear(_ aTemplate: CPTemplate, animated: Bool) {
        MainActor.assumeIsolated {
            if let call, aTemplate === call.template { call.end() }
        }
    }

    // MARK: List

    private func reload() {
        guard list != nil else { return }
        generation += 1
        let current = generation
        guard let server = connect() else {
            show(.message(CarPlayText.notSetUp))
            return
        }
        server.directory { [weak self] status, data in
            MainActor.assumeIsolated {
                guard let self, current == self.generation else { return }
                self.show(CarPlayListModel.content(hasCredential: true, status: status, data: data,
                                                   limit: Int(CPListTemplate.maximumItemCount)))
            }
        }
    }

    // connect picks the server and credential: Moa-Voice from the incoming
    // call secret. nil: this iPhone was never set up for calls.
    private func connect() -> CarPlayServer? {
        #if DEBUG
        if ProcessInfo.processInfo.arguments.contains(CarPlayFakeServer.launchArgument) {
            if server == nil { server = CarPlayFakeServer() }
            authorization = "Moa-Voice fake"
            return server
        }
        #endif
        guard let stored = IncomingCallSecretStore.load() else {
            server?.invalidate()
            server = nil
            return nil
        }
        let header = IncomingCallAuth.header(stored)
        if let server, server.origin == stored.origin, header == authorization { return server }
        server?.invalidate()
        authorization = header
        let client = CarPlayClient(origin: stored.origin, authorization: header)
        server = client
        return client
    }

    private func show(_ content: CarPlayListContent) {
        guard let list else { return }
        switch content {
        case .message(let text):
            list.showsSpinnerWhileEmpty = false
            list.emptyViewTitleVariants = [text]
            list.updateSections([])
        case .owners(let rows):
            list.showsSpinnerWhileEmpty = false
            list.emptyViewTitleVariants = []
            let items = rows.map { row -> CPListItem in
                let item = CPListItem(text: row.name, detailText: row.subtitle, image: avatar(row))
                item.isEnabled = row.enabled
                if row.enabled {
                    item.handler = { [weak self] _, completion in
                        MainActor.assumeIsolated { self?.select(row) }
                        completion()
                    }
                }
                return item
            }
            list.updateSections([CPListSection(items: items)])
        }
    }

    private func avatar(_ row: CarPlayRow, size wanted: CGSize? = nil) -> UIImage {
        let size = wanted ?? CPListItem.maximumImageSize
        let key = "\(row.id)|\(row.enabled)|\(size.width)"
        if let cached = avatars[key] { return cached }
        let scale = interface?.carTraitCollection.displayScale ?? 2
        let image = CarPlayAvatar.image(row.avatar, size: size, scale: scale > 0 ? scale : 2, enabled: row.enabled)
        avatars[key] = image
        return image
    }

    // MARK: Call

    private func select(_ row: CarPlayRow) {
        guard call == nil, let interface, let server = connect() else { return }
        if NativeVoiceCallManager.shared.hasCall {
            alert(.callActive)
            return
        }
        // The car cannot ask for the microphone: without it there is no call.
        if AVAudioSession.sharedInstance().recordPermission != .granted {
            alert(.micOff)
            return
        }
        let call = CarPlayCall(row: row, client: server, authorization: authorization,
                               image: avatar(row, size: CGSize(width: 150, height: 150))) { [weak self] call, how in
            self?.ended(call, how)
        }
        self.call = call
        interface.pushTemplate(call.template, animated: true) { pushed, _ in
            MainActor.assumeIsolated {
                if pushed { call.start() } else { self.ended(call, .alert(.unreachable)) }
            }
        }
    }

    private func ended(_ ended: CarPlayCall, _ how: CarPlayCall.Finish) {
        guard call === ended else { return }
        call = nil
        guard let interface else { return }
        interface.popToRootTemplate(animated: true) { [weak self] _, _ in
            MainActor.assumeIsolated {
                guard let self else { return }
                switch how {
                case .alert(let alert?): self.alert(alert)
                case .alert(nil): break
                case .reload: self.reload()
                }
            }
        }
    }

    private func alert(_ alert: CarPlayAlert) {
        guard let interface else { return }
        let ok = CPAlertAction(title: CarPlayText.ok, style: .cancel) { [weak self] _ in
            MainActor.assumeIsolated { self?.interface?.dismissTemplate(animated: true, completion: nil) }
        }
        let template = CPAlertTemplate(titleVariants: [CarPlayText.message(alert)], actions: [ok])
        if interface.presentedTemplate != nil {
            interface.dismissTemplate(animated: false, completion: nil)
        }
        interface.presentTemplate(template, animated: true, completion: nil)
    }
}

#if DEBUG
// A made-up directory for looking at the car screen in the simulator,
// behind a launch argument that Release builds do not know. Calls answer
// "no plan" after a while, so the voice screen and an alert can both be seen.
final class CarPlayFakeServer: CarPlayServer {
    static let launchArgument = "-MoaCarPlayFakeDirectory"
    let origin = "https://carplay.invalid"

    func directory(_ done: @escaping (Int?, Data?) -> Void) {
        let palette = ["peach", "mauve", "sage", "sky", "azure", "rose", "mint", "lilac"]
        let tones = ["deep", "dark", "pale"]
        let names = ["moa", "Serena", "Landing", "Finance", "Taxes", "Reddit video", "Push relay", "Android",
                     "Home", "Notes", "Recipes", "Garden", "Travel", "Unknown shape"]
        var owners: [[String: Any]] = []
        for (i, name) in names.enumerated() {
            let shape = i < CarPlayAvatar.shapes.count - 1 ? CarPlayAvatar.shapes[i] : "star"
            let state = i == 2 || i == 6 ? "closed" : (i == 4 ? "none" : "open")
            owners.append(["id": "fake-\(i)", "name": name, "state": state,
                           "avatar": ["shape": shape, "color": palette[i % palette.count], "tone": tones[i % tones.count]]])
        }
        let data = try? JSONSerialization.data(withJSONObject: ["owners": owners])
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { done(200, data) }
    }

    func call(owner id: String, _ done: @escaping (Int?, Data?) -> Void) {
        let data = try? JSONSerialization.data(withJSONObject: ["error": "plan_unavailable", "reason": "no_plan"])
        DispatchQueue.main.asyncAfter(deadline: .now() + 20) { done(503, data) }
    }

    func socketURL(_ wsPath: String) -> URL? { nil }
    func invalidate() {}
}
#endif
