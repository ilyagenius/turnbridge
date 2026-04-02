import SwiftUI
import Combine

struct LogView: View {
    @State private var selectedFilter = 0
    @State private var logs: [String] = []
    
    let timer = Timer.publish(every: 2.0, on: .main, in: .common).autoconnect()
    
    var filteredLogs: [String] {
        if selectedFilter == 1 {
            return logs.filter { $0.contains("[WG]") }
        } else if selectedFilter == 2 {
            return logs.filter { $0.contains("[TP]") }
        }
        return logs
    }
    
    var body: some View {
        VStack(spacing: 0) {
            Picker("Log Filter", selection: $selectedFilter) {
                Text("All").tag(0)
                Text("WireGuard").tag(1)
                Text("Turn Proxy").tag(2)
            }
            .pickerStyle(.segmented)
            .padding()
            .background(Color(UIColor.systemBackground))
            
            Divider()
            
            ScrollView {
                ScrollViewReader { proxy in
                    VStack(alignment: .leading, spacing: 6) {
                        ForEach(filteredLogs.indices, id: \.self) { index in
                            Text(filteredLogs[index])
                                .font(.system(.caption, design: .monospaced))
                                .foregroundColor(.primary)
                                .id(index)
                        }
                    }
                    .padding()
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .onChange(of: logs) { _ in
                        if !filteredLogs.isEmpty {
                            withAnimation {
                                proxy.scrollTo(filteredLogs.count - 1, anchor: .bottom)
                            }
                        }
                    }
                }
            }
            .background(Color(UIColor.secondarySystemBackground))
        }
        .navigationTitle("Logs")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItemGroup(placement: .navigationBarTrailing) {
                if let logURL = SharedLogger.logFileURL {
                    ShareLink(item: logURL) {
                        Image(systemName: "square.and.arrow.up")
                            .foregroundColor(.blue)
                    }
                }
                
                Button(action: {
                    SharedLogger.clearLogs()
                    logs = SharedLogger.readLogs()
                }) {
                    Image(systemName: "trash")
                        .foregroundColor(.red)
                }
            }
        }
        .onAppear {
            logs = SharedLogger.readLogs()
        }
        .onReceive(timer) { _ in
            let newLogs = SharedLogger.readLogs()
            if newLogs.count != logs.count {
                logs = newLogs
            }
        }
    }
}
