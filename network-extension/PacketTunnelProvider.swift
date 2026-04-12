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

    private var cacheRefreshTimer: DispatchSourceTimer?
    private var activeProvider: ProviderType = .vk
    private var activeServerID: String = ""
    private var usedDirectPath: Bool = false

    // Original connection params for in-tunnel bootstrap restart
    private var savedVkLink: String = ""
    private var savedFallbackLink: String = ""
    private var savedPeerAddr: String = ""
    private var savedListenAddr: String = ""
    private var savedNValue: Int32 = 1
    private var savedLinkServer: String = ""
    private var savedTunnelConfig: TunnelConfiguration?

    
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

        guard let vkLinkRaw = providerConfiguration["vkLink"] as? String,
              let peerAddr = providerConfiguration["peerAddr"] as? String,
              let listenAddr = providerConfiguration["listenAddr"] as? String,
              let nValueInt = providerConfiguration["nValue"] as? Int else {
            sharedLogger.error("Missing proxy parameters in configuration")
            SharedLogger.error("Missing proxy parameters in configuration", source: .tunnel)
            completionHandler(PacketTunnelProviderError.invalidProtocolConfiguration)
            return
        }
        let nValue = Int32(nValueInt)
        let fallbackLinkRaw = providerConfiguration["fallbackLink"] as? String ?? ""
        let linkServer = providerConfiguration["linkServer"] as? String ?? ""

        // Save original params for possible in-tunnel bootstrap restart
        self.savedVkLink = vkLinkRaw
        self.savedFallbackLink = fallbackLinkRaw
        self.savedPeerAddr = peerAddr
        self.savedListenAddr = listenAddr
        self.savedNValue = nValue
        self.savedLinkServer = linkServer
        self.savedTunnelConfig = tunnelConfiguration

        // --- Fast-connect path ---
        // If we have a fresh cached link for a supported provider, use it as the primary
        // link AND clear fallback. That routes StartProxy through runDirectProxy, skipping
        // the ~30s VK bootstrap entirely. On failure we detect timeout, invalidate cache,
        // and let the system auto-reconnect into the full bootstrap flow.
        //
        // Prefer the explicit providerType from the profile (set by setup.sh). Fall back
        // to detect-from-link for older profiles that predate the field, or profiles with
        // an empty turn link (Jazz/MAX profiles where the link is only fetched in-tunnel).
        let providerTypeFromConfig = (providerConfiguration["providerType"] as? String) ?? ""
        let providerType: ProviderType = {
            if !providerTypeFromConfig.isEmpty,
               let explicit = ProviderType(rawValue: providerTypeFromConfig) {
                return explicit
            }
            return ProviderType.detect(from: vkLinkRaw)
        }()
        self.activeProvider = providerType
        self.activeServerID = peerAddr

        // Optional manual override: force bootstrap (set by cancelTunnelWithError recovery).
        var forceBootstrap = false
        if let groupID = SharedLogger.appGroupID,
           let defaults = UserDefaults(suiteName: groupID),
           defaults.bool(forKey: "tb_force_bootstrap") {
            SharedLogger.info("[FastConnect] force_bootstrap flag set, using VK bootstrap", source: .tunnel)
            defaults.removeObject(forKey: "tb_force_bootstrap")
            defaults.synchronize()
            forceBootstrap = true
        }

        var vkLink = vkLinkRaw
        var fallbackLink = fallbackLinkRaw
        var useDirectPath = false

        // Cache is keyed by the server's peer address so that each profile (each
        // physical server) has its own cached link. Previously the cache was
        // keyed only by provider type, which meant connecting to Server 2 with
        // a Jazz profile would pick up Server 1's Jazz link and fail WG
        // handshake with `invalid mac1` because the bridge/room/keys didn't
        // match. peerAddr is the stable per-server identifier.
        let serverID = peerAddr

        if !forceBootstrap, providerType == .max {
            // MAX links are static (set once by setup.sh, not server-refreshed).
            // Bootstrap through VK is pointless — always go direct with the cached
            // link if any, otherwise with the link from the profile.
            let linkToUse = LinkCache.shared.get(server: serverID, provider: .max)?.link ?? vkLinkRaw
            SharedLogger.info("[FastConnect] MAX provider — always direct (no bootstrap)", source: .tunnel)
            vkLink = linkToUse
            fallbackLink = ""
            useDirectPath = true
        } else if !forceBootstrap,
                  providerType.supportsFastConnect,
                  let cached = LinkCache.shared.get(server: serverID, provider: providerType),
                  cached.isFresh {
            let age = Int(Date().timeIntervalSince(cached.fetchedAt))
            SharedLogger.info("[FastConnect] Using cached \(providerType.rawValue) link (age=\(age)s) for server \(serverID), skipping VK bootstrap", source: .tunnel)
            vkLink = cached.link
            fallbackLink = ""  // critical: empty fallback → runDirectProxy in Go
            useDirectPath = true
        } else if providerType.supportsFastConnect {
            SharedLogger.info("[FastConnect] No fresh cache for \(providerType.rawValue)@\(serverID), using VK bootstrap", source: .tunnel)
        }
        self.usedDirectPath = useDirectPath

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

        // Pass App Group container path to Go for file-based IPC
        if let containerURL = SharedLogger.logFileURL?.deletingLastPathComponent() {
            let cpath = containerURL.path
            SharedLogger.info("[DEBUG] App Group container: \(cpath)", source: .tunnel)
            cpath.withCString { ProxySetContainerPath($0) }
        }

        DispatchQueue.global(qos: .userInteractive).async {
            StartProxy(vkLink, fallbackLink, peerAddr, listenAddr, nValue, linkServer)
        }

        // Direct path: TURN allocate should complete in a few seconds. Use a short
        // timeout so that a dead cached link is detected fast and we can fall back
        // to bootstrap on the next attempt. Bootstrap path keeps the original 45s
        // since Phase 2 (in-tunnel fetch) can legitimately take ~30s.
        let waitTimeoutMs: Int32 = useDirectPath ? 8000 : 45000

        DispatchQueue.global(qos: .userInteractive).async { [weak self] in
            let ready = ProxyWaitReady(waitTimeoutMs)
            guard let self = self else { return }

            if ready == 0 {
                sharedLogger.error("Proxy transport timeout!")
                SharedLogger.error("Proxy transport timeout (\(waitTimeoutMs)ms)", source: .tunnel)
                if useDirectPath {
                    LinkCache.shared.invalidate(server: serverID, provider: providerType)
                    // For MAX, bootstrap through VK won't recover — the link is
                    // static and server-side doesn't refresh it. User needs to
                    // refresh login_token / call link manually via setup.sh.
                    if providerType != .max {
                        SharedLogger.info("[FastConnect] Direct path failed, will use bootstrap on retry", source: .tunnel)
                        if let groupID = SharedLogger.appGroupID,
                           let defaults = UserDefaults(suiteName: groupID) {
                            defaults.set(true, forKey: "tb_force_bootstrap")
                            defaults.synchronize()
                        }
                    } else {
                        SharedLogger.error("[FastConnect] MAX link dead — refresh login_token/call via setup.sh", source: .tunnel)
                    }
                }
                StopProxy()
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

                    // Fetch updated room links through the WG tunnel and start
                    // periodic refresh so the cache stays warm while the tunnel is up.
                    self.fetchUpdatedLinks()
                    self.startCacheRefreshTimer()

                    // Fast-connect health check: if WG handshake doesn't complete
                    // within 3s, the cached link likely points to a dead room
                    // (bridge was restarted into a new room by link-refresh).
                    // Invalidate cache and cancel so the system auto-reconnects
                    // through VK bootstrap with a fresh link.
                    if useDirectPath {
                        self.scheduleHandshakeCheck(server: serverID, provider: providerType)
                    }
                }
                completionHandler(adapterError)
            }
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        sharedLogger.log("Stopping tunnel")
        SharedLogger.info("Stopping tunnel (reason: \(reason.rawValue))", source: .tunnel)

        stopCacheRefreshTimer()
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
            SharedLogger.error("[IPC] handleAppMessage: failed to decode \(messageData.count) bytes as UTF-8", source: .tunnel)
            completionHandler?(nil)
            return
        }

        SharedLogger.info("[IPC] handleAppMessage: \(messageData.count) bytes, msg=\(message.prefix(60))", source: .tunnel)

        // Fresh links from main app: "links:JSON"
        if message.hasPrefix("links:") {
            let json = String(message.dropFirst("links:".count))

            // DEBUG: Write marker to confirm this branch executes
            SharedLogger.info("[LinkRefresh] Handler entered, json=\(json.prefix(80))...", source: .tunnel)

            // Write to App Group container (same dir as vpn_tunnel.log)
            if let containerURL = SharedLogger.logFileURL?.deletingLastPathComponent() {
                let linksPath = containerURL.appendingPathComponent("tb_pending_links.json").path
                do {
                    try json.write(toFile: linksPath, atomically: true, encoding: .utf8)
                    SharedLogger.info("[LinkRefresh] Wrote to App Group: \(linksPath)", source: .tunnel)
                } catch {
                    SharedLogger.error("[LinkRefresh] App Group write FAILED: \(error)", source: .tunnel)
                }
            } else {
                SharedLogger.error("[LinkRefresh] No App Group container!", source: .tunnel)
            }

            // Also write to temp dir (for comparison)
            let tmpPath = NSTemporaryDirectory() + "tb_pending_links.json"
            try? json.write(toFile: tmpPath, atomically: true, encoding: .utf8)
            SharedLogger.info("[LinkRefresh] Also wrote to temp: \(tmpPath)", source: .tunnel)

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

    // MARK: - Fast-connect handshake health check

    /// After a fast-connect (direct path) start, verify WG handshake completes
    /// within 3 seconds. If the cached link pointed at a dead room (bridge was
    /// rotated by link-refresh), no valid WG response will arrive. In that case
    /// invalidate the cache and tear down the tunnel so the system auto-reconnects
    /// through VK bootstrap with a fresh link.
    private func scheduleHandshakeCheck(server: String, provider: ProviderType) {
        // 8s timeout: when n workers join a WebRTC room simultaneously, the SFU
        // must renegotiate the bridge's subscriber PC to include the new publishers.
        // The first WG handshake initiation may be lost (bridge not subscribed yet).
        // WG retries at 5s, by which point the bridge is ready. 8s gives enough
        // margin for SFU renegotiation + WG retry + bridge response round-trip.
        DispatchQueue.global(qos: .userInteractive).asyncAfter(deadline: .now() + 8.0) { [weak self] in
            guard let self = self else { return }
            self.adapter.getRuntimeConfiguration { [weak self] configStr in
                guard let self = self else { return }
                guard let configStr = configStr else {
                    SharedLogger.error("[FastConnect] Health check: no runtime config", source: .tunnel)
                    self.handleDeadDirectPath(server: server, provider: provider)
                    return
                }
                // Parse last_handshake_time_sec from UAPI output
                var hasHandshake = false
                for line in configStr.split(separator: "\n") {
                    if line.hasPrefix("last_handshake_time_sec="),
                       let val = Int64(line.dropFirst("last_handshake_time_sec=".count)),
                       val > 0 {
                        hasHandshake = true
                        break
                    }
                }
                if hasHandshake {
                    SharedLogger.info("[FastConnect] Health check passed — WG handshake OK", source: .tunnel)
                } else {
                    SharedLogger.warning("[FastConnect] Health check FAILED — no WG handshake after 8s, cached link is dead", source: .tunnel)
                    self.handleDeadDirectPath(server: server, provider: provider)
                }
            }
        }
    }

    private func handleDeadDirectPath(server: String, provider: ProviderType) {
        LinkCache.shared.invalidate(server: server, provider: provider)
        SharedLogger.info("[FastConnect] Cache invalidated for \(provider.rawValue)@\(server), restarting via bootstrap in-tunnel", source: .tunnel)

        // For MAX, bootstrap through VK won't help — the link is static.
        guard provider != .max else {
            SharedLogger.error("[FastConnect] MAX link dead — refresh login_token/call via setup.sh", source: .tunnel)
            self.cancelTunnelWithError(PacketTunnelProviderError.invalidProtocolConfiguration)
            return
        }

        // In-tunnel bootstrap: stop current proxy + WG, restart with VK
        // bootstrap path. iOS sees "reasserting" (briefly reconnecting),
        // not "disconnected", so the user doesn't have to tap Connect again.
        self.reasserting = true
        self.stopCacheRefreshTimer()

        self.adapter.stop { [weak self] _ in
            guard let self = self else { return }
            StopProxy()
            SharedLogger.info("[FastConnect] Stopped dead proxy, waiting for port release...", source: .tunnel)

            let vkLink = self.savedVkLink
            let fallback = self.savedFallbackLink
            let peer = self.savedPeerAddr
            let listen = self.savedListenAddr
            let n = self.savedNValue
            let linkSrv = self.savedLinkServer

            // StopProxy cancels the Go context but goroutines release the UDP
            // listener asynchronously. Wait briefly for port 9000 to be freed
            // before starting the new proxy, otherwise bind fails with EADDRINUSE.
            DispatchQueue.global(qos: .userInteractive).asyncAfter(deadline: .now() + 1.5) {
                SharedLogger.info("[FastConnect] Starting VK bootstrap...", source: .tunnel)
                StartProxy(vkLink, fallback, peer, listen, n, linkSrv)
            }

            DispatchQueue.global(qos: .userInteractive).async { [weak self] in
                let ready = ProxyWaitReady(45000)
                guard let self = self else { return }

                if ready == 0 {
                    SharedLogger.error("[FastConnect] Bootstrap also failed", source: .tunnel)
                    self.reasserting = false
                    self.cancelTunnelWithError(PacketTunnelProviderError.invalidProtocolConfiguration)
                    return
                }

                guard let tunnelConfig = self.savedTunnelConfig else {
                    SharedLogger.error("[FastConnect] No saved tunnel config", source: .tunnel)
                    self.reasserting = false
                    self.cancelTunnelWithError(PacketTunnelProviderError.invalidProtocolConfiguration)
                    return
                }

                SharedLogger.info("[FastConnect] Bootstrap proxy ready, restarting WG...", source: .tunnel)
                self.adapter.start(tunnelConfiguration: tunnelConfig) { [weak self] error in
                    guard let self = self else { return }
                    self.reasserting = false
                    if let error = error {
                        SharedLogger.error("[FastConnect] WG restart failed: \(error)", source: .tunnel)
                        self.cancelTunnelWithError(error)
                    } else {
                        SharedLogger.info("[FastConnect] Bootstrap successful, tunnel restored", source: .tunnel)
                        self.fetchUpdatedLinks()
                        self.startCacheRefreshTimer()
                    }
                }
            }
        }
    }

    // MARK: - Link auto-refresh

    /// Initial fetch ~3s after tunnel is up.
    private func fetchUpdatedLinks() {
        DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + 3) { [weak self] in
            self?.refreshLinkCacheFromServer()
        }
    }

    /// Periodic in-tunnel refresh: pulls fresh links through 10.77.77.1:8080 every
    /// 10 minutes and writes them to the LinkCache file. This keeps the cache
    /// warmer than its TTL so subsequent connects take the direct path.
    private func startCacheRefreshTimer() {
        stopCacheRefreshTimer()
        let timer = DispatchSource.makeTimerSource(queue: DispatchQueue.global(qos: .utility))
        timer.schedule(deadline: .now() + 600, repeating: 600)
        timer.setEventHandler { [weak self] in
            self?.refreshLinkCacheFromServer()
        }
        timer.resume()
        self.cacheRefreshTimer = timer
        SharedLogger.info("[LinkRefresh] Cache refresh timer started (10 min)", source: .tunnel)
    }

    private func stopCacheRefreshTimer() {
        cacheRefreshTimer?.cancel()
        cacheRefreshTimer = nil
    }

    private func refreshLinkCacheFromServer() {
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

        let server = self.activeServerID
        guard !server.isEmpty else {
            SharedLogger.error("[LinkRefresh] activeServerID empty, can't write cache", source: .tunnel)
            return
        }
        if let jazz = dict["jazz"] as? String, !jazz.isEmpty {
            LinkCache.shared.set(server: server, provider: .jazz, link: jazz)
        }
        if let telemost = dict["telemost"] as? String, !telemost.isEmpty {
            LinkCache.shared.set(server: server, provider: .telemost, link: telemost)
        }
        if let max = dict["max"] as? String, !max.isEmpty {
            LinkCache.shared.set(server: server, provider: .max, link: max)
        }
        SharedLogger.info("[LinkRefresh] Cache updated for \(server)", source: .tunnel)
    }

    override func sleep(completionHandler: @escaping () -> Void) {
        completionHandler()
    }

    override func wake() {
        // Immediate refresh on wake to catch any server-side rotation that
        // happened while the device was asleep.
        DispatchQueue.global(qos: .utility).async { [weak self] in
            self?.refreshLinkCacheFromServer()
        }
    }
}
