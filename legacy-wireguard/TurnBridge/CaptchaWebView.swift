import SwiftUI
import WebKit

// MARK: - VKCaptchaSheet
// Loads the VK Smart Captcha page (redirect_uri from Go) in a WKWebView.
// Injects JavaScript that intercepts the captchaNotRobot.check API response
// and extracts the success_token, then sends it back via a message handler.
//
// Flow:
//   1. Go exhausts PoW attempts → calls Swift callback with redirect_uri
//   2. ContentView shows this sheet
//   3. VK's JS runs the PoW automatically (real browser JS, usually passes)
//   4. If VK shows image captcha, user solves it manually
//   5. JS intercepts the success_token and posts it to Swift
//   6. Swift sends "captcha:TOKEN" to the Network Extension
//   7. Go retries calls.getAnonymousToken with the token

struct VKCaptchaSheet: UIViewControllerRepresentable {
    let redirectUri: String
    let onToken: (String) -> Void
    let onDismiss: () -> Void

    func makeUIViewController(context: Context) -> VKCaptchaViewController {
        VKCaptchaViewController(redirectUri: redirectUri, onToken: onToken, onDismiss: onDismiss)
    }

    func updateUIViewController(_ uiViewController: VKCaptchaViewController, context: Context) {}
}

class VKCaptchaViewController: UIViewController {
    private var webView: WKWebView!
    private let redirectUri: String
    private let onToken: (String) -> Void
    private let onDismiss: () -> Void
    private var tokenDelivered = false

    init(redirectUri: String, onToken: @escaping (String) -> Void, onDismiss: @escaping () -> Void) {
        self.redirectUri = redirectUri
        self.onToken = onToken
        self.onDismiss = onDismiss
        super.init(nibName: nil, bundle: nil)
    }

    required init?(coder: NSCoder) { fatalError() }

    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = .systemBackground
        setupWebView()
        loadCaptchaPage()
    }

    private func setupWebView() {
        let config = WKWebViewConfiguration()

        // Inject JS before page scripts run.
        // Wraps fetch + XHR to intercept captchaNotRobot.check → success_token.
        let js = """
        (function() {
            function trySend(obj) {
                try {
                    var r = obj && obj.response;
                    if (r && r.success_token) {
                        window.webkit.messageHandlers.vkCaptchaResult.postMessage({
                            success_token: r.success_token
                        });
                    }
                } catch(e) {}
            }

            // Intercept fetch
            var origFetch = window.fetch;
            window.fetch = function() {
                return origFetch.apply(this, arguments).then(function(resp) {
                    var clone = resp.clone();
                    clone.json().then(function(data) { trySend(data); }).catch(function(){});
                    return resp;
                });
            };

            // Intercept XHR
            var origOpen = XMLHttpRequest.prototype.open;
            var origSend = XMLHttpRequest.prototype.send;
            XMLHttpRequest.prototype.send = function() {
                this.addEventListener('load', function() {
                    try { trySend(JSON.parse(this.responseText)); } catch(e) {}
                });
                origSend.apply(this, arguments);
            };
        })();
        """

        let userScript = WKUserScript(source: js, injectionTime: .atDocumentStart, forMainFrameOnly: false)
        config.userContentController.addUserScript(userScript)
        config.userContentController.add(MessageHandler(owner: self), name: "vkCaptchaResult")

        webView = WKWebView(frame: .zero, configuration: config)
        webView.translatesAutoresizingMaskIntoConstraints = false
        webView.navigationDelegate = self
        // Set a consistent UA for all requests the webView makes (including JS fetch/XHR),
        // not just the initial URLRequest. Using a mobile Chrome UA for best compatibility
        // with VK's captcha widget on iOS.
        webView.customUserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/136.0.7103.56 Mobile/15E148 Safari/604.1"
        view.addSubview(webView)

        NSLayoutConstraint.activate([
            webView.topAnchor.constraint(equalTo: view.topAnchor),
            webView.bottomAnchor.constraint(equalTo: view.bottomAnchor),
            webView.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            webView.trailingAnchor.constraint(equalTo: view.trailingAnchor),
        ])
    }

    private func loadCaptchaPage() {
        guard let url = URL(string: redirectUri) else {
            onDismiss()
            return
        }
        webView.load(URLRequest(url: url))
    }

    func deliverToken(_ token: String) {
        guard !tokenDelivered else { return }
        tokenDelivered = true
        DispatchQueue.main.async { self.onToken(token) }
    }

    // MARK: - WKScriptMessageHandler bridge
    class MessageHandler: NSObject, WKScriptMessageHandler {
        weak var owner: VKCaptchaViewController?
        init(owner: VKCaptchaViewController) { self.owner = owner }

        func userContentController(_ userContentController: WKUserContentController,
                                   didReceive message: WKScriptMessage) {
            guard message.name == "vkCaptchaResult",
                  let body = message.body as? [String: Any],
                  let token = body["success_token"] as? String,
                  !token.isEmpty else { return }
            owner?.deliverToken(token)
        }
    }
}

// MARK: - WKNavigationDelegate
extension VKCaptchaViewController: WKNavigationDelegate {
    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        showError(error)
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        showError(error)
    }

    private func showError(_ error: Error) {
        let html = "<html><body style='font-family:-apple-system;padding:20px;color:#c00'>" +
                   "<b>Failed to load captcha page</b><br><br>\(error.localizedDescription)" +
                   "</body></html>"
        webView.loadHTMLString(html, baseURL: nil)
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        // Look for a redirect that carries the success_token in the URL (some VK captcha variants).
        if let url = navigationAction.request.url,
           let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
           let token = components.queryItems?.first(where: { $0.name == "success_token" })?.value,
           !token.isEmpty {
            deliverToken(token)
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }
}
