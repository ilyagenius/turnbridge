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
	"github.com/pion/rtp"
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
	RoomID       string `json:"room_id"`
	PeerID       string `json:"peer_id"`
	Credentials  string `json:"credentials"`
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

// vp8KAFrame is a minimal valid 31-byte libvpx 16×16 black keyframe.
// Sent at 50Hz when no WG data is available, keeps videoMid bound by SFU.
var vp8KAFrame = []byte{
	0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, 0x10, 0x00,
	0x10, 0x00, 0x00, 0x47, 0x08, 0x85, 0x85, 0x88,
	0x85, 0x84, 0x88, 0x02, 0x02, 0x00, 0x0c, 0x0d,
	0x60, 0x00, 0xfe, 0xff, 0xba, 0xff, 0x40,
}

// buildVP8Payload wraps wgData into a VP8 keyframe payload.
// If wgData is nil, returns a keepalive frame (vp8KAFrame with payload descriptor).
// Frame layout: [0x10][frame_tag 3B][0x9d 0x01 0x2a][W 2B][H 2B][0x57 0x47][len 2B LE][wgData]
func buildVP8Payload(wgData []byte) []byte {
	if wgData == nil {
		payload := make([]byte, 1+len(vp8KAFrame))
		payload[0] = 0x10
		copy(payload[1:], vp8KAFrame)
		return payload
	}
	firstPartSize := uint32(len(wgData) + 6)
	ft := firstPartSize<<5 | (1 << 4)
	// 15 bytes of header: [0]=desc, [1..3]=frame_tag, [4..6]=startcode,
	// [7..8]=W, [9..10]=H, [11..12]=magic "WG", [13..14]=wgLen LE.
	// Previous `1+3+3+4+2` summed to 13 and truncated two bytes off every WG packet.
	payload := make([]byte, 15+len(wgData))
	payload[0] = 0x10
	payload[1] = byte(ft)
	payload[2] = byte(ft >> 8)
	payload[3] = byte(ft >> 16)
	payload[4] = 0x9d
	payload[5] = 0x01
	payload[6] = 0x2a
	payload[7] = 0x10
	payload[8] = 0x00
	payload[9] = 0x10
	payload[10] = 0x00
	payload[11] = 0x57
	payload[12] = 0x47
	payload[13] = byte(len(wgData))
	payload[14] = byte(len(wgData) >> 8)
	copy(payload[15:], wgData)
	return payload
}

// parseVP8WGData extracts WireGuard data from a VP8 keyframe payload.
// Returns nil if the payload is a keepalive or has wrong magic.
func parseVP8WGData(payload []byte) []byte {
	if len(payload) < 15 {
		return nil
	}
	if payload[0] != 0x10 {
		return nil
	}
	if payload[4] != 0x9d || payload[5] != 0x01 || payload[6] != 0x2a {
		return nil
	}
	if payload[11] != 0x57 || payload[12] != 0x47 {
		return nil
	}
	wgLen := int(payload[13]) | int(payload[14])<<8
	if wgLen == 0 || 15+wgLen > len(payload) {
		return nil
	}
	return payload[15 : 15+wgLen]
}

