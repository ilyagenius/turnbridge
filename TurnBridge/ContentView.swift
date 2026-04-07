import SwiftUI
import NetworkExtension
import Network

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
        startDate = Date()
        elapsed = 0
        timer?.invalidate()
        timer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            guard let self, let start = self.startDate else { return }
            self.elapsed = Date().timeIntervalSince(start)
        }
    }

    func stop() {
        timer?.invalidate()
        timer = nil
        startDate = nil
        elapsed = 0
    }

    var formatted: String {
        let h = Int(elapsed) / 3600
        let m = (Int(elapsed) % 3600) / 60
        let s = Int(elapsed) % 60
        if h > 0 { return String(format: "%d:%02d:%02d", h, m, s) }
        return String(format: "%02d:%02d", m, s)
    }
}

// MARK: - Provider Detection

enum TunnelProvider {
    case jazz, vk, wb, unknown

    var label: String {
        switch self {
        case .jazz:    return "Jazz"
        case .vk:      return "VK"
        case .wb:      return "WB"
        case .unknown: return "—"
        }
    }

    var icon: String {
        switch self {
        case .jazz:    return "waveform"
        case .vk:      return "bubble.left.and.bubble.right"
        case .wb:      return "shippingbox"
        case .unknown: return "questionmark.circle"
        }
    }

    var color: Color {
        switch self {
        case .jazz:    return .purple
        case .vk:      return .blue
        case .wb:      return Color(red: 0.9, green: 0.3, blue: 0.1)
        case .unknown: return .secondary
        }
    }

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

    var body: some View {
        VStack(spacing: 6) {
            Image(systemName: icon)
                .font(.system(size: 18, weight: .semibold))
                .foregroundColor(color)
            Text(value)
                .font(.system(size: 15, weight: .bold, design: .monospaced))
                .foregroundColor(.primary)
                .lineLimit(1)
                .minimumScaleFactor(0.7)
            Text(label)
                .font(.system(size: 11, weight: .medium))
                .foregroundColor(.secondary)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 14)
        .background(.ultraThinMaterial)
        .clipShape(RoundedRectangle(cornerRadius: 16))
    }
}

// MARK: - Connection Orb

struct ConnectionOrb: View {
    let status: NEVPNStatus

    @State private var pulse = false
    @State private var rotation: Double = 0

    private var mainColor: Color {
        switch status {
        case .connected:                return .green
        case .connecting, .reasserting: return .orange
        case .disconnecting:            return .red.opacity(0.8)
        default:                        return Color(.systemGray3)
        }
    }

    private var isAnimating: Bool {
        status == .connecting || status == .reasserting
    }

    var body: some View {
        ZStack {
            // Outer pulse ring (connected only)
            if status == .connected {
                Circle()
                    .stroke(mainColor.opacity(pulse ? 0 : 0.4), lineWidth: pulse ? 1 : 8)
                    .frame(width: pulse ? 180 : 140)
                    .animation(.easeOut(duration: 2).repeatForever(autoreverses: false), value: pulse)
            }

            // Spinning arc (connecting)
            if isAnimating {
                Circle()
                    .trim(from: 0, to: 0.7)
                    .stroke(
                        AngularGradient(colors: [mainColor, mainColor.opacity(0)], center: .center),
                        style: StrokeStyle(lineWidth: 4, lineCap: .round)
                    )
                    .frame(width: 150)
                    .rotationEffect(.degrees(rotation))
                    .animation(.linear(duration: 1).repeatForever(autoreverses: false), value: rotation)
            }

            // Main orb
            Circle()
                .fill(
                    RadialGradient(
                        colors: [mainColor.opacity(0.25), mainColor.opacity(0.05)],
                        center: .center,
                        startRadius: 10,
                        endRadius: 65
                    )
                )
                .frame(width: 130)
                .overlay(
                    Circle()
                        .stroke(mainColor.opacity(0.5), lineWidth: 1.5)
                        .frame(width: 130)
                )

            Image(systemName: status == .connected ? "lock.shield.fill" : "lock.shield")
                .font(.system(size: 48, weight: .medium))
                .foregroundStyle(
                    LinearGradient(
                        colors: [mainColor, mainColor.opacity(0.7)],
                        startPoint: .top,
                        endPoint: .bottom
                    )
                )
                .shadow(color: mainColor.opacity(0.5), radius: 12)
                .scaleEffect(isAnimating ? 0.95 : 1.0)
                .animation(isAnimating ? .easeInOut(duration: 0.8).repeatForever() : .default, value: isAnimating)
        }
        .frame(width: 180, height: 180)
        .onAppear {
            pulse = true
            withAnimation(.linear(duration: 1).repeatForever(autoreverses: false)) {
                rotation = 360
            }
        }
    }
}

