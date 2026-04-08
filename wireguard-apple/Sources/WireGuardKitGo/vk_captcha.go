package main

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var captchaMu sync.Mutex

// region VkCaptchaError

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
	if captchaSid == "" {
		if sidNum, ok := errData["captcha_sid"].(float64); ok {
			captchaSid = fmt.Sprintf("%.0f", sidNum)
		}
	}

	captchaImg, _ := errData["captcha_img"].(string)
	errorMsg, _ := errData["error_msg"].(string)

	var sessionToken string
	if redirectUri != "" {
		if parsed, err := neturl.Parse(redirectUri); err == nil {
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

// endregion

// region Captcha types

type captchaBootstrap struct {
	PowInput   string
	Difficulty int
	Settings   *captchaSettingsResponse
}

type captchaSettingsResponse struct {
	ShowCaptchaType string
	SettingsByType  map[string]string
}

type captchaCheckResult struct {
	Status          string
	SuccessToken    string
	ShowCaptchaType string
}

// endregion

// region Captcha session

type captchaSession struct {
	ctx          context.Context
	client       *http.Client
	profile      Profile
	sessionToken string
	hash         string
	browserFp    string
}

func newCaptchaSession(ctx context.Context, client *http.Client, profile Profile, sessionToken string, hash string) *captchaSession {
	return &captchaSession{
		ctx:          ctx,
		client:       client,
		profile:      profile,
		sessionToken: sessionToken,
		hash:         hash,
		browserFp:    generateBrowserFp(profile),
	}
}

func (s *captchaSession) baseValues() neturl.Values {
	values := neturl.Values{}
	values.Set("session_token", s.sessionToken)
	values.Set("domain", "vk.com")
	values.Set("adFp", "")
	values.Set("access_token", "")
	return values
}

func (s *captchaSession) request(method string, values neturl.Values) (map[string]interface{}, error) {
	reqURL := "https://api.vk.ru/method/" + method + "?v=5.131"

	req, err := http.NewRequestWithContext(s.ctx, "POST", reqURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}

	applyBrowserHeaders(req, s.profile)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", "https://id.vk.ru")
	req.Header.Set("Referer", "https://id.vk.ru/")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-GPC", "1")
	req.Header.Set("Priority", "u=1, i")

	httpResp, err := s.client.Do(req)
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

func (s *captchaSession) requestSettings() (*captchaSettingsResponse, error) {
	resp, err := s.request("captchaNotRobot.settings", s.baseValues())
	if err != nil {
		return nil, fmt.Errorf("settings failed: %w", err)
	}
	return parseCaptchaSettingsResponse(resp)
}

func (s *captchaSession) requestComponentDone() error {
	values := s.baseValues()
	values.Set("browser_fp", s.browserFp)
	values.Set("device", buildCaptchaDeviceJSON(s.profile))

	resp, err := s.request("captchaNotRobot.componentDone", values)
	if err != nil {
		return fmt.Errorf("componentDone failed: %w", err)
	}

	respObj, ok := resp["response"].(map[string]interface{})
	if ok {
		if status, _ := respObj["status"].(string); status != "" && status != "OK" {
			return fmt.Errorf("componentDone status: %s", status)
		}
	}

	return nil
}

func (s *captchaSession) requestCheckboxCheck() (*captchaCheckResult, error) {
	return s.requestCheck("[]", base64.StdEncoding.EncodeToString([]byte("{}")))
}

func (s *captchaSession) requestSliderContent(sliderSettings string) (*sliderCaptchaContent, error) {
	values := s.baseValues()
	if sliderSettings != "" {
		values.Set("captcha_settings", sliderSettings)
	}

	resp, err := s.request("captchaNotRobot.getContent", values)
	if err != nil {
		return nil, fmt.Errorf("getContent failed: %w", err)
	}
	return parseSliderCaptchaContentResponse(resp)
}

func (s *captchaSession) requestSliderCheck(activeSteps []int, candidateIndex int, candidateCount int) (*captchaCheckResult, error) {
	answer, err := encodeSliderAnswer(activeSteps)
	if err != nil {
		return nil, err
	}

	return s.requestCheck(generateSliderCursor(candidateIndex, candidateCount), answer)
}

func (s *captchaSession) requestCheck(cursor string, answer string) (*captchaCheckResult, error) {
	debugInfoBytes := md5.Sum([]byte(s.profile.UserAgent + strconv.FormatInt(time.Now().UnixNano(), 10)))
	debugInfo := hex.EncodeToString(debugInfoBytes[:])

	values := s.baseValues()
	values.Set("accelerometer", "[]")
	values.Set("gyroscope", "[]")
	values.Set("motion", "[]")
	values.Set("cursor", cursor)
	values.Set("taps", "[]")
	values.Set("connectionRtt", "[50,50,50,50,50,50,50,50,50,50]")
	values.Set("connectionDownlink", "[9.5,9.5,9.5,9.5,9.5,9.5,9.5,9.5,9.5,9.5,9.5,9.5,9.5,9.5,9.5,9.5]")
	values.Set("browser_fp", s.browserFp)
	values.Set("hash", s.hash)
	values.Set("answer", answer)
	values.Set("debug_info", debugInfo)

	resp, err := s.request("captchaNotRobot.check", values)
	if err != nil {
		return nil, fmt.Errorf("check failed: %w", err)
	}
	return parseCaptchaCheckResultResponse(resp)
}

func (s *captchaSession) requestEndSession() {
	log.Printf("[Captcha] Step 4/4: endSession")
	if _, err := s.request("captchaNotRobot.endSession", s.baseValues()); err != nil {
		log.Printf("[Captcha] Warning: endSession failed: %v", err)
	}
}

// endregion

// region Main captcha solver

func createCaptchaHTTPClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Timeout: 20 * time.Second,
		Jar:     jar,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   20 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
	}
}

func solveVkCaptcha(ctx context.Context, captchaErr *VkCaptchaError, profile Profile) (string, error) {
	captchaMu.Lock()
	defer captchaMu.Unlock()

	log.Printf("[Captcha] Solving captcha...")

	if captchaErr.SessionToken == "" {
		return "", fmt.Errorf("no session_token in redirect_uri")
	}
	if captchaErr.RedirectUri == "" {
		return "", fmt.Errorf("no redirect_uri for captcha solve")
	}

	client := createCaptchaHTTPClient()

	bootstrap, err := fetchCaptchaBootstrap(ctx, captchaErr.RedirectUri, client, profile)
	if err != nil {
		return "", fmt.Errorf("failed to fetch captcha bootstrap: %w", err)
	}

	log.Printf("[Captcha] PoW input: %s, difficulty: %d", bootstrap.PowInput, bootstrap.Difficulty)

	hash := solvePoW(bootstrap.PowInput, bootstrap.Difficulty)
	log.Printf("[Captcha] PoW solved: hash=%s", hash)

	session := newCaptchaSession(ctx, client, profile, captchaErr.SessionToken, hash)

	// Step 1: settings
	log.Printf("[Captcha] Step 1/4: settings")
	settingsResp, err := session.requestSettings()
	if err != nil {
		return "", err
	}
	settingsResp = mergeCaptchaSettings(settingsResp, bootstrap.Settings)

	time.Sleep(200 * time.Millisecond)

	// Step 2: componentDone
	log.Printf("[Captcha] Step 2/4: componentDone")
	if err := session.requestComponentDone(); err != nil {
		return "", err
	}

	time.Sleep(200 * time.Millisecond)

	// Step 3: checkbox check
	log.Printf("[Captcha] Step 3/4: check (checkbox)")
	initialCheck, err := session.requestCheckboxCheck()
	if err != nil {
		return "", err
	}

	if initialCheck.Status == "OK" {
		if initialCheck.SuccessToken == "" {
			return "", fmt.Errorf("success_token not found in checkbox check")
		}
		log.Printf("[Captcha] Checkbox check passed!")
		session.requestEndSession()
		return initialCheck.SuccessToken, nil
	}

	// Checkbox failed — try slider captcha
	sliderSettings, hasSlider := settingsResp.SettingsByType[sliderCaptchaType]
	log.Printf(
		"[Captcha] Checkbox check returned status=%s (settings show_type=%q, check show_type=%q, available_types=%s)",
		initialCheck.Status,
		settingsResp.ShowCaptchaType,
		initialCheck.ShowCaptchaType,
		describeCaptchaTypes(settingsResp.SettingsByType),
	)

	if !hasSlider {
		log.Printf("[Captcha] Slider settings not found. Trying getContent without captcha_settings...")
	} else {
		log.Printf("[Captcha] Trying slider solver...")
	}

	sliderContent, err := session.requestSliderContent(sliderSettings)
	if err != nil {
		return "", fmt.Errorf("checkbox status: %s (slider getContent failed: %w)", initialCheck.Status, err)
	}

	candidates, err := rankSliderCandidates(sliderContent.Image, sliderContent.Size, sliderContent.Steps)
	if err != nil {
		return "", err
	}

	log.Printf(
		"[Captcha] Ranked %d slider positions; submitting top %d (attempt budget %d)",
		len(candidates),
		minInt(sliderContent.Attempts, len(candidates)),
		sliderContent.Attempts,
	)

	successToken, err := trySliderCaptchaCandidates(candidates, sliderContent.Attempts, func(candidate sliderCandidate) (*captchaCheckResult, error) {
		log.Printf("[Captcha] Slider guess position=%d score=%d", candidate.Index, candidate.Score)
		return session.requestSliderCheck(candidate.ActiveSteps, candidate.Index, len(candidates))
	})
	if err != nil {
		return "", err
	}

	log.Printf("[Captcha] Slider solved! Got success_token")
	session.requestEndSession()
	return successToken, nil
}

// endregion

// region Bootstrap & PoW

func fetchCaptchaBootstrap(ctx context.Context, redirectUri string, client *http.Client, profile Profile) (*captchaBootstrap, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", redirectUri, nil)
	if err != nil {
		return nil, err
	}

	applyBrowserHeaders(req, profile)
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return parseCaptchaBootstrapHTML(string(body))
}

func parseCaptchaBootstrapHTML(html string) (*captchaBootstrap, error) {
	powInputRe := regexp.MustCompile(`const\s+powInput\s*=\s*"([^"]+)"`)
	powInputMatch := powInputRe.FindStringSubmatch(html)
	if len(powInputMatch) < 2 {
		return nil, fmt.Errorf("powInput not found in captcha HTML")
	}

	difficulty := 2
	for _, expr := range []*regexp.Regexp{
		regexp.MustCompile(`startsWith\('0'\.repeat\((\d+)\)\)`),
		regexp.MustCompile(`const\s+difficulty\s*=\s*(\d+)`),
	} {
		if match := expr.FindStringSubmatch(html); len(match) >= 2 {
			if parsed, err := strconv.Atoi(match[1]); err == nil {
				difficulty = parsed
				break
			}
		}
	}

	settings, err := parseCaptchaSettingsFromHTML(html)
	if err != nil {
		return nil, err
	}

	return &captchaBootstrap{
		PowInput:   powInputMatch[1],
		Difficulty: difficulty,
		Settings:   settings,
	}, nil
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

// endregion

// region Settings parsing

func parseCaptchaSettingsFromHTML(html string) (*captchaSettingsResponse, error) {
	initRe := regexp.MustCompile(`(?s)window\.init\s*=\s*(\{.*?})\s*;\s*window\.lang`)
	initMatch := initRe.FindStringSubmatch(html)
	if len(initMatch) < 2 {
		return &captchaSettingsResponse{SettingsByType: make(map[string]string)}, nil
	}

	var initPayload struct {
		Data struct {
			ShowCaptchaType string      `json:"show_captcha_type"`
			CaptchaSettings interface{} `json:"captcha_settings"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(initMatch[1]), &initPayload); err != nil {
		return nil, fmt.Errorf("parse window.init captcha data: %w", err)
	}

	return parseCaptchaSettingsResponse(map[string]interface{}{
		"response": map[string]interface{}{
			"show_captcha_type": initPayload.Data.ShowCaptchaType,
			"captcha_settings":  initPayload.Data.CaptchaSettings,
		},
	})
}

func parseCaptchaSettingsResponse(resp map[string]interface{}) (*captchaSettingsResponse, error) {
	respObj, ok := resp["response"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid settings response: %v", resp)
	}

	settings := &captchaSettingsResponse{
		SettingsByType: make(map[string]string),
	}
	settings.ShowCaptchaType, _ = respObj["show_captcha_type"].(string)

	rawSettings, ok := expandCaptchaSettings(respObj["captcha_settings"])
	if !ok {
		return settings, nil
	}

	for _, rawItem := range rawSettings {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			continue
		}

		captchaType, _ := item["type"].(string)
		if captchaType == "" {
			continue
		}

		normalized, err := normalizeCaptchaSettings(item["settings"])
		if err != nil {
			return nil, fmt.Errorf("invalid captcha_settings for %s: %w", captchaType, err)
		}

		settings.SettingsByType[captchaType] = normalized
	}

	return settings, nil
}

func parseCaptchaCheckResultResponse(resp map[string]interface{}) (*captchaCheckResult, error) {
	respObj, ok := resp["response"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid check response: %v", resp)
	}

	result := &captchaCheckResult{}
	result.Status, _ = respObj["status"].(string)
	result.SuccessToken, _ = respObj["success_token"].(string)
	result.ShowCaptchaType, _ = respObj["show_captcha_type"].(string)
	if result.Status == "" {
		return nil, fmt.Errorf("check status missing: %v", resp)
	}

	return result, nil
}

func mergeCaptchaSettings(primary *captchaSettingsResponse, fallback *captchaSettingsResponse) *captchaSettingsResponse {
	if primary == nil {
		return cloneCaptchaSettings(fallback)
	}
	if primary.SettingsByType == nil {
		primary.SettingsByType = make(map[string]string)
	}
	if fallback == nil {
		return primary
	}
	if primary.ShowCaptchaType == "" {
		primary.ShowCaptchaType = fallback.ShowCaptchaType
	}
	for captchaType, settings := range fallback.SettingsByType {
		if _, exists := primary.SettingsByType[captchaType]; !exists {
			primary.SettingsByType[captchaType] = settings
		}
	}
	return primary
}

func cloneCaptchaSettings(src *captchaSettingsResponse) *captchaSettingsResponse {
	if src == nil {
		return nil
	}

	cloned := &captchaSettingsResponse{
		ShowCaptchaType: src.ShowCaptchaType,
		SettingsByType:  make(map[string]string, len(src.SettingsByType)),
	}
	for captchaType, settings := range src.SettingsByType {
		cloned.SettingsByType[captchaType] = settings
	}
	return cloned
}

func expandCaptchaSettings(raw interface{}) ([]interface{}, bool) {
	switch value := raw.(type) {
	case nil:
		return nil, false
	case []interface{}:
		return value, true
	case map[string]interface{}:
		items := make([]interface{}, 0, len(value))
		for captchaType, settings := range value {
			items = append(items, map[string]interface{}{
				"type":     captchaType,
				"settings": settings,
			})
		}
		return items, true
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, false
		}

		var items []interface{}
		if err := json.Unmarshal([]byte(trimmed), &items); err == nil {
			return items, true
		}

		var mapping map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &mapping); err == nil {
			return expandCaptchaSettings(mapping)
		}
	}

	return nil, false
}

func normalizeCaptchaSettings(raw interface{}) (string, error) {
	switch value := raw.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}

// endregion