func startTelemostWebRTCProxy(ctx context.Context, roomURL string, listenConn net.PacketConn, inCh <-chan []byte, wgAddr *atomic.Value) error {
	participantName := fmt.Sprintf("turnbridge-ios-%d", time.Now().UnixNano()%100000)

	conn, err := fetchTelemostConnectionInfo(roomURL, participantName)
	if err != nil {
		return fmt.Errorf("fetch telemost connection info: %w", err)
	}
	log.Printf("Telemost connection info: roomID=%s peerID=%s wsURL=%s", conn.RoomID, conn.PeerID, conn.ClientConfig.MediaServerURL)

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

	// Opus audio track: Goloom SFU refuses to emit publisherSdpAnswer unless
	// the publisher advertises at least one audio sender. We send silent
	// Opus — the remote side drops it, we never write packets into it.
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		"audio", "telemost-bridge",
	)
	if err != nil {
		return fmt.Errorf("create audio track: %w", err)
	}
	if _, err := pcPub.AddTransceiverFromTrack(audioTrack,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		return fmt.Errorf("add audio transceiver: %w", err)
	}

	// VP8 video track: WG packets are wrapped as VP8 keyframes and sent through SFU.
	// Must use AddTransceiverFromTrack(sendonly) so the SDP advertises a=sendonly —
	// AddTrack defaults to sendrecv, which made the SFU expect inbound video and
	// drop our outbound media.
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000},
		"video", "turnbridge-vp8",
	)
	if err != nil {
		return fmt.Errorf("create VP8 track: %w", err)
	}
	if _, err := pcPub.AddTransceiverFromTrack(videoTrack,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		return fmt.Errorf("add VP8 transceiver: %w", err)
	}

	errCh := make(chan error, 4)

	// Single handler per PC: route failed/closed to errCh, signal proxyReady
	// when the publisher connects. The previous code registered two handlers
	// on pcPub; the second one overwrote the first, so error reporting for
	// the publisher was lost.
	for _, entry := range []struct {
		name string
		pc   *webrtc.PeerConnection
	}{
		{"sub", pcSub},
		{"pub", pcPub},
	} {
		name, pc := entry.name, entry.pc
		pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
			log.Printf("Telemost PC[%s] state: %s", name, state)
			if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
				select {
				case errCh <- fmt.Errorf("PC %s %s", name, state):
				default:
				}
			}
			if name == "pub" && state == webrtc.PeerConnectionStateConnected {
				select {
				case proxyReady <- struct{}{}:
				default:
				}
			}
		})
	}

	// VP8 sender goroutine: start immediately so media flow is present before
	// the SFU runs its first setSlots pass. Previously it was started from
	// inside OnConnectionStateChange(Connected), which raced with setSlots
	// and caused the SFU to reject our video mid.
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		var seq uint16
		ssrc := uint32(time.Now().UnixNano() & 0xffffffff)
		for {
			select {
			case <-ctx.Done():
				return
			case wgPkt, ok := <-inCh:
				if !ok {
					return
				}
				payload := buildVP8Payload(wgPkt)
				pkt := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,
						SequenceNumber: seq,
						Timestamp:      uint32(time.Now().UnixNano() / 1e6 * 90),
						SSRC:           ssrc,
						Marker:         true,
					},
					Payload: payload,
				}
				seq++
				if err := videoTrack.WriteRTP(pkt); err != nil {
					return
				}
				log.Printf("Telemost VP8 WG->video len=%d seq=%d", len(wgPkt), seq-1)
			case <-ticker.C:
				// Drain any buffered packet; otherwise send keepalive.
				var wgPkt []byte
				select {
				case wgPkt = <-inCh:
				default:
				}
				var payload []byte
				if wgPkt != nil {
					payload = buildVP8Payload(wgPkt)
					log.Printf("Telemost VP8 WG->video(tick) len=%d seq=%d", len(wgPkt), seq)
				} else {
					payload = buildVP8Payload(nil)
				}
				pkt := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,
						SequenceNumber: seq,
						Timestamp:      uint32(time.Now().UnixNano() / 1e6 * 90),
						SSRC:           ssrc,
						Marker:         true,
					},
					Payload: payload,
				}
				seq++
				if err := videoTrack.WriteRTP(pkt); err != nil {
					return
				}
			}
		}
	}()

	// Subscriber: receive VP8 video from VPS, extract and forward WG packets.
	pcSub.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		kind := track.Kind().String()
		log.Printf("Telemost OnTrack: kind=%s codec=%s ssrc=%d", kind, track.Codec().MimeType, track.SSRC())
		if kind != "video" {
			go func() {
				buf := make([]byte, 1500)
				for {
					if _, _, err := track.Read(buf); err != nil {
						return
					}
				}
			}()
			return
		}
		go func() {
			buf := make([]byte, 1500)
			for {
				n, _, err := track.Read(buf)
				if err != nil {
					return
				}
				pkt := &rtp.Packet{}
				if err := pkt.Unmarshal(buf[:n]); err != nil {
					continue
				}
				wgData := parseVP8WGData(pkt.Payload)
				if wgData == nil {
					continue
				}
				addr, ok := wgAddr.Load().(net.Addr)
				if !ok {
					continue
				}
				if _, err := listenConn.WriteTo(wgData, addr); err != nil {
					log.Printf("Telemost VP8 video->WG write failed: %v", err)
				}
				log.Printf("Telemost VP8 video->WG len=%d ssrc=%d seq=%d", len(wgData), pkt.SSRC, pkt.SequenceNumber)
			}
		}()
	})

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

	// Hello with sendAudio/sendVideo=true; selfVadStatus=DISABLED so we never
	// have to negotiate VAD ownership. Adds sendSelfViewVideoSlot/pin/joinOrder
	// caps so the SFU keeps our video slot bound across slot recalculations.
	if err := writeJSON(map[string]any{
		"uid": uuid.New().String(),
		"hello": map[string]any{
			"participantMeta": map[string]any{
				"name":      participantName,
				"role":      "SPEAKER",
				"sendAudio": true,
				"sendVideo": true,
			},
			"participantAttributes": map[string]any{
				"name": participantName,
				"role": "SPEAKER",
			},
			"sendAudio":     true,
			"sendVideo":     true,
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
				"selfVadStatus":          []string{"DISABLED"},
				"dataChannelSharing":     []string{"TO_RTP"},
				"sendSelfViewVideoSlot":  []string{"ENABLED"},
				"pinLayout":              []string{"ENABLED"},
				"joinOrderLayout":        []string{"ENABLED"},
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

	log.Printf("Telemost VP8 proxy started on %s", listenConn.LocalAddr().String())

	// Keep-alive: WS pings, app pings, and vadActivity.
	go func() {
		wsPing := time.NewTicker(30 * time.Second)
		appPing := time.NewTicker(5 * time.Second)
		vadTick := time.NewTicker(2 * time.Second)
		defer wsPing.Stop()
		defer appPing.Stop()
		defer vadTick.Stop()
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
			case <-vadTick.C:
				_ = writeJSON(map[string]any{
					"uid": uuid.New().String(),
					"vadActivity": map[string]any{
						"active": true,
					},
				})
			}
		}
	}()

	// Signaling loop.
	go func() {
		pubSent := false
		slotsKey := 1
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
			if _, ok := msg["slotsConfig"]; ok {
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

				// Extract tracks[] from transceivers — required for setSlots to work.
				var tracks []map[string]any
				for _, tr := range pcPub.GetTransceivers() {
					if tr.Sender() == nil || tr.Sender().Track() == nil {
						continue
					}
					t := tr.Sender().Track()
					kindStr := "VIDEO"
					if t.Kind() == webrtc.RTPCodecTypeAudio {
						kindStr = "AUDIO"
					}
					tracks = append(tracks, map[string]any{
						"mid":            tr.Mid(),
						"transceiverMid": tr.Mid(),
						"kind":           kindStr,
						"priority":       0,
						"label":          t.ID(),
						"codecs":         map[string]any{},
						"groupId":        1,
						"description":    "",
					})
				}

				_ = writeJSON(map[string]any{
					"uid": uuid.New().String(),
					"publisherSdpOffer": map[string]any{
						"pcSeq":  1,
						"sdp":    pubOffer.SDP,
						"tracks": tracks,
					},
				})
				log.Printf("Telemost publisher offer sent (tracks=%d)", len(tracks))
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

				// Send setSlots after publisherSdpAnswer so SFU routes video mids.
				_ = writeJSON(map[string]any{
					"uid":            uuid.New().String(),
					"setSlotsOffset": map[string]any{"offset": 0},
				})
				_ = writeJSON(map[string]any{
					"uid": uuid.New().String(),
					"setSlots": map[string]any{
						"slots": []map[string]any{
							{"label": "video"},
							{"label": "video"},
						},
						"audioSlotsCount":    0,
						"key":                slotsKey,
						"shutdownAllVideo":   false,
						"withSelfView":       false,
						"selfViewVisibility": "HIDE",
						"gridConfig":         map[string]any{},
					},
				})
				slotsKey++
				log.Printf("Telemost setSlots sent (key=%d)", slotsKey-1)
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
