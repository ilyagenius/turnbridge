import SwiftUI
import WebKit

struct CaptchaWebViewSimple: UIViewRepresentable {
    let onDismiss: () -> Void
    let onSuccess: () -> Void

    func makeUIView(context: Context) -> WKWebView {
        let webView = WKWebView()
        webView.navigationDelegate = context.coordinator

        // Load simple HTML page with iframe to VK captcha
        let htmlString = """
        <html>
        <head>
            <meta name="viewport" content="width=device-width, initial-scale=1">
            <style>
                body { margin: 0; padding: 10px; background: white; font-family: -apple-system; }
                .container { text-align: center; }
                h2 { color: #333; }
                iframe { width: 100%; height: 600px; border: none; }
            </style>
        </head>
        <body>
            <div class="container">
                <h2>Please solve the CAPTCHA</h2>
                <p>Verify that you are not a bot</p>
                <iframe id="captcha" src="https://vk.com/captcha_page"></iframe>
            </div>
            <script>
                // Monitor for captcha completion
                window.addEventListener('message', function(e) {
                    if (e.data && e.data.type === 'captcha_success') {
                        window.webkit.messageHandlers.captchaHandler.postMessage({
                            success: true,
                            token: e.data.token
                        });
                    }
                });
            </script>
        </body>
        </html>
        """

        webView.loadHTMLString(htmlString, baseURL: URL(string: "https://vk.com"))

        // Add message handler
        webView.configuration.userContentController.add(context.coordinator, name: "captchaHandler")

        return webView
    }

    func updateUIView(_ uiView: WKWebView, context: Context) {}

    func makeCoordinator() -> Coordinator {
        Coordinator(onDismiss: onDismiss, onSuccess: onSuccess)
    }

    class Coordinator: NSObject, WKNavigationDelegate, WKScriptMessageHandler {
        let onDismiss: () -> Void
        let onSuccess: () -> Void

        init(onDismiss: @escaping () -> Void, onSuccess: @escaping () -> Void) {
            self.onDismiss = onDismiss
            self.onSuccess = onSuccess
        }

        func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
            if message.name == "captchaHandler" {
                if let dict = message.body as? [String: Any], dict["success"] as? Bool == true {
                    onSuccess()
                }
            }
        }
    }
}
