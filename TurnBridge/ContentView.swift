import SwiftUI
import NetworkExtension
import Network
import Combine

struct SettingsSheet: Identifiable {
    let id = UUID()
    let profileID: UUID
    let isNew: Bool
}

// MARK: - Connection Timer

class ConnectionTimer: ObservableObject {
    @Published var elapsed: TimeInterval = 0
    private var timer: Timer?
    private var startDate: Date?

    func start() {
        startDate = Date(); elapsed = 0; timer?.invalidate()
        timer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            guard let self, let s = self.startDate else { return }
            self.elapsed = Date().timeIntervalSince(s)
        }
    }
    func stop() { timer?.invalidate(); timer = nil; startDate = nil; elapsed = 0 }

    var formatted: String {
        let h = Int(elapsed) / 3600, m = (Int(elapsed) % 3600) / 60, s = Int(elapsed) % 60
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, s) : String(format: "%02d:%02d", m, s)
    }
}

// MARK: - Tunnel Stats

struct TunnelStats {
    var lastHandshake: Date? = nil
    var txBytes: Int64 = 0
    var rxBytes: Int64 = 0
    var dtlsConnected: Bool = false

    var handshakeLabel: String {
        guard let d = lastHandshake else { return "—" }
        let ago = Int(Date().timeIntervalSince(d))
        if ago < 60 { return "\(ago)s ago" }
        if ago < 3600 { return "\(ago/60)m ago" }
        return "\(ago/3600)h ago"
    }

    static func formatBytes(_ bytes: Int64) -> String {
        let kb = Double(bytes) / 1024
        if kb < 1 { return "—" }
        if kb < 1024 { return String(format: "%.0f KB", kb) }
        let mb = kb / 1024
        if mb < 1024 { return String(format: "%.1f MB", mb) }
        return String(format: "%.2f GB", mb / 1024)
    }
}

// MARK: - Provider

enum TunnelProvider {
    case jazz, vk, wb, unknown
    var label: String { switch self { case .jazz: return "Jazz"; case .vk: return "VK"; case .wb: return "WB"; case .unknown: return "—" } }
    var icon: String { switch self { case .jazz: return "waveform"; case .vk: return "bubble.left.and.bubble.right"; case .wb: return "shippingbox"; case .unknown: return "questionmark.circle" } }
    var color: Color { switch self { case .jazz: return .purple; case .vk: return .blue; case .wb: return Color(red:0.9,green:0.3,blue:0.1); case .unknown: return .secondary } }
    static func detect(from link: String) -> TunnelProvider {
        let l = link.lowercased()
        if l.contains("salutejazz") || l.contains("jazz.sber") { return .jazz }
        if l == "wb" || l.contains("wildberries") || l.contains("stream.wb") { return .wb }
        if l.contains("vk.com") || l.contains("vk.ru") { return .vk }
        return .unknown
    }
}

// MARK: - Stat Card

struct StatCard: View {
    let icon: String
    let label: String
    let value: String
    let color: Color
    let theme: AppTheme

    var body: some View {
        VStack(spacing: 6) {
            Image(systemName: icon)
                .font(.system(size: 16, weight: .semibold))
                .foregroundColor(color)
                .shadow(color: theme.isCyber ? color.opacity(0.8) : .clear, radius: 6)
            Text(value)
                .font(.system(size: 13, weight: .bold, design: .monospaced))
                .foregroundColor(theme.isCyber ? .white : .primary)
                .lineLimit(1).minimumScaleFactor(0.6)
            Text(label)
                .font(.system(size: 10, weight: .medium))
                .foregroundColor(.secondary)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 12)
        .themedCard(theme, accent: color)
    }
}

// MARK: - Connection Orb

struct ConnectionOrb: View {
    let status: NEVPNStatus
    let theme: AppTheme
    @State private var pulse = false
    @State private var rotation: Double = 0

    private var mainColor: Color {
        switch status {
        case .connected: return theme.connectedColor
        case .connecting, .reasserting: return .orange
        case .disconnecting: return .red.opacity(0.8)
        default: return theme.isCyber ? Color(red:0.2,green:0.2,blue:0.4) : Color(.systemGray3)
        }
    }

