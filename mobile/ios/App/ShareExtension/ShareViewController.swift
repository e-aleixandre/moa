import UIKit
import UniformTypeIdentifiers

class ShareViewController: UIViewController {
    private final class CaptureState {
        var textParts: [String] = []
        var sharedURL = ""
    }

    private typealias MessageAction = (String, () -> Void)

    private enum Section {
        case compose, owners, recent, empty
    }

    private let navigationBar = UINavigationBar()
    private let barItem = UINavigationItem(title: "Send to moa")
    private lazy var cancelItem = UIBarButtonItem(
        barButtonSystemItem: .cancel, target: self, action: #selector(cancelTapped))
    private lazy var sendItem = UIBarButtonItem(
        title: "Send", style: .done, target: self, action: #selector(sendTapped))
    private let spinnerItem = UIBarButtonItem(customView: UIActivityIndicatorView(style: .medium))
    let tableView = UITableView(frame: .zero, style: .insetGrouped)
    private let searchBar = UISearchBar()
    private let composeCell = UITableViewCell()
    private let summaryLabel = UILabel()
    let messageField = UITextView()
    private let placeholder = UILabel()
    let messageStack = UIStackView()
    private let spinner = UIActivityIndicatorView(style: .large)

    var transaction: SharedInboxTransaction?
    var sharedTitle = ""
    var sharedText = ""
    var sharedURL = ""
    var client: ShareClient?
    var destinations: [ShareDestination] = []
    var selectedID: String?
    private var query = ""
    var sending = false

    private var visibleSections: [Section] {
        let rows = ShareDestinations.filter(destinations, query: query)
        var sections: [Section] = [.compose]
        if rows.contains(where: { $0.kind == .owner }) { sections.append(.owners) }
        if rows.contains(where: { $0.kind == .session }) { sections.append(.recent) }
        if rows.isEmpty { sections.append(.empty) }
        return sections
    }

    private func rows(in section: Section) -> [ShareDestination] {
        let rows = ShareDestinations.filter(destinations, query: query)
        switch section {
        case .owners: return rows.filter { $0.kind == .owner }
        case .recent: return rows.filter { $0.kind == .session }
        default: return []
        }
    }

    override func viewDidLoad() {
        super.viewDidLoad()
        configureView()
        captureShare()
    }

    private func configureView() {
        view.backgroundColor = .systemGroupedBackground

        barItem.leftBarButtonItem = cancelItem
        barItem.rightBarButtonItem = sendItem
        sendItem.isEnabled = false
        navigationBar.items = [barItem]
        navigationBar.delegate = self
        navigationBar.translatesAutoresizingMaskIntoConstraints = false

        let fieldFont = UIFont.systemFont(ofSize: max(16, UIFont.preferredFont(forTextStyle: .body).pointSize))
        summaryLabel.font = .preferredFont(forTextStyle: .footnote)
        summaryLabel.textColor = .secondaryLabel
        summaryLabel.numberOfLines = 2
        messageField.font = fieldFont
        messageField.backgroundColor = .clear
        messageField.isScrollEnabled = false
        messageField.delegate = self
        placeholder.text = "Add a message (optional)"
        placeholder.font = fieldFont
        placeholder.textColor = .placeholderText
        placeholder.translatesAutoresizingMaskIntoConstraints = false
        messageField.addSubview(placeholder)
        let compose = UIStackView(arrangedSubviews: [summaryLabel, messageField])
        compose.axis = .vertical
        compose.spacing = 8
        compose.translatesAutoresizingMaskIntoConstraints = false
        composeCell.contentView.addSubview(compose)
        composeCell.selectionStyle = .none

        searchBar.placeholder = "Search"
        searchBar.searchTextField.font = fieldFont
        searchBar.autocapitalizationType = .none
        searchBar.delegate = self
        searchBar.sizeToFit()

        tableView.dataSource = self
        tableView.delegate = self
        tableView.keyboardDismissMode = .interactive
        tableView.tableHeaderView = searchBar
        tableView.translatesAutoresizingMaskIntoConstraints = false
        tableView.isHidden = true

        messageStack.axis = .vertical
        messageStack.alignment = .center
        messageStack.spacing = 14
        messageStack.translatesAutoresizingMaskIntoConstraints = false
        spinner.translatesAutoresizingMaskIntoConstraints = false

        view.addSubview(tableView)
        view.addSubview(navigationBar)
        view.addSubview(messageStack)
        view.addSubview(spinner)
        NSLayoutConstraint.activate([
            navigationBar.topAnchor.constraint(equalTo: view.safeAreaLayoutGuide.topAnchor),
            navigationBar.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            navigationBar.trailingAnchor.constraint(equalTo: view.trailingAnchor),
            tableView.topAnchor.constraint(equalTo: navigationBar.bottomAnchor),
            tableView.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            tableView.trailingAnchor.constraint(equalTo: view.trailingAnchor),
            tableView.bottomAnchor.constraint(equalTo: view.bottomAnchor),
            compose.topAnchor.constraint(equalTo: composeCell.contentView.layoutMarginsGuide.topAnchor),
            compose.bottomAnchor.constraint(equalTo: composeCell.contentView.layoutMarginsGuide.bottomAnchor),
            compose.leadingAnchor.constraint(equalTo: composeCell.contentView.layoutMarginsGuide.leadingAnchor),
            compose.trailingAnchor.constraint(equalTo: composeCell.contentView.layoutMarginsGuide.trailingAnchor),
            messageField.heightAnchor.constraint(greaterThanOrEqualToConstant: 72),
            placeholder.topAnchor.constraint(equalTo: messageField.topAnchor, constant: 8),
            placeholder.leadingAnchor.constraint(equalTo: messageField.leadingAnchor, constant: 5),
            messageStack.leadingAnchor.constraint(greaterThanOrEqualTo: view.leadingAnchor, constant: 28),
            messageStack.trailingAnchor.constraint(lessThanOrEqualTo: view.trailingAnchor, constant: -28),
            messageStack.centerXAnchor.constraint(equalTo: view.centerXAnchor),
            messageStack.centerYAnchor.constraint(equalTo: view.centerYAnchor),
            spinner.centerXAnchor.constraint(equalTo: view.centerXAnchor),
            spinner.centerYAnchor.constraint(equalTo: view.centerYAnchor)
        ])
        showLoading()
    }

    // MARK: States

    private func showLoading() {
        clearMessage()
        tableView.isHidden = true
        spinner.startAnimating()
        barItem.rightBarButtonItem = sendItem
        sendItem.isEnabled = false
    }

    private func showPicker() {
        guard !destinations.isEmpty else {
            showFailure(ShareError.notFound, whileSending: false)
            return
        }
        clearMessage()
        spinner.stopAnimating()
        barItem.rightBarButtonItem = sendItem
        tableView.isHidden = false
        tableView.reloadData()
        updateSendEnabled()
    }

    private func clearMessage() {
        messageStack.arrangedSubviews.forEach { $0.removeFromSuperview() }
    }

    // `actions`: the first one is the primary button.
    private func showMessage(_ title: String, _ detail: String, _ actions: [MessageAction]) {
        clearMessage()
        spinner.stopAnimating()
        tableView.isHidden = true
        barItem.rightBarButtonItem = nil
        let titleLabel = UILabel()
        titleLabel.font = .preferredFont(forTextStyle: .headline)
        titleLabel.text = title
        titleLabel.textAlignment = .center
        titleLabel.numberOfLines = 0
        let detailLabel = UILabel()
        detailLabel.font = .preferredFont(forTextStyle: .body)
        detailLabel.textColor = .secondaryLabel
        detailLabel.text = detail
        detailLabel.numberOfLines = 0
        detailLabel.textAlignment = .center
        messageStack.addArrangedSubview(titleLabel)
        messageStack.addArrangedSubview(detailLabel)
        detailLabel.widthAnchor.constraint(lessThanOrEqualToConstant: 330).isActive = true
        for (index, action) in actions.enumerated() {
            var configuration: UIButton.Configuration = index == 0 ? .filled() : .plain()
            configuration.title = action.0
            let button = UIButton(configuration: configuration, primaryAction: UIAction { _ in action.1() })
            button.heightAnchor.constraint(greaterThanOrEqualToConstant: 44).isActive = true
            messageStack.addArrangedSubview(button)
        }
    }

    private func setSending(_ value: Bool) {
        sending = value
        tableView.isUserInteractionEnabled = !value
        cancelItem.isEnabled = !value
        if value {
            (spinnerItem.customView as? UIActivityIndicatorView)?.startAnimating()
            barItem.rightBarButtonItem = spinnerItem
        } else {
            barItem.rightBarButtonItem = sendItem
        }
    }

    private func updateSendEnabled() {
        sendItem.isEnabled = selectedID != nil && !sending
    }

    // MARK: Capture

    private func captureShare() {
        // Tests inject a prepared transaction; the real capture only runs in the extension.
        if transaction != nil { return }
        do {
            transaction = try SharedInbox.transaction()
        } catch {
            show(error: error)
            return
        }

        let items = (extensionContext?.inputItems as? [NSExtensionItem]) ?? []
        let title = items.compactMap { $0.attributedTitle?.string }.first ?? ""
        let providers = items.flatMap { $0.attachments ?? [] }
        let state = CaptureState()

        process(providers: providers, index: 0, state: state) { [weak self] error in
            guard let self else { return }
            if let error {
                self.show(error: error)
                return
            }
            self.sharedTitle = title
            self.sharedText = state.textParts.joined(separator: "\n")
            self.sharedURL = state.sharedURL
            let files = self.transaction?.stagedFiles ?? []
            let hasText = !ShareText.composed(title: title, text: self.sharedText, url: self.sharedURL).isEmpty
            guard hasText || !files.isEmpty else {
                self.show(error: SharedInboxError.empty)
                return
            }
            self.summaryLabel.text = ShareText.summary(
                fileNames: files.map { $0.file.name },
                title: title, text: self.sharedText, url: self.sharedURL)
            self.loadDestinations()
        }
    }

    private func process(
        providers: [NSItemProvider],
        index: Int,
        state: CaptureState,
        completion: @escaping (Error?) -> Void
    ) {
        guard index < providers.count else {
            completion(nil)
            return
        }
        let provider = providers[index]
        load(provider: provider) { [weak self] value, error in
            DispatchQueue.main.async {
                guard let self else { return }
                if let error {
                    completion(error)
                    return
                }
                switch value {
                case .text(let text):
                    state.textParts.append(text)
                case .url(let url):
                    if state.sharedURL.isEmpty { state.sharedURL = url.absoluteString }
                    else { state.textParts.append(url.absoluteString) }
                case .none:
                    break
                }
                self.process(
                    providers: providers,
                    index: index + 1,
                    state: state,
                    completion: completion
                )
            }
        }
    }

    private enum LoadedValue {
        case text(String)
        case url(URL)
    }

    private func load(provider: NSItemProvider, completion: @escaping (LoadedValue?, Error?) -> Void) {
        if provider.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier) {
            provider.loadItem(forTypeIdentifier: UTType.fileURL.identifier, options: nil) { [weak self] item, error in
                guard let self else { return }
                guard error == nil, let url = item as? URL else {
                    completion(nil, error ?? SharedInboxError.empty)
                    return
                }
                completion(nil, self.copyFile(url: url, provider: provider, type: nil))
            }
            return
        }

        let registeredTypes = provider.registeredTypeIdentifiers.compactMap { UTType($0) }
        let preferredFileType: UTType? = {
            if let image = registeredTypes.first(where: { $0.conforms(to: .image) }) { return image }
            if let pdf = registeredTypes.first(where: { $0.conforms(to: .pdf) }) { return pdf }
            return registeredTypes.first { type in
                type.conforms(to: .content) || type.conforms(to: .data)
            }
        }()
        if let type = preferredFileType,
           !type.conforms(to: .url),
           !type.conforms(to: .plainText) {
            provider.loadFileRepresentation(forTypeIdentifier: type.identifier) { [weak self] url, error in
                guard let self else { return }
                guard error == nil, let url else {
                    completion(nil, error ?? SharedInboxError.empty)
                    return
                }
                completion(nil, self.copyFile(url: url, provider: provider, type: type))
            }
            return
        }

        if provider.hasItemConformingToTypeIdentifier(UTType.url.identifier) {
            provider.loadItem(forTypeIdentifier: UTType.url.identifier, options: nil) { item, error in
                if let error {
                    completion(nil, error)
                } else if let url = item as? URL {
                    completion(.url(url), nil)
                } else {
                    completion(nil, SharedInboxError.empty)
                }
            }
            return
        }

        if provider.hasItemConformingToTypeIdentifier(UTType.plainText.identifier) {
            provider.loadItem(forTypeIdentifier: UTType.plainText.identifier, options: nil) { item, error in
                let text = (item as? String) ?? (item as? NSAttributedString)?.string
                if let error {
                    completion(nil, error)
                } else if let text {
                    completion(.text(text), nil)
                } else {
                    completion(nil, SharedInboxError.empty)
                }
            }
            return
        }
        completion(nil, nil)
    }

    private func copyFile(url: URL, provider: NSItemProvider, type: UTType?) -> Error? {
        let accessed = url.startAccessingSecurityScopedResource()
        defer {
            if accessed { url.stopAccessingSecurityScopedResource() }
        }
        do {
            let resourceType = try? url.resourceValues(forKeys: [.contentTypeKey]).contentType
            let resolvedType = type ?? resourceType
            var name = provider.suggestedName ?? url.lastPathComponent
            if URL(fileURLWithPath: name).pathExtension.isEmpty,
               let suffix = resolvedType?.preferredFilenameExtension {
                name += ".\(suffix)"
            }
            guard let transaction else { throw SharedInboxError.corrupt }
            try transaction.addFile(
                at: url,
                name: name,
                mime: resolvedType?.preferredMIMEType
            )
            return nil
        } catch {
            return error
        }
    }

    private func show(error: Error) {
        showMessage("Could not use this share", error.localizedDescription, [("Close", { [weak self] in self?.close() })])
    }

    // MARK: Destinations

    private func loadDestinations() {
        showLoading()
        Task { @MainActor [weak self] in
            guard let self else { return }
            do {
                // A keychain failure reads the same as no pairing to the user.
                guard let client = try? ShareCredential.load() else { throw ShareError.notPaired }
                self.client = client
                self.destinations = try await client.destinations()
                if self.destinations.isEmpty {
                    self.showMessage(
                        "No conversations yet",
                        "Start one in moa, or save this to place it later.",
                        [("Save for later", { [weak self] in self?.saveForLater() }),
                         ("Close", { [weak self] in self?.close() })])
                } else {
                    self.showPicker()
                }
            } catch {
                self.showFailure(error, whileSending: false)
            }
        }
    }

    func showFailure(_ error: Error, whileSending: Bool) {
        setSending(false)
        let save: MessageAction = ("Save for later", { [weak self] in self?.saveForLater() })
        let close: MessageAction = ("Close", { [weak self] in self?.close() })
        let retry: MessageAction = ("Try again", { [weak self] in
            guard let self else { return }
            if whileSending { self.showPicker() } else { self.loadDestinations() }
        })
        switch error as? ShareError {
        case .notPaired:
            showMessage("moa isn't paired", "Open moa and pair this iPhone, then share again. Or save this to place it later.", [save, close])
        case .unreachable:
            showMessage("Can't reach moa", "Check your connection or VPN, then try again.", [retry, save])
        case .outcomeUnknown:
            // Nothing may be saved or retried: it could arrive twice.
            showMessage("It may have been sent", "Open moa to check the conversation.", [close])
        case .notFound where !whileSending:
            // A server without GET /api/share/destinations: there is nothing to pick from here.
            showMessage("This moa can't list conversations", "Save this, then choose the conversation in moa.", [save, close])
        case .notFound:
            showMessage("Conversation not found", "Choose another conversation, or save this for later.", [("Choose another", { [weak self] in self?.showPicker() }), save])
        case .server(let status):
            showMessage("moa returned an error (\(status))", "Try again, or save this to place it later.", [retry, save])
        default:
            showMessage("Something went wrong", "Try again, or save this to place it later.", [retry, save])
        }
    }

    // MARK: Actions

    @objc func sendTapped() {
        guard !sending,
              let destination = destinations.first(where: { $0.sessionID == selectedID }),
              let client, let transaction else { return }
        view.endEditing(true)
        let note = messageField.text ?? ""
        let text = ShareText.message(note: note, title: sharedTitle, text: sharedText, url: sharedURL)
        // Same ceiling as the queue, measured on exactly what is uploaded.
        guard text.utf8.count <= SharedInbox.maximumTextBytes else {
            showMessage("Too much text", "Shorten your message or share a shorter selection.", [
                ("Back", { [weak self] in self?.showPicker() }),
                ("Close", { [weak self] in self?.close() })])
            return
        }
        setSending(true)
        let attachments = transaction.stagedFiles.map {
            ShareAttachment(name: $0.file.name, mime: $0.file.mime, url: $0.url)
        }
        let body = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".json")
        Task { @MainActor [weak self] in
            defer { try? FileManager.default.removeItem(at: body) }
            do {
                try await Task.detached {
                    try ShareBodyWriter.write(text: text, attachments: attachments, to: body)
                }.value
                try await client.send(to: destination.sessionID, bodyFile: body)
                self?.transaction?.discard()
                self?.extensionContext?.completeRequest(returningItems: nil)
            } catch {
                self?.showFailure(error, whileSending: true)
            }
        }
    }

