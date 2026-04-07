# TurnBridge

**TurnBridge** is annetwork utility. It allows you to securely route your iOS network traffic through TURN servers and WireGuard / Amnezia WG endpoints.

To run the application, you must use a [server](https://github.com/cacggghp/vk-turn-proxy/releases/tag/v1.0.0) running on a VPS.

The project is based on the repositories listed in the **Acknowledgments** section.

## 🔒 Tunnel Providers

TurnBridge supports multiple WebRTC/TURN tunnel backends, each disguising WireGuard traffic as legitimate platform traffic:

### SberJazz / SaluteJazz
Tunnels WireGuard through **SberJazz WebRTC datachannels** — traffic looks like a Sber video call. No direct UDP to the server. Works in heavily filtered networks.

Set `turn` to the Jazz room link printed by `jazz-turn-proxy`, e.g. `https://salutejazz.ru/call/ROOM_ID/PASSWORD`. Leave `peer` empty — the server-side proxy already knows the WireGuard endpoint.

> **Server component required.**
> Due to ongoing blocking in Russia, the `jazz-turn-proxy` server binary is distributed privately for a small fee.
> Contact **[@ilkl34](https://t.me/ilkl34)** on Telegram.

### Wildberries (WB)
Tunnels WireGuard through **Wildberries TURN servers** — traffic looks like WB video streaming.

Set `turn` to `wb` and `peer` to your VPS address with the vk-turn-proxy port (e.g. `158.160.x.x:56000`).

### VK (VKontakte)
The original backend — tunnels through **VK TURN servers** using DTLS. Traffic looks like a VK video call.

Set `turn` to a VK call join link and `peer` to your VPS address with vk-turn-proxy port.


---

## ✨ Features

* **Multiple Tunnel Backends:** Jazz (SberJazz), WB (Wildberries), and VK TURN — all disguising traffic as legitimate Russian platform traffic.
* **WireGuard & Amnezia WG Integration:**
  - Complete WireGuard protocol support with key management, routing, and DNS configuration
  - Full Amnezia WireGuard obfuscation support including jitter parameters (Jc, Jmin, Jmax), packet size obfuscation (S1-S4), and magic headers (H1-H4)
* **1-Click Import:** Quickly import complex configurations via base64-encoded clipboard links (`turnbridge://`).
* **Multi-Profile Management:** Create, edit (swipe left), and switch between multiple VPN profiles — each showing its provider (Jazz / VK / WB) with a color badge.
* **Redesigned UI:** Live connection uptime timer, per-provider color badges, swipe-to-delete profiles, animated connection orb.

## 📸 Screenshot
![Main Screen](IMG_1662.jpeg)

## 🚀 Installation & Build

To build and run TurnBridge locally, you need a macOS environment with Xcode installed, as well as Go (for compiling the WireGuard/TURN bridge).

> ⚠️ **Important:** TurnBridge uses a Network Extension (VPN). Signing with a **free Apple ID** (via standard AltStore or Sideloadly) **will not work** because free accounts lack the required VPN entitlements. You must use a paid Apple Developer account ($99/year) or a third-party paid signing service.

1. **Clone the repository:**
   ```bash
   git clone https://github.com/nullcstring/turnbridge.git
   cd TurnBridge
   ```

2. **Build the Go Bridge:**
Ensure you have Go installed (`brew install go`).

Modify the Go path in the script at script/build_wireguard_go_bridge.sh, according to your setup. Refer to [this Stack Overflow answer](https://stackoverflow.com/a/64212121) for guidance.

3. **Open the project in Xcode:**
Open `TurnBridge.xcodeproj` (or `.xcworkspace` if applicable) in Xcode.
4. **Configure Code Signing:**
* Select the `TurnBridge` project in the Project Navigator.
* Go to the **Signing & Capabilities** tab.
* Select your personal Apple Developer Team.
* Ensure you update the Bundle Identifier (for both the main app and the `network-extension` target) to match your team provisioning profile.

5. **Build and Run:**
Select your target device (iPhone/iPad) and press `Cmd + R` to build and run the app.

## 📲 Install Pre-built IPA (No Xcode Required)

If you don't have a Mac or Xcode, you can download the pre-built unsigned IPA from the [Releases](https://github.com/nullcstring/turnbridge/releases) page and sign it yourself.

### Signing & Installation

**Manual Installation Tools:**
If you already possess a paid Apple Developer certificate (or bought one from the services above), you can sign and install the IPA yourself using:

| Tool | Requirement |
|------|-------------|
| [KravaSign](https://www.kravasign.com/) | ⚠️ Without a developer certificate, price $10 https://github.com/nullcstring/turnbridge/issues/2#issuecomment-4129716584 |
| [GBox](https://gbox.run) | Paid Certificate Needed |
| [ESign](https://esign.yyyue.xyz) | Paid Certificate Needed |


## 🛠 Usage (Configuration Import)

TurnBridge uses a specific JSON structure encoded in Base64 for fast configuration imports for WireGuard / Amnezia WG.

### Configuration JSON Structure

The `turn` field selects both the backend and the room:

| Backend | `turn` value | `peer` value |
|---------|-------------|--------------|
| Jazz    | `https://salutejazz.ru/call/ROOM_ID/PASSWORD` | *(empty)* |
| WB      | `wb` | `YOUR_VPS_IP:56000` |
| VK      | `https://vk.com/call/join/LINK_ID` | `YOUR_VPS_IP:56000` |

```json
{
  "name": "My Server",
  "turn": "https://salutejazz.ru/call/ROOM_ID/PASSWORD",
  "peer": "",
  "listen": "127.0.0.1:9000",
  "n": 1,
  "wg": "[Interface]\nPrivateKey = ...\nAddress = 10.77.77.2/24\nDNS = 8.8.8.8\nMTU = 1280\n\n[Peer]\nPublicKey = ...\nAllowedIPs = 0.0.0.0/0, ::/0\nEndpoint = 127.0.0.1:9000\nPersistentKeepalive = 25"
}
```

> For Jazz mode the `peer` field must be **empty** — the server-side `jazz-turn-proxy` already knows the WireGuard endpoint.  
> For WB/VK mode `peer` points to your VPS running `vk-turn-proxy` (default port `56000`).

### Generate a Quick Import Link

You can use the included `quick_link.py` script to easily generate valid `turnbridge://` clipboard links.

Fill in the `config` dict with your chosen provider settings (see table above), then run:

1. Open `quick_link.py` in your text editor and replace the placeholder values in the `config` dictionary with your actual server parameters and WireGuard keys.
2. Run the script from your terminal:
   ```bash
   python3 quick_link.py
   ```

3. Copy the generated `turnbridge://...` link from the terminal output to your iOS clipboard.
4. Open TurnBridge, tap the `+` icon, select **Paste from Clipboard**, and tap **Connect**.

## ☕ Support My Work

If TurnBridge saved you some time, consider supporting its development! As an independent open-source project, any contribution is greatly appreciated.

**Crypto:**
* **TON:** `UQBisIcwzfQz5Rj0TofZhN2CSZXvUhQrwMmTGEiSSa9ErW5b`

Thank you for keeping the open-source spirit alive! 🚀

## License

TurnBridge is released under the [GNU General Public License v3.0](LICENSE).

Copyright (C) 2026 nullcstring

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU General Public License for more details.

---
## Acknowledgements

This project was made possible thanks to the work of the open-source community. It includes code and concepts from the following excellent repositories:

* [WireGuard-Apple](https://github.com/ut360e/wireguard-apple) — Licensed under MIT / GPL.
* [Wireguardkit](https://github.com/Shahzainali/Wireguardkit) — Licensed under MIT / GPL.
* [vk-turn-proxy](https://github.com/cacggghp/vk-turn-proxy) — Licensed under the GNU GPL.
* [Amneziawg-Apple](https://github.com/amnezia-vpn/amneziawg-apple.git) — Licensed under MIT.