    var body: some View {
        ZStack {
            if status == .connected {
                // Outer pulse
                Circle()
                    .stroke(mainColor.opacity(pulse ? 0 : 0.5), lineWidth: pulse ? 1 : 10)
                    .frame(width: pulse ? 190 : 140)
                    .animation(.easeOut(duration: 2.5).repeatForever(autoreverses: false), value: pulse)
                // Second pulse (cyber extra ring)
                if theme.isCyber {
                    Circle()
                        .stroke(theme.secondaryAccent.opacity(pulse ? 0 : 0.3), lineWidth: pulse ? 1 : 6)
                        .frame(width: pulse ? 210 : 155)
                        .animation(.easeOut(duration: 2.5).repeatForever(autoreverses: false).delay(0.5), value: pulse)
                }
            }
            if status == .connecting || status == .reasserting {
                Circle()
                    .trim(from: 0, to: 0.7)
                    .stroke(AngularGradient(colors: [mainColor, mainColor.opacity(0)], center: .center),
                            style: StrokeStyle(lineWidth: 3, lineCap: .round))
                    .frame(width: 152)
                    .rotationEffect(.degrees(rotation))
                    .animation(.linear(duration: 1).repeatForever(autoreverses: false), value: rotation)
            }
            // Main orb
            Circle()
                .fill(RadialGradient(colors: [mainColor.opacity(0.22), mainColor.opacity(0.04)],
                                     center: .center, startRadius: 8, endRadius: 65))
                .frame(width: 130)
                .overlay(Circle().stroke(mainColor.opacity(theme.isCyber ? 0.8 : 0.4), lineWidth: theme.isCyber ? 1.5 : 1).frame(width: 130))
                .shadow(color: theme.isCyber ? mainColor.opacity(0.6) : mainColor.opacity(0.2),
                        radius: theme.isCyber ? 24 : 8)

            Image(systemName: status == .connected ? "lock.shield.fill" : "lock.shield")
                .font(.system(size: 46, weight: .medium))
                .foregroundStyle(LinearGradient(colors: [mainColor, mainColor.opacity(0.7)], startPoint: .top, endPoint: .bottom))
                .shadow(color: mainColor.opacity(theme.isCyber ? 0.9 : 0.3), radius: theme.isCyber ? 16 : 6)
                .scaleEffect((status == .connecting || status == .reasserting) ? 0.95 : 1.0)
                .animation(.easeInOut(duration: 0.8).repeatForever().delay(0), value: status == .connecting)
        }
        .frame(width: 190, height: 190)
        .onAppear {
            pulse = true
            withAnimation(.linear(duration: 1).repeatForever(autoreverses: false)) { rotation = 360 }
        }
    }
}

// MARK: - Profile Row

struct ProfileRow: View {
    let profile: VPNProfile
    let isSelected: Bool
    let isConnected: Bool
    let theme: AppTheme
    let onTap: () -> Void
    let onEdit: () -> Void
    let onDelete: () -> Void
    let onPasteConfig: () -> Void
    // swipeActions applied externally in List

    var provider: TunnelProvider { TunnelProvider.detect(from: profile.vkLink) }

    var body: some View {
        HStack(spacing: 14) {
            ZStack {
                RoundedRectangle(cornerRadius: 10)
                    .fill(provider.color.opacity(0.15))
                    .frame(width: 40, height: 40)
                    .shadow(color: theme.isCyber ? provider.color.opacity(0.4) : .clear, radius: 8)
                Image(systemName: provider.icon)
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundColor(provider.color)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(profile.name)
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundColor(theme.isCyber ? .white : .primary)
                Text(provider.label + " · " + shortAddr(profile))
                    .font(.system(size: 12))
                    .foregroundColor(.secondary)
            }
            Spacer()
            if isSelected {
                Image(systemName: "checkmark.circle.fill")
                    .foregroundColor(theme.accent)
                    .shadow(color: theme.isCyber ? theme.accent.opacity(0.7) : .clear, radius: 6)
            }
        }
        .padding(.horizontal, 16).padding(.vertical, 12)
        .background(isSelected ? theme.accent.opacity(0.08) : Color.clear)
        .contentShape(Rectangle())
        .onTapGesture { if !isConnected { onTap() } }
    }

