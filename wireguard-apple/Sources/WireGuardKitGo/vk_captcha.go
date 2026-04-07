package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
)

var captchaMu sync.Mutex

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(mathrand.Intn(256))
		}
	}
	return hex.EncodeToString(b)
}

// sha256("") — VK expects a consistent, browser-invariant debug_info value.
const debugInfoHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type VkCaptchaError struct {
	ErrorCode               int
	ErrorMsg                string
	CaptchaSid              string
	CaptchaImg              string
	RedirectUri             string
	IsSoundCaptchaAvailable bool
	SessionToken            string
	CaptchaTs               string
	CaptchaAttempt          string
}

func ParseVkCaptchaError(errData map[string]interface{}) *VkCaptchaError {
	codeFloat, _ := errData["error_code"].(float64)
	code := int(codeFloat)

	redirectUri, _ := errData["redirect_uri"].(string)

	// captcha_sid may be string or float64
	var captchaSid string
	switch v := errData["captcha_sid"].(type) {
	case string:
		captchaSid = v
	case float64:
		captchaSid = fmt.Sprintf("%.0f", v)
	}

	captchaImg, _ := errData["captcha_img"].(string)
	errorMsg, _ := errData["error_msg"].(string)

	var sessionToken string
	if redirectUri != "" {
		if parsed, err := url.Parse(redirectUri); err == nil {
			sessionToken = parsed.Query().Get("session_token")
		}
	}

	isSound, _ := errData["is_sound_captcha_available"].(bool)

	// captcha_ts: float or string
	var captchaTs string
	switch v := errData["captcha_ts"].(type) {
	case float64:
		captchaTs = fmt.Sprintf("%.3f", v)
	case string:
		captchaTs = v
	}

	// captcha_attempt: float or string
	var captchaAttempt string
	switch v := errData["captcha_attempt"].(type) {
	case float64:
		captchaAttempt = fmt.Sprintf("%.0f", v)
	case string:
		captchaAttempt = v
	}

	return &VkCaptchaError{
		ErrorCode:               code,
		ErrorMsg:                errorMsg,
		CaptchaSid:              captchaSid,
		CaptchaImg:              captchaImg,
		RedirectUri:             redirectUri,
		IsSoundCaptchaAvailable: isSound,
		SessionToken:            sessionToken,
		CaptchaTs:               captchaTs,
		CaptchaAttempt:          captchaAttempt,
	}
}

func (e *VkCaptchaError) IsCaptchaError() bool {
	return e.ErrorCode == 14 && e.RedirectUri != "" && e.SessionToken != ""
}

// newCaptchaClient creates an HTTP client with:
//   - a cookie jar (cookies from id.vk.ru flow to api.vk.ru API calls)
//   - a uTLS dialer that presents a Chrome ClientHello (avoids Go JA3 fingerprint)
func newCaptchaClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		// DialTLSContext replaces stdlib TLS with a Chrome-fingerprinted uTLS handshake.
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			uconn := utls.UClient(conn, &utls.Config{ServerName: host}, utls.HelloChrome_Auto)
			if err := uconn.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			return uconn, nil
		},
		DialContext:       dialer.DialContext,
		MaxIdleConns:      10,
		IdleConnTimeout:   90 * time.Second,
		ForceAttemptHTTP2: true,
	}
	return &http.Client{
		Jar:       jar,
		Timeout:   30 * time.Second,
		Transport: transport,
	}
}

// solveVkCaptcha picks a browser profile, fetches the PoW challenge, solves it,
// and submits the 4-step captchaNotRobot API sequence.
// Returns the success_token to include in the VK calls.getAnonymousToken retry.
func solveVkCaptcha(ctx context.Context, captchaErr *VkCaptchaError) (string, error) {
	captchaMu.Lock()
	defer captchaMu.Unlock()

	profile := getRandomProfile()
	ua := profile.UserAgent
	logUA := ua
	if len(logUA) > 60 {
		logUA = logUA[:60]
	}
	log.Printf("[Captcha] Solving PoW — UA: %s...", logUA)

	// Random initial delay (1.5–2.5s) matching real browser HAR timing
	delay := time.Duration(1500+mathrand.Intn(1000)) * time.Millisecond
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}

	// One HTTP client with cookie jar for the entire captcha session.
	// This ensures cookies from the captcha page are sent to the API calls.
	client := newCaptchaClient()

	sessionToken := captchaErr.SessionToken
	if sessionToken == "" {
		return "", fmt.Errorf("no session_token in redirect_uri")
	}

	powInput, difficulty, err := fetchPowInput(ctx, client, captchaErr.RedirectUri, profile)
	if err != nil {
		return "", fmt.Errorf("fetchPoW: %w", err)
	}
	log.Printf("[Captcha] PoW input=%s difficulty=%d", powInput, difficulty)

	hash := solvePoW(powInput, difficulty)
	if hash == "" {
		return "", fmt.Errorf("PoW: no solution within 10M iterations")
	}
	log.Printf("[Captcha] PoW solved: %s...%s", hash[:8], hash[len(hash)-8:])

	// Brief pause after PoW (simulate browser JS execution time)
	time.Sleep(time.Duration(200+mathrand.Intn(300)) * time.Millisecond)

	successToken, err := callCaptchaNotRobot(ctx, client, sessionToken, hash, profile)
	if err != nil {
		return "", fmt.Errorf("captchaNotRobot: %w", err)
	}

	log.Printf("[Captcha] Success! success_token=%d chars", len(successToken))
	return successToken, nil
}

