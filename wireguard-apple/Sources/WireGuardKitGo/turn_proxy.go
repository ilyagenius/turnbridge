package main

/*
#include <stdlib.h>

typedef void(*proxy_logger_fn_t)(void *context, int level, const char *msg);

static inline void call_proxy_logger(proxy_logger_fn_t fn, void *ctx, int level, const char *msg) {
    if (fn != NULL) {
        fn(ctx, level, msg);
    }
}

// Captcha callback: called when PoW fails and the app must show a WebView.
// redirectUri is the VK Smart Captcha URL to load in WKWebView.
typedef void(*proxy_captcha_fn_t)(void *context, const char *redirectUri);

static inline void call_proxy_captcha(proxy_captcha_fn_t fn, void *ctx, const char *redirectUri) {
    if (fn != NULL) {
        fn(ctx, redirectUri);
    }
}
*/
import "C"

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/cbeuw/connutil"
	"github.com/google/uuid"
	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

var proxyLoggerFunc C.proxy_logger_fn_t
var proxyLoggerCtx unsafe.Pointer
var proxyCancel context.CancelFunc

// Captcha WebView fallback — set by the Swift side on startup.
var proxyCaptchaFunc C.proxy_captcha_fn_t
var proxyCaptchaCtx unsafe.Pointer

// captchaSolutionCh receives the success_token from ProxySolveCaptcha (Swift → Go).
// Buffered so Swift can fire-and-forget without blocking.
var captchaSolutionCh = make(chan string, 1)

// proxyCaptchaNeeded signals ProxyWaitReady to return 2 (fail-fast flow).
var proxyCaptchaNeeded = make(chan struct{}, 1)

// savedWebViewToken is set by ProxySetCaptchaToken before StartProxy.
var savedWebViewToken string
var savedWebViewTokenMu sync.Mutex

//export ProxySetLogger
func ProxySetLogger(context unsafe.Pointer, loggerFn C.proxy_logger_fn_t) {
	proxyLoggerCtx = context
	proxyLoggerFunc = loggerFn
}

// ProxySetCaptchaHandler registers the Swift callback invoked when the Go proxy
// exhausts all automatic PoW attempts and needs the user to solve captcha in a WebView.
//
//export ProxySetCaptchaHandler
func ProxySetCaptchaHandler(ctx unsafe.Pointer, fn C.proxy_captcha_fn_t) {
	proxyCaptchaCtx = ctx
	proxyCaptchaFunc = fn
}

// ProxySolveCaptcha is called by Swift after the user solves the captcha in the WebView.
// successToken is the value extracted from the captcha page (success_token field).
//
//export ProxySolveCaptcha
func ProxySolveCaptcha(cToken *C.char) {
	token := C.GoString(cToken)
	// Non-blocking send: if nothing is waiting, the token is dropped (old solve).
	select {
	case captchaSolutionCh <- token:
	default:
	}
}

// ProxySetCaptchaToken stores a pre-solved success_token before calling StartProxy.
// Go uses it on the next connection attempt to skip PoW entirely.
//
//export ProxySetCaptchaToken
func ProxySetCaptchaToken(cToken *C.char) {
	savedWebViewTokenMu.Lock()
	defer savedWebViewTokenMu.Unlock()
	savedWebViewToken = C.GoString(cToken)
}

// ProxyWaitReady returns: 0 = timeout, 1 = ready, 2 = captcha_needed (fail-fast).
//
//export ProxyWaitReady
func ProxyWaitReady(timeoutMs C.int) C.int {
	select {
	case <-proxyReady:
		return 1
	case <-proxyCaptchaNeeded:
		return 2
	case <-time.After(time.Duration(timeoutMs) * time.Millisecond):
		return 0
	}
}

type ProxyLogger int

func (l ProxyLogger) Write(p []byte) (n int, err error) {
	if proxyLoggerFunc == nil {
		return len(p), nil
	}

	cleanMsg := bytes.TrimRight(p, "\n")
	cMsg := C.CString(string(cleanMsg))
	defer C.free(unsafe.Pointer(cMsg))

	C.call_proxy_logger(proxyLoggerFunc, proxyLoggerCtx, C.int(l), cMsg)

	return len(p), nil
}

