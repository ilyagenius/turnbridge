package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	maxOneMeEndpoint   = "wss://ws-api.oneme.ru/websocket"
	maxOneMeOrigin     = "https://web.max.ru"
	maxCallsEndpoint   = "https://calls.okcdn.ru/fb.do"
	maxApplicationKey  = "CNHIJPLGDIHBABABA"
	maxProtocolVersion = 11
)

// OneMe WebSocket message envelope
type oneMeMsg struct {
	Seq     int         `json:"seq"`
	Opcode  int         `json:"opcode"`
	Payload interface{} `json:"payload"`
	Ver     int         `json:"ver"`
	Cmd     int         `json:"cmd"`
}

// ClientHello payload
type maxClientHello struct {
	UserAgent maxUserAgent `json:"userAgent"`
	DeviceID  string       `json:"deviceId"`
}

type maxUserAgent struct {
	DeviceType      string `json:"deviceType"`
	Locale          string `json:"locale"`
	DeviceLocale    string `json:"deviceLocale"`
	OSVersion       string `json:"osVersion"`
	DeviceName      string `json:"deviceName"`
	UserAgentHeader string `json:"headerUserAgent"`
	AppVersion      string `json:"appVersion"`
	Screen          string `json:"screen"`
	Timezone        string `json:"timezone"`
}

// ChatSyncRequest payload
type maxChatSync struct {
	Token        string `json:"token"`
	Interactive  bool   `json:"interactive"`
	ChatsCount   int    `json:"chatsCount"`
	ChatsSync    int    `json:"chatsSync"`
	ContactsSync int    `json:"contactsSync"`
	PresenceSync int    `json:"presenceSync"`
	DraftsSync   int    `json:"draftsSync"`
}

// auth.anonymLogin session_data
type maxSessionData struct {
	AuthToken     string `json:"auth_token"`
	ClientType    string `json:"client_type"`
	ClientVersion string `json:"client_version"`
	DeviceID      string `json:"device_id"`
	Version       int    `json:"version"`
}

// auth.anonymLogin response
type maxLoginData struct {
	UID            string `json:"uid"`
	SessionKey     string `json:"session_key"`
	ExternalUserID string `json:"external_user_id"`
}

// vchat.startConversation response
type maxStartedConversation struct {
	TurnServer struct {
		URLs       []string `json:"urls"`
		Username   string   `json:"username"`
		Credential string   `json:"credential"`
	} `json:"turn_server"`
}

// getCredsMAX fetches TURN credentials from MAX/OK.ru infrastructure.
// link format:
//   "<login_token>"              — self-call mode (auto-discover target)
//   "<login_token>|<target_id>"  — explicit target
func getCredsMAX(link string) (string, string, string, error) {
	loginToken := link
	explicitTarget := ""
	if idx := strings.LastIndex(link, "|"); idx > 0 && idx < len(link)-1 {
		loginToken = link[:idx]
		explicitTarget = link[idx+1:]
	}

	log.Printf("[MAX] Connecting to OneMe WebSocket...")

	// Step 1: OneMe WebSocket — get call token
	callToken, err := maxGetCallToken(loginToken)
	if err != nil {
		return "", "", "", fmt.Errorf("OneMe auth: %w", err)
	}
	log.Printf("[MAX] Got call token")

	// Step 2: auth.anonymLogin on fb.do → session_key + external_user_id
	login, err := maxCallsLogin(callToken)
	if err != nil {
		return "", "", "", fmt.Errorf("calls login: %w", err)
	}
	log.Printf("[MAX] Logged in (uid=%s, extId=%s)", login.UID, login.ExternalUserID)

	// Determine target: explicit or self-call
	targetID := explicitTarget
	if targetID == "" {
		targetID = login.ExternalUserID
		log.Printf("[MAX] Self-call mode (target=%s)", targetID)
	}

	// Step 3: vchat.startConversation on fb.do → TURN creds
	user, pass, addr, err := maxStartConversation(login.SessionKey, targetID)
	if err != nil {
		return "", "", "", fmt.Errorf("start conversation: %w", err)
	}
	log.Printf("[MAX] Got TURN server: %s", addr)

	return user, pass, addr, nil
}

