// max-auth: one-time SMS authentication for MAX messenger.
// Produces a login_token for use in TurnBridge profiles.
//
// Usage:
//   go run . -phone +79991234567
//   go run . -phone +79991234567 -test   (also verifies TURN creds work)
//
// The tool will:
//  1. Connect to OneMe WebSocket
//  2. Request SMS code for the given phone
//  3. Prompt you to enter the code from SMS
//  4. Authenticate and obtain login_token + external_user_id
//  5. Optionally test the full TURN credential flow (self-call)
//  6. Print the ready-to-use TurnBridge link: max:<login_token>
package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	oneMeEndpoint  = "wss://ws-api.oneme.ru/websocket"
	oneMeOrigin    = "https://web.max.ru"
	callsEndpoint  = "https://calls.okcdn.ru/fb.do"
	applicationKey = "CNHIJPLGDIHBABABA"
	protoVer       = 11
)

// --- OneMe message types ---

type wsMsg struct {
	Seq     int         `json:"seq"`
	Opcode  int         `json:"opcode"`
	Payload interface{} `json:"payload"`
	Ver     int         `json:"ver"`
	Cmd     int         `json:"cmd"`
}

type clientHello struct {
	UserAgent userAgent `json:"userAgent"`
	DeviceID  string    `json:"deviceId"`
}

type userAgent struct {
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

type verificationReq struct {
	Phone    string `json:"phone"`
	Type     string `json:"type"`
	Language string `json:"language"`
}

type codeEnter struct {
	Token         string `json:"token"`
	VerifyCode    string `json:"verifyCode"`
	AuthTokenType string `json:"authTokenType"`
}

type chatSyncReq struct {
	Token        string `json:"token"`
	Interactive  bool   `json:"interactive"`
	ChatsCount   int    `json:"chatsCount"`
	ChatsSync    int    `json:"chatsSync"`
	ContactsSync int    `json:"contactsSync"`
	PresenceSync int    `json:"presenceSync"`
	DraftsSync   int    `json:"draftsSync"`
}

// --- Calls API types ---

type sessionData struct {
	AuthToken     string `json:"auth_token"`
	ClientType    string `json:"client_type"`
	ClientVersion string `json:"client_version"`
	DeviceID      string `json:"device_id"`
	Version       int    `json:"version"`
}

type loginData struct {
	UID            string `json:"uid"`
	SessionKey     string `json:"session_key"`
	ExternalUserID string `json:"external_user_id"`
}

type startedConversation struct {
	TurnServer struct {
		URLs       []string `json:"urls"`
		Username   string   `json:"username"`
		Credential string   `json:"credential"`
	} `json:"turn_server"`
	StunServer struct {
		URLs []string `json:"urls"`
	} `json:"stun_server"`
	Endpoint string `json:"endpoint"`
}

// --- WebSocket helpers ---

type oneMeClient struct {
	conn *websocket.Conn
	seq  int
}

func newOneMeClient() (*oneMeClient, error) {
	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{},
		HandshakeTimeout: 10 * time.Second,
	}
	conn, _, err := dialer.Dial(oneMeEndpoint, http.Header{
		"Origin": {oneMeOrigin},
	})
	if err != nil {
		return nil, fmt.Errorf("dial OneMe: %w", err)
	}
	return &oneMeClient{conn: conn}, nil
}

func (c *oneMeClient) send(opcode int, payload interface{}) error {
	msg := wsMsg{Seq: c.seq, Opcode: opcode, Payload: payload, Ver: protoVer, Cmd: 0}
	return c.conn.WriteJSON(msg)
}

func (c *oneMeClient) recv() (map[string]interface{}, error) {
	expectSeq := c.seq
	c.seq++
	c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, data, err := c.conn.ReadMessage()
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

func (c *oneMeClient) payload(resp map[string]interface{}) (map[string]interface{}, error) {
	p, ok := resp["payload"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("no payload in response")
	}
	if errMsg, ok := p["error"].(string); ok {
		return nil, fmt.Errorf("server error: %s", errMsg)
	}
	return p, nil
}

func (c *oneMeClient) close() { c.conn.Close() }

// --- Calls API helpers ---

func callsPost(form url.Values) ([]byte, error) {
	cl := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{}},
	}
	resp, err := cl.PostForm(callsEndpoint, form)
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

