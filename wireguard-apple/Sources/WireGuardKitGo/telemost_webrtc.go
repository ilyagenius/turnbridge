package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

const telemostAPIBase = "https://cloud-api.yandex.ru/telemost_front/v2/telemost"

func isTelemostLink(link string) bool {
	parsed, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "telemost.yandex.ru" && strings.HasPrefix(parsed.Path, "/j/")
}

type telemostICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
}

type telemostConnInfo struct {
	RoomID      string `json:"room_id"`
	PeerID      string `json:"peer_id"`
	Credentials string `json:"credentials"`
	ClientConfig struct {
		MediaServerURL string              `json:"media_server_url"`
		ICEServers     []telemostICEServer `json:"ice_servers"`
	} `json:"client_configuration"`
}

func fetchTelemostConnectionInfo(roomURL, displayName string) (*telemostConnInfo, error) {
	apiURL := fmt.Sprintf("%s/conferences/%s/connection", telemostAPIBase, url.QueryEscape(roomURL))

	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Add("next_gen_media_platform_allowed", "true")
	q.Add("display_name", displayName)
	q.Add("waiting_room_supported", "true")
	req.URL.RawQuery = q.Encode()

	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:149.0) Gecko/20100101 Firefox/149.0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Client-Instance-Id", uuid.New().String())
	req.Header.Set("X-Telemost-Client-Version", "187.1.0")
	req.Header.Set("Idempotency-Key", uuid.New().String())
	req.Header.Set("Origin", "https://telemost.yandex.ru")
	req.Header.Set("Referer", "https://telemost.yandex.ru/")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("telemost API %d: %s", resp.StatusCode, body)
	}

	var info telemostConnInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// parseICEServersFromServerHello extracts TURN credentials from serverHello.rtcConfiguration.iceServers.
// Goloom moved TURN credentials from the API response to serverHello (April 2026).
func parseICEServersFromServerHello(sh map[string]any) []webrtc.ICEServer {
	rtcCfg, ok := sh["rtcConfiguration"].(map[string]any)
	if !ok {
		return nil
	}
	servers, ok := rtcCfg["iceServers"].([]any)
	if !ok || len(servers) == 0 {
		return nil
	}

	var result []webrtc.ICEServer
	for _, s := range servers {
		srv, ok := s.(map[string]any)
		if !ok {
			continue
		}
		var urls []string
		if rawURLs, ok := srv["urls"].([]any); ok {
			for _, u := range rawURLs {
				if us, ok := u.(string); ok {
					urls = append(urls, us)
				}
			}
		}
		if len(urls) == 0 {
			continue
		}
		is := webrtc.ICEServer{URLs: urls}
		if cred, ok := srv["credential"].(string); ok {
			is.Credential = cred
		}
		if user, ok := srv["username"].(string); ok {
			is.Username = user
		}
		result = append(result, is)
	}
	return result
}

// msgType returns the first key in a Goloom WS message that isn't "uid".
func msgType(msg map[string]any) string {
	for k := range msg {
		if k != "uid" {
			return k
		}
	}
	return ""
}

