import SwiftUI

struct GlobalSettingsView: View {
    @AppStorage("excludeAPNs") private var excludeAPNs = false
    @AppStorage("excludeCellularServices") private var excludeCellularServices = false
    @AppStorage("excludeLocalNetworks") private var excludeLocalNetworks = true

    @State private var showCacheClearedAlert = false

    var body: some View {
        Form {
            Section(header: Text("General")) {
                NavigationLink(destination: AboutView()) {
                    Label(
                        title: { Text("About") },
                        icon: { Image(systemName: "info.circle").foregroundColor(.secondary) }
                    )
                }

                NavigationLink(destination: LogView()) {
                    Label(
                        title: { Text("Logs") },
                        icon: { Image(systemName: "doc.text.magnifyingglass").foregroundColor(.secondary) }
                    )
                }
            }

            Section(header: Text("Routing")) {
                Toggle("Allow LAN Access", isOn: $excludeLocalNetworks)
                Toggle("Bypass APNs", isOn: $excludeAPNs)
                Toggle("Bypass Cellular", isOn: $excludeCellularServices)
            }

            Section(header: Text("Fast-connect cache"),
                    footer: Text("Clears all cached TURN links. Next connect will go through the full VK bootstrap (~40s) and rebuild the cache.")) {
                Button {
                    LinkCache.shared.clearAll()
                    SharedLogger.info("[LinkCache] Cleared all entries via settings button")
                    showCacheClearedAlert = true
                } label: {
                    Label(
                        title: { Text("Clear Link Cache").foregroundColor(.red) },
                        icon: { Image(systemName: "trash").foregroundColor(.red) }
                    )
                }
            }
        }
        .navigationTitle("Settings")
        .navigationBarTitleDisplayMode(.inline)
        .alert("Cache cleared", isPresented: $showCacheClearedAlert) {
            Button("OK", role: .cancel) { }
        } message: {
            Text("Next connect will use the full VK bootstrap path.")
        }
    }
}
