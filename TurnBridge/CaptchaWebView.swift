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
        var request = URLRequest(url: url)
        // Match the User-Agent the Go code used when fetching the captcha page,
        // so VK's session cookies are considered valid.
        request.setValue(
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36",
            forHTTPHeaderField: "User-Agent"
        )
        webView.load(request)
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
        // Navigation errors are non-fatal; the user can see the error in the WebView.
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
