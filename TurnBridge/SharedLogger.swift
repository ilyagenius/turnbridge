import Foundation

// MARK: - Log Level

public enum LogLevel: Int, Codable, CaseIterable, Comparable {
    case debug = 0
    case info = 1
    case warning = 2
    case error = 3

    public static func < (lhs: LogLevel, rhs: LogLevel) -> Bool {
        lhs.rawValue < rhs.rawValue
    }

    public var label: String {
        switch self {
        case .debug:   return "DEBUG"
        case .info:    return "INFO"
        case .warning: return "WARN"
        case .error:   return "ERROR"
        }
    }

    public var icon: String {
        switch self {
        case .debug:   return "\u{1FAB2}"  // 🪲
        case .info:    return "\u{2139}"    // ℹ
        case .warning: return "\u{26A0}"    // ⚠
        case .error:   return "\u{1F534}"   // 🔴
        }
    }
}

// MARK: - Log Source

public enum LogSource: String, Codable, CaseIterable {
    case app = "APP"
    case tunnel = "TP"
    case wireguard = "WG"

    public var displayName: String {
        switch self {
        case .app:       return "App"
        case .tunnel:    return "Turn Proxy"
        case .wireguard: return "WireGuard"
        }
    }
}

// MARK: - Log Entry

public struct LogEntry: Identifiable, Equatable {
    public let id: UUID
    public let timestamp: Date
    public let level: LogLevel
    public let source: LogSource
    public let message: String

    /// Raw line format: `[MM-dd HH:mm:ss] LEVEL|SOURCE|message`
    public var rawLine: String {
        let formatter = DateFormatter()
        formatter.dateFormat = "MM-dd HH:mm:ss"
        return "[\(formatter.string(from: timestamp))] \(level.label)|\(source.rawValue)|\(message)"
    }

    public var displayLine: String {
        let formatter = DateFormatter()
        formatter.dateFormat = "HH:mm:ss"
        return "\(formatter.string(from: timestamp)) \(level.icon) [\(source.rawValue)] \(message)"
    }

    /// Parse a raw log line back into a LogEntry
    public static func parse(_ line: String) -> LogEntry? {
        // Format: [MM-dd HH:mm:ss] LEVEL|SOURCE|message
        guard line.hasPrefix("[") else { return nil }

        guard let closeBracket = line.firstIndex(of: "]") else { return nil }
        let dateString = String(line[line.index(after: line.startIndex)..<closeBracket])

        let afterBracket = line.index(closeBracket, offsetBy: 2, limitedBy: line.endIndex) ?? line.endIndex
        let rest = String(line[afterBracket...])

        let parts = rest.split(separator: "|", maxSplits: 2)
        guard parts.count == 3 else {
            // Legacy format fallback: [MM-dd HH:mm:ss] message
            let formatter = DateFormatter()
            formatter.dateFormat = "MM-dd HH:mm:ss"
            let date = formatter.date(from: dateString) ?? Date()
            return LogEntry(
                id: UUID(),
                timestamp: date,
                level: .info,
                source: inferSource(from: rest),
                message: rest
            )
        }

        let levelStr = String(parts[0])
        let sourceStr = String(parts[1])
        let message = String(parts[2])

        let formatter = DateFormatter()
        formatter.dateFormat = "MM-dd HH:mm:ss"
        let date = formatter.date(from: dateString) ?? Date()

        let level = LogLevel.allCases.first { $0.label == levelStr } ?? .info
        let source = LogSource.allCases.first { $0.rawValue == sourceStr } ?? .app

        return LogEntry(id: UUID(), timestamp: date, level: level, source: source, message: message)
    }

    /// Infer source from legacy log lines (e.g. those with [WG] or [TP] markers)
    private static func inferSource(from message: String) -> LogSource {
        if message.contains("[WG]") { return .wireguard }
        if message.contains("[TP]") { return .tunnel }
        return .app
    }
}

// MARK: - SharedLogger

public struct SharedLogger {
    static var appGroupID: String {
        let bundleID = Bundle.main.bundleIdentifier ?? "com.netlab.TurnBridge"
        let baseBundleID = bundleID.replacingOccurrences(of: ".network-extension", with: "")
        return "group.\(baseBundleID)"
    }

    static var logFileURL: URL? {
        let container = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroupID)
        return container?.appendingPathComponent("vpn_tunnel.log")
    }

    /// Max log file size in bytes (500 KB)
    private static let maxLogSize: UInt64 = 500 * 1024

    /// Number of lines to keep after rotation (keep the newest ~60%)
    private static let rotationKeepRatio: Double = 0.6

    // MARK: - Write

    public static func log(_ message: String, level: LogLevel = .info, source: LogSource = .app) {
        guard let url = logFileURL else { return }

        let entry = LogEntry(id: UUID(), timestamp: Date(), level: level, source: source, message: message)
        let logLine = entry.rawLine + "\n"
        guard let data = logLine.data(using: .utf8) else { return }

        if FileManager.default.fileExists(atPath: url.path) {
            if let fileHandle = try? FileHandle(forWritingTo: url) {
                fileHandle.seekToEndOfFile()
                fileHandle.write(data)
                fileHandle.closeFile()
            }
            rotateIfNeeded()
        } else {
            try? data.write(to: url)
        }
    }

    // MARK: - Convenience methods

    public static func debug(_ message: String, source: LogSource = .app) {
        log(message, level: .debug, source: source)
    }

    public static func info(_ message: String, source: LogSource = .app) {
        log(message, level: .info, source: source)
    }

    public static func warning(_ message: String, source: LogSource = .app) {
        log(message, level: .warning, source: source)
    }

    public static func error(_ message: String, source: LogSource = .app) {
        log(message, level: .error, source: source)
    }

    // MARK: - Read

    public static func readEntries() -> [LogEntry] {
        guard let url = logFileURL,
              let content = try? String(contentsOf: url, encoding: .utf8) else {
            return []
        }
        return content
            .components(separatedBy: "\n")
            .filter { !$0.isEmpty }
            .compactMap { LogEntry.parse($0) }
    }

    public static func readLogs() -> [String] {
        guard let url = logFileURL,
              let content = try? String(contentsOf: url, encoding: .utf8) else {
            return []
        }
        return content.components(separatedBy: "\n").filter { !$0.isEmpty }
    }

    // MARK: - Clear

    public static func clearLogs() {
        guard let url = logFileURL else { return }
        try? "".write(to: url, atomically: true, encoding: .utf8)
    }

    // MARK: - Rotation

    private static func rotateIfNeeded() {
        guard let url = logFileURL else { return }
        guard let attrs = try? FileManager.default.attributesOfItem(atPath: url.path),
              let fileSize = attrs[.size] as? UInt64,
              fileSize > maxLogSize else { return }

        guard let content = try? String(contentsOf: url, encoding: .utf8) else { return }
        let lines = content.components(separatedBy: "\n").filter { !$0.isEmpty }
        let keepCount = Int(Double(lines.count) * rotationKeepRatio)
        let trimmed = Array(lines.suffix(keepCount))
        let newContent = trimmed.joined(separator: "\n") + "\n"
        try? newContent.write(to: url, atomically: true, encoding: .utf8)
    }
}
