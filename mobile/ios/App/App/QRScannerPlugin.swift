import AVFoundation
import Capacitor
import UIKit

// The pairing screen's QR scanner, on the system camera stack only. It
// replaces Google's ML Kit barcode plugin, which reports diagnostics to Google
// and would stop the App Store privacy label from being "Data Not Collected".
//
// It answers the same four calls the local pairing page already makes
// (www/barcode-scanner.js), under the same plugin name, so the page did not
// change: isSupported, checkPermissions, requestPermissions and scan, which
// resolves with `{ barcodes: [{ rawValue }] }` -- an empty list when the person
// cancels.
final class QRScannerPlugin: CAPInstancePlugin, CAPBridgedPlugin {
    let identifier = "MoaQRScanner"
    let jsName = "BarcodeScanner"
    let pluginMethods: [CAPPluginMethod] = [
        CAPPluginMethod(name: "isSupported", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "checkPermissions", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "requestPermissions", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "scan", returnType: CAPPluginReturnPromise)
    ]

    private var scanner: QRScannerViewController?

    @objc func isSupported(_ call: CAPPluginCall) {
        call.resolve(["supported": AVCaptureDevice.default(for: .video) != nil])
    }

    @objc override func checkPermissions(_ call: CAPPluginCall) {
        call.resolve(["camera": Self.permissionState()])
    }

    @objc override func requestPermissions(_ call: CAPPluginCall) {
        AVCaptureDevice.requestAccess(for: .video) { _ in
            call.resolve(["camera": Self.permissionState()])
        }
    }

    @objc func scan(_ call: CAPPluginCall) {
        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            guard self.scanner == nil else {
                call.reject("A scan is already in progress.")
                return
            }
            guard AVCaptureDevice.authorizationStatus(for: .video) == .authorized else {
                call.reject("Camera permission was not granted.")
                return
            }
            guard AVCaptureDevice.default(for: .video) != nil else {
                call.reject("The camera is not available.")
                return
            }
            guard let presenter = self.bridge?.viewController else {
                call.reject("Nothing to present the scanner from.")
                return
            }
            let scanner = QRScannerViewController { [weak self] result in
                self?.scanner = nil
                switch result {
                case .scanned(let value):
                    call.resolve(["barcodes": [["rawValue": value, "displayValue": value, "format": "QR_CODE"]]])
                case .cancelled:
                    call.resolve(["barcodes": []])
                case .failed(let message):
                    call.reject(message)
                }
            }
            self.scanner = scanner
            scanner.modalPresentationStyle = .fullScreen
            presenter.present(scanner, animated: true)
        }
    }

    private static func permissionState() -> String {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized: return "granted"
        case .notDetermined: return "prompt"
        default: return "denied"
        }
    }
}

enum QRScanResult {
    case scanned(String)
    case cancelled
    case failed(String)
}

// Full-screen camera preview that finishes on the first QR code it reads, or
// when the person taps Cancel.
final class QRScannerViewController: UIViewController, AVCaptureMetadataOutputObjectsDelegate {
    private let session = AVCaptureSession()
    private let sessionQueue = DispatchQueue(label: "com.ealeixandre.moa.qr-scanner")
    private let preview = AVCaptureVideoPreviewLayer()
    private var completion: ((QRScanResult) -> Void)?

    init(completion: @escaping (QRScanResult) -> Void) {
        self.completion = completion
        super.init(nibName: nil, bundle: nil)
    }

    required init?(coder: NSCoder) { nil }

    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = .black

        preview.session = session
        preview.videoGravity = .resizeAspectFill
        view.layer.addSublayer(preview)

        let hint = UILabel()
        hint.text = "Point the camera at the pairing code"
        hint.textColor = .white
        hint.font = .preferredFont(forTextStyle: .headline)
        hint.textAlignment = .center
        hint.numberOfLines = 0
        hint.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(hint)