func init() {
	log.SetFlags(0)
	log.SetOutput(ProxyLogger(0))
}

type getCredsFunc func(string) (string, string, string, error)

// vkCredentialsList contains 5 VK app credential pairs.
// getCreds shuffles and rotates through them to reduce per-app rate limiting.
type vkCredentials struct{ id, secret string }

var vkCredentialsList = []vkCredentials{
	{"6287487", "QbYic1K3lEV5kTGiqlq2"},
	{"7879029", "aR5NKGmm03GYrCiNKsaw"},
	{"52461373", "o557NLIkAErNhakXrQ7A"},
	{"52649896", "WStp4ihWG4l3nmXZgIbC"},
	{"51781872", "IjjCNl4L4Tf5QZEXIHKK"},
}

// getCreds tries each VK credential in random order, returning on first success.
// Non-captcha errors advance to the next credential; captcha errors are retried
// with fresh PoW attempts before giving up on a credential.
func getCreds(link string) (string, string, string, error) {
	creds := make([]vkCredentials, len(vkCredentialsList))
	copy(creds, vkCredentialsList)
	mathrand.Shuffle(len(creds), func(i, j int) { creds[i], creds[j] = creds[j], creds[i] })

	var lastErr error
	for i, vc := range creds {
		log.Printf("vk: credential %d/%d (client_id=%s)", i+1, len(creds), vc.id)
		user, pass, addr, err := getVKCredsOnce(link, vc.id, vc.secret)
		if err == nil {
			return user, pass, addr, nil
		}
		log.Printf("vk: client_id=%s failed: %v", vc.id, err)
		lastErr = err
	}
	return "", "", "", fmt.Errorf("all %d VK credentials failed, last: %w", len(creds), lastErr)
}

