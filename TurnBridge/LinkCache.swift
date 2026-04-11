//
//  LinkCache.swift
//  Shared between main app and network extension via App Group container.
//  Stores the last-known-good TURN link per (server, provider) with a TTL so
//  subsequent connects can skip VK bootstrap and go through runDirectProxy.
//

import Foundation

struct CachedLink: Codable {
    let link: String
    let fetchedAt: Date
    let ttlSeconds: TimeInterval

    var isFresh: Bool {
        Date().timeIntervalSince(fetchedAt) < ttlSeconds
    }
}

enum ProviderType: String {
    case vk, jazz, telemost, max, wb, unknown

    static func detect(from link: String) -> ProviderType {
        if link.hasPrefix("max:") { return .max }
        if link.contains("salutejazz.ru") { return .jazz }
        if link.contains("telemost.yandex.ru") { return .telemost }
        if link.contains("stream.wb") || link.contains("wildberries") { return .wb }
        if link.contains("vkvideo.ru") || link.contains("vk.com") || link.contains("okko.tv") || link.contains("ok.ru") {
            return .vk
        }
        return .unknown
    }

    /// Default TTL in seconds. For providers that are server-refreshed (Jazz/Telemost)
    /// we keep TTL below the server refresh interval (45 min) to avoid serving stale
    /// links. For MAX the join link doesn't rotate server-side so TTL is much longer.
    var cacheTTL: TimeInterval {
        switch self {
        case .jazz, .telemost: return 30 * 60
        case .max: return 24 * 60 * 60
        default: return 0
        }
    }

    /// Whether it makes sense to cache this provider (skip VK bootstrap).
    var supportsFastConnect: Bool {
        switch self {
        case .jazz, .telemost, .max: return true
        default: return false
        }
    }
}

final class LinkCache {
    static let shared = LinkCache()

    // v2 filename: keyed by "{serverID}|{provider}" instead of just "{provider}".
    // Bumping the filename forces any v1 cache from older builds to be ignored
    // so the next connect takes the cold path and rebuilds the cache correctly.
    private let filename = "tb_link_cache_v2.json"
    private let lock = NSLock()

    private init() {}

    private var cacheURL: URL? {
        guard let groupID = SharedLogger.appGroupID,
              let container = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: groupID) else {
            return nil
        }
        return container.appendingPathComponent(filename)
    }

    private func readAll() -> [String: CachedLink] {
        guard let url = cacheURL,
              let data = try? Data(contentsOf: url),
              let dict = try? JSONDecoder.iso8601.decode([String: CachedLink].self, from: data) else {
            return [:]
        }
        return dict
    }

    private func writeAll(_ dict: [String: CachedLink]) {
        guard let url = cacheURL else { return }
        guard let data = try? JSONEncoder.iso8601.encode(dict) else { return }
        try? data.write(to: url, options: .atomic)
    }

    /// Normalize a server identifier so trailing whitespace / case differences
    /// don't produce distinct cache entries for the same physical server.
    private func normalizeServerID(_ s: String) -> String {
        return s.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    }

    private func key(server: String, provider: ProviderType) -> String {
        return "\(normalizeServerID(server))|\(provider.rawValue)"
    }

    func get(server: String, provider: ProviderType) -> CachedLink? {
        lock.lock(); defer { lock.unlock() }
        return readAll()[key(server: server, provider: provider)]
    }

    func set(server: String, provider: ProviderType, link: String, ttl: TimeInterval? = nil) {
        guard provider.supportsFastConnect, !server.isEmpty else { return }
        lock.lock(); defer { lock.unlock() }
        var dict = readAll()
        dict[key(server: server, provider: provider)] = CachedLink(
            link: link,
            fetchedAt: Date(),
            ttlSeconds: ttl ?? provider.cacheTTL
        )
        writeAll(dict)
    }

    func invalidate(server: String, provider: ProviderType) {
        lock.lock(); defer { lock.unlock() }
        var dict = readAll()
        dict.removeValue(forKey: key(server: server, provider: provider))
        writeAll(dict)
    }

    /// Wipe every cached entry (for manual "force cold connect" debugging and
    /// for reacting to config-level changes that invalidate all links at once).
    func clearAll() {
        lock.lock(); defer { lock.unlock() }
        writeAll([:])
        if let url = cacheURL {
            try? FileManager.default.removeItem(at: url)
        }
    }
}

private extension JSONEncoder {
    static let iso8601: JSONEncoder = {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .iso8601
        return e
    }()
}

private extension JSONDecoder {
    static let iso8601: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .iso8601
        return d
    }()
}
