# TurnBridge (iOS)

**TurnBridge** — iOS-клиент, который прячет твой интернет-трафик **внутрь видеозвонка**
(VK Звонки, Yandex Telemost, WB Stream, Dion) и обходит блокировки «белых списков».

Для DPI это выглядит как обычный видеозвонок — а внутри течёт твой интернет.

> **Версия на движке `whitelist-bypass` (GPLv3).** Это рабочая переработка: вместо
> прежней схемы WireGuard-через-TURN приложение использует туннель через
> DataChannel/VP8-видео внутри звонка (Pion) с ChaCha20-обфускацией и поддержкой
> 4 сервисов. Старое приложение сохранено в `legacy-wireguard/`.

## Как это работает

```
   Телефон (TurnBridge)                     Сервер-«помощник» (твой VPS)
        │                                            │
        └──── оба заходят в ОДИН видеозвонок ────────┘
                 VK / Telemost / WB / Dion
```

Приложение заходит в звонок (ссылку даёт сервер) и поднимает **локальный SOCKS5**
на телефоне. Дальше ты открываешь этот SOCKS в **[Happ](https://apps.apple.com/app/happ-proxy-utility/id6504287215)**
(бесплатно в App Store) — и весь телефон идёт через туннель, как через VPN.

Серверная часть: [vk-turn-proxy-v2](https://github.com/ilyagenius/vk-turn-proxy-v2)
(ветка `port/whitelist-bypass-engine`).

## Состав

| Папка | Что это |
|---|---|
| `ios-proxy-app/` | SwiftUI-приложение TurnBridge (UI, мост к Go-движку, экспорт SOCKS в Happ/Telegram) |
| `relay/` | Go-движок (туннель, SOCKS5, обфускация); собирается в `Mobile.xcframework` через gomobile |
| `build-ios.sh` | Сборка: gomobile → `Mobile.xcframework`, затем xcodebuild → неподписанный IPA |
| `legacy-wireguard/` | Прежнее приложение (WireGuard-через-TURN) — не используется |

## Сборка

Нужны **полный Xcode**, **Go** и **gomobile**:

```bash
# один раз
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
gomobile init

# сборка
./build-ios.sh
```

Скрипт:
1. `gomobile bind -target=ios ./relay/pion/ios` → `ios-proxy-app/Mobile.xcframework`;
2. `xcodebuild` собирает `.app` (без подписи);
3. пакует неподписанный `prebuilts/whitelist-bypass-proxy.ipa` для сайдлоада.

Подпись/установка: открой `ios-proxy-app/whitelist-bypass-proxy.xcodeproj` в Xcode,
поставь свою **Team** в Signing & Capabilities (`com.turnbridge.app` уже задан),
собери на устройство. Либо ставь неподписанный IPA через AltStore/Sideloadly.

> Имя приложения на экране — **TurnBridge** (`CFBundleDisplayName`). Внутреннее имя
> проекта/таргета осталось `whitelist-bypass-proxy` (на работу не влияет). Полное
> переименование — опционально в Xcode.
>
> Иконку можно заменить в `ios-proxy-app/whitelist-bypass-proxy/Assets.xcassets/AppIcon.appiconset`
> (исходники прежней иконки — в `legacy-wireguard/icons/`).

## Использование

1. На сервере подними креатора/бота → получи **ссылку на звонок**
   (VK / Telemost / `wbstream://...` / `dion://...`).
2. Вставь ссылку в поле, нажми **Go** — поднимется туннель.
3. Нажми **Copy v2ray URL** (или **Open in Telegram**) → вставь в **Happ** →
   весь телефон идёт через туннель.

Настройки (шестерёнка): режим туннеля (DataChannel / Video VP8), авторизация SOCKS,
имя в звонке, VP8 FPS/Batch, dual-track.

## Лицензия

GPLv3 (наследуется от `kulikov0/whitelist-bypass`).