// maxGetCallToken connects to OneMe, authenticates with stored login token,
// and returns a short-lived call token for the Calls API.
func maxGetCallToken(loginToken string) (string, error) {
	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{},
		HandshakeTimeout: 10 * time.Second,
	}
	conn, _, err := dialer.Dial(maxOneMeEndpoint, http.Header{
		"Origin": {maxOneMeOrigin},
	})
	if err != nil {
		return "", fmt.Errorf("dial OneMe: %w", err)
	}
	defer conn.Close()

	seq := 0

	// ClientHello (opcode 6)
	hello := maxClientHello{
		UserAgent: maxUserAgent{
			DeviceType:      "WEB",
			Locale:          "ru",
			DeviceLocale:    "ru",
			OSVersion:       "Windows",
			DeviceName:      "Chrome",
			UserAgentHeader: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
			AppVersion:      "25.11.2",
			Screen:          "1080x1920 1.0x",
			Timezone:        "Europe/Moscow",
		},
		DeviceID: uuid.NewString(),
	}
	if err := maxSend(conn, seq, 6, hello); err != nil {
		return "", fmt.Errorf("send ClientHello: %w", err)
	}
	if _, err := maxRecv(conn, seq); err != nil {
		return "", fmt.Errorf("recv ClientHello: %w", err)
	}
	seq++

	// ChatSyncRequest (opcode 19)
	chatSync := maxChatSync{
		Token:        loginToken,
		Interactive:  false,
		ChatsCount:   40,
		ChatsSync:    0,
		ContactsSync: 0,
		PresenceSync: 0,
		DraftsSync:   0,
	}
	if err := maxSend(conn, seq, 19, chatSync); err != nil {
		return "", fmt.Errorf("send ChatSync: %w", err)
	}
	if _, err := maxRecv(conn, seq); err != nil {
		return "", fmt.Errorf("recv ChatSync: %w", err)
	}
	seq++

	// CallTokenRequest (opcode 158)
	if err := maxSend(conn, seq, 158, struct{}{}); err != nil {
		return "", fmt.Errorf("send CallTokenRequest: %w", err)
	}
	resp, err := maxRecv(conn, seq)
	if err != nil {
		return "", fmt.Errorf("recv CallTokenRequest: %w", err)
	}

	payload, ok := resp["payload"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid CallToken response payload")
	}
	if errMsg, ok := payload["error"].(string); ok {
		return "", fmt.Errorf("CallToken error: %s", errMsg)
	}
	token, ok := payload["token"].(string)
	if !ok || token == "" {
		return "", fmt.Errorf("no token in CallToken response: %v", payload)
	}
	return token, nil
}

func maxSend(conn *websocket.Conn, seq, opcode int, payload interface{}) error {
	msg := oneMeMsg{
		Seq:     seq,
		Opcode:  opcode,
		Payload: payload,
		Ver:     maxProtocolVersion,
		Cmd:     0,
	}
	return conn.WriteJSON(msg)
}

func maxRecv(conn *websocket.Conn, expectSeq int) (map[string]interface{}, error) {
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		seqVal, ok := msg["seq"].(float64)
		if !ok {
			continue
		}
		if int(seqVal) == expectSeq {
			return msg, nil
		}
	}
}

// maxCallsLogin calls auth.anonymLogin on calls.okcdn.ru/fb.do
// Returns full login data including external_user_id (needed for self-call).
func maxCallsLogin(callToken string) (*maxLoginData, error) {
	sd := maxSessionData{
		AuthToken:     callToken,
		ClientType:    "SDK_JS",
		ClientVersion: "1.1",
		DeviceID:      uuid.NewString(),
		Version:       3,
	}
	sdJSON, _ := json.Marshal(sd)

	body := url.Values{
		"method":          {"auth.anonymLogin"},
		"format":          {"JSON"},
		"application_key": {maxApplicationKey},
		"session_data":    {string(sdJSON)},
	}

	resp, err := maxPost(body)
	if err != nil {
		return nil, err
	}
	var login maxLoginData
	if err := json.Unmarshal(resp, &login); err != nil {
		return nil, fmt.Errorf("parse login: %w (body: %s)", err, string(resp))
	}
	if login.SessionKey == "" {
		return nil, fmt.Errorf("no session_key in login response: %s", string(resp))
	}
	return &login, nil
}

// maxStartConversation calls vchat.startConversation and returns TURN credentials
func maxStartConversation(sessionKey, targetID string) (string, string, string, error) {
	payloadJSON, _ := json.Marshal(map[string]bool{"is_video": false})

	body := url.Values{
		"method":          {"vchat.startConversation"},
		"format":          {"JSON"},
		"application_key": {maxApplicationKey},
		"session_key":     {sessionKey},
		"conversationId":  {uuid.NewString()},
		"isVideo":         {"false"},
		"protocolVersion": {"5"},
		"payload":         {string(payloadJSON)},
		"externalIds":     {targetID},
	}

	resp, err := maxPost(body)
	if err != nil {
		return "", "", "", err
	}
	var conv maxStartedConversation
	if err := json.Unmarshal(resp, &conv); err != nil {
		return "", "", "", fmt.Errorf("parse conversation: %w (body: %s)", err, string(resp))
	}
	if conv.TurnServer.Username == "" {
		return "", "", "", fmt.Errorf("no TURN credentials in response: %s", string(resp))
	}

	// Extract address from first TURN URL
	for _, u := range conv.TurnServer.URLs {
		if strings.HasPrefix(u, "turn") {
			clean := strings.Split(u, "?")[0]
			addr := strings.TrimPrefix(strings.TrimPrefix(clean, "turns:"), "turn:")
			return conv.TurnServer.Username, conv.TurnServer.Credential, addr, nil
		}
	}

	return "", "", "", fmt.Errorf("no TURN URL found in: %v", conv.TurnServer.URLs)
}

func maxPost(form url.Values) ([]byte, error) {
	cl := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{}},
	}
	resp, err := cl.PostForm(maxCallsEndpoint, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return b, nil
}