// MARK: - Profile Row

struct ProfileRow: View {
    let profile: VPNProfile
    let isSelected: Bool
    let isConnected: Bool
    let onTap: () -> Void
    let onEdit: () -> Void
    let onDelete: () -> Void

    var provider: TunnelProvider { TunnelProvider.detect(from: profile.vkLink) }

    var body: some View {
        HStack(spacing: 14) {
            // Provider badge
            ZStack {
                RoundedRectangle(cornerRadius: 10)
                    .fill(provider.color.opacity(0.15))
                    .frame(width: 42, height: 42)
                Image(systemName: provider.icon)
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundColor(provider.color)
            }

            VStack(alignment: .leading, spacing: 3) {
                Text(profile.name)
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundColor(.primary)
                Text(provider.label + " · " + shortAddr(profile))
                    .font(.system(size: 12, weight: .regular))
                    .foregroundColor(.secondary)
            }

            Spacer()

            if isSelected {
                Image(systemName: "checkmark.circle.fill")
                    .foregroundColor(.blue)
                    .font(.system(size: 18))
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .background(isSelected ? Color.blue.opacity(0.08) : Color.clear)
        .contentShape(Rectangle())
        .onTapGesture { if !isConnected { onTap() } }
        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
            Button(role: .destructive, action: onDelete) {
                Label("Delete", systemImage: "trash")
            }
            if !isConnected {
                Button(action: onEdit) {
                    Label("Edit", systemImage: "pencil")
                }
                .tint(.orange)
            }
        }
    }

    private func shortAddr(_ p: VPNProfile) -> String {
        if p.peerAddr.isEmpty { return "Auto" }
        let host = p.peerAddr.components(separatedBy: ":").first ?? p.peerAddr
        return host.count > 20 ? String(host.prefix(18)) + "…" : host
    }
}

// MARK: - Main View

struct ContentView: View {
    var app: TurnBridge

    @State private var vpnStatus: NEVPNStatus = .disconnected
    @StateObject private var store = ProfileStore()
    @StateObject private var connTimer = ConnectionTimer()

    @State private var showImportModal = false
    @State private var showProfileList = false
    @State private var showingAlert = false
    @State private var alertTitle = ""
    @State private var alertMessage = ""
    @State private var settingsSheet: SettingsSheet?

    private var selectedProvider: TunnelProvider {
        guard let p = store.selectedProfile else { return .unknown }
        return TunnelProvider.detect(from: p.vkLink)
    }