    private func shortAddr(_ p: VPNProfile) -> String {
        if p.peerAddr.isEmpty { return "Auto" }
        let h = p.peerAddr.components(separatedBy: ":").first ?? p.peerAddr
        return h.count > 18 ? String(h.prefix(16)) + "…" : h
    }
}

// MARK: - Theme Picker

struct ThemePicker: View {
    @Binding var theme: AppTheme

    var body: some View {
        HStack(spacing: 8) {
            ForEach(AppTheme.allCases, id: \.rawValue) { t in
                Button(action: { withAnimation(.spring(response: 0.3)) { theme = t } }) {
                    HStack(spacing: 5) {
                        Image(systemName: t.icon).font(.system(size: 11, weight: .semibold))
                        Text(t.displayName).font(.system(size: 12, weight: .semibold))
                    }
                    .padding(.horizontal, 12).padding(.vertical, 7)
                    .background(theme == t ? Color.primary.opacity(0.15) : Color.clear)
                    .clipShape(Capsule())
                    .overlay(Capsule().stroke(theme == t ? Color.primary.opacity(0.4) : Color.clear, lineWidth: 1))
                    .foregroundColor(theme == t ? .primary : .secondary)
                }
            }
        }
        .padding(4)
        .background(.ultraThinMaterial)
        .clipShape(Capsule())
    }
}

// MARK: - Main View

struct ContentView: View {
    var app: TurnBridge

    @State private var vpnStatus: NEVPNStatus = .disconnected
    @StateObject private var store = ProfileStore()
    @StateObject private var connTimer = ConnectionTimer()

    @AppStorage("appTheme") private var rawTheme: String = AppTheme.system.rawValue
    private var theme: AppTheme { AppTheme(rawValue: rawTheme) ?? .system }

    @State private var stats = TunnelStats()
    @State private var statsTimer: Timer? = nil

    @State private var showImportModal = false
    @State private var showingAlert = false
    @State private var alertTitle = ""
    @State private var alertMessage = ""
    @State private var settingsSheet: SettingsSheet?

    // Captcha WebView fallback
    @State private var captchaURL: String? = nil
    @State private var showCaptchaSheet = false

    private var selectedProvider: TunnelProvider {
        guard let p = store.selectedProfile else { return .unknown }
        return TunnelProvider.detect(from: p.vkLink)
    }