    func saveForLater() {
        do {
            guard let transaction else { throw SharedInboxError.corrupt }
            let note = (messageField.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            let text = [note, sharedText].filter { !$0.isEmpty }.joined(separator: "\n")
            _ = try transaction.commit(title: sharedTitle, text: text, url: sharedURL)
            showMessage("Saved to moa", "Open moa to choose a conversation.", [("Done", { [weak self] in
                self?.extensionContext?.completeRequest(returningItems: nil)
            })])
        } catch {
            show(error: error)
        }
    }

    @objc private func cancelTapped() { close() }

    func close() {
        transaction?.discard()
        extensionContext?.cancelRequest(withError: NSError(domain: NSCocoaErrorDomain, code: NSUserCancelledError))
    }
}

extension ShareViewController: UITableViewDataSource, UITableViewDelegate {
    func numberOfSections(in tableView: UITableView) -> Int { visibleSections.count }

    func tableView(_ tableView: UITableView, numberOfRowsInSection section: Int) -> Int {
        switch visibleSections[section] {
        case .compose, .empty: return 1
        case .owners, .recent: return rows(in: visibleSections[section]).count
        }
    }

    func tableView(_ tableView: UITableView, titleForHeaderInSection section: Int) -> String? {
        switch visibleSections[section] {
        case .owners: return "Owners"
        case .recent: return "Recent"
        default: return nil
        }
    }

