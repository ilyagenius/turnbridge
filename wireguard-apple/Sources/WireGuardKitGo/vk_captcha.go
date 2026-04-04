package main

import (
    "context"
    "crypto/rand"
    "crypto/sha256"
    "crypto/tls"
    "encoding/base64"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "math"
    mathrand "math/rand"
    "net"
    "net/http"
    "net/url"
    "regexp"
    "strconv"
    "strings"
    "sync"
    "time"
)

var captchaMu sync.Mutex

// Consistent User-Agent across all VK requests (must match getCreds in turn_proxy.go)
const commonUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"

func init() {
    mathrand.Seed(time.Now().UnixNano())
}

func randomHex(n int) string {
    bytes := make([]byte, n)
    if _, err := rand.Read(bytes); err != nil {
        // Fallback
        for i := range bytes {
            bytes[i] = byte(mathrand.Intn(256))
        }
    }
    return hex.EncodeToString(bytes)
}

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
    captchaSid, _ := errData["captcha_sid"].(string)
    captchaImg, _ := errData["captcha_img"].(string)
    errorMsg, _ := errData["error_msg"].(string)

    var sessionToken string
    if redirectUri != "" {
        if parsed, err := url.Parse(redirectUri); err == nil {
            sessionToken = parsed.Query().Get("session_token")
        }
    }

    isSound, _ := errData["is_sound_captcha_available"].(bool)

    var captchaTs string
    if tsFloat, ok := errData["captcha_ts"].(float64); ok {
        captchaTs = fmt.Sprintf("%.0f", tsFloat)
    } else if tsStr, ok := errData["captcha_ts"].(string); ok {
        captchaTs = tsStr
    }

    var captchaAttempt string
    if attFloat, ok := errData["captcha_attempt"].(float64); ok {
        captchaAttempt = fmt.Sprintf("%.0f", attFloat)
    } else if attStr, ok := errData["captcha_attempt"].(string); ok {
        captchaAttempt = attStr
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

func solveVkCaptcha(ctx context.Context, captchaErr *VkCaptchaError) (string, error) {
    captchaMu.Lock()
    defer captchaMu.Unlock()

    sessionToken := captchaErr.SessionToken
    if sessionToken == "" {
        return "", fmt.Errorf("no session_token in redirect_uri")
    }

    const maxRetries = 4
    backoff := 3 * time.Second

    for attempt := 0; attempt < maxRetries; attempt++ {
        if attempt > 0 {
            log.Printf("[Captcha] Retry %d/%d after ERROR_LIMIT, waiting %v...", attempt, maxRetries-1, backoff)
            select {
            case <-ctx.Done():
                return "", ctx.Err()
            case <-time.After(backoff):
            }
            backoff = backoff * 2 // exponential: 3s, 6s, 12s, 24s
        }

        time.Sleep(time.Duration(1000+mathrand.Intn(1500)) * time.Millisecond)

        log.Printf("[Captcha] Solving Not Robot Captcha...")

        powInput, difficulty, err := fetchPowInput(ctx, captchaErr.RedirectUri)
        if err != nil {
            return "", fmt.Errorf("failed to fetch PoW input: %w", err)
        }

        log.Printf("[Captcha] PoW input: %s, difficulty: %d", powInput, difficulty)

        hash := solvePoW(powInput, difficulty)
        log.Printf("[Captcha] PoW solved: hash=%s", hash)

        successToken, err := callCaptchaNotRobot(ctx, sessionToken, hash)
        if err != nil {
            if strings.Contains(err.Error(), "ERROR_LIMIT") {
                log.Printf("[Captcha] Got ERROR_LIMIT, will retry...")
                continue
            }
            return "", fmt.Errorf("captchaNotRobot API failed: %w", err)
        }

        log.Printf("[Captcha] Success! Got success_token")
        return successToken, nil
    }

    return "", fmt.Errorf("captchaNotRobot API failed: ERROR_LIMIT after %d retries", maxRetries)
}

func fetchPowInput(ctx context.Context, redirectUri string) (string, int, error) {
    req, err := http.NewRequestWithContext(ctx, "GET", redirectUri, nil)
    if err != nil {
        return "", 0, err
    }
    
    req.Header.Set("User-Agent", commonUserAgent)
    req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
    req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")

    client := &http.Client{
        Timeout: 20 * time.Second,
        Transport: &http.Transport{
            DialContext: (&net.Dialer{
                Timeout:   30 * time.Second,
                KeepAlive: 30 * time.Second,
            }).DialContext,
            TLSClientConfig: &tls.Config{
                InsecureSkipVerify: false,
            },
        },
    }

    resp, err := client.Do(req)
    if err != nil {
        return "", 0, err
    }
    defer resp.Body.Close()

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return "", 0, err
    }

    html := string(body)

    powInputRe := regexp.MustCompile(`const\s+powInput\s*=\s*"([^"]+)"`)
    powInputMatch := powInputRe.FindStringSubmatch(html)
    if len(powInputMatch) < 2 {
        return "", 0, fmt.Errorf("powInput not found in captcha HTML")
    }
    powInput := powInputMatch[1]

    diffRe := regexp.MustCompile(`startsWith\('0'\.repeat\((\d+)\)\)`)
    diffMatch := diffRe.FindStringSubmatch(html)
    difficulty := 2
    if len(diffMatch) >= 2 {
        if d, err := strconv.Atoi(diffMatch[1]); err == nil {
            difficulty = d
        }
    }

    return powInput, difficulty, nil
}

