import json
import base64

config = {
    "name": "My Server",
    "turn": "https://salutejazz.ru/call/ROOM_ID/PASSWORD",
    "peer": "",
    "listen": "127.0.0.1:9000",
    "n": 1,
    "wg": """[Interface]
PrivateKey = YOUR_CLIENT_PRIVATE_KEY
Address = 10.77.77.2/24
DNS = 8.8.8.8

[Peer]
PublicKey = YOUR_SERVER_PUBLIC_KEY
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 127.0.0.1:9000
PersistentKeepalive = 25"""
}

json_string = json.dumps(config, separators=(',', ':'))
base64_bytes = base64.b64encode(json_string.encode('utf-8'))
base64_string = base64_bytes.decode('utf-8')

final_link = f"turnbridge://{base64_string}"

print("\n=== ===")
print(final_link)
print("========================================\n")