// getVKCredsOnce fetches VK TURN credentials using a single app credential pair.
// Implements step 1 (anon token) → 1.5 (preview warm-up) → 2 (call token, with
// up to 3 PoW retries on captcha, each with a fresh captcha session) → 3 (okcdn
// login) → 4 (join + TURN creds).
func getVKCredsOnce(link, clientID, clientSecret string) (resUser string, resPass string, resTurn string, resErr error) {
	profile := getRandomProfile()
	name := generateName()
	escapedName := neturl.QueryEscape(name)

	ua := profile.UserAgent
	logUA := ua
	if len(logUA) > 60 {
		logUA = logUA[:60]
	}
	log.Printf("vk: connecting as %q UA=%s...", name, logUA)

	doRequest := func(data string, url string) (resp map[string]interface{}, err error) {
		client := &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		}
		defer client.CloseIdleConnections()
		req, err := http.NewRequest("POST", url, bytes.NewBuffer([]byte(data)))
		if err != nil {
			return nil, err
		}
		req.Header.Add("User-Agent", ua)
		req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

		httpResp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() {
			if closeErr := httpResp.Body.Close(); closeErr != nil {
				log.Printf("close response body: %s", closeErr)
			}
		}()

		body, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, err
		}
		err = json.Unmarshal(body, &resp)
		if err != nil {
			return nil, err
		}
		return resp, nil
	}

	var resp map[string]interface{}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("get TURN creds panic (bad JSON?): %v", resp)
			resErr = fmt.Errorf("panic in getVKCredsOnce: %v", r)
		}
	}()

	// Step 1: anonymous messages token
	step1Data := fmt.Sprintf("client_id=%s&token_type=messages&client_secret=%s&version=1&app_id=%s",
		clientID, clientSecret, clientID)
	var err error
	resp, err = doRequest(step1Data, "https://login.vk.ru/?act=get_anonym_token")
	if err != nil {
		return "", "", "", fmt.Errorf("step1: %w", err)
	}
	token1 := resp["data"].(map[string]interface{})["access_token"].(string)

	// Step 1.5: warm up session — matches reference HAR flow, non-fatal
	previewData := fmt.Sprintf("vk_join_link=https://vk.com/call/join/%s&access_token=%s", link, token1)
	_, _ = doRequest(previewData, fmt.Sprintf("https://api.vk.ru/method/calls.getCallPreview?v=5.275&client_id=%s", clientID))

	// Step 2: anonymous call token, with PoW captcha retry
	step2URL := fmt.Sprintf("https://api.vk.ru/method/calls.getAnonymousToken?v=5.275&client_id=%s", clientID)
	step2Data := fmt.Sprintf("vk_join_link=https://vk.com/call/join/%s&name=%s&access_token=%s",
		link, escapedName, token1)

	const maxCaptchaRounds = 3
	const maxPoWRetries = 3
	var token2 string

	for round := 0; round < maxCaptchaRounds; round++ {
		resp, err = doRequest(step2Data, step2URL)
		if err != nil {
			return "", "", "", fmt.Errorf("step2: %w", err)
		}

		errObj, hasErr := resp["error"].(map[string]interface{})
		if !hasErr {
			token2 = resp["response"].(map[string]interface{})["token"].(string)
			break
		}

		errCode, _ := errObj["error_code"].(float64)
		if int(errCode) != 14 {
			return "", "", "", fmt.Errorf("vk API error: %v", errObj)
		}

		captchaErr := ParseVkCaptchaError(errObj)
		if !captchaErr.IsCaptchaError() {
			return "", "", "", fmt.Errorf("error 14 but no redirect_uri/session_token: %v", errObj)
		}

		// Check if a pre-solved token is available from a previous WebView session.
		savedWebViewTokenMu.Lock()
		presolvedToken := savedWebViewToken
		savedWebViewToken = ""
		savedWebViewTokenMu.Unlock()
		if presolvedToken != "" {
			log.Printf("vk: using pre-solved WebView token, skipping PoW")
			if captchaErr.CaptchaAttempt == "" || captchaErr.CaptchaAttempt == "0" {
				captchaErr.CaptchaAttempt = "1"
			}
			step2Data = fmt.Sprintf(
				"vk_join_link=https://vk.com/call/join/%s&name=%s&access_token=%s"+
					"&captcha_key=&captcha_sid=%s&is_sound_captcha=0&success_token=%s"+
					"&captcha_ts=%s&captcha_attempt=%s",
				link, escapedName, token1,
				captchaErr.CaptchaSid, neturl.QueryEscape(presolvedToken),
				captchaErr.CaptchaTs, captchaErr.CaptchaAttempt,
			)
			continue
		}

		log.Printf("vk: captcha required (round %d/%d), starting PoW", round+1, maxCaptchaRounds)

		// Up to 3 PoW attempts; on failure fetch a fresh captcha URL and retry.
		currentCaptcha := captchaErr
		var powToken string
		var powErr error

		for powTry := 1; powTry <= maxPoWRetries; powTry++ {
			log.Printf("vk: PoW attempt %d/%d", powTry, maxPoWRetries)
			powCtx, powCancel := context.WithTimeout(context.Background(), 30*time.Second)
			powToken, powErr = solveVkCaptcha(powCtx, currentCaptcha)
			powCancel()
			if powErr == nil {
				break
			}
			log.Printf("vk: PoW %d/%d failed: %v", powTry, maxPoWRetries, powErr)
			if powTry < maxPoWRetries {
				// Request a fresh captcha session before the next attempt
				freshData := fmt.Sprintf("vk_join_link=https://vk.com/call/join/%s&name=%s&access_token=%s",
					link, escapedName, token1)
				freshResp, freshErr := doRequest(freshData, step2URL)
				if freshErr == nil {
					if fe, ok := freshResp["error"].(map[string]interface{}); ok {
						freshCaptcha := ParseVkCaptchaError(fe)
						if freshCaptcha.IsCaptchaError() {
							currentCaptcha = freshCaptcha
							log.Printf("vk: refreshed captcha for PoW retry %d", powTry+1)
						}
					}
				}
			}
		}

		if powErr != nil {
			// All automatic PoW attempts failed — fall back to WebView if a handler is set.
			// The currentCaptcha.RedirectUri was already visited by fetchPowInput during the
			// last PoW attempt, so VK would return a stale page. Request a fresh captcha URL
			// that has never been fetched, so the WebView gets a clean session.
			freshData := fmt.Sprintf("vk_join_link=https://vk.com/call/join/%s&name=%s&access_token=%s",
				link, escapedName, token1)
			if freshResp, freshErr := doRequest(freshData, step2URL); freshErr == nil {
				if fe, ok := freshResp["error"].(map[string]interface{}); ok {
					freshCaptcha := ParseVkCaptchaError(fe)
					if freshCaptcha.IsCaptchaError() {
						log.Printf("vk: refreshed captcha URI for WebView")
						currentCaptcha = freshCaptcha
					}
				}
			}
			if proxyCaptchaFunc != nil && currentCaptcha.RedirectUri != "" {
				log.Printf("vk: PoW exhausted, triggering fail-fast for WebView: %s", currentCaptcha.RedirectUri)
				cURI := C.CString(currentCaptcha.RedirectUri)
				C.call_proxy_captcha(proxyCaptchaFunc, proxyCaptchaCtx, cURI)
				C.free(unsafe.Pointer(cURI))

				// Fail fast: signal ProxyWaitReady to return 2 so the VPN goes to
				// "disconnected" while internet is still accessible, allowing the
				// WebView to load. The app will reconnect with the solved token.
				select {
				case proxyCaptchaNeeded <- struct{}{}:
				default:
				}
				return "", "", "", fmt.Errorf("captcha: WebView needed (fail-fast)")
			}
			return "", "", "", fmt.Errorf("captcha: all %d PoW attempts failed: %w", maxPoWRetries, powErr)
		}

		if currentCaptcha.CaptchaAttempt == "" || currentCaptcha.CaptchaAttempt == "0" {
			currentCaptcha.CaptchaAttempt = "1"
		}

		step2Data = fmt.Sprintf(
			"vk_join_link=https://vk.com/call/join/%s&name=%s&access_token=%s"+
				"&captcha_key=&captcha_sid=%s&is_sound_captcha=0&success_token=%s"+
				"&captcha_ts=%s&captcha_attempt=%s",
			link, escapedName, token1,
			currentCaptcha.CaptchaSid, neturl.QueryEscape(powToken),
			currentCaptcha.CaptchaTs, currentCaptcha.CaptchaAttempt,
		)
	}

	if token2 == "" {
		return "", "", "", fmt.Errorf("step2: failed after %d captcha rounds", maxCaptchaRounds)
	}

	// Step 3: OK.ru anonymous login
	step3Data := fmt.Sprintf("%s%s%s",
		"session_data=%7B%22version%22%3A2%2C%22device_id%22%3A%22",
		uuid.New(),
		"%22%2C%22client_version%22%3A1.1%2C%22client_type%22%3A%22SDK_JS%22%7D&method=auth.anonymLogin&format=JSON&application_key=CGMMEJLGDIHBABABA",
	)
	resp, err = doRequest(step3Data, "https://calls.okcdn.ru/fb.do")
	if err != nil {
		return "", "", "", fmt.Errorf("step3: %w", err)
	}
	token3 := resp["session_key"].(string)

	// Step 4: join call and get TURN credentials
	step4Data := fmt.Sprintf(
		"joinLink=%s&isVideo=false&protocolVersion=5&anonymToken=%s&method=vchat.joinConversationByLink&format=JSON&application_key=CGMMEJLGDIHBABABA&session_key=%s",
		link, token2, token3,
	)
	resp, err = doRequest(step4Data, "https://calls.okcdn.ru/fb.do")
	if err != nil {
		return "", "", "", fmt.Errorf("step4: %w", err)
	}

	user := resp["turn_server"].(map[string]interface{})["username"].(string)
	pass := resp["turn_server"].(map[string]interface{})["credential"].(string)
	turnURL := resp["turn_server"].(map[string]interface{})["urls"].([]interface{})[0].(string)

	clean := strings.Split(turnURL, "?")[0]
	address := strings.TrimPrefix(strings.TrimPrefix(clean, "turn:"), "turns:")

	return user, pass, address, nil
}

