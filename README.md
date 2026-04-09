# TurnBridge

**TurnBridge** — сетевая утилита для iOS. Позволяет маршрутизировать трафик через TURN-серверы и WireGuard / Amnezia WG эндпоинты, маскируя его под легитимный трафик российских платформ.

> **Важно:** Часть критичных файлов (модули SberJazz и Telemost) удалена из публичного репозитория во избежание блокировок сервисов. Полная версия с поддержкой всех провайдеров доступна за символическую плату.
>
> Контакт: **[@ilkl34](https://t.me/ilkl34)** в Telegram

---

## Провайдеры туннелей

### VK (ВКонтакте)

Оригинальный бэкенд — через **TURN-серверы ВКонтакте** по DTLS. Трафик выглядит как видеозвонок VK.

`turn` = ссылка на VK-звонок, `peer` = адрес VPS с портом vk-turn-proxy.

### Wildberries (WB)

Туннелирует через **TURN-серверы Wildberries** — трафик выглядит как видеостриминг WB.

`turn` = `wb`, `peer` = адрес VPS с портом vk-turn-proxy (например `158.160.x.x:56000`).
### НА ДАННЫЙ МОМЕНТ ЗАБЛОКИРОВАНО, РАЗБИРАЮСЬ.

### SberJazz / SaluteJazz

Туннелирует WireGuard через **WebRTC DataChannel SberJazz** — трафик выглядит как видеозвонок Сбера. Работает в сетях с жёсткой фильтрацией.

В поле `turn` указывается ссылка на комнату Jazz от `jazz-turn-proxy`. Поле `peer` оставить пустым.

> **Требуется серверный компонент.** Контакт: **[@ilkl34](https://t.me/ilkl34)**

### Telemost (Яндекс Телемост)

Туннелирует WireGuard через **WebRTC DataChannel Телемоста** — трафик выглядит как видеозвонок Яндекса.

> **Требуется серверный компонент.** Контакт: **[@ilkl34](https://t.me/ilkl34)**

### FUCK MAX

Туннелирует WireGuard через **TURN-серверы MAX** (ex VK Teams / ICQ New, инфраструктура OK.ru) — трафик выглядит как видеозвонок MAX.

Добавлено:
- Полный auth flow через OneMe WebSocket API + Calls API (fb.do)
- Получение TURN credentials через `joinConversationByLink` (не требует дружбы)
- CreatePermission на произвольные IP — разрешён
- Бейдж провайдера в приложении (cyan)

**Serverless** — серверный компонент НЕ нужен. MAX TURN-серверы напрямую релеят трафик на ваш VPS.

`turn` = `max:<login_token>|<join_link_id>`, `peer` = адрес VPS с портом (например `158.160.x.x:56000`).

> **Как получить токен:** залогиниться в `web.max.ru` через QR → DevTools → WS → opcode 19 → `token`.
> **Как получить join_link_id:** создать звонок в MAX → скопировать ссылку → часть после `/joincall/`.

---

## Возможности

* **Несколько бэкендов:** Jazz, Telemost, WB, VK и MAX
* **WireGuard и Amnezia WG:** полная поддержка включая обфускацию (Jc, Jmin, Jmax, S1-S4, H1-H4)
* **Импорт в 1 клик:** ссылки `turnbridge://` из буфера обмена
* **Мультипрофиль:** несколько VPN-профилей с цветными бейджами провайдеров
* **Автоматическое решение капчи VK:** встроенный солвер слайдер-капчи

## Скриншот
![Главный экран](IMG_1662.jpeg)

---

## Сборка

Нужен macOS + Xcode + Go.

> **Важно:** Подпись через бесплатный Apple ID **не работает** — нужен платный Apple Developer ($99/год) или сторонний сервис подписи.

1. Клон: `git clone https://github.com/ilyagenius/turnbridge.git && cd turnbridge`
2. Собрать Go Bridge: поправить путь в `script/build_wireguard_go_bridge.sh`
3. Открыть `TurnBridge.xcodeproj` в Xcode
4. Настроить подпись (Signing & Capabilities)
5. `Cmd + R`

## Установка готового IPA

Скачать со страницы [Releases](https://github.com/ilyagenius/turnbridge/releases).

| Инструмент | Требования |
|-----------|-----------|
| [KravaSign](https://www.kravasign.com/) | Без сертификата — $10 |
| [GBox](https://gbox.run) | Нужен платный сертификат |
| [ESign](https://esign.yyyue.xyz) | Нужен платный сертификат |

---

## Настройка

| Бэкенд | `turn` | `peer` |
|--------|--------|--------|
| VK | `https://vk.com/call/join/LINK_ID` | `IP_VPS:56000` |
| WB | `wb` | `IP_VPS:56000` |
| Jazz | `https://salutejazz.ru/calls/ROOM_ID?psw=PASSWORD` | *(пусто)* |
| Telemost | `https://telemost.yandex.ru/j/ROOM_ID` | *(пусто)* |
| MAX | `max:<login_token>\|<join_link_id>` | `IP_VPS:56000` |

```json
{
  "name": "Мой сервер",
  "turn": "https://salutejazz.ru/calls/ROOM_ID?psw=PASSWORD",
  "peer": "",
  "listen": "127.0.0.1:9000",
  "n": 1,
  "wg": "[Interface]\nPrivateKey = ...\nAddress = 10.77.77.2/24\nDNS = 8.8.8.8\nMTU = 1280\n\n[Peer]\nPublicKey = ...\nAllowedIPs = 0.0.0.0/0, ::/0\nEndpoint = 127.0.0.1:9000\nPersistentKeepalive = 25"
}
```

Быстрая ссылка: `python3 quick_link.py` → скопировать `turnbridge://...` → в TurnBridge нажать `+` → **Вставить из буфера**

---

## Поддержать проект

**TON:** `UQBisIcwzfQz5Rj0TofZhN2CSZXvUhQrwMmTGEiSSa9ErW5b`

## Лицензия

[GNU General Public License v3.0](LICENSE)

## Благодарности

* [WireGuard-Apple](https://github.com/ut360e/wireguard-apple) — MIT / GPL
* [Wireguardkit](https://github.com/Shahzainali/Wireguardkit) — MIT / GPL
* [vk-turn-proxy](https://github.com/cacggghp/vk-turn-proxy) — GNU GPL
* [Amneziawg-Apple](https://github.com/amnezia-vpn/amneziawg-apple.git) — MIT
