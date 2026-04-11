import Foundation

struct VPNProfile: Codable, Identifiable, Equatable {
    let id: UUID
    var name: String
    var vkLink: String
    var peerAddr: String
    var listenAddr: String
    var nValue: Int
    var wgQuickConfig: String
    var fallbackLink: String
    var linkServer: String
    var providerType: String

    init(id: UUID = UUID(), name: String = "", vkLink: String = "", peerAddr: String = "", listenAddr: String = "127.0.0.1:9000", nValue: Int = 1, wgQuickConfig: String = "", fallbackLink: String = "", linkServer: String = "10.77.77.1:8080", providerType: String = "") {
        self.id = id
        self.name = name
        self.vkLink = vkLink
        self.peerAddr = peerAddr
        self.listenAddr = listenAddr
        self.nValue = nValue
        self.wgQuickConfig = wgQuickConfig
        self.fallbackLink = fallbackLink
        self.linkServer = linkServer
        self.providerType = providerType
    }

    // Backward compatibility: decode profiles saved without new fields.
    enum CodingKeys: String, CodingKey {
        case id, name, vkLink, peerAddr, listenAddr, nValue, wgQuickConfig, fallbackLink, linkServer, providerType
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(UUID.self, forKey: .id)
        name = try c.decode(String.self, forKey: .name)
        vkLink = try c.decode(String.self, forKey: .vkLink)
        peerAddr = try c.decode(String.self, forKey: .peerAddr)
        listenAddr = try c.decode(String.self, forKey: .listenAddr)
        nValue = try c.decode(Int.self, forKey: .nValue)
        wgQuickConfig = try c.decode(String.self, forKey: .wgQuickConfig)
        fallbackLink = try c.decodeIfPresent(String.self, forKey: .fallbackLink) ?? ""
        linkServer = try c.decodeIfPresent(String.self, forKey: .linkServer) ?? "10.77.77.1:8080"
        providerType = try c.decodeIfPresent(String.self, forKey: .providerType) ?? ""
    }
}
