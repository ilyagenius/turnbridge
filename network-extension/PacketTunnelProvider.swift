//
//  Created by nullcstring.
//

import NetworkExtension
import WireGuardKit
import WireGuardKitGo
import os

let sharedLogger = Logger(subsystem: "com.netlab.TurnBridge.network-extension", category: "wgtunnel")

// C callback: Go calls this when automatic PoW fails and a WebView is needed.
// We store the URL in App Group UserDefaults so the main app can observe it.
private let goProxyCaptchaCallback: @convention(c) (UnsafeMutableRawPointer?, UnsafePointer<CChar>?) -> Void = { _, redirectUriCStr in
    let redirectUri = redirectUriCStr.map { String(cString: $0) } ?? ""
    guard !redirectUri.isEmpty else { return }
    sharedLogger.log("[Captcha] WebView fallback requested: \(redirectUri, privacy: .public)")
    SharedLogger.info("Captcha WebView needed: \(redirectUri)", source: .tunnel)
    if let groupID = SharedLogger.appGroupID,
       let defaults = UserDefaults(suiteName: groupID) {
        defaults.set(redirectUri, forKey: "tb_captcha_url")
        defaults.synchronize()
    }
}

enum PacketTunnelProviderError: String, Error {
    case invalidProtocolConfiguration
    case cantParseWgQuickConfig
    case captchaRequired
}

private let goProxyCLoggerCallback: @convention(c) (UnsafeMutableRawPointer?, Int32, UnsafePointer<CChar>?) -> Void = { context, level, messageCStr in
    guard let cStr = messageCStr else { return }
    let message = String(cString: cStr).trimmingCharacters(in: .newlines)

    // Detect tunnel transport layer connected
    if message.contains("Established DTLS connection") || message.contains("Established Jazz WebRTC data channel") || message.contains("Established Telemost WebRTC data channel") {
        if let groupID = SharedLogger.appGroupID,
           let defaults = UserDefaults(suiteName: groupID) {
            defaults.set(true, forKey: "tb_dtls_connected")
            defaults.synchronize()
        }
    }

    if level == 1 {
        sharedLogger.error("[TP]: \(message, privacy: .public)")
        SharedLogger.error(message, source: .tunnel)
    } else {
        sharedLogger.log("[TP]: \(message, privacy: .public)")
        SharedLogger.info(message, source: .tunnel)
    }
}

class PacketTunnelProvider: NEPacketTunnelProvider {

