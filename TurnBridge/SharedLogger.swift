import Foundation

public struct SharedLogger {
    //static let appGroupID = "group.com.netlab.TurnBridge"
    static var appGroupID: String {
        let bundleID = Bundle.main.bundleIdentifier ?? "com.netlab.TurnBridge"
        let baseBundleID = bundleID.replacingOccurrences(of: ".network-extension", with: "")
        return "group.\(baseBundleID)"
    }
    
    static var logFileURL: URL? {
        let container = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroupID)
        return container?.appendingPathComponent("vpn_tunnel.log")
    }
    
    public static func log(_ message: String) {
        guard let url = logFileURL else { return }
        
        let formatter = DateFormatter()
        formatter.dateFormat = "MM-dd HH:mm:ss"
        let time = formatter.string(from: Date())
        
        let logLine = "[\(time)] \(message)\n"
        guard let data = logLine.data(using: .utf8) else { return }
        
        if FileManager.default.fileExists(atPath: url.path) {
            if let fileHandle = try? FileHandle(forWritingTo: url) {
                fileHandle.seekToEndOfFile()
                fileHandle.write(data)
                fileHandle.closeFile()
            }
        } else {
            try? data.write(to: url)
        }
    }
    
    public static func readLogs() -> [String] {
        guard let url = logFileURL,
              let content = try? String(contentsOf: url, encoding: .utf8) else {
            return ["The logs are currently empty, or the App Group hasn't been configured yet..."]
        }
        return content.components(separatedBy: "\n").filter { !$0.isEmpty }
    }
    
    public static func clearLogs() {
        guard let url = logFileURL else { return }
        try? "".write(to: url, atomically: true, encoding: .utf8)
    }
}