    var body: some View {
        NavigationStack {
            ZStack(alignment: .bottom) {
                // Background
                Color(.systemGroupedBackground)
                    .ignoresSafeArea()

                ScrollView {
                    VStack(spacing: 24) {

                        // ── Status Hero ──────────────────────────────
                        VStack(spacing: 16) {
                            ConnectionOrb(status: vpnStatus)
                                .padding(.top, 8)

                            VStack(spacing: 4) {
                                Text(statusTitle)
                                    .font(.system(size: 22, weight: .bold))
                                    .foregroundColor(.primary)
                                    .animation(.none, value: vpnStatus)

                                Text(statusSubtitle)
                                    .font(.system(size: 14, weight: .medium))
                                    .foregroundColor(.secondary)
                                    .animation(.none, value: vpnStatus)
                            }
                        }
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 20)
                        .background(.regularMaterial)
                        .clipShape(RoundedRectangle(cornerRadius: 24))
                        .padding(.horizontal, 16)

                        // ── Stats Row ────────────────────────────────
                        HStack(spacing: 10) {
                            StatCard(
                                icon: selectedProvider.icon,
                                label: "Protocol",
                                value: selectedProvider.label,
                                color: selectedProvider.color
                            )
                            StatCard(
                                icon: "timer",
                                label: "Uptime",
                                value: vpnStatus == .connected ? connTimer.formatted : "—",
                                color: .green
                            )
                            StatCard(
                                icon: "server.rack",
                                label: "Profile",
                                value: store.selectedProfile?.name ?? "None",
                                color: .blue
                            )
                        }
                        .padding(.horizontal, 16)

                        // ── Profile List ─────────────────────────────
                        VStack(spacing: 0) {
                            HStack {
                                Text("Profiles")
                                    .font(.system(size: 13, weight: .semibold))
                                    .foregroundColor(.secondary)
                                    .textCase(.uppercase)
                                Spacer()
                            }
                            .padding(.horizontal, 20)
                            .padding(.bottom, 8)

                            VStack(spacing: 0) {
                                if store.profiles.isEmpty {
                                    HStack {
                                        Spacer()
                                        VStack(spacing: 8) {
                                            Image(systemName: "plus.circle.dashed")
                                                .font(.system(size: 32))
                                                .foregroundColor(.secondary)
                                            Text("No profiles yet")
                                                .font(.system(size: 14))
                                                .foregroundColor(.secondary)
                                        }
                                        .padding(.vertical, 32)
                                        Spacer()
                                    }
                                    .background(.regularMaterial)
                                    .clipShape(RoundedRectangle(cornerRadius: 16))
                                } else {
                                    ForEach(Array(store.profiles.enumerated()), id: \.element.id) { idx, profile in
                                        ProfileRow(
                                            profile: profile,
                                            isSelected: profile.id == store.selectedProfileID,
                                            isConnected: vpnStatus != .disconnected,
                                            onTap: {
                                                withAnimation(.spring(response: 0.3)) {
                                                    store.selectedProfileID = profile.id
                                                    store.save()
                                                }
                                            },
                                            onEdit: {
                                                settingsSheet = SettingsSheet(profileID: profile.id, isNew: false)
                                            },
                                            onDelete: {
                                                withAnimation {
                                                    store.deleteProfile(profile.id)
                                                }
                                            }
                                        )

                                        if idx < store.profiles.count - 1 {
                                            Divider()
                                                .padding(.leading, 72)
                                        }
                                    }
                                }
                            }
                            .background(.regularMaterial)
                            .clipShape(RoundedRectangle(cornerRadius: 16))
                            .padding(.horizontal, 16)
                        }

                        // Bottom padding for button
                        Color.clear.frame(height: 90)
                    }
                    .padding(.top, 8)
                }

                // ── Connect Button ───────────────────────────────────
                VStack(spacing: 0) {
                    LinearGradient(
                        colors: [Color(.systemGroupedBackground).opacity(0), Color(.systemGroupedBackground)],
                        startPoint: .top,
                        endPoint: .bottom
                    )
                    .frame(height: 20)

                    Button(action: toggleTunnel) {
                        HStack(spacing: 10) {
                            if vpnStatus == .connecting || vpnStatus == .disconnecting {
                                ProgressView()
                                    .progressViewStyle(CircularProgressViewStyle(tint: .white))
                                    .scaleEffect(0.85)
                            } else {
                                Image(systemName: vpnStatus == .connected ? "stop.circle.fill" : "play.circle.fill")
                                    .font(.system(size: 20))
                            }
                            Text(buttonText)
                                .font(.system(size: 17, weight: .semibold))
                        }
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 17)
                        .background(
                            LinearGradient(
                                colors: buttonGradient,
                                startPoint: .topLeading,
                                endPoint: .bottomTrailing
                            )
                        )
                        .foregroundColor(.white)
                        .clipShape(RoundedRectangle(cornerRadius: 18))
                        .shadow(color: buttonGradient.first?.opacity(0.4) ?? .clear, radius: 12, x: 0, y: 6)
                    }
                    .disabled(vpnStatus == .connecting || vpnStatus == .disconnecting || store.selectedProfile == nil)
                    .padding(.horizontal, 16)
                    .padding(.bottom, 32)
                    .background(Color(.systemGroupedBackground))
                }
            }
            .overlay {
                if showImportModal {
                    importModalView
                }
            }
            .navigationTitle("TurnBridge")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button(action: {
                        if vpnStatus == .disconnected {
                            withAnimation(.spring()) { showImportModal = true }
                        }
                    }) {
                        Image(systemName: "plus.circle.fill")
                            .font(.system(size: 20))
                            .foregroundColor(vpnStatus == .disconnected ? .blue : .secondary)
                    }
                }