    private lazy var adapter: WireGuardAdapter = {
        return WireGuardAdapter(with: self) { [weak self] _, message in
            sharedLogger.log("[WG]: \(message, privacy: .public)")
            SharedLogger.info(message, source: .wireguard)
        }
    }()

    
    override func startTunnel(options: [String : NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        sharedLogger.log("=== Starting tunnel ===")
        SharedLogger.info("Starting tunnel", source: .tunnel)

        guard let protocolConfiguration = self.protocolConfiguration as? NETunnelProviderProtocol,
              let providerConfiguration = protocolConfiguration.providerConfiguration else {
            sharedLogger.error("Invalid provider configuration")
            SharedLogger.error("Invalid provider configuration", source: .tunnel)
            completionHandler(PacketTunnelProviderError.invalidProtocolConfiguration)
            return
        }

        guard let wgQuickConfig = providerConfiguration["wgQuickConfig"] as? String else {
            sharedLogger.error("wgQuickConfig missing from provider configuration")
            SharedLogger.error("WireGuard config missing from provider configuration", source: .wireguard)
            completionHandler(PacketTunnelProviderError.cantParseWgQuickConfig)
            return
        }

        let tunnelConfiguration: TunnelConfiguration
        do {
            tunnelConfiguration = try TunnelConfiguration(fromWgQuickConfig: wgQuickConfig)
        } catch {
            sharedLogger.error("wg-quick config parse error: \(error.localizedDescription)")
            SharedLogger.error("Failed to parse WireGuard config: \(error.localizedDescription)", source: .wireguard)
            completionHandler(PacketTunnelProviderError.cantParseWgQuickConfig)
            return
        }

        guard var vkLink = providerConfiguration["vkLink"] as? String,
              let peerAddr = providerConfiguration["peerAddr"] as? String,
              let listenAddr = providerConfiguration["listenAddr"] as? String,
              let nValueInt = providerConfiguration["nValue"] as? Int else {
            sharedLogger.error("Missing proxy parameters in configuration")
            SharedLogger.error("Missing proxy parameters in configuration", source: .tunnel)
            completionHandler(PacketTunnelProviderError.invalidProtocolConfiguration)
            return
        }
        let nValue = Int32(nValueInt)
        let fallbackLink = providerConfiguration["fallbackLink"] as? String ?? ""
        let linkServer = providerConfiguration["linkServer"] as? String ?? ""

        // Check for auto-refreshed links from previous session
        if let groupID = SharedLogger.appGroupID,
           let defaults = UserDefaults(suiteName: groupID) {
            if vkLink.contains("salutejazz.ru"),
               let updated = defaults.string(forKey: "tb_updated_link_jazz"), !updated.isEmpty {
                SharedLogger.info("[LinkRefresh] Using updated Jazz link", source: .tunnel)
                vkLink = updated
            } else if vkLink.contains("telemost.yandex.ru"),
                      let updated = defaults.string(forKey: "tb_updated_link_telemost"), !updated.isEmpty {
                SharedLogger.info("[LinkRefresh] Using updated Telemost link", source: .tunnel)
                vkLink = updated
            }
        }

        SharedLogger.info("Peer: \(peerAddr), Listen: \(listenAddr), N: \(nValue)", source: .tunnel)
        SharedLogger.info("Starting TURN proxy...", source: .tunnel)

        ProxySetLogger(nil, goProxyCLoggerCallback)
        ProxySetCaptchaHandler(nil, goProxyCaptchaCallback)

        // Pass pre-solved captcha token if available from a previous WebView solve.
        if let groupID = SharedLogger.appGroupID,
           let defaults = UserDefaults(suiteName: groupID),
           let savedToken = defaults.string(forKey: "tb_captcha_success_token"), !savedToken.isEmpty {
            sharedLogger.log("[Captcha] Applying saved token for this connection")
            SharedLogger.info("Applying saved captcha token", source: .tunnel)
            savedToken.withCString { ProxySetCaptchaToken($0) }
            defaults.removeObject(forKey: "tb_captcha_success_token")
            defaults.synchronize()
        }

        DispatchQueue.global(qos: .userInteractive).async {
            StartProxy(vkLink, fallbackLink, peerAddr, listenAddr, nValue, linkServer)
        }

        DispatchQueue.global(qos: .userInteractive).async { [weak self] in
            let ready = ProxyWaitReady(45000)
            guard let self = self else { return }

            if ready == 0 {
                sharedLogger.error("Proxy transport timeout!")
                SharedLogger.error("Proxy transport timeout (45s)", source: .tunnel)
                completionHandler(PacketTunnelProviderError.invalidProtocolConfiguration)
                return
            }

            if ready == 2 {
                sharedLogger.log("[Captcha] Captcha required — failing tunnel for WebView")
                SharedLogger.info("Captcha required, aborting tunnel start", source: .tunnel)
                completionHandler(PacketTunnelProviderError.captchaRequired)
                return
            }

            SharedLogger.info("Proxy transport ready, starting WireGuard adapter...", source: .tunnel)
            self.adapter.start(tunnelConfiguration: tunnelConfiguration) { [weak self] adapterError in
                guard let self = self else { return }
                if let adapterError = adapterError {
                    sharedLogger.error("WireGuard adapter error: \(adapterError.localizedDescription)")
                    SharedLogger.error("WireGuard adapter failed: \(adapterError.localizedDescription)", source: .wireguard)
                } else {
                    let interfaceName = self.adapter.interfaceName ?? "unknown"
                    sharedLogger.log("Tunnel interface is \(interfaceName)")
                    SharedLogger.info("Tunnel up on interface \(interfaceName)", source: .wireguard)

                    // Fetch updated room links through the WG tunnel
                    self.fetchUpdatedLinks()
                }
                completionHandler(adapterError)
            }
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        sharedLogger.log("Stopping tunnel")
        SharedLogger.info("Stopping tunnel (reason: \(reason.rawValue))", source: .tunnel)

        StopProxy()
        SharedLogger.info("TURN proxy stopped", source: .tunnel)

        if let groupID = SharedLogger.appGroupID,
           let defaults = UserDefaults(suiteName: groupID) {
            defaults.set(false, forKey: "tb_dtls_connected")
            defaults.synchronize()
        }

        adapter.stop { [weak self] error in
            guard self != nil else { return }
            if let error = error {
                sharedLogger.error("Failed to stop WireGuard adapter: \(error.localizedDescription)")
                SharedLogger.error("WireGuard adapter stop failed: \(error.localizedDescription)", source: .wireguard)
            } else {
                SharedLogger.info("WireGuard adapter stopped", source: .wireguard)
            }
            SharedLogger.info("Tunnel stopped", source: .tunnel)
            completionHandler()

            #if os(macOS)
            // HACK: We have to kill the tunnel process ourselves because of a macOS bug
            exit(0)
            #endif
        }
    }
    

    override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)?) {
        guard let message = String(data: messageData, encoding: .utf8) else {
            completionHandler?(nil)
            return
        }

        // Fresh links from main app: "links:JSON"
        if message.hasPrefix("links:") {
            let json = String(message.dropFirst("links:".count))
            let tmpPath = NSTemporaryDirectory() + "tb_pending_links.json"
            do {
                try json.write(toFile: tmpPath, atomically: true, encoding: .utf8)
                SharedLogger.info("[LinkRefresh] Wrote links to \(tmpPath)", source: .tunnel)
            } catch {
                SharedLogger.error("[LinkRefresh] Failed to write links file: \(error)", source: .tunnel)
            }
            completionHandler?(nil)
            return
        }

        // Captcha token from WebView: "captcha:SUCCESS_TOKEN"
        if message.hasPrefix("captcha:") {
            let token = String(message.dropFirst("captcha:".count))
            sharedLogger.log("[Captcha] Received WebView token (\(token.count, privacy: .public) chars)")
            SharedLogger.info("Captcha WebView token received", source: .tunnel)
            // Clear the pending captcha URL so ContentView dismisses the sheet
            if let groupID = SharedLogger.appGroupID,
               let defaults = UserDefaults(suiteName: groupID) {
                defaults.removeObject(forKey: "tb_captcha_url")
                defaults.synchronize()
            }
            token.withCString { cToken in
                ProxySolveCaptcha(cToken)
            }
            completionHandler?(nil)
            return
        }

        guard message == "stats" else {
            completionHandler?(nil)
            return
        }
        adapter.getRuntimeConfiguration { configStr in
            var lastHandshakeSec: TimeInterval = 0
            var txBytes: Int64 = 0
            var rxBytes: Int64 = 0
            if let config = configStr {
                for line in config.components(separatedBy: "\n") {
                    if line.hasPrefix("last_handshake_time_sec="),
                       let val = TimeInterval(line.dropFirst("last_handshake_time_sec=".count)), val > 0 {
                        lastHandshakeSec = max(lastHandshakeSec, val)
                    } else if line.hasPrefix("tx_bytes="),
                              let val = Int64(line.dropFirst("tx_bytes=".count)) {
                        txBytes += val
                    } else if line.hasPrefix("rx_bytes="),
                              let val = Int64(line.dropFirst("rx_bytes=".count)) {
                        rxBytes += val
                    }
                }
            }
            let stats: [String: Any] = [
                "lastHandshake": lastHandshakeSec,
                "txBytes": txBytes,
                "rxBytes": rxBytes
            ]
            let data = try? JSONSerialization.data(withJSONObject: stats)
            completionHandler?(data)
        }
    }