    var body: some View {
        NavigationStack {
            ZStack {
                // Background
                theme.pageBackground.ignoresSafeArea()
                if theme.isCyber { CyberGrid() }

                ZStack(alignment: .bottom) {
                    ScrollView {
                        VStack(spacing: 20) {
                            // ── Status Hero ──────────────────────────
                            VStack(spacing: 14) {
                                ConnectionOrb(status: vpnStatus, theme: theme)
                                    .padding(.top, 6)
                                VStack(spacing: 4) {
                                    Text(statusTitle)
                                        .font(.system(size: 22, weight: .bold))
                                        .foregroundColor(theme.isCyber ? .white : .primary)
                                        .shadow(color: theme.isCyber ? statusColor.opacity(0.6) : .clear, radius: 8)
                                    Text(statusSubtitle)
                                        .font(.system(size: 14, weight: .medium))
                                        .foregroundColor(.secondary)
                                }
                            }
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 20)
                            .themedCard(theme, accent: statusColor, cornerRadius: 24)
                            .padding(.horizontal, 16)

                            // ── Stats Grid ───────────────────────────
                            LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible())], spacing: 10) {
                                StatCard(icon: selectedProvider.icon, label: "Protocol",
                                         value: selectedProvider.label, color: selectedProvider.color, theme: theme)
                                StatCard(icon: "timer", label: "Uptime",
                                         value: vpnStatus == .connected ? connTimer.formatted : "—",
                                         color: theme.connectedColor, theme: theme)
                                StatCard(
                                    icon: stats.dtlsConnected ? "lock.shield.fill" : "lock.shield",
                                    label: "Tunnel",
                                    value: stats.dtlsConnected ? "Active" : "—",
                                    color: stats.dtlsConnected ? theme.connectedColor : .secondary,
                                    theme: theme
                                )
                                StatCard(
                                    icon: "clock.arrow.circlepath",
                                    label: "Handshake",
                                    value: vpnStatus == .connected ? stats.handshakeLabel : "—",
                                    color: theme.accent,
                                    theme: theme
                                )
                            }
                            .padding(.horizontal, 16)

                            // ── Traffic Row ──────────────────────────
                            if vpnStatus == .connected && (stats.txBytes > 0 || stats.rxBytes > 0) {
                                HStack(spacing: 10) {
                                    StatCard(icon: "arrow.up.circle.fill", label: "Upload",
                                             value: TunnelStats.formatBytes(stats.txBytes),
                                             color: .orange, theme: theme)
                                    StatCard(icon: "arrow.down.circle.fill", label: "Download",
                                             value: TunnelStats.formatBytes(stats.rxBytes),
                                             color: .cyan, theme: theme)
                                }
                                .padding(.horizontal, 16)
                                .transition(.opacity.combined(with: .move(edge: .top)))
                            }

                            // ── Profiles ─────────────────────────────
                            VStack(spacing: 0) {
                                HStack {
                                    Text("Profiles")
                                        .font(.system(size: 12, weight: .semibold))
                                        .foregroundColor(.secondary)
                                        .textCase(.uppercase)
                                    Spacer()
                                }
                                .padding(.horizontal, 20).padding(.bottom, 8)

                                if store.profiles.isEmpty {
                                    VStack(spacing: 8) {
                                        Image(systemName: "plus.circle.dashed")
                                            .font(.system(size: 28)).foregroundColor(.secondary)
                                        Text("No profiles yet")
                                            .font(.system(size: 13)).foregroundColor(.secondary)
                                    }
                                    .frame(maxWidth: .infinity).padding(.vertical, 28)
                                    .themedCard(theme)
                                    .padding(.horizontal, 16)
                                } else {
                                    List {
                                        ForEach(store.profiles) { profile in
                                            ProfileRow(
                                                profile: profile,
                                                isSelected: profile.id == store.selectedProfileID,
                                                isConnected: vpnStatus != .disconnected,
                                                theme: theme,
                                                onTap: {
                                                    withAnimation(.spring(response: 0.3)) {
                                                        store.selectedProfileID = profile.id; store.save()
                                                    }
                                                },
                                                onEdit: { settingsSheet = SettingsSheet(profileID: profile.id, isNew: false) },
                                                onDelete: { withAnimation { store.deleteProfile(profile.id) } },
                                                onPasteConfig: { pasteConfigIntoProfile(profile) }
                                            )
                                            .swipeActions(edge: .trailing, allowsFullSwipe: true) {
                                                Button(role: .destructive) {
                                                    withAnimation { store.deleteProfile(profile.id) }
                                                } label: { Label("Delete", systemImage: "trash") }
                                                if vpnStatus == .disconnected {
                                                    Button { settingsSheet = SettingsSheet(profileID: profile.id, isNew: false) }
                                                        label: { Label("Edit", systemImage: "pencil") }
                                                        .tint(.orange)
                                                    Button { pasteConfigIntoProfile(profile) }
                                                        label: { Label("Paste Config", systemImage: "doc.on.clipboard") }
                                                        .tint(.blue)
                                                }
                                            }
                                            .listRowBackground(
                                                profile.id == store.selectedProfileID
                                                    ? theme.accent.opacity(0.08)
                                                    : theme.cardBackground
                                            )
                                            .listRowInsets(EdgeInsets())
                                            .listRowSeparatorTint(Color.primary.opacity(0.08))
                                        }
                                    }
                                    .listStyle(.plain)
                                    .scrollDisabled(true)
                                    .frame(height: CGFloat(store.profiles.count) * 68)
                                    .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
                                    .overlay(RoundedRectangle(cornerRadius: 16, style: .continuous)
                                        .strokeBorder(Color.primary.opacity(theme.cardBorderOpacity), lineWidth: theme.cardBorderWidth))
                                    .shadow(color: theme.isCyber ? theme.accent.opacity(0.15) : .clear, radius: theme.glowRadius)
                                    .padding(.horizontal, 16)
                                }
                            }

                            // ── Theme Picker ─────────────────────────
                            HStack {
                                Spacer()
                                ThemePicker(theme: Binding(
                                    get: { theme },
                                    set: { rawTheme = $0.rawValue }
                                ))
                                Spacer()
                            }
                            .padding(.top, 4)

                            Color.clear.frame(height: 90)
                        }
                        .padding(.top, 8)
                    }