func solvePoW(powInput string, difficulty int) string {
    target := strings.Repeat("0", difficulty)

    for nonce := 1; nonce <= 10000000; nonce++ {
        data := powInput + strconv.Itoa(nonce)
        hash := sha256.Sum256([]byte(data))
        hexHash := hex.EncodeToString(hash[:])

        if strings.HasPrefix(hexHash, target) {
            return hexHash
        }
    }
    return ""
}

// Generate realistic cursor path with natural acceleration/deceleration
func generateRealisticCursorPath(screenW, screenH int) []map[string]interface{} {
    var cursor []map[string]interface{}

    // Start from somewhere off the widget (natural browsing position)
    startX := float64(100 + mathrand.Intn(300))
    startY := float64(100 + mathrand.Intn(200))

    // Target: center of captcha widget with slight randomness
    targetX := float64(screenW/2 + mathrand.Intn(100) - 50)
    targetY := float64(screenH/2 + mathrand.Intn(80) - 40)

    // Generate main movement path with S-curve acceleration
    totalSteps := 35 + mathrand.Intn(25) // 35-60 steps
    for i := 0; i < totalSteps; i++ {
        t := float64(i) / float64(totalSteps)

        // S-curve (sigmoid): slow start, fast middle, slow end
        ease := (1 - math.Cos(math.Pi*t)) / 2

        x := startX + (targetX-startX)*ease
        y := startY + (targetY-startY)*ease

        // Add natural hand tremor/micro-movements
        tremor := float64(1 + mathrand.Intn(2))
        x += (float64(mathrand.Intn(10)) - 5) / tremor
        y += (float64(mathrand.Intn(8)) - 4) / tremor

        cursor = append(cursor, map[string]interface{}{
            "x": int(x),
            "y": int(y),
        })
    }

    // Fine adjustment phase: hover around target with small movements
    for attempt := 0; attempt < 2+mathrand.Intn(2); attempt++ {
        offsetX := float64(mathrand.Intn(20) - 10)
        offsetY := float64(mathrand.Intn(15) - 7)

        adjustX := targetX + offsetX
        adjustY := targetY + offsetY

        points := 4 + mathrand.Intn(3)
        for j := 0; j < points; j++ {
            cursor = append(cursor, map[string]interface{}{
                "x": int(adjustX) + mathrand.Intn(4) - 2,
                "y": int(adjustY) + mathrand.Intn(4) - 2,
            })
        }

        // Small pause between adjustments
        time.Sleep(time.Duration(100+mathrand.Intn(150)) * time.Millisecond)
    }

    return cursor
}