func startTelemostWebRTCProxy(ctx context.Context, roomURL string, listenConn net.PacketConn, inCh <-chan []byte, wgAddr *atomic.Value) error {
	participantName := fmt.Sprintf("turnbridge-ios-%d", time.Now().UnixNano()%100000)

	conn, err := fetchTelemostConnectionInfo(roomURL, participantName)
	if err != nil {
		return fmt.Errorf("fetch telemost connection info: %w", err)
	}
	log.Printf("Telemost: room=%s peer=%s ws=%s", conn.RoomID, conn.PeerID, conn.ClientConfig.MediaServerURL)

	ws, _, err := websocket.DefaultDialer.Dial(conn.ClientConfig.MediaServerURL, nil)
	if err != nil {
		return fmt.Errorf("dial telemost ws: %w", err)
	}
	defer ws.Close()

	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	})

	var wsMu sync.Mutex
	writeJSON := func(v any) error {
		wsMu.Lock()
		defer wsMu.Unlock()
		return ws.WriteJSON(v)
	}
	sendAck := func(uid string) {
		_ = writeJSON(map[string]any{
			"uid": uid,
			"ack": map[string]any{"status": map[string]any{"code": "OK"}},
		})
	}
	sendPong := func(uid string) {
		_ = writeJSON(map[string]any{"uid": uid, "pong": map[string]any{}})
	}

	errCh := make(chan error, 4)

	// ── Phase 1: Send hello → wait for serverHello → extract TURN credentials ──

	if err := writeJSON(map[string]any{
		"uid": uuid.New().String(),
		"hello": map[string]any{
			"participantMeta": map[string]any{
				"name":      participantName,
				"role":      "SPEAKER",
				"sendAudio": false,
				"sendVideo": false,
			},
			"participantAttributes": map[string]any{
				"name": participantName,
				"role": "SPEAKER",
			},
			"sendAudio":     true,
			"sendVideo":     false,
			"sendSharing":   false,
			"participantId": conn.PeerID,
			"roomId":        conn.RoomID,
			"serviceName":   "telemost",
			"credentials":   conn.Credentials,
			"capabilitiesOffer": map[string]any{
				"offerAnswerMode":        []string{"SEPARATE"},
				"initialSubscriberOffer": []string{"ON_HELLO"},
				"slotsMode":              []string{"FROM_CONTROLLER"},
				"simulcastMode":          []string{"DISABLED"},
				"selfVadStatus":          []string{"FROM_SERVER"},
				"dataChannelSharing":     []string{"TO_RTP"},
			},
			"sdkInfo": map[string]any{
				"implementation": "go",
				"version":        "1.0.0",
				"userAgent":      "TurnBridge-" + participantName,
			},
			"sdkInitializationId": uuid.New().String(),
			"disablePublisher":    false,
			"disableSubscriber":   false,
		},
	}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	// Fallback ICE: STUN + anything from API.
	iceServers := []webrtc.ICEServer{
		{URLs: []string{"stun:stun.rtc.yandex.net:3478"}},
	}
	for _, s := range conn.ClientConfig.ICEServers {
		iceServers = append(iceServers, webrtc.ICEServer{
			URLs: s.URLs, Username: s.Username, Credential: s.Credential,
		})
	}

	var buffered []map[string]any
	deadline := time.Now().Add(15 * time.Second)

	for time.Now().Before(deadline) {
		_ = ws.SetReadDeadline(deadline)
		var msg map[string]any
		if err := ws.ReadJSON(&msg); err != nil {
			return fmt.Errorf("waiting for serverHello: %w", err)
		}

		uid, _ := msg["uid"].(string)
		mt := msgType(msg)

		switch mt {
		case "ack":
			continue
		case "ping":
			sendPong(uid)
			continue
		case "serverHello":
			sh, _ := msg["serverHello"].(map[string]any)
			if servers := parseICEServersFromServerHello(sh); len(servers) > 0 {
				iceServers = servers
				log.Printf("Telemost TURN from serverHello: %d servers", len(servers))
			}
			sendAck(uid)
			log.Printf("Telemost serverHello received")
			goto phase2
		default:
			sendAck(uid)
			buffered = append(buffered, msg)
			log.Printf("Telemost buffered [%s] during phase1", mt)
		}
	}
	return fmt.Errorf("serverHello not received within 15s")

phase2:
	_ = ws.SetReadDeadline(time.Time{})

	// ── Phase 2: Create PeerConnections WITH TURN credentials ──

	pcConfig := webrtc.Configuration{ICEServers: iceServers}

	pcSub, err := webrtc.NewPeerConnection(pcConfig)
	if err != nil {
		return err
	}
	defer pcSub.Close()

	pcPub, err := webrtc.NewPeerConnection(pcConfig)
	if err != nil {
		return err
	}
	defer pcPub.Close()

	// Dummy audio track: Goloom SFU only relays DataChannel data between
	// participants when the publisher has at least one media track.
	audioTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		"audio",
		"telemost-bridge",
	)
	if err != nil {
		return fmt.Errorf("create audio track: %w", err)
	}
	if _, err := pcPub.AddTransceiverFromTrack(
		audioTrack,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly},
	); err != nil {
		return fmt.Errorf("add audio transceiver: %w", err)
	}

	pubDC, err := pcPub.CreateDataChannel("_reliable", nil)
	if err != nil {
		return fmt.Errorf("create publisher data channel: %w", err)
	}

	for _, pc := range []*webrtc.PeerConnection{pcSub, pcPub} {
		pc := pc
		pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
			log.Printf("Telemost PC state: %s", state)
			if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
				select {
				case errCh <- fmt.Errorf("peer connection %s", state):
				default:
				}
			}
		})
	}

	pcSub.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		_ = writeJSON(map[string]any{
			"uid": uuid.New().String(),
			"webrtcIceCandidate": map[string]any{
				"candidate":     init.Candidate,
				"sdpMid":        init.SDPMid,
				"sdpMlineIndex": init.SDPMLineIndex,
				"target":        "SUBSCRIBER",
				"pcSeq":         1,
			},
		})
	})
	pcPub.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		_ = writeJSON(map[string]any{
			"uid": uuid.New().String(),
			"webrtcIceCandidate": map[string]any{
				"candidate":     init.Candidate,
				"sdpMid":        init.SDPMid,
				"sdpMlineIndex": init.SDPMLineIndex,
				"target":        "PUBLISHER",
				"pcSeq":         1,
			},
		})
	})

	pubDC.OnOpen(func() {
		log.Printf("Telemost publisher DataChannel open")
		select {
		case proxyReady <- struct{}{}:
		default:
		}

		go func() {
			for pkt := range inCh {
				if err := pubDC.Send(pkt); err != nil {
					log.Printf("Telemost local->DC send failed: %v", err)
					return
				}
			}
		}()
	})

	pcSub.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Telemost subscriber DC: label=%s", dc.Label())
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if len(msg.Data) == 0 {
				return
			}
			addr1, ok := wgAddr.Load().(net.Addr)
			if !ok {
				return
			}
			if _, err := listenConn.WriteTo(msg.Data, addr1); err != nil {
				log.Printf("Telemost DC->local write failed: %v", err)
			}
		})
	})

	log.Printf("Telemost proxy started on %s", listenConn.LocalAddr().String())

	// ── Phase 3: Process buffered messages → signaling loop ──

	pubSent := false

	processMsg := func(msg map[string]any) {
		uid, _ := msg["uid"].(string)
		mt := msgType(msg)

		switch mt {
		case "ack", "pong":
			return

		case "ping":
			sendPong(uid)
			return

		case "serverHello":
			sendAck(uid)
			return

		case "subscriberSdpOffer":
			offer, ok := msg["subscriberSdpOffer"].(map[string]any)
			if !ok || pubSent {
				sendAck(uid)
				return
			}
			sdp, _ := offer["sdp"].(string)
			pcSeq, _ := offer["pcSeq"].(float64)
			log.Printf("Telemost subscriber offer (len=%d)", len(sdp))

			if err := pcSub.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeOffer, SDP: sdp,
			}); err != nil {
				errCh <- fmt.Errorf("set subscriber remote desc: %w", err)
				return
			}

			answer, err := pcSub.CreateAnswer(nil)
			if err != nil {
				errCh <- fmt.Errorf("create subscriber answer: %w", err)
				return
			}
			if err := pcSub.SetLocalDescription(answer); err != nil {
				errCh <- fmt.Errorf("set subscriber local desc: %w", err)
				return
			}

			_ = writeJSON(map[string]any{
				"uid": uuid.New().String(),
				"subscriberSdpAnswer": map[string]any{
					"pcSeq": int(pcSeq),
					"sdp":   answer.SDP,
				},
			})
			sendAck(uid)

			time.Sleep(300 * time.Millisecond)

			pubOffer, err := pcPub.CreateOffer(nil)
			if err != nil {
				errCh <- fmt.Errorf("create publisher offer: %w", err)
				return
			}
			if err := pcPub.SetLocalDescription(pubOffer); err != nil {
				errCh <- fmt.Errorf("set publisher local desc: %w", err)
				return
			}
			_ = writeJSON(map[string]any{
				"uid": uuid.New().String(),
				"publisherSdpOffer": map[string]any{
					"pcSeq": 1,
					"sdp":   pubOffer.SDP,
				},
			})
			log.Printf("Telemost publisher offer sent")
			pubSent = true
			return

		case "publisherSdpAnswer":
			answer, ok := msg["publisherSdpAnswer"].(map[string]any)
			if !ok {
				sendAck(uid)
				return
			}
			sdp, _ := answer["sdp"].(string)
			log.Printf("Telemost publisher answer (len=%d)", len(sdp))
			if err := pcPub.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeAnswer, SDP: sdp,
			}); err != nil {
				errCh <- fmt.Errorf("set publisher remote desc: %w", err)
				return
			}
			sendAck(uid)
			return

		case "webrtcIceCandidate":
			cand, ok := msg["webrtcIceCandidate"].(map[string]any)
			if !ok {
				return
			}
			candStr, _ := cand["candidate"].(string)
			target, _ := cand["target"].(string)
			sdpMid, _ := cand["sdpMid"].(string)
			sdpMLineIndex, _ := cand["sdpMlineIndex"].(float64)
			idx := uint16(sdpMLineIndex)
			init := webrtc.ICECandidateInit{
				Candidate:     candStr,
				SDPMid:        &sdpMid,
				SDPMLineIndex: &idx,
			}
			switch target {
			case "SUBSCRIBER":
				_ = pcSub.AddICECandidate(init)
			case "PUBLISHER":
				_ = pcPub.AddICECandidate(init)
			}
			return

		default:
			// Catch-all: ack unknown messages (setSlots, slotsConfig, slotsMeta, etc.)
			// Goloom closes WS if ack not received within 9 seconds.
			log.Printf("Telemost acking [%s]", mt)
			sendAck(uid)
			return
		}
	}

	for _, msg := range buffered {
		processMsg(msg)
	}
	buffered = nil

	// Keep-alive goroutine.
	go func() {
		wsPing := time.NewTicker(30 * time.Second)
		appPing := time.NewTicker(5 * time.Second)
		defer wsPing.Stop()
		defer appPing.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wsPing.C:
				wsMu.Lock()
				_ = ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second))
				wsMu.Unlock()
			case <-appPing.C:
				_ = writeJSON(map[string]any{
					"uid":  uuid.New().String(),
					"ping": map[string]any{},
				})
			}
		}
	}()

	// Signaling loop.
	go func() {
		for {
			var msg map[string]any
			if err := ws.ReadJSON(&msg); err != nil {
				select {
				case errCh <- fmt.Errorf("telemost ws read: %w", err):
				default:
				}
				return
			}
			processMsg(msg)
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