                    // ── Connect Button ────────────────────────────
                    VStack(spacing: 0) {
                        LinearGradient(colors: [theme.pageBackground.opacity(0), theme.pageBackground],
                                       startPoint: .top, endPoint: .bottom)
                        .frame(height: 24)

                        Button(action: toggleTunnel) {
                            HStack(spacing: 10) {
                                if vpnStatus == .connecting || vpnStatus == .disconnecting {
                                    ProgressView().progressViewStyle(CircularProgressViewStyle(tint: .white)).scaleEffect(0.85)
                                } else {
                                    Image(systemName: vpnStatus == .connected ? "stop.circle.fill" : "play.circle.fill")
                                        .font(.system(size: 19))
                                        .shadow(color: theme.isCyber ? .white.opacity(0.5) : .clear, radius: 6)
                                }
                                Text(buttonText).font(.system(size: 17, weight: .semibold))
                            }
                            .frame(maxWidth: .infinity).padding(.vertical, 17)
                            .background(LinearGradient(colors: buttonGradient, startPoint: .topLeading, endPoint: .bottomTrailing))
                            .foregroundColor(.white)
                            .clipShape(RoundedRectangle(cornerRadius: 18, style: .continuous))
                            .shadow(color: (buttonGradient.first ?? .clear).opacity(theme.isCyber ? 0.6 : 0.3),
                                    radius: theme.isCyber ? 18 : 10, x: 0, y: 6)
                        }
                        .disabled(vpnStatus == .connecting || vpnStatus == .disconnecting || store.selectedProfile == nil)
                        .padding(.horizontal, 16).padding(.bottom, 32)
                        .background(theme.pageBackground)
                    }
                }
            }
            .overlay { if showImportModal { importModalView } }
            .preferredColorScheme(theme.colorScheme)
            .navigationTitle("TurnBridge")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(theme == .system ? .automatic : .visible, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button(action: {
                        if vpnStatus == .disconnected {
                            withAnimation(.spring()) { showImportModal = true }
                        }
                    }) {
                        Image(systemName: "plus.circle.fill")
                            .font(.system(size: 20))
                            .foregroundColor(vpnStatus == .disconnected ? theme.accent : .secondary)
                            .shadow(color: theme.isCyber ? theme.accent.opacity(0.6) : .clear, radius: 8)
                    }
                }
                ToolbarItem(placement: .navigationBarTrailing) {
                    NavigationLink(destination: GlobalSettingsView()) {
                        Image(systemName: "gearshape.fill").font(.system(size: 18))
                    }
                }
            }
            .sheet(item: $settingsSheet) { sheet in
                NavigationStack { SettingsView(store: store, profileID: sheet.profileID, isNewProfile: sheet.isNew) }
            }
            .sheet(isPresented: $showCaptchaSheet, onDismiss: {
                // User closed the sheet without solving — clear the pending URL
                if let groupID = SharedLogger.appGroupID,
                   let defaults = UserDefaults(suiteName: groupID) {
                    defaults.removeObject(forKey: "tb_captcha_url")
                }
                captchaURL = nil
            }) {
                if let url = captchaURL {
                    NavigationStack {
                        VKCaptchaSheet(redirectUri: url, onToken: { token in
                            submitCaptchaToken(token)
                        }, onDismiss: {
                            showCaptchaSheet = false
                        })
                        .navigationTitle("Verify — Not a Robot")
                        .navigationBarTitleDisplayMode(.inline)
                        .toolbar {
                            ToolbarItem(placement: .navigationBarLeading) {
                                Button("Cancel") { showCaptchaSheet = false }
                            }
                        }
                    }
                }
            }
            .onAppear(perform: checkInitialStatus)
            .onReceive(NotificationCenter.default.publisher(for: .NEVPNStatusDidChange)) { notification in
                if let conn = notification.object as? NEVPNConnection {
                    handleStatusChange(conn.status)
                }
            }
            .alert(alertTitle, isPresented: $showingAlert) {
                Button("OK", role: .cancel) {}
            } message: { Text(alertMessage) }
        }
    }

    // MARK: - Computed

    private var statusColor: Color {
        switch vpnStatus {
        case .connected: return theme.connectedColor
        case .connecting, .reasserting: return .orange
        default: return .secondary
        }
    }

    private var statusTitle: String {
        switch vpnStatus {
        case .connected: return "Connected"
        case .connecting: return "Connecting…"
        case .disconnecting: return "Disconnecting…"
        case .reasserting: return "Reconnecting…"
        default: return "Disconnected"
        }
    }

    private var statusSubtitle: String {
        switch vpnStatus {
        case .connected: return "Tunnel active via \(selectedProvider.label)"
        case .connecting: return "Establishing \(selectedProvider.label) tunnel"
        case .disconnecting: return "Closing tunnel"
        default: return store.selectedProfile != nil ? "Tap Connect to start" : "Add a profile to get started"
        }
    }

    private var buttonText: String {
        switch vpnStatus {
        case .connected: return "Disconnect"
        case .connecting: return "Connecting…"
        case .disconnecting: return "Stopping…"
        default: return "Connect"
        }
    }

    private var buttonGradient: [Color] {
        switch vpnStatus {
        case .connected: return [Color(red:0.9,green:0.15,blue:0.15), .red]
        case .connecting, .disconnecting, .reasserting: return [.orange, Color(red:1,green:0.6,blue:0)]
        default:
            return theme.isCyber
                ? [theme.accent, theme.secondaryAccent]
                : [.blue, Color(red:0.1,green:0.5,blue:1)]
        }
    }

    // MARK: - Stats

    private func startStatsTimer() {
        statsTimer?.invalidate()
        loadStats()
        statsTimer = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { _ in
            loadStats()
            checkCaptchaRequest()
        }
    }

    /// Polls App Group UserDefaults for a pending captcha URL written by the Network Extension.
    private func checkCaptchaRequest() {
        guard let groupID = SharedLogger.appGroupID,
              let defaults = UserDefaults(suiteName: groupID) else { return }
        let url = defaults.string(forKey: "tb_captcha_url") ?? ""
        DispatchQueue.main.async {
            if !url.isEmpty && !self.showCaptchaSheet {
                self.captchaURL = url
                self.showCaptchaSheet = true
            } else if url.isEmpty && self.showCaptchaSheet {
                self.showCaptchaSheet = false
                self.captchaURL = nil
            }
        }
    }

    /// Sends the success_token obtained from the captcha WebView back to the Network Extension.
    private func submitCaptchaToken(_ token: String) {
        showCaptchaSheet = false
        captchaURL = nil
        NETunnelProviderManager.loadAllFromPreferences { managers, _ in
            guard let session = managers?.first?.connection as? NETunnelProviderSession,
                  let data = "captcha:\(token)".data(using: .utf8) else { return }
            try? session.sendProviderMessage(data) { _ in }
        }
    }

    private func stopStatsTimer() {
        statsTimer?.invalidate(); statsTimer = nil
        stats = TunnelStats()
    }

    private func loadStats() {
        // DTLS from shared UserDefaults
        if let groupID = SharedLogger.appGroupID,
           let defaults = UserDefaults(suiteName: groupID) {
            stats.dtlsConnected = defaults.bool(forKey: "tb_dtls_connected")
        }
        // WG stats via IPC
        NETunnelProviderManager.loadAllFromPreferences { managers, _ in
            guard let manager = managers?.first,
                  let session = manager.connection as? NETunnelProviderSession,
                  let data = "stats".data(using: .utf8) else { return }
            try? session.sendProviderMessage(data) { responseData in
                guard let d = responseData,
                      let json = try? JSONSerialization.jsonObject(with: d) as? [String: Any] else { return }
                DispatchQueue.main.async {
                    if let ts = json["lastHandshake"] as? TimeInterval, ts > 0 {
                        self.stats.lastHandshake = Date(timeIntervalSince1970: ts)
                    }
                    self.stats.txBytes = (json["txBytes"] as? Int64) ?? (json["txBytes"] as? Int).map { Int64($0) } ?? 0
                    self.stats.rxBytes = (json["rxBytes"] as? Int64) ?? (json["rxBytes"] as? Int).map { Int64($0) } ?? 0
                }
            }
        }
    }

    // MARK: - Status handling

    private func handleStatusChange(_ newStatus: NEVPNStatus) {
        let name: String = {
            switch newStatus {
            case .connected: return "Connected"; case .connecting: return "Connecting"
            case .disconnected: return "Disconnected"; case .disconnecting: return "Disconnecting"
            case .reasserting: return "Reasserting"; case .invalid: return "Invalid"
            @unknown default: return "Unknown"
            }
        }()
        SharedLogger.info("VPN status: \(name)")
        withAnimation(.spring(response: 0.4)) {
            let prev = self.vpnStatus
            self.vpnStatus = newStatus
            if newStatus == .connected && prev != .connected {
                connTimer.start(); startStatsTimer()
            } else if newStatus == .disconnected {
                connTimer.stop(); stopStatsTimer()
            }
        }
    }

    // MARK: - Import Modal

    private var importModalView: some View {
        ZStack {
            Color.black.opacity(0.45).ignoresSafeArea()
                .onTapGesture { withAnimation(.spring()) { showImportModal = false } }

            VStack(spacing: 0) {
                Capsule().fill(Color.secondary.opacity(0.4)).frame(width: 36, height: 4).padding(.top, 12).padding(.bottom, 18)
                Text("Add Profile").font(.system(size: 18, weight: .bold)).padding(.bottom, 18)
                VStack(spacing: 10) {
                    importButton(icon: "doc.on.clipboard.fill", title: "Paste from Clipboard",
                                 subtitle: "Import a turnbridge:// link", color: theme.accent, action: importFromClipboard)
                    importButton(icon: "square.and.pencil", title: "Create Manually",
                                 subtitle: "Configure a new profile", color: .green, action: addManualProfile)
                }
                .padding(.horizontal, 20).padding(.bottom, 24)
            }
            .frame(maxWidth: .infinity)
            .background(.regularMaterial)
            .clipShape(RoundedRectangle(cornerRadius: 28))
            .padding(.horizontal, 14)
            .transition(.move(edge: .bottom).combined(with: .opacity))
        }
    }

    private func importButton(icon: String, title: String, subtitle: String, color: Color, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 14) {
                ZStack {
                    RoundedRectangle(cornerRadius: 10).fill(color.opacity(0.15)).frame(width: 44, height: 44)
                    Image(systemName: icon).font(.system(size: 18, weight: .semibold)).foregroundColor(color)
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text(title).font(.system(size: 14, weight: .semibold)).foregroundColor(.primary)
                    Text(subtitle).font(.system(size: 12)).foregroundColor(.secondary)
                }
                Spacer()
                Image(systemName: "chevron.right").font(.system(size: 12, weight: .semibold)).foregroundColor(.secondary)
            }
            .padding(12)
            .background(Color(.secondarySystemGroupedBackground))
            .clipShape(RoundedRectangle(cornerRadius: 14))
        }
    }

    // MARK: - Logic

    private func isJazzProfile(_ profile: VPNProfile) -> Bool {
        let link = profile.vkLink.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return link.hasPrefix("https://salutejazz.ru/call/") || link.hasPrefix("https://jazz.sber.ru/call/")
            || link.hasPrefix("http://salutejazz.ru/call/") || link.hasPrefix("http://jazz.sber.ru/call/")
    }

    private func validateConfig(_ profile: VPNProfile) -> String? {
        if profile.vkLink.isEmpty { return "Please provide a valid TURN Server URL." }
        if profile.peerAddr.isEmpty && !isJazzProfile(profile) { return "Please provide a valid Peer Address." }
        if profile.listenAddr.isEmpty { return "Please provide a valid Listen Address." }
        if profile.wgQuickConfig.isEmpty { return "Please provide a valid WireGuard configuration." }
        return nil
    }

    private func toggleTunnel() {
        if vpnStatus == .connected {
            SharedLogger.info("User requested disconnect"); app.turnOffTunnel()
        } else {
            guard let profile = store.selectedProfile else { return }
            if let err = validateConfig(profile) {
                showAlert(title: "Configuration Required", message: err); return
            }
            SharedLogger.info("User requested connect with profile \"\(profile.name)\"")
            vpnStatus = .connecting
            app.turnOnTunnel(vkLink: profile.vkLink, peerAddr: profile.peerAddr,
                             listenAddr: profile.listenAddr, nValue: profile.nValue,
                             wgQuickConfig: profile.wgQuickConfig) { isSuccess in
                if !isSuccess { vpnStatus = .disconnected; SharedLogger.error("Tunnel start failed") }
            }
        }
    }

    private func checkInitialStatus() {
        NETunnelProviderManager.loadAllFromPreferences { managers, _ in
            if let manager = managers?.first {
                self.vpnStatus = manager.connection.status
                if manager.connection.status == .connected { connTimer.start(); startStatsTimer() }
            } else { self.vpnStatus = .disconnected }
        }
    }

    private func importFromClipboard() {
        guard let str = UIPasteboard.general.string else {
            withAnimation { showImportModal = false }
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) { showAlert(title: "Error", message: "Clipboard is empty.") }
            return
        }
        do {
            let config = try ConfigParser.parse(from: str)
            let profile = VPNProfile(name: config.name ?? "Profile", vkLink: config.turn, peerAddr: config.peer,
                                     listenAddr: config.listen, nValue: config.n, wgQuickConfig: config.wg)
            store.addProfile(profile)
            SharedLogger.info("Profile \"\(store.selectedProfile?.name ?? "")\" imported from clipboard")
            withAnimation { showImportModal = false }
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
                showAlert(title: "Success", message: "Profile \"\(store.selectedProfile?.name ?? "")\" imported.")
            }
        } catch {
            withAnimation { showImportModal = false }
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) { showAlert(title: "Error", message: error.localizedDescription) }
        }
    }

    private func pasteConfigIntoProfile(_ existing: VPNProfile) {
        guard let str = UIPasteboard.general.string else { showAlert(title: "Error", message: "Clipboard is empty."); return }
        do {
            let config = try ConfigParser.parse(from: str)
            var updated = existing
            updated.vkLink = config.turn; updated.peerAddr = config.peer
            updated.listenAddr = config.listen; updated.nValue = config.n; updated.wgQuickConfig = config.wg
            store.updateProfile(updated)
            SharedLogger.info("Profile \"\(existing.name)\" updated from clipboard")
            showAlert(title: "Updated", message: "Profile \"\(existing.name)\" config replaced.")
        } catch { showAlert(title: "Error", message: error.localizedDescription) }
    }

    private func addManualProfile() {
        withAnimation { showImportModal = false }
        let profile = VPNProfile(name: "Profile")
        store.addProfile(profile)
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
            settingsSheet = SettingsSheet(profileID: profile.id, isNew: true)
        }
    }

    private func showAlert(title: String, message: String) {
        alertTitle = title; alertMessage = message; showingAlert = true
    }

    static func isOnWiFi() -> Bool {
        let monitor = NWPathMonitor(); let sem = DispatchSemaphore(value: 0); var result = false
        monitor.pathUpdateHandler = { result = $0.usesInterfaceType(.wifi); sem.signal() }
        monitor.start(queue: DispatchQueue(label: "wifi-check"))
        _ = sem.wait(timeout: .now() + 1); monitor.cancel(); return result
    }
}
