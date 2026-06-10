package main

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wbBase = "https://stream.wb.ru"
	wbUA   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

func wbReq(cl *http.Client, method, ep string, body []byte, tok string) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	rq, _ := http.NewRequest(method, wbBase+ep, rd)
	rq.Header.Set("User-Agent", wbUA)
	rq.Header.Set("Accept", "application/json")
	rq.Header.Set("Accept-Language", "en-US,en;q=0.9")
	rq.Header.Set("Origin", wbBase)
	rq.Header.Set("Referer", wbBase+"/")
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		rq.Header.Set("Authorization", "Bearer "+tok)
	}
	rs, err := cl.Do(rq)
	if err != nil {
		return nil, err
	}
	defer rs.Body.Close()
	var r io.Reader = rs.Body
	if rs.Header.Get("Content-Encoding") == "gzip" {
		if g, e := gzip.NewReader(rs.Body); e == nil {
			defer g.Close()
			r = g
		}
	}
	b, _ := io.ReadAll(r)
	if rs.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", rs.StatusCode, string(b))
	}
	return b, nil
}

func getCredsWB(_ string) (resUser string, resPass string, resTurn string, resErr error) {
	defer func() {
		if r := recover(); r != nil {
			resErr = fmt.Errorf("panic in getCredsWB: %v", r)
		}
	}()

	cl := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{}},
	}
	nm := fmt.Sprintf("tb_%d", time.Now().UnixMilli()%100000)

	log.Printf("[WB] Connecting to WB Stream...")

	// 1. guest register
	rr, err := wbReq(cl, "POST", "/auth/api/v1/auth/user/guest-register",
		[]byte(`{"displayName":"`+nm+`"}`), "")
	if err != nil {
		return "", "", "", fmt.Errorf("guest register: %w", err)
	}
	var reg struct {
		AccessToken string `json:"accessToken"`
	}
	json.Unmarshal(rr, &reg)
	if reg.AccessToken == "" {
		return "", "", "", fmt.Errorf("no access token from WB")
	}
	log.Printf("[WB] Guest registered")

	// 2. create room
	rr, err = wbReq(cl, "POST", "/api-room/api/v2/room",
		[]byte(`{"roomType":"ROOM_TYPE_ALL_ON_SCREEN","roomPrivacy":"ROOM_PRIVACY_FREE"}`),
		reg.AccessToken)
	if err != nil {
		return "", "", "", fmt.Errorf("create room: %w", err)
	}
	var room struct {
		RoomID string `json:"roomId"`
	}
	json.Unmarshal(rr, &room)
	if room.RoomID == "" {
		return "", "", "", fmt.Errorf("no room ID from WB")
	}
	log.Printf("[WB] Room created: %s", room.RoomID[:minInt(8, len(room.RoomID))])

	// 3. join room
	wbReq(cl, "POST", fmt.Sprintf("/api-room/api/v1/room/%s/join", room.RoomID),
		[]byte("{}"), reg.AccessToken)

	// 4. get room token
	rr, err = wbReq(cl, "GET", fmt.Sprintf(
		"/api-room-manager/api/v1/room/%s/token?deviceType=PARTICIPANT_DEVICE_TYPE_WEB_DESKTOP&displayName=%s",
		room.RoomID, url.QueryEscape(nm)), nil, reg.AccessToken)
	if err != nil {
		return "", "", "", fmt.Errorf("get token: %w", err)
	}
	var tok struct {
		RoomToken string `json:"roomToken"`
	}
	json.Unmarshal(rr, &tok)
	if tok.RoomToken == "" {
		return "", "", "", fmt.Errorf("no room token from WB")
	}

	// 5. LiveKit WebSocket → extract TURN credentials from protobuf
	log.Printf("[WB] Negotiating ICE (LiveKit)...")
	creds, err := lkICE(tok.RoomToken)
	if err != nil {
		return "", "", "", fmt.Errorf("livekit ICE: %w", err)
	}

	// Find first TURN credential (not STUN)
	for _, c := range creds {
		if strings.HasPrefix(c.URL, "turn") {
			clean := strings.Split(c.URL, "?")[0]
			address := strings.TrimPrefix(strings.TrimPrefix(clean, "turn:"), "turns:")
			log.Printf("[WB] Got TURN server: %s", address)
			return c.User, c.Pass, address, nil
		}
	}

	return "", "", "", fmt.Errorf("no TURN credentials found in WB response")
}

func lkICE(token string) ([]turnCred2, error) {
	u := "wss://wbstream01-el.wb.ru:7880/rtc?access_token=" + url.QueryEscape(token) +
		"&auto_subscribe=1&sdk=js&version=2.15.3&protocol=16&adaptive_stream=1"
	conn, _, err := (&websocket.Dialer{
		TLSClientConfig:  &tls.Config{},
		HandshakeTimeout: 10 * time.Second,
	}).Dial(u, http.Header{
		"User-Agent": {wbUA},
		"Origin":     {wbBase},
	})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	for i := 0; i < 15; i++ {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if c := pbICE(msg); len(c) > 0 {
			return dedup(c), nil
		}
	}
	return nil, fmt.Errorf("TURN not found in LiveKit response")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
