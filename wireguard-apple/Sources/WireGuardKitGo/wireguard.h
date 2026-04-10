/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2018-2023 WireGuard LLC. All Rights Reserved.
 */

#ifndef WIREGUARD_H
#define WIREGUARD_H

#include <sys/types.h>
#include <stdint.h>
#include <stdbool.h>

typedef void(*logger_fn_t)(void *context, int level, const char *msg);
extern void wgSetLogger(void *context, logger_fn_t logger_fn);
extern int wgTurnOn(const char *settings, int32_t tun_fd);
extern void wgTurnOff(int handle);
extern int64_t wgSetConfig(int handle, const char *settings);
extern char *wgGetConfig(int handle);
extern void wgBumpSockets(int handle);
extern void wgDisableSomeRoamingForBrokenMobileSemantics(int handle);
extern const char *wgVersion();

typedef void (*libxray_sockcallback)(uintptr_t fd, void* ctx);
extern char *LibXrayCutGeoData(const char *datDir, const char *dstDir, const char *cutCodePath);
extern char *LibXrayLoadGeoData(const char *datDir, const char *name, const char *geoType);
extern char *LibXrayPing(const char *datDir, const char *configPath, int timeout, const char *url, const char *proxy);
extern char *LibXrayQueryStats(const char *server, const char *dir);
extern char *LibXrayCustomUUID(const char *text);
extern char *LibXrayTestXray(const char *datDir, const char *configPath);
extern char *LibXrayRunXray(const char *datDir, const char *configPath, int64_t maxMemory);
extern char *LibXrayStopXray();
extern char *LibXrayXrayVersion();
extern char* LibXraySetSockCallback(libxray_sockcallback cb, void* ctx);

extern void StartProxy(const char *link, const char *fallbackLink, const char *peerAddrStr, const char *localAddrStr, int n, const char *linkServerURL);
extern void StopProxy(void);
extern void ProxySetLogger(void *context, logger_fn_t logger_fn);
extern int ProxyWaitReady(int timeoutMs);

// Captcha WebView fallback.
// The callback is invoked when automatic PoW fails; redirectUri is the VK captcha page URL.
typedef void(*proxy_captcha_fn_t)(void *context, const char *redirectUri);
extern void ProxySetCaptchaHandler(void *context, proxy_captcha_fn_t fn);
// Call this from Swift with the success_token extracted from the WebView.
extern void ProxySolveCaptcha(const char *successToken);
// Set a pre-solved token before StartProxy; Go will use it to skip PoW.
extern void ProxySetCaptchaToken(const char *successToken);
// Returns: 0=timeout, 1=ready, 2=captcha_needed (fail-fast).

// Fetch updated room links from link-server via WG tunnel.
// Returns JSON string (caller must free) or NULL on error.
extern char *ProxyFetchLinks(const char *url);

// Push fresh links JSON from main app to Go hot-swap loop via IPC.
extern void ProxySetLinks(const char *linksJSON);

// Set App Group container path (called before StartProxy).
extern void ProxySetContainerPath(const char *path);

// Shared C buffer for link delivery (Swift writes, Go reads).
extern char *pendingLinksPtr;
extern int pendingLinksReady;

#endif
