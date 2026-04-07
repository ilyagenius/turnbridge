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

// isTelemostLink detects Telemost room links: https://telemost.yandex.ru/j/ROOM_ID
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
		MediaServerURL string               `json:"media_server_url"`
		ICEServers     []telemostICEServer  `json:"ice_servers"`
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

func telemostICEConfig(conn *telemostConnInfo) []webrtc.ICEServer {
	servers := []webrtc.ICEServer{
		{URLs: []string{"stun:stun.rtc.yandex.net:3478"}},
	}
	for _, s := range conn.ClientConfig.ICEServers {
		servers = append(servers, webrtc.ICEServer{
			URLs:       s.URLs,
			Username:   s.Username,
			Credential: s.Credential,
		})
	}
	if len(conn.ClientConfig.ICEServers) > 0 {
		log.Printf("Telemost ICE servers from API: %d", len(conn.ClientConfig.ICEServers))
	}
	return servers
}

func startTelemostWebRTCProxy(ctx context.Context, roomURL string, localAddrStr string) error {
	participantName := fmt.Sprintf("turnbridge-ios-%d", time.Now().UnixNano()%100000)

	conn, err := fetchTelemostConnectionInfo(roomURL, participantName)
	if err != nil {
		return fmt.Errorf("fetch telemost connection info: %w", err)
	}
	log.Printf("Telemost connection info: roomID=%s peerID=%s wsURL=%s", conn.RoomID, conn.PeerID, conn.ClientConfig.MediaServerURL)

	listenConn, err := net.ListenPacket("udp", localAddrStr)
	if err != nil {
		return fmt.Errorf("listen telemost local UDP: %w", err)
	}
	defer listenConn.Close()

	context.AfterFunc(ctx, func() {
		_ = listenConn.SetDeadline(time.Now())
		_ = listenConn.Close()
	})

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

	pcConfig := webrtc.Configuration{
		ICEServers: telemostICEConfig(conn),
	}

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

	// Publisher DC: iOS sends WG packets to server.
	pubDC, err := pcPub.CreateDataChannel("_reliable", nil)
	if err != nil {
		return fmt.Errorf("create publisher data channel: %w", err)
	}

	errCh := make(chan error, 4)

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

	// lastAddr tracks the most recent WireGuard sender address on listenConn.
	var lastAddr atomic.Value

	// Publisher DC open: signal proxy ready, then read from listenConn and send to server.
	pubDC.OnOpen(func() {
		log.Printf("Established Telemost WebRTC data channel")
		select {
		case proxyReady <- struct{}{}:
		default:
		}

		go func() {
			buf := make([]byte, 4096)
			for {
				n, addr1, err := listenConn.ReadFrom(buf)
				if err != nil {
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						select {
						case <-ctx.Done():
							return
						default:
							continue
						}
					}
					return
				}
				lastAddr.Store(addr1)
				if err := pubDC.Send(buf[:n]); err != nil {
					log.Printf("Telemost local->DataChannel send failed: %v", err)
					return
				}
			}
		}()
	})

	// Subscriber DC: receive WG responses from server, write to local UDP.
	pcSub.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Telemost subscriber DC: label=%s", dc.Label())
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if len(msg.Data) == 0 {
				return
			}
			addr1, ok := lastAddr.Load().(net.Addr)
			if !ok {
				return
			}
			if _, err := listenConn.WriteTo(msg.Data, addr1); err != nil {
				log.Printf("Telemost DataChannel->local write failed: %v", err)
			}
		})
	})

	// Send hello handshake.
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
			"sendAudio":     false,
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

	log.Printf("Telemost proxy started on %s", localAddrStr)

	// Keep-alive: WS pings every 30s, app-level pings every 5s.
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
		pubSent := false
		for {
			var msg map[string]any
			if err := ws.ReadJSON(&msg); err != nil {
				select {
				case errCh <- fmt.Errorf("telemost ws read: %w", err):
				default:
				}
				return
			}

			uid, _ := msg["uid"].(string)

			sendAck := func() {
				_ = writeJSON(map[string]any{
					"uid": uid,
					"ack": map[string]any{"status": map[string]any{"code": "OK"}},
				})
			}

			if _, ok := msg["serverHello"]; ok {
				log.Printf("Telemost serverHello received")
				sendAck()
			}

			if _, ok := msg["updateDescription"]; ok {
				sendAck()
			}

			if _, ok := msg["vadActivity"]; ok {
				sendAck()
			}

			if _, ok := msg["ping"]; ok {
				_ = writeJSON(map[string]any{"uid": uid, "pong": map[string]any{}})
			}

			if offer, ok := msg["subscriberSdpOffer"].(map[string]any); ok && !pubSent {
				sdp, _ := offer["sdp"].(string)
				pcSeq, _ := offer["pcSeq"].(float64)
				log.Printf("Telemost subscriber offer received (len=%d)", len(sdp))

				if err := pcSub.SetRemoteDescription(webrtc.SessionDescription{
					Type: webrtc.SDPTypeOffer, SDP: sdp,
				}); err != nil {
					select {
					case errCh <- fmt.Errorf("set subscriber remote desc: %w", err):
					default:
					}
					return
				}

				answer, err := pcSub.CreateAnswer(nil)
				if err != nil {
					select {
					case errCh <- fmt.Errorf("create subscriber answer: %w", err):
					default:
					}
					return
				}
				if err := pcSub.SetLocalDescription(answer); err != nil {
					select {
					case errCh <- fmt.Errorf("set subscriber local desc: %w", err):
					default:
					}
					return
				}

				_ = writeJSON(map[string]any{
					"uid": uuid.New().String(),
					"subscriberSdpAnswer": map[string]any{
						"pcSeq": int(pcSeq),
						"sdp":   answer.SDP,
					},
				})
				sendAck()

				time.Sleep(300 * time.Millisecond)

				pubOffer, err := pcPub.CreateOffer(nil)
				if err != nil {
					select {
					case errCh <- fmt.Errorf("create publisher offer: %w", err):
					default:
					}
					return
				}
				if err := pcPub.SetLocalDescription(pubOffer); err != nil {
					select {
					case errCh <- fmt.Errorf("set publisher local desc: %w", err):
					default:
					}
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
			}

			if answer, ok := msg["publisherSdpAnswer"].(map[string]any); ok {
				sdp, _ := answer["sdp"].(string)
				log.Printf("Telemost publisher answer received (len=%d)", len(sdp))
				if err := pcPub.SetRemoteDescription(webrtc.SessionDescription{
					Type: webrtc.SDPTypeAnswer, SDP: sdp,
				}); err != nil {
					select {
					case errCh <- fmt.Errorf("set publisher remote desc: %w", err):
					default:
					}
					return
				}
				sendAck()
			}

			if cand, ok := msg["webrtcIceCandidate"].(map[string]any); ok {
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
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