func dtlsFunc(ctx context.Context, conn net.PacketConn, peer *net.UDPAddr) (net.Conn, error) {
	certificate, err := selfsign.GenerateSelfSigned()
	if err != nil {
		return nil, err
	}
	config := &dtls.Config{
		Certificates:          []tls.Certificate{certificate},
		InsecureSkipVerify:    true,
		ExtendedMasterSecret:  dtls.RequireExtendedMasterSecret,
		CipherSuites:          []dtls.CipherSuiteID{dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
		ConnectionIDGenerator: dtls.OnlySendCIDGenerator(),
	}
	ctx1, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dtlsConn, err := dtls.Client(conn, peer, config)
	if err != nil {
		return nil, err
	}

	if err := dtlsConn.HandshakeContext(ctx1); err != nil {
		return nil, err
	}
	return dtlsConn, nil
}

func oneDtlsConnection(ctx context.Context, peer *net.UDPAddr, listenConn net.PacketConn, connchan chan<- net.PacketConn, okchan chan<- struct{}, singleShot bool, c1 chan<- error) {
	var err error = nil
	defer func() { c1 <- err }()
	dtlsctx, dtlscancel := context.WithCancel(ctx)
	defer dtlscancel()
	var conn1, conn2 net.PacketConn
	conn1, conn2 = connutil.AsyncPacketPipe()
	go func() {
		if singleShot {
			select {
			case <-dtlsctx.Done():
				return
			case connchan <- conn2:
				return
			}
		}
		for {
			select {
			case <-dtlsctx.Done():
				return
			case connchan <- conn2:
			}
		}
	}()
	dtlsConn, err1 := dtlsFunc(dtlsctx, conn1, peer)
	if err1 != nil {
		err = fmt.Errorf("failed to connect DTLS: %s", err1)
		return
	}
	defer func() {
		if closeErr := dtlsConn.Close(); closeErr != nil {
			err = fmt.Errorf("failed to close DTLS connection: %s", closeErr)
			return
		}
		log.Printf("Closed DTLS connection\n")
	}()
	log.Printf("Established DTLS connection!\n")
	select {
	case proxyReady <- struct{}{}:
	default:
	}
	go func() {
		for {
			select {
			case <-dtlsctx.Done():
				return
			case okchan <- struct{}{}:
			}
		}
	}()

	wg := sync.WaitGroup{}
	wg.Add(2)
	context.AfterFunc(dtlsctx, func() {
		listenConn.SetDeadline(time.Now())
		dtlsConn.SetDeadline(time.Now())
	})
	var addr atomic.Value
	go func() {
		defer wg.Done()
		defer dtlscancel()
		buf := make([]byte, 1600)
		for {
			select {
			case <-dtlsctx.Done():
				return
			default:
			}
			n, addr1, err1 := listenConn.ReadFrom(buf)
			if err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}

			addr.Store(addr1)

			_, err1 = dtlsConn.Write(buf[:n])
			if err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		defer dtlscancel()
		buf := make([]byte, 1600)
		for {
			select {
			case <-dtlsctx.Done():
				return
			default:
			}
			n, err1 := dtlsConn.Read(buf)
			if err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}
			addr1, ok := addr.Load().(net.Addr)
			if !ok {
				log.Printf("Failed: no listener ip")
				return
			}

			_, err1 = listenConn.WriteTo(buf[:n], addr1)
			if err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}
		}
	}()

	wg.Wait()
	listenConn.SetDeadline(time.Time{})
	dtlsConn.SetDeadline(time.Time{})
}