// fetchPowInput loads the VK captcha page and extracts the PoW challenge parameters.
// The shared client carries the resulting cookies into subsequent API calls.
func fetchPowInput(ctx context.Context, client *http.Client, redirectUri string, p Profile) (powInput string, difficulty int, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", redirectUri, nil)
	if err != nil {
		return "", 0, err
	}

	req.Header.Set("User-Agent", p.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,ru;q=0.8")
	req.Header.Set("sec-ch-ua", p.SecChUA())
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", p.SecChUAPlatform())
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("DNT", "1")
	req.Header.Set("Sec-GPC", "1")

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("GET captcha page: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("[Captcha] fetchPoW HTTP status=%d", resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, err
	}
	html := string(body)

	// Extract: const powInput = "..."
	powRe := regexp.MustCompile(`const\s+powInput\s*=\s*"([^"]+)"`)
	m := powRe.FindStringSubmatch(html)
	if len(m) < 2 {
		preview := html
		if len(preview) > 500 {
			preview = preview[:500]
		}
		log.Printf("[Captcha] HTML preview: %s", preview)
		return "", 0, fmt.Errorf("powInput not found (%d bytes)", len(html))
	}
	powInput = m[1]

	// Extract: startsWith('0'.repeat(N))
	diffRe := regexp.MustCompile(`startsWith\('0'\.repeat\((\d+)\)\)`)
	dm := diffRe.FindStringSubmatch(html)
	difficulty = 2
	if len(dm) >= 2 {
		if d, e := strconv.Atoi(dm[1]); e == nil {
			difficulty = d
		}
	}

	return powInput, difficulty, nil
}

// solvePoW brute-forces SHA-256 until the hex digest starts with `difficulty` zeros.
func solvePoW(powInput string, difficulty int) string {
	target := strings.Repeat("0", difficulty)
	for nonce := 1; nonce <= 10_000_000; nonce++ {
		data := powInput + strconv.Itoa(nonce)
		h := sha256.Sum256([]byte(data))
		hexH := hex.EncodeToString(h[:])
		if strings.HasPrefix(hexH, target) {
			return hexH
		}
	}
	return ""
}

// callCaptchaNotRobot executes the 4-step VK captchaNotRobot API handshake.
// The shared client (with cookie jar) must be the same one used for fetchPowInput.
func callCaptchaNotRobot(ctx context.Context, client *http.Client, sessionToken, hash string, p Profile) (string, error) {
	vkReq := func(method, postData string) (map[string]interface{}, error) {
		reqURL := "https://api.vk.ru/method/" + method + "?v=5.131"

		req, err := http.NewRequestWithContext(ctx, "POST", reqURL, strings.NewReader(postData))
		if err != nil {
			return nil, err
		}

		req.Header.Set("User-Agent", p.UserAgent)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9,ru;q=0.8")
		req.Header.Set("Origin", "https://id.vk.ru")
		req.Header.Set("Referer", "https://id.vk.ru/")
		req.Header.Set("sec-ch-ua", p.SecChUA())
		req.Header.Set("sec-ch-ua-mobile", "?0")
		req.Header.Set("sec-ch-ua-platform", p.SecChUAPlatform())
		req.Header.Set("Sec-Fetch-Site", "same-site")
		req.Header.Set("Sec-Fetch-Mode", "cors")
		req.Header.Set("Sec-Fetch-Dest", "empty")
		req.Header.Set("DNT", "1")
		req.Header.Set("Sec-GPC", "1")

		httpResp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("POST %s: %w", method, err)
		}
		defer httpResp.Body.Close()

		body, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, err
		}

		preview := string(body)
		if len(preview) > 300 {
			preview = preview[:300] + "..."
		}
		log.Printf("[Captcha] %s: %s", method, preview)

		var resp map[string]interface{}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal %s: %w", method, err)
		}
		return resp, nil
	}

	domain := "vk.com"
	baseParams := fmt.Sprintf("session_token=%s&domain=%s&adFp=&access_token=",
		url.QueryEscape(sessionToken), url.QueryEscape(domain))

	// 1/4: settings — initialise the captcha session
	log.Printf("[Captcha] 1/4: captchaNotRobot.settings")
	_, err := vkReq("captchaNotRobot.settings", baseParams)
	if err != nil {
		return "", fmt.Errorf("settings: %w", err)
	}
	time.Sleep(time.Duration(100+mathrand.Intn(100)) * time.Millisecond)

	// 2/4: componentDone — send randomised browser fingerprint
	log.Printf("[Captcha] 2/4: captchaNotRobot.componentDone")
	browserFp := fmt.Sprintf("%016x%016x", mathrand.Int63(), mathrand.Int63())

	resolutions := [][]int{{1920, 1080}, {1366, 768}, {1440, 900}, {1536, 864}, {2560, 1440}}
	res := resolutions[mathrand.Intn(len(resolutions))]
	screenW, screenH := res[0], res[1]
	cores := []int{4, 8, 12, 16}[mathrand.Intn(4)]
	ram := []int{4, 8, 16, 32}[mathrand.Intn(4)]
	dpr := []float64{1, 1.25, 1.5, 2}[mathrand.Intn(4)]

	deviceMap := map[string]interface{}{
		"screenWidth":             screenW,
		"screenHeight":            screenH,
		"screenAvailWidth":        screenW,
		"screenAvailHeight":       screenH - 40,
		"innerWidth":              screenW - mathrand.Intn(100),
		"innerHeight":             screenH - 100 - mathrand.Intn(50),
		"devicePixelRatio":        dpr,
		"language":                "en-US",
		"languages":               []string{"en-US", "en", "ru"},
		"webdriver":               false,
		"hardwareConcurrency":     cores,
		"deviceMemory":            ram,
		"connectionEffectiveType": "4g",
		"notificationsPermission": "default",
	}
	deviceBytes, _ := json.Marshal(deviceMap)

	componentDoneData := baseParams + fmt.Sprintf("&browser_fp=%s&device=%s",
		browserFp, url.QueryEscape(string(deviceBytes)))

	_, err = vkReq("captchaNotRobot.componentDone", componentDoneData)
	if err != nil {
		return "", fmt.Errorf("componentDone: %w", err)
	}

	// Reference HAR timing: 1950–3200ms before check submission
	checkDelay := time.Duration(1950+mathrand.Intn(1250)) * time.Millisecond
	log.Printf("[Captcha] Waiting %s before check...", checkDelay.Round(time.Millisecond))
	select {
	case <-time.After(checkDelay):
	case <-ctx.Done():
		return "", ctx.Err()
	}

	// 3/4: check — submit PoW hash and sensor data
	log.Printf("[Captcha] 3/4: captchaNotRobot.check")

	// Cursor points with Unix-ms timestamps (VK validates the T field)
	type CursorPoint struct {
		X int   `json:"x"`
		Y int   `json:"y"`
		T int64 `json:"t"`
	}
	now := time.Now().UnixMilli()
	startX := screenW/2 + mathrand.Intn(200) - 100
	startY := screenH/2 + mathrand.Intn(200) - 100
	pointCount := 4 + mathrand.Intn(5)
	cursor := make([]CursorPoint, pointCount)
	for i := 0; i < pointCount; i++ {
		cursor[i] = CursorPoint{
			X: startX,
			Y: startY,
			T: now - int64((pointCount-i)*500) - int64(mathrand.Intn(100)),
		}
		startX += mathrand.Intn(30) - 15
		startY += mathrand.Intn(30) - 15
	}
	cursorBytes, _ := json.Marshal(cursor)

	// Realistic downlink samples (8–12 Mbps with small jitter)
	baseDownlink := 8.0 + mathrand.Float64()*4.0
	downlinkParts := make([]string, 7)
	for i := range downlinkParts {
		downlinkParts[i] = fmt.Sprintf("%.1f", baseDownlink+mathrand.Float64()*0.5-0.25)
	}
	connectionDownlink := "[" + strings.Join(downlinkParts, ",") + "]"

	answer := base64.StdEncoding.EncodeToString([]byte("{}"))

	checkData := baseParams + fmt.Sprintf(
		"&accelerometer=%s&gyroscope=%s&motion=%s&cursor=%s&taps=%s&connectionRtt=%s&connectionDownlink=%s"+
			"&browser_fp=%s&hash=%s&answer=%s&debug_info=%s",
		url.QueryEscape("[]"),
		url.QueryEscape("[]"),
		url.QueryEscape("[]"),
		url.QueryEscape(string(cursorBytes)),
		url.QueryEscape("[]"),
		url.QueryEscape("[]"),
		url.QueryEscape(connectionDownlink),
		browserFp,
		hash,
		answer,
		debugInfoHash,
	)

	checkResp, err := vkReq("captchaNotRobot.check", checkData)
	if err != nil {
		return "", fmt.Errorf("check: %w", err)
	}

	respObj, ok := checkResp["response"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("check: unexpected response shape: %v", checkResp)
	}

	status, _ := respObj["status"].(string)
	if status != "OK" {
		return "", fmt.Errorf("check: status=%q full=%v", status, checkResp)
	}

	successToken, ok := respObj["success_token"].(string)
	if !ok || successToken == "" {
		return "", fmt.Errorf("check: no success_token in response")
	}

	time.Sleep(200 * time.Millisecond)

	// 4/4: endSession — non-fatal cleanup
	log.Printf("[Captcha] 4/4: captchaNotRobot.endSession")
	_, _ = vkReq("captchaNotRobot.endSession", baseParams)

	return successToken, nil
}