    // MARK: - Link auto-refresh

    private func fetchUpdatedLinks() {
        DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + 3) { [weak self] in
            guard self != nil else { return }
            let url = "http://10.77.77.1:8080/links"
            guard let cResult = url.withCString({ ProxyFetchLinks($0) }) else {
                SharedLogger.info("[LinkRefresh] No links from server (NULL)", source: .tunnel)
                return
            }
            let json = String(cString: cResult)
            free(cResult)

            SharedLogger.info("[LinkRefresh] Received: \(json)", source: .tunnel)

            guard let data = json.data(using: .utf8),
                  let dict = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
                SharedLogger.error("[LinkRefresh] Failed to parse JSON", source: .tunnel)
                return
            }

            guard let groupID = SharedLogger.appGroupID,
                  let defaults = UserDefaults(suiteName: groupID) else { return }

            if let jazz = dict["jazz"] as? String, !jazz.isEmpty {
                defaults.set(jazz, forKey: "tb_updated_link_jazz")
            }
            if let telemost = dict["telemost"] as? String, !telemost.isEmpty {
                defaults.set(telemost, forKey: "tb_updated_link_telemost")
            }
            defaults.synchronize()
            SharedLogger.info("[LinkRefresh] Links saved to App Group", source: .tunnel)
        }
    }

    override func sleep(completionHandler: @escaping () -> Void) {
        // Add code here to get ready to sleep.
        completionHandler()
    }

    override func wake() {
        // Add code here to wake up.
    }
}
