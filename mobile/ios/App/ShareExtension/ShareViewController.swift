import UIKit
import UniformTypeIdentifiers

final class ShareViewController: UIViewController {
    private final class CaptureState {
        var textParts: [String] = []
        var sharedURL = ""
    }

    private let titleLabel = UILabel()
    private let detailLabel = UILabel()
    private let doneButton = UIButton(type: .system)
    private var transaction: SharedInboxTransaction?

    override func viewDidLoad() {
        super.viewDidLoad()
        configureView()
        captureShare()
    }

    private func configureView() {
        view.backgroundColor = .systemBackground
        titleLabel.font = .preferredFont(forTextStyle: .headline)
        titleLabel.text = "Saving to moa…"
        detailLabel.font = .preferredFont(forTextStyle: .body)
        detailLabel.textColor = .secondaryLabel
        detailLabel.numberOfLines = 0
        detailLabel.textAlignment = .center
        doneButton.setTitle("Cancel", for: .normal)
        doneButton.titleLabel?.font = .preferredFont(forTextStyle: .headline)
        doneButton.addTarget(self, action: #selector(finish), for: .touchUpInside)

        let stack = UIStackView(arrangedSubviews: [titleLabel, detailLabel, doneButton])
        stack.axis = .vertical
        stack.alignment = .center
        stack.spacing = 18
        stack.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(greaterThanOrEqualTo: view.leadingAnchor, constant: 28),
            stack.trailingAnchor.constraint(lessThanOrEqualTo: view.trailingAnchor, constant: -28),
            stack.centerXAnchor.constraint(equalTo: view.centerXAnchor),
            stack.centerYAnchor.constraint(equalTo: view.centerYAnchor),
            detailLabel.widthAnchor.constraint(lessThanOrEqualToConstant: 330),
            doneButton.heightAnchor.constraint(greaterThanOrEqualToConstant: 44)
        ])
    }

    private func captureShare() {
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
            do {
                guard let transaction = self.transaction else {
                    throw SharedInboxError.corrupt
                }
                _ = try transaction.commit(
                    title: title,
                    text: state.textParts.joined(separator: "\n"),
                    url: state.sharedURL
                )
                self.titleLabel.text = "Saved to moa"
                self.detailLabel.text = "Open moa to choose a conversation."
                self.doneButton.setTitle("Done", for: .normal)
            } catch {
                self.show(error: error)
            }
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
        titleLabel.text = "Could not save this share"
        detailLabel.text = error.localizedDescription
        doneButton.setTitle("Close", for: .normal)
    }

    @objc private func finish() {
        extensionContext?.completeRequest(returningItems: nil)
    }
}
