//go:build android

package androidbind

/*
#include <stdint.h>

void disable_fdsan() {
#ifdef __ANDROID__
    extern void android_fdsan_set_error_level(uint32_t) __attribute__((weak));
    if (android_fdsan_set_error_level) {
        android_fdsan_set_error_level(0);
    }
#endif
}
*/
import "C"

import (
	"fmt"
	"os"
	"sync"

	"github.com/xjasonlyu/tun2socks/v2/engine"

	"whitelist-bypass/relay/common"
)

var (
	tunReady sync.WaitGroup
)

func StartTun2Socks(fd, mtu, socksPort int, socksUser, socksPass string) error {
	var proxy string
	if socksUser != "" {
		proxy = fmt.Sprintf("socks5://%s:%s@%s:%d", socksUser, socksPass, common.SocksLocalhostIP, socksPort)
	} else {
		proxy = fmt.Sprintf("socks5://%s:%d", common.SocksLocalhostIP, socksPort)
	}
	logMsg("tun2socks: starting fd=%d mtu=%d proxy=%s", fd, mtu, proxy)
	os.Setenv("TUN2SOCKS_LOG_LEVEL", "info")
	tunReady.Add(1)
	defer tunReady.Done()
	key := &engine.Key{
		Proxy:  proxy,
		Device: fmt.Sprintf("fd://%d", fd),
		MTU:    mtu,
	}
	engine.Insert(key)
	engine.Start()
	logMsg("tun2socks: running")
	return nil
}

func StopTun2Socks() {
	tunReady.Wait()
	C.disable_fdsan()
	engine.Stop()
	logMsg("tun2socks: stopped")
}