type connectedUDPConn struct {
	*net.UDPConn
}

func (c *connectedUDPConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return c.Write(p)
}

type turnParams struct {
	host       string
	port       string
	link       string
	udp        bool
	getCreds   getCredsFunc
	onAllocate func(context.Context, string) error
	singleShot bool
}

func oneTurnConnection(ctx context.Context, turnParams *turnParams, peer *net.UDPAddr, conn2 net.PacketConn, c chan<- error) {
	var err error = nil
	defer func() { c <- err }()
	user, pass, url, err1 := turnParams.getCreds(turnParams.link)
	if err1 != nil {
		err = fmt.Errorf("failed to get TURN credentials: %s", err1)
		return
	}
	urlhost, urlport, err1 := net.SplitHostPort(url)
	if err1 != nil {
		err = fmt.Errorf("failed to parse TURN server address: %s", err1)
		return
	}
	if turnParams.host != "" {
		urlhost = turnParams.host
	}
	if turnParams.port != "" {
		urlport = turnParams.port
	}
	var turnServerAddr string
	turnServerAddr = net.JoinHostPort(urlhost, urlport)
	turnServerUdpAddr, err1 := net.ResolveUDPAddr("udp", turnServerAddr)
	if err1 != nil {
		err = fmt.Errorf("failed to resolve TURN server address: %s", err1)
		return
	}
	turnServerAddr = turnServerUdpAddr.String()
	fmt.Println(turnServerUdpAddr.IP)
	// Dial TURN Server
	var cfg *turn.ClientConfig
	var turnConn net.PacketConn
	var d net.Dialer
	ctx1, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if turnParams.udp {
		conn, err2 := net.DialUDP("udp", nil, turnServerUdpAddr) // nolint: noctx
		if err2 != nil {
			err = fmt.Errorf("failed to connect to TURN server: %s", err2)
			return
		}
		defer func() {
			if err1 = conn.Close(); err1 != nil {
				err = fmt.Errorf("failed to close TURN server connection: %s", err1)
				return
			}
		}()
		turnConn = &connectedUDPConn{conn}
	} else {
		conn, err2 := d.DialContext(ctx1, "tcp", turnServerAddr) // nolint: noctx
		if err2 != nil {
			err = fmt.Errorf("failed to connect to TURN server: %s", err2)
			return
		}
		defer func() {
			if err1 = conn.Close(); err1 != nil {
				err = fmt.Errorf("failed to close TURN server connection: %s", err1)
				return
			}
		}()
		turnConn = turn.NewSTUNConn(conn)
	}
	var addrFamily turn.RequestedAddressFamily
	if peer.IP.To4() != nil {
		addrFamily = turn.RequestedAddressFamilyIPv4
	} else {
		addrFamily = turn.RequestedAddressFamilyIPv6
	}
	// Start a new TURN Client and wrap our net.Conn in a STUNConn
	// This allows us to simulate datagram based communication over a net.Conn
	cfg = &turn.ClientConfig{
		STUNServerAddr:         turnServerAddr,
		TURNServerAddr:         turnServerAddr,
		Conn:                   turnConn,
		Username:               user,
		Password:               pass,
		RequestedAddressFamily: addrFamily,
		LoggerFactory:          logging.NewDefaultLoggerFactory(),
	}

	client, err1 := turn.NewClient(cfg)
	if err1 != nil {
		err = fmt.Errorf("failed to create TURN client: %s", err1)
		return
	}
	defer client.Close()

	// Start listening on the conn provided.
	err1 = client.Listen()
	if err1 != nil {
		err = fmt.Errorf("failed to listen: %s", err1)
		return
	}

	// Allocate a relay socket on the TURN server. On success, it
	// will return a net.PacketConn which represents the remote
	// socket.
	relayConn, err1 := client.Allocate()
	if err1 != nil {
		err = fmt.Errorf("failed to allocate: %s", err1)
		return
	}
	defer func() {
		if err1 := relayConn.Close(); err1 != nil {
			err = fmt.Errorf("failed to close TURN allocated connection: %s", err1)
		}
	}()

	// The relayConn's local address is actually the transport
	// address assigned on the TURN server.
	log.Printf("relayed-address=%s", relayConn.LocalAddr().String())
	if turnParams.onAllocate != nil {
		if err1 := turnParams.onAllocate(ctx, relayConn.LocalAddr().String()); err1 != nil {
			err = fmt.Errorf("failed to signal allocated relay: %s", err1)
			return
		}
	}

	wg := sync.WaitGroup{}
	wg.Add(2)
	turnctx, turncancel := context.WithCancel(context.Background())
	context.AfterFunc(turnctx, func() {
		if err := relayConn.SetDeadline(time.Now()); err != nil {
			log.Printf("Failed to set relay deadline: %s", err)
		}
		if err := conn2.SetDeadline(time.Now()); err != nil {
			log.Printf("Failed to set upstream deadline: %s", err)
		}
	})
	var addr atomic.Value
	// Start read-loop on conn2 (output of DTLS)
	go func() {
		defer wg.Done()
		defer turncancel()
		buf := make([]byte, 1600)
		for {
			select {
			case <-turnctx.Done():
				return
			default:
			}
			n, addr1, err1 := conn2.ReadFrom(buf)
			if err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}

			addr.Store(addr1) // store peer

			_, err1 = relayConn.WriteTo(buf[:n], peer)
			if err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}
		}
	}()

	// Start read-loop on relayConn
	go func() {
		defer wg.Done()
		defer turncancel()
		buf := make([]byte, 1600)
		for {
			select {
			case <-turnctx.Done():
				return
			default:
			}
			n, _, err1 := relayConn.ReadFrom(buf)
			if err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}
			addr1, ok := addr.Load().(net.Addr)
			if !ok {
				log.Printf("Failed: no listener ip")
				return
			}

			_, err1 = conn2.WriteTo(buf[:n], addr1)
			if err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}
		}
	}()

	wg.Wait()
	if err := relayConn.SetDeadline(time.Time{}); err != nil {
		log.Printf("Failed to clear relay deadline: %s", err)
	}
	if err := conn2.SetDeadline(time.Time{}); err != nil {
		log.Printf("Failed to clear upstream deadline: %s", err)
	}
}

