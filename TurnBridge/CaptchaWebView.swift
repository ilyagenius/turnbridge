import SwiftUI
import WebKit

struct CaptchaWebViewContainer: UIViewControllerRepresentable {
    let sessionToken: String
    let onSuccess: (String) -> Void
    let onDismiss: () -> Void

    func makeUIViewController(context: Context) -> CaptchaWebViewController {
        let controller = CaptchaWebViewController(
            sessionToken: sessionToken,
            onSuccess: onSuccess,
            onDismiss: onDismiss
        )
        return controller
    }

    func updateUIViewController(_ uiViewController: CaptchaWebViewController, context: Context) {}
}

class CaptchaWebViewController: UIViewController, WKScriptMessageHandler {
    private let webView = WKWebView()
    private let sessionToken: String
    private let onSuccess: (String) -> Void
    private let onDismiss: () -> Void

    init(sessionToken: String, onSuccess: @escaping (String) -> Void, onDismiss: @escaping () -> Void) {
        self.sessionToken = sessionToken
        self.onSuccess = onSuccess
        self.onDismiss = onDismiss
        super.init(nibName: nil, bundle: nil)
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    override func viewDidLoad() {
        super.viewDidLoad()

        view.backgroundColor = .white
        webView.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(webView)

        NSLayoutConstraint.activate([
            webView.topAnchor.constraint(equalTo: view.topAnchor),
            webView.bottomAnchor.constraint(equalTo: view.bottomAnchor),
            webView.leftAnchor.constraint(equalTo: view.leftAnchor),
            webView.rightAnchor.constraint(equalTo: view.rightAnchor)
        ])

        // Setup close button
        let closeButton = UIBarButtonItem(
            barButtonSystemItem: .done,
            target: self,
            action: #selector(closeTapped)
        )
        navigationItem.rightBarButtonItem = closeButton

        // Add JavaScript bridge
        webView.configuration.userContentController.add(self, name: "captchaHandler")

        // Load VK captcha page
        loadCaptchaPage()
    }

    private func loadCaptchaPage() {
        // Build captcha URL with session token
        let captchaURL = "https://api.vk.ru/method/auth.getWebauthnChallenge?session_token=\(sessionToken)"

        if let url = URL(string: captchaURL) {
            let request = URLRequest(url: url)
            webView.load(request)
        }
    }

    @objc private func closeTapped() {
        onDismiss()
    }

    // Handle messages from JavaScript
    func userContentController(
        _ userContentController: WKUserContentController,
        didReceive message: WKScriptMessage
    ) {
        if message.name == "captchaHandler" {
            if let body = message.body as? [String: Any],
               let successToken = body["success_token"] as? String {
                onSuccess(successToken)
            }
        }
    }
}

// Simplified version: just open VK captcha in WebView
struct SimpleCaptchaWebView: UIViewRepresentable {
    let sessionToken: String
    let onDismiss: () -> Void
    let onTokenReceived: (String) -> Void

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        let webView = WKWebView(frame: .zero, configuration: config)

        // Inject script to detect when captcha is solved
        let script = """
        document.addEventListener('captchaSuccess', function(e) {
            window.webkit.messageHandlers.captcha.postMessage({
                success_token: e.detail.token
            });
        });
        """
        config.userContentController.addUserScript(WKUserScript(source: script, injectionTime: .atDocumentEnd, forMainFrameOnly: true))
        config.userContentController.add(context.coordinator, name: "captcha")

        // Load VK captcha page
        if let url = URL(string: "https://vk.com/") {
            webView.load(URLRequest(url: url))
        }

        return webView
    }

    func updateUIView(_ uiView: WKWebView, context: Context) {}

    func makeCoordinator() -> Coordinator {
        Coordinator(onDismiss: onDismiss, onTokenReceived: onTokenReceived)
    }

    class Coordinator: NSObject, WKScriptMessageHandler {
        let onDismiss: () -> Void
        let onTokenReceived: (String) -> Void

        init(onDismiss: @escaping () -> Void, onTokenReceived: @escaping (String) -> Void) {
            self.onDismiss = onDismiss
            self.onTokenReceived = onTokenReceived
        }

        func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
            if let dict = message.body as? [String: String], let token = dict["success_token"] {
                onTokenReceived(token)
            }
        }
    }
}