func callCaptchaNotRobot(ctx context.Context, sessionToken, hash string) (string, error) {
    vkReq := func(method string, postData string) (map[string]interface{}, error) {
        requestURL := "https://api.vk.ru/method/" + method + "?v=5.131"

        req, err := http.NewRequestWithContext(ctx, "POST", requestURL, strings.NewReader(postData))
        if err != nil {
            return nil, err
        }
        
        req.Header.Set("User-Agent", commonUserAgent)
        req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
        req.Header.Set("Accept", "*/*")
        req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
        req.Header.Set("Origin", "https://vk.com")
        req.Header.Set("Referer", "https://vk.com/")
        req.Header.Set("sec-ch-ua-platform", "\"Windows\"")
        req.Header.Set("sec-ch-ua", "\"Chromium\";v=\"128\", \"Not;A=Brand\";v=\"24\", \"Google Chrome\";v=\"128\"")
        req.Header.Set("sec-ch-ua-mobile", "?0")
        req.Header.Set("Sec-Fetch-Site", "same-site")
        req.Header.Set("Sec-Fetch-Mode", "cors")
        req.Header.Set("Sec-Fetch-Dest", "empty")

        client := &http.Client{
            Timeout: 20 * time.Second,
            Transport: &http.Transport{
                DialContext: (&net.Dialer{
                    Timeout:   30 * time.Second,
                    KeepAlive: 30 * time.Second,
                }).DialContext,
            },
        }

        httpResp, err := client.Do(req)
        if err != nil {
            return nil, err
        }
        defer httpResp.Body.Close()

        body, err := io.ReadAll(httpResp.Body)
        if err != nil {
            return nil, err
        }

        var resp map[string]interface{}
        if err := json.Unmarshal(body, &resp); err != nil {
            return nil, err
        }

        return resp, nil
    }

    domain := "vk.com"
    browserFp := randomHex(16)

    // Pick a consistent device profile for this session
    type deviceProfile struct {
        screenW, screenH int
        dpr              float64
        cores            int
        ram              int
    }
    profiles := []deviceProfile{
        {1920, 1080, 1, 8, 8},
        {1920, 1080, 1.25, 12, 16},
        {1366, 768, 1, 4, 8},
        {1536, 864, 1.25, 8, 16},
        {2560, 1440, 2, 16, 32},
        {1440, 900, 1, 8, 8},
        {1920, 1080, 1.5, 8, 16},
    }
    prof := profiles[mathrand.Intn(len(profiles))]
    screenW, screenH := prof.screenW, prof.screenH

    baseParams := fmt.Sprintf("session_token=%s&domain=%s&adFp=&access_token=",
        url.QueryEscape(sessionToken), url.QueryEscape(domain))

    // Step 1/4: settings
    log.Printf("[Captcha] Step 1/4: settings")
    _, err := vkReq("captchaNotRobot.settings", baseParams)
    if err != nil {
        return "", fmt.Errorf("settings failed: %w", err)
    }
    // Human-like delay: page loading + reading (1.5-3 sec)
    time.Sleep(time.Duration(1500+mathrand.Intn(1500)) * time.Millisecond)

    // Step 2/4: componentDone
    log.Printf("[Captcha] Step 2/4: componentDone")

    taskbarH := 30 + mathrand.Intn(20) // 30-50px taskbar
    innerW := screenW - mathrand.Intn(40)
    innerH := screenH - taskbarH - 60 - mathrand.Intn(80)

    deviceMap := map[string]interface{}{
        "screenWidth":             screenW,
        "screenHeight":            screenH,
        "screenAvailWidth":        screenW,
        "screenAvailHeight":       screenH - taskbarH,
        "innerWidth":              innerW,
        "innerHeight":             innerH,
        "outerWidth":              screenW,
        "outerHeight":             screenH - taskbarH,
        "devicePixelRatio":        prof.dpr,
        "colorDepth":              24,
        "language":                "ru-RU",
        "languages":               []string{"ru-RU", "ru", "en-US", "en"},
        "webdriver":               false,
        "hardwareConcurrency":     prof.cores,
        "deviceMemory":            prof.ram,
        "connectionEffectiveType": "4g",
        "connectionType":          "wifi",
        "notificationsPermission": "default",
        "platform":                "Win32",
        "maxTouchPoints":          0,
        "vendor":                  "Google Inc.",
        "cookieEnabled":           true,
        "doNotTrack":              nil,
    }
    deviceBytes, _ := json.Marshal(deviceMap)

    componentDoneData := baseParams + fmt.Sprintf("&browser_fp=%s&device=%s",
        browserFp, url.QueryEscape(string(deviceBytes)))

    _, err = vkReq("captchaNotRobot.componentDone", componentDoneData)
    if err != nil {
        return "", fmt.Errorf("componentDone failed: %w", err)
    }
    // Human-like delay: component rendered, user analyzing captcha (2-4 sec)
    time.Sleep(time.Duration(2000+mathrand.Intn(2000)) * time.Millisecond)

    // Step 3/4: check
    log.Printf("[Captcha] Step 3/4: check")

    // Generate realistic cursor movement
    cursorData := generateRealisticCursorPath(screenW, screenH)
    cursorBytes, _ := json.Marshal(cursorData)

    // Realistic network metrics (physical WiFi/mobile values)
    // WiFi typical: 10-50 Mbps, mobile: 5-30 Mbps
    baseSpeed := float64(15+mathrand.Intn(25)) + float64(mathrand.Intn(100))/100.0 // 15.0-39.99 Mbps
    var downlink []float64
    for i := 0; i < 16; i++ {
        // Fluctuation: ±15%
        variation := (float64(mathrand.Intn(30)) - 15) / 100.0
        dl := baseSpeed * (1 + variation)
        if dl < 5.0 {
            dl = 5.0
        }
        downlink = append(downlink, float64(int(dl*100))/100.0)
    }
    downlinkBytes, _ := json.Marshal(downlink)

    // Realistic RTT: 20-100ms for typical WiFi/mobile
    var rtt []int
    baseRtt := 25 + mathrand.Intn(60) // 25-85ms
    for i := 0; i < 16; i++ {
        r := baseRtt + mathrand.Intn(15) - 7 // ±7ms variation
        if r < 5 {
            r = 5
        }
        rtt = append(rtt, r)
    }
    rttBytes, _ := json.Marshal(rtt)

    answer := base64.StdEncoding.EncodeToString([]byte("{}"))
    debugInfo := randomHex(32)

    checkData := baseParams + fmt.Sprintf(
        "&accelerometer=%s&gyroscope=%s&motion=%s&cursor=%s&taps=%s&connectionRtt=%s&connectionDownlink=%s"+
            "&browser_fp=%s&hash=%s&answer=%s&debug_info=%s",
        url.QueryEscape("[]"),
        url.QueryEscape("[]"),
        url.QueryEscape("[]"),
        url.QueryEscape(string(cursorBytes)),
        url.QueryEscape("[]"),
        url.QueryEscape(string(rttBytes)),
        url.QueryEscape(string(downlinkBytes)),
        browserFp,
        hash,
        answer,
        debugInfo,
    )

    checkResp, err := vkReq("captchaNotRobot.check", checkData)
    if err != nil {
        return "", fmt.Errorf("check failed: %w", err)
    }

    respObj, ok := checkResp["response"].(map[string]interface{})
    if !ok {
        return "", fmt.Errorf("invalid check response: %v", checkResp)
    }

    status, _ := respObj["status"].(string)
    if status != "OK" {
        return "", fmt.Errorf("check response status: %s, full response: %v", status, checkResp)
    }

    successToken, ok := respObj["success_token"].(string)
    if !ok || successToken == "" {
        return "", fmt.Errorf("success_token not found in check response: %v", checkResp)
    }
    
    // Delay after solving (600-1500ms)
    time.Sleep(time.Duration(600+mathrand.Intn(900)) * time.Millisecond)

    log.Printf("[Captcha] Step 4/4: endSession")
    _, err = vkReq("captchaNotRobot.endSession", baseParams)
    if err != nil {
        log.Printf("[Captcha] Warning: endSession failed: %v", err)
    }

    return successToken, nil
}