    func tableView(_ tableView: UITableView, cellForRowAt indexPath: IndexPath) -> UITableViewCell {
        let section = visibleSections[indexPath.section]
        if section == .compose { return composeCell }
        let cell = UITableViewCell(style: .subtitle, reuseIdentifier: nil)
        var content = UIListContentConfiguration.subtitleCell()
        if section == .empty {
            content.text = "No matches"
            content.textProperties.color = .secondaryLabel
            cell.selectionStyle = .none
        } else {
            let destination = rows(in: section)[indexPath.row]
            content.text = destination.displayTitle
            content.secondaryText = destination.subtitle
            content.secondaryTextProperties.color = .secondaryLabel
            cell.accessoryType = destination.sessionID == selectedID ? .checkmark : .none
        }
        cell.contentConfiguration = content
        return cell
    }

    func tableView(_ tableView: UITableView, didSelectRowAt indexPath: IndexPath) {
        tableView.deselectRow(at: indexPath, animated: true)
        let section = visibleSections[indexPath.section]
        guard section == .owners || section == .recent else { return }
        selectedID = rows(in: section)[indexPath.row].sessionID
        tableView.reloadData()
        updateSendEnabled()
    }
}

extension ShareViewController: UITextViewDelegate {
    func textViewDidChange(_ textView: UITextView) {
        placeholder.isHidden = !textView.text.isEmpty
        tableView.performBatchUpdates(nil)
    }
}

extension ShareViewController: UISearchBarDelegate, UINavigationBarDelegate {
    func searchBar(_ searchBar: UISearchBar, textDidChange searchText: String) {
        query = searchText
        tableView.reloadData()
    }

    func searchBarSearchButtonClicked(_ searchBar: UISearchBar) { searchBar.resignFirstResponder() }

    func position(for bar: UIBarPositioning) -> UIBarPosition { .topAttached }
}