func oneDtlsConnectionLoop(ctx context.Context, peer *net.UDPAddr, listenConnChan <-chan net.PacketConn, connchan chan<- net.PacketConn, okchan chan<- struct{}, singleShot bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case listenConn := <-listenConnChan:
			c := make(chan error)
			go oneDtlsConnection(ctx, peer, listenConn, connchan, okchan, singleShot, c)
			if err := <-c; err != nil {
				log.Printf("%s", err)
			}
		}
	}
}

func oneTurnConnectionLoop(ctx context.Context, turnParams *turnParams, peer *net.UDPAddr, connchan <-chan net.PacketConn, t <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case conn2 := <-connchan:
			if turnParams.singleShot {
				c := make(chan error)
				go oneTurnConnection(ctx, turnParams, peer, conn2, c)
				if err := <-c; err != nil {
					log.Printf("%s", err)
				}
				continue
			}
			select {
			case <-t:
				c := make(chan error)
				go oneTurnConnection(ctx, turnParams, peer, conn2, c)
				if err := <-c; err != nil {
					log.Printf("%s", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}
}

type turnCred struct {
	user, pass, addr string
}

func poolCreds(f getCredsFunc, poolSize int) getCredsFunc {
	var mu sync.Mutex
	var pool []turnCred
	var cTime time.Time
	var idx int

	return func(link string) (string, string, string, error) {
		mu.Lock()
		defer mu.Unlock()

		if !cTime.IsZero() && time.Since(cTime) > 10*time.Minute {
			pool = nil
			cTime = time.Time{}
		}

		if len(pool) < poolSize {
			u, p, a, err := f(link)
			if err == nil {
				pool = append(pool, turnCred{u, p, a})
				cTime = time.Now()
				log.Printf("Successfully registered User Identity %d/%d", len(pool), poolSize)

				// Space out requests by 1000ms to avoid API limits
				if len(pool) < poolSize {
					time.Sleep(1000 * time.Millisecond)
				}

				c := pool[len(pool)-1]
				idx++
				return c.user, c.pass, c.addr, nil
			}

			log.Printf("Failed to get unique TURN identity: %v", err)
			if len(pool) > 0 {
				log.Printf("Falling back to reusing a previous identity...")
				c := pool[idx%len(pool)]
				idx++
				return c.user, c.pass, c.addr, nil
			}
			return "", "", "", err
		}

		c := pool[idx%len(pool)]
		idx++
		return c.user, c.pass, c.addr, nil
	}
}

//export StartProxy
func StartProxy(cLink *C.char, cPeerAddr *C.char, cLocalAddr *C.char, cN C.int) {
	select {
	case <-proxyReady:
	default:
	}
	select {
	case <-proxyCaptchaNeeded:
	default:
	}

	link := C.GoString(cLink)
	peerAddrStr := C.GoString(cPeerAddr)
	localAddrStr := C.GoString(cLocalAddr)

	host := ""
	port := "19302"
	n := int(cN)
	udp := true

	ctx, cancel := context.WithCancel(context.Background())
	proxyCancel = cancel
	defer cancel()

	// Detect provider from link
	isWB := strings.Contains(link, "wb") || strings.Contains(link, "wildberries") || strings.Contains(link, "stream.wb")
	isJazz := isJazzSignalingLink(link)
	isTelemost := isTelemostLink(link)

	var credFunc getCredsFunc
	var peer *net.UDPAddr
	var err error
	var onAllocate func(context.Context, string) error
	if isWB {
		log.Printf("Using WB (Wildberries) TURN provider")
		credFunc = getCredsWB
		link = "" // WB creates its own rooms, no link needed
		port = "" // WB: use port from TURN server response (3478), don't override
		peer, err = net.ResolveUDPAddr("udp", peerAddrStr)
		if err != nil {
			log.Printf("Resolve UDP error: %v", err)
			return
		}
	} else if isJazz {
		log.Printf("Using Jazz WebRTC provider")
		if err := startJazzWebRTCProxy(ctx, link, localAddrStr); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Jazz WebRTC failed: %v", err)
		}
		return
	} else if isTelemost {
		log.Printf("Using Telemost WebRTC provider")
		if err := startTelemostWebRTCProxy(ctx, link, localAddrStr); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Telemost WebRTC failed: %v", err)
		}
		return
	} else {
		log.Printf("Using VK TURN provider")
		credFunc = getCreds
		peer, err = net.ResolveUDPAddr("udp", peerAddrStr)
		if err != nil {
			log.Printf("Resolve UDP error: %v", err)
			return
		}
		parts := strings.Split(link, "join/")
		link = parts[len(parts)-1]
		if idx := strings.IndexAny(link, "/?#"); idx != -1 {
			link = link[:idx]
		}
	}

	params := &turnParams{
		host:       host,
		port:       port,
		link:       link,
		udp:        udp,
		getCreds:   poolCreds(credFunc, n),
		onAllocate: onAllocate,
	}

	listenConnChan := make(chan net.PacketConn)
	listenConn, err := net.ListenPacket("udp", localAddrStr)
	if err != nil {
		log.Printf("Failed to listen: %s", err)
		return
	}

	context.AfterFunc(ctx, func() {
		if closeErr := listenConn.Close(); closeErr != nil {
			log.Printf("Failed to close local connection: %s", closeErr)
		}
	})

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case listenConnChan <- listenConn:
			}
		}
	}()

	wg1 := sync.WaitGroup{}
	t := time.Tick(200 * time.Millisecond)

	okchan := make(chan struct{})
	connchan := make(chan net.PacketConn)

	wg1.Go(func() {
		oneDtlsConnectionLoop(ctx, peer, listenConnChan, connchan, okchan, params.singleShot)
	})
	wg1.Go(func() {
		oneTurnConnectionLoop(ctx, params, peer, connchan, t)
	})

	select {
	case <-okchan:
	case <-ctx.Done():
	}

	for i := 0; i < n-1; i++ {
		cChan := make(chan net.PacketConn)
		wg1.Go(func() {
			oneDtlsConnectionLoop(ctx, peer, listenConnChan, cChan, nil, params.singleShot)
		})
		wg1.Go(func() {
			oneTurnConnectionLoop(ctx, params, peer, cChan, t)
		})
	}

	log.Printf("Proxy started on %s", localAddrStr)
	wg1.Wait()
}

//export StopProxy
func StopProxy() {
	if proxyCancel != nil {
		proxyCancel()
		proxyCancel = nil
		log.Println("Proxy gracefully stopped")
	}
}