                ToolbarItemGroup(placement: .navigationBarTrailing) {
                    NavigationLink(destination: GlobalSettingsView()) {
                        Image(systemName: "gearshape.fill")
                            .font(.system(size: 18))
                            .foregroundColor(.primary)
                    }
                }
            }
            .sheet(item: $settingsSheet) { sheet in
                NavigationStack {
                    SettingsView(store: store, profileID: sheet.profileID, isNewProfile: sheet.isNew)
                }
            }
            .onAppear(perform: checkInitialStatus)
            .onReceive(NotificationCenter.default.publisher(for: .NEVPNStatusDidChange)) { notification in
                if let connection = notification.object as? NEVPNConnection {
                    let newStatus = connection.status
                    let statusName: String = {
                        switch newStatus {
                        case .connected:     return "Connected"
                        case .connecting:    return "Connecting"
                        case .disconnected:  return "Disconnected"
                        case .disconnecting: return "Disconnecting"
                        case .reasserting:   return "Reasserting"
                        case .invalid:       return "Invalid"
                        @unknown default:    return "Unknown"
                        }
                    }()
                    SharedLogger.info("VPN status: \(statusName)")
                    withAnimation(.spring(response: 0.4)) {
                        let prev = self.vpnStatus
                        self.vpnStatus = newStatus
                        if newStatus == .connected && prev != .connected {
                            connTimer.start()
                        } else if newStatus == .disconnected {
                            connTimer.stop()
                        }
                    }
                }
            }
            .alert(alertTitle, isPresented: $showingAlert) {
                Button("OK", role: .cancel) { }
            } message: {
                Text(alertMessage)
            }
        }
    }

    // MARK: - Computed

    private var statusTitle: String {
        switch vpnStatus {
        case .connected:     return "Connected"
        case .connecting:    return "Connecting…"
        case .disconnecting: return "Disconnecting…"
        case .reasserting:   return "Reconnecting…"
        default:             return "Disconnected"
        }
    }

    private var statusSubtitle: String {
        switch vpnStatus {
        case .connected:
            return "Tunnel active via \(selectedProvider.label)"
        case .connecting:
            return "Establishing \(selectedProvider.label) tunnel"
        case .disconnecting:
            return "Closing tunnel"
        default:
            return store.selectedProfile != nil
                ? "Tap Connect to start"
                : "Add a profile to get started"
        }
    }

    private var buttonText: String {
        switch vpnStatus {
        case .connected:     return "Disconnect"
        case .connecting:    return "Connecting…"
        case .disconnecting: return "Stopping…"
        default:             return "Connect"
        }
    }

    private var buttonGradient: [Color] {
        switch vpnStatus {
        case .connected:                return [Color(red: 0.9, green: 0.2, blue: 0.2), .red]
        case .connecting, .disconnecting, .reasserting: return [.orange, Color(red: 1, green: 0.6, blue: 0)]
        default:                        return [.blue, Color(red: 0.1, green: 0.5, blue: 1)]
        }
    }

    // MARK: - Import Modal

    private var importModalView: some View {
        ZStack {
            Color.black.opacity(0.4)
                .ignoresSafeArea()
                .onTapGesture {
                    withAnimation(.spring()) { showImportModal = false }
                }

            VStack(spacing: 0) {
                // Handle
                Capsule()
                    .fill(Color.secondary.opacity(0.4))
                    .frame(width: 36, height: 4)
                    .padding(.top, 12)
                    .padding(.bottom, 20)

                Text("Add Profile")
                    .font(.system(size: 18, weight: .bold))
                    .padding(.bottom, 20)

                VStack(spacing: 12) {
                    importButton(
                        icon: "doc.on.clipboard.fill",
                        title: "Paste from Clipboard",
                        subtitle: "Import a turnbridge:// link",
                        color: .blue,
                        action: importFromClipboard
                    )

                    importButton(
                        icon: "square.and.pencil",
                        title: "Create Manually",
                        subtitle: "Configure a new profile",
                        color: .green,
                        action: addManualProfile
                    )
                }
                .padding(.horizontal, 20)
                .padding(.bottom, 24)
            }
            .frame(maxWidth: .infinity)
            .background(.regularMaterial)
            .clipShape(RoundedRectangle(cornerRadius: 28))
            .padding(.horizontal, 12)
            .transition(.move(edge: .bottom).combined(with: .opacity))
        }
    }

    private func importButton(icon: String, title: String, subtitle: String, color: Color, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 16) {
                ZStack {
                    RoundedRectangle(cornerRadius: 12)
                        .fill(color.opacity(0.15))
                        .frame(width: 48, height: 48)
                    Image(systemName: icon)
                        .font(.system(size: 20, weight: .semibold))
                        .foregroundColor(color)
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.system(size: 15, weight: .semibold))
                        .foregroundColor(.primary)
                    Text(subtitle)
                        .font(.system(size: 12))
                        .foregroundColor(.secondary)
                }
                Spacer()
                Image(systemName: "chevron.right")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundColor(.secondary)
            }
            .padding(14)
            .background(Color(.secondarySystemGroupedBackground))
            .clipShape(RoundedRectangle(cornerRadius: 16))
        }
    }

    // MARK: - Logic

    private func isJazzProfile(_ profile: VPNProfile) -> Bool {
        let link = profile.vkLink.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !link.isEmpty else { return false }
        return link.hasPrefix("https://salutejazz.ru/call/")
            || link.hasPrefix("https://jazz.sber.ru/call/")
            || link.hasPrefix("http://salutejazz.ru/call/")
            || link.hasPrefix("http://jazz.sber.ru/call/")
    }

    private func validateConfig(_ profile: VPNProfile) -> String? {
        if profile.vkLink.isEmpty {
            return "Please provide a valid TURN Server URL."
        }
        if profile.peerAddr.isEmpty && !isJazzProfile(profile) {
            return "Please provide a valid Peer Address."
        }
        if profile.listenAddr.isEmpty {
            return "Please provide a valid Listen Address."
        }
        if profile.wgQuickConfig.isEmpty {
            return "Please provide a valid WireGuard configuration."
        }
        return nil
    }

    private func toggleTunnel() {
        if vpnStatus == .connected {
            SharedLogger.info("User requested disconnect")
            app.turnOffTunnel()
        } else {
            guard let profile = store.selectedProfile else { return }
            if let errorMessage = validateConfig(profile) {
                SharedLogger.warning("Config validation failed: \(errorMessage)")
                showAlert(title: "Configuration Required", message: errorMessage)
                return
            }
            SharedLogger.info("User requested connect with profile \"\(profile.name)\"")
            vpnStatus = .connecting
            app.turnOnTunnel(
                vkLink: profile.vkLink,
                peerAddr: profile.peerAddr,
                listenAddr: profile.listenAddr,
                nValue: profile.nValue,
                wgQuickConfig: profile.wgQuickConfig
            ) { isSuccess in
                if !isSuccess {
                    vpnStatus = .disconnected
                    SharedLogger.error("Tunnel start failed")
                }
            }
        }
    }

    private func checkInitialStatus() {
        NETunnelProviderManager.loadAllFromPreferences { managers, error in
            if let manager = managers?.first {
                self.vpnStatus = manager.connection.status
                if manager.connection.status == .connected {
                    connTimer.start()
                }
            } else {
                self.vpnStatus = .disconnected
            }
        }
    }

    private func importFromClipboard() {
        guard let clipboardString = UIPasteboard.general.string else {
            SharedLogger.warning("Clipboard import failed: clipboard is empty")
            withAnimation { showImportModal = false }
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
                showAlert(title: "Error", message: "Clipboard is empty.")
            }
            return
        }

        SharedLogger.debug("Parsing clipboard config (\(clipboardString.count) chars)")
        do {
            let config = try ConfigParser.parse(from: clipboardString)
            let profile = VPNProfile(
                name: config.name ?? "Profile",
                vkLink: config.turn,
                peerAddr: config.peer,
                listenAddr: config.listen,
                nValue: config.n,
                wgQuickConfig: config.wg
            )
            store.addProfile(profile)
            SharedLogger.info("Profile \"\(store.selectedProfile?.name ?? "")\" imported from clipboard")
            withAnimation { showImportModal = false }
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
                showAlert(title: "Success", message: "Profile \"\(store.selectedProfile?.name ?? "")\" imported.")
            }
        } catch {
            SharedLogger.error("Clipboard import failed: \(error.localizedDescription)")
            withAnimation { showImportModal = false }
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
                showAlert(title: "Error", message: error.localizedDescription)
            }
        }
    }

    private func addManualProfile() {
        withAnimation { showImportModal = false }
        let profile = VPNProfile(name: "Profile")
        store.addProfile(profile)
        SharedLogger.info("New manual profile created: \"\(store.selectedProfile?.name ?? "")\"")
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
            settingsSheet = SettingsSheet(profileID: profile.id, isNew: true)
        }
    }

    private func showAlert(title: String, message: String) {
        alertTitle = title
        alertMessage = message
        showingAlert = true
    }

    static func isOnWiFi() -> Bool {
        let monitor = NWPathMonitor()
        let semaphore = DispatchSemaphore(value: 0)
        var result = false
        monitor.pathUpdateHandler = { path in
            result = path.usesInterfaceType(.wifi)
            semaphore.signal()
        }
        let queue = DispatchQueue(label: "wifi-check")
        monitor.start(queue: queue)
        _ = semaphore.wait(timeout: .now() + 1)
        monitor.cancel()
        return result
    }
}