func main() {
	phone := flag.String("phone", "", "Phone number in +7XXXXXXXXXX format")
	token := flag.String("token", "", "Login token (from browser DevTools, skips SMS flow)")
	target := flag.String("target", "", "Target external_user_id (friend's ID). If empty, uses self-call")
	test := flag.Bool("test", false, "Test TURN credential retrieval after auth")
	flag.Parse()

	if *phone == "" && *token == "" {
		fmt.Println("Usage:")
		fmt.Println("  go run . -token <login_token> -test -target <friend_id>")
		fmt.Println("  go run . -phone +79991234567 -test")
		os.Exit(1)
	}

	var loginToken string

	if *token != "" {
		// --- Direct token mode: skip SMS, go straight to CallToken ---
		loginToken = *token
		fmt.Println("Using provided login token")
	} else {
		// --- SMS auth mode ---
		fmt.Println("[1/6] Connecting to OneMe WebSocket...")
		client, err := newOneMeClient()
		fatal(err, "connect")
		defer client.close()

		fmt.Println("[2/6] Sending ClientHello...")
		err = client.send(6, clientHello{
			UserAgent: userAgent{
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
		})
		fatal(err, "send ClientHello")
		helloResp, err := client.recv()
		fatal(err, "recv ClientHello")
		helloJSON, _ := json.MarshalIndent(helloResp, "", "  ")
		fmt.Printf("    ClientHello response: %s\n", string(helloJSON))

		fmt.Printf("[3/6] Requesting SMS to %s...\n", *phone)
		err = client.send(17, verificationReq{
			Phone:    *phone,
			Type:     "START_AUTH",
			Language: "ru",
		})
		fatal(err, "send VerificationRequest")
		resp, err := client.recv()
		fatal(err, "recv VerificationRequest")
		respJSON, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Printf("    VerificationRequest raw response: %s\n", string(respJSON))
		p, err := client.payload(resp)
		fatal(err, "VerificationRequest payload")
		verifyToken, ok := p["token"].(string)
		if !ok || verifyToken == "" {
			fatal(fmt.Errorf("no verification token: %v", p), "VerificationRequest")
		}
		fmt.Println("    SMS sent!")

		fmt.Print("[4/6] Enter SMS code: ")
		reader := bufio.NewReader(os.Stdin)
		code, _ := reader.ReadString('\n')
		code = strings.TrimSpace(code)
		if code == "" {
			fatal(fmt.Errorf("empty code"), "input")
		}

		err = client.send(18, codeEnter{
			Token:         verifyToken,
			VerifyCode:    code,
			AuthTokenType: "CHECK_CODE",
		})
		fatal(err, "send CodeEnter")
		resp, err = client.recv()
		fatal(err, "recv CodeEnter")
		p, err = client.payload(resp)
		fatal(err, "CodeEnter payload")

		tokenAttrs, ok := p["tokenAttrs"].(map[string]interface{})
		if !ok {
			fatal(fmt.Errorf("no tokenAttrs: %v", p), "CodeEnter")
		}
		loginObj, ok := tokenAttrs["LOGIN"].(map[string]interface{})
		if !ok {
			fatal(fmt.Errorf("no LOGIN in tokenAttrs: %v", tokenAttrs), "CodeEnter")
		}
		loginToken, ok = loginObj["token"].(string)
		if !ok || loginToken == "" {
			fatal(fmt.Errorf("no token in LOGIN: %v", loginObj), "CodeEnter")
		}
		fmt.Println("    Authenticated!")
	}

	// --- ChatSync + CallToken (both modes) ---
	fmt.Println("[5/6] Connecting to OneMe for call token...")
	client2, err := newOneMeClient()
	fatal(err, "connect for CallToken")
	defer client2.close()

	err = client2.send(6, clientHello{
		UserAgent: userAgent{
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
	})
	fatal(err, "send ClientHello 2")
	_, err = client2.recv()
	fatal(err, "recv ClientHello 2")

	err = client2.send(19, chatSyncReq{
		Token:        loginToken,
		Interactive:  false,
		ChatsCount:   40,
		ChatsSync:    0,
		ContactsSync: 0,
		PresenceSync: 0,
		DraftsSync:   0,
	})
	fatal(err, "send ChatSync")
	chatResp, err := client2.recv()
	fatal(err, "recv ChatSync")
	chatJSON, _ := json.MarshalIndent(chatResp, "", "  ")
	fmt.Printf("    ChatSync response: %s\n", string(chatJSON))

	err = client2.send(158, struct{}{})
	fatal(err, "send CallTokenRequest")
	resp2, err := client2.recv()
	fatal(err, "recv CallTokenRequest")
	p2, err := client2.payload(resp2)
	fatal(err, "CallTokenRequest payload")
	callToken, ok := p2["token"].(string)
	if !ok || callToken == "" {
		fatal(fmt.Errorf("no call token: %v", p2), "CallTokenRequest")
	}
	fmt.Println("    Got call token!")

	// --- Calls API login ---
	fmt.Println("[6/6] Logging into Calls API...")
	sd := sessionData{
		AuthToken:     callToken,
		ClientType:    "SDK_JS",
		ClientVersion: "1.1",
		DeviceID:      uuid.NewString(),
		Version:       3,
	}
	sdJSON, _ := json.Marshal(sd)

	body, err := callsPost(url.Values{
		"method":          {"auth.anonymLogin"},
		"format":          {"JSON"},
		"application_key": {applicationKey},
		"session_data":    {string(sdJSON)},
	})
	fatal(err, "auth.anonymLogin")

	var login loginData
	fatal(json.Unmarshal(body, &login), "parse login")
	if login.SessionKey == "" {
		fatal(fmt.Errorf("no session_key: %s", string(body)), "login")
	}

	fmt.Println()
	fmt.Println("=== SUCCESS ===")
	fmt.Printf("Login Token:      %s\n", loginToken)
	fmt.Printf("External User ID: %s\n", login.ExternalUserID)
	fmt.Printf("UID:              %s\n", login.UID)
	fmt.Println()
	fmt.Printf("TurnBridge link:  max:%s\n", loginToken)
	fmt.Println()

	// --- Optional: test TURN ---
	if *test {
		targetID := login.ExternalUserID
		if *target != "" {
			targetID = *target
		}
		fmt.Printf("=== TESTING TURN CREDENTIALS (target=%s) ===\n", targetID)
		testTURN(login.SessionKey, targetID)
	}
}

func testTURN(sessionKey, externalID string) {
	payloadJSON, _ := json.Marshal(map[string]bool{"is_video": false})
	body, err := callsPost(url.Values{
		"method":          {"vchat.startConversation"},
		"format":          {"JSON"},
		"application_key": {applicationKey},
		"session_key":     {sessionKey},
		"conversationId":  {uuid.NewString()},
		"isVideo":         {"false"},
		"protocolVersion": {"5"},
		"payload":         {string(payloadJSON)},
		"externalIds":     {externalID},
	})
	if err != nil {
		fmt.Printf("TURN test FAILED: %v\n", err)
		return
	}

	var conv startedConversation
	if err := json.Unmarshal(body, &conv); err != nil {
		fmt.Printf("TURN test FAILED (parse): %v\nBody: %s\n", err, string(body))
		return
	}

	if conv.TurnServer.Username == "" {
		fmt.Printf("TURN test FAILED: no credentials in response\nBody: %s\n", string(body))
		return
	}

	fmt.Printf("TURN Username:   %s\n", conv.TurnServer.Username)
	fmt.Printf("TURN Credential: %s\n", conv.TurnServer.Credential)
	fmt.Printf("TURN URLs:       %v\n", conv.TurnServer.URLs)
	fmt.Printf("STUN URLs:       %v\n", conv.StunServer.URLs)
	fmt.Printf("Signaling:       %s\n", conv.Endpoint)
	fmt.Println()
	fmt.Println("TURN credentials retrieved successfully!")
}

func fatal(err error, context string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL [%s]: %v\n", context, err)
		os.Exit(1)
	}
}