        var configuration = UIButton.Configuration.filled()
        configuration.title = "Cancel"
        configuration.baseBackgroundColor = UIColor(white: 0.2, alpha: 0.85)
        configuration.baseForegroundColor = .white
        configuration.cornerStyle = .large
        configuration.contentInsets = NSDirectionalEdgeInsets(top: 14, leading: 28, bottom: 14, trailing: 28)
        let cancel = UIButton(configuration: configuration, primaryAction: UIAction { [weak self] _ in
            self?.finish(.cancelled)
        })
        cancel.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(cancel)

        NSLayoutConstraint.activate([
            hint.topAnchor.constraint(equalTo: view.safeAreaLayoutGuide.topAnchor, constant: 24),
            hint.leadingAnchor.constraint(equalTo: view.safeAreaLayoutGuide.leadingAnchor, constant: 24),
            hint.trailingAnchor.constraint(equalTo: view.safeAreaLayoutGuide.trailingAnchor, constant: -24),
            cancel.centerXAnchor.constraint(equalTo: view.centerXAnchor),
            cancel.bottomAnchor.constraint(equalTo: view.safeAreaLayoutGuide.bottomAnchor, constant: -24)
        ])

        configureSession()
    }

    override func viewDidLayoutSubviews() {
        super.viewDidLayoutSubviews()
        preview.frame = view.bounds
        guard let connection = preview.connection,
              let orientation = view.window?.windowScene?.interfaceOrientation else { return }
        if #available(iOS 17.0, *) {
            let angle: CGFloat
            switch orientation {
            case .portraitUpsideDown: angle = 270
            case .landscapeLeft: angle = 180
            case .landscapeRight: angle = 0
            default: angle = 90
            }
            if connection.isVideoRotationAngleSupported(angle) { connection.videoRotationAngle = angle }
        } else if connection.isVideoOrientationSupported,
                  let video = AVCaptureVideoOrientation(rawValue: orientation.rawValue) {
            connection.videoOrientation = video
        }
    }

    override func viewWillAppear(_ animated: Bool) {
        super.viewWillAppear(animated)
        sessionQueue.async { [session] in
            if !session.isRunning { session.startRunning() }
        }
    }

    override func viewDidDisappear(_ animated: Bool) {
        super.viewDidDisappear(animated)
        sessionQueue.async { [session] in
            if session.isRunning { session.stopRunning() }
        }
    }

    private func configureSession() {
        guard
            let device = AVCaptureDevice.default(for: .video),
            let input = try? AVCaptureDeviceInput(device: device),
            session.canAddInput(input)
        else {
            failAfterPresentation("The camera is not available.")
            return
        }
        session.addInput(input)

        let output = AVCaptureMetadataOutput()
        guard session.canAddOutput(output) else {
            failAfterPresentation("The camera is not available.")
            return
        }
        session.addOutput(output)
        output.setMetadataObjectsDelegate(self, queue: .main)
        output.metadataObjectTypes = [.qr]
    }

    func metadataOutput(
        _ output: AVCaptureMetadataOutput,
        didOutput metadataObjects: [AVMetadataObject],
        from connection: AVCaptureConnection
    ) {
        for object in metadataObjects {
            if let code = object as? AVMetadataMachineReadableCodeObject,
               code.type == .qr,
               let value = code.stringValue,
               !value.isEmpty {
                finish(.scanned(value))
                return
            }
        }
    }

    // viewDidLoad runs while the presentation is still starting, and a dismissal
    // issued then is dropped.
    private func failAfterPresentation(_ message: String) {
        DispatchQueue.main.async { [weak self] in self?.finish(.failed(message)) }
    }

    // Reports once. Dismissal can race a second metadata callback or a tap on
    // Cancel; the completion is consumed by whichever arrives first.
    private func finish(_ result: QRScanResult) {
        guard let completion else { return }
        self.completion = nil
        if presentingViewController != nil {
            dismiss(animated: true) { completion(result) }
        } else {
            completion(result)
        }
    }
}
