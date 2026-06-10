import Foundation
import Network

class NetworkTypeDetector {
    static let shared = NetworkTypeDetector()

    private var monitor: NWPathMonitor?
    private(set) var currentType: NetworkType = .unknown

    enum NetworkType {
        case wifi
        case cellular
        case unknown
    }

    func startMonitoring() {
        monitor = NWPathMonitor()
        let queue = DispatchQueue(label: "NetworkTypeDetector")

        monitor?.pathUpdateHandler = { [weak self] path in
            DispatchQueue.main.async {
                self?.currentType = self?.determineNetworkType(path) ?? .unknown
            }
        }

        monitor?.start(queue: queue)
    }

    func stopMonitoring() {
        monitor?.cancel()
        monitor = nil
    }

    private func determineNetworkType(_ path: NWPath) -> NetworkType {
        if path.usesInterfaceType(.wifi) {
            return .wifi
        } else if path.usesInterfaceType(.cellular) {
            return .cellular
        }
        return .unknown
    }

    var isWiFi: Bool {
        return currentType == .wifi
    }
}
