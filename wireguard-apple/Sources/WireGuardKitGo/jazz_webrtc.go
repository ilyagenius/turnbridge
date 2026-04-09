package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

const (
	jazzWSURLClient = "wss://ws.salutejazz.ru/connector"
	jazzUAClient    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
)

type jazzRTCSignalMessage struct {
	Event   string          `json:"event"`
	GroupID string          `json:"groupId"`
	Payload json.RawMessage `json:"payload"`
}

type jazzRTCMethodPayload struct {
	Method string `json:"method"`
}

type jazzRTCTurnServer struct {
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
	URLs       []string `json:"urls"`
}

type jazzRTCConfig struct {
	Method        string `json:"method"`
	Configuration struct {
		ICEServers []jazzRTCTurnServer `json:"iceServers"`
	} `json:"configuration"`
}

type jazzRTCJoin struct {
	Method string `json:"method"`
	Join   struct {
		Participant struct {
			Identity string `json:"identity"`
		} `json:"participant"`
		PingInterval int `json:"pingInterval"`
		PingTimeout  int `json:"pingTimeout"`
	} `json:"join"`
}

type jazzRTCDescription struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

type jazzRTCOffer struct {
	Method      string             `json:"method"`
	Description jazzRTCDescription `json:"description"`
}

type jazzRTCAnswer struct {
	Method      string             `json:"method"`
	Description jazzRTCDescription `json:"description"`
}

type jazzRTCIceCandidate struct {
	Candidate     string `json:"candidate"`
	SDPMLineIndex int    `json:"sdpMLineIndex"`
	Target        string `json:"target,omitempty"`
}

type jazzRTCIce struct {
	Method           string                `json:"method"`
	RTCIceCandidates []jazzRTCIceCandidate `json:"rtcIceCandidates"`
}

type jazzRTCConnector struct {
	conn            *websocket.Conn
	roomID          string
	roomPassword    string
	participantName string
	participantID   string
	groupID         string
	sessionID       string
	incoming        chan jazzRTCSignalMessage
	pendingMu       sync.Mutex
	pending         []jazzRTCSignalMessage
	pingInterval    time.Duration
	lastPingTime    int64
}

type jazzClientCandidateSender struct {
	mu        sync.Mutex
	target    string
	pending   []jazzRTCIceCandidate
	sendBatch func([]jazzRTCIceCandidate) error
}

type jazzClientRemoteCandidateQueue struct {
	mu      sync.Mutex
	pending []webrtc.ICECandidateInit
}

func openJazzRTCConnector(ctx context.Context, room *jazzRoomInfo) (*jazzRTCConnector, error) {
	header := http.Header{
		"Origin":     {"https://salutejazz.ru"},
		"User-Agent": {jazzUAClient},
	}

	wsDialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig:  &tls.Config{MinVersion: tls.VersionTLS12},
	}

	ws, _, err := wsDialer.DialContext(ctx, jazzWSURLClient, header)
	if err != nil {
		return nil, fmt.Errorf("dial Jazz websocket: %w", err)
	}

	connector := &jazzRTCConnector{
		conn:            ws,
		roomID:          room.RoomID,
		roomPassword:    room.Password,
		participantName: fmt.Sprintf("turnbridge-ios-%d", time.Now().UnixNano()%100000),
		sessionID:       uuid.New().String(),
		incoming:        make(chan jazzRTCSignalMessage, 64),
	}

	if err := connector.join(ctx); err != nil {
		_ = ws.Close()
		return nil, err
	}

	go connector.readLoop()
	return connector, nil
}

func (c *jazzRTCConnector) join(ctx context.Context) error {
	payload := map[string]any{
		"event":     "join",
		"roomId":    c.roomID,
		"requestId": uuid.New().String(),
		"payload": map[string]any{
			"password":        c.roomPassword,
			"participantName": c.participantName,
			"sessionId":       c.sessionID,
			"supportedFeatures": map[string]bool{
				"attachedRooms": true,
				"sessionGroups": true,
				"transcription": true,
			},
			"isSilent": false,
		},
	}
	return c.writeJSON(payload)
}

func (c *jazzRTCConnector) writeJSON(payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.TextMessage, raw)
}

func (c *jazzRTCConnector) readLoop() {
	defer close(c.incoming)
	for {
		if err := c.conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
			return
		}
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			return
		}

		var envelope jazzRTCSignalMessage
		if err := json.Unmarshal(msg, &envelope); err != nil {
			continue
		}
		var payload jazzRTCMethodPayload
		_ = json.Unmarshal(envelope.Payload, &payload)
		if payload.Method == "rtc:pong" {
			continue
		}
		if payload.Method != "" {
			log.Printf("Jazz WS recv method=%q", payload.Method)
		}

		select {
		case c.incoming <- envelope:
		default:
		}
	}
}

func (c *jazzRTCConnector) close() {
	_ = c.conn.Close()
}

func (c *jazzRTCConnector) popPending(method string) *jazzRTCSignalMessage {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	for i, msg := range c.pending {
		var payload jazzRTCMethodPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			continue
		}
		if payload.Method != method {
			continue
		}

		match := msg
		c.pending = append(c.pending[:i], c.pending[i+1:]...)
		return &match
	}

	return nil
}

func (c *jazzRTCConnector) storePending(msg jazzRTCSignalMessage) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	c.pending = append(c.pending, msg)
}

func (c *jazzRTCConnector) waitForMethod(ctx context.Context, method string) (*jazzRTCSignalMessage, error) {
	for {
		if msg := c.popPending(method); msg != nil {
			return msg, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case msg, ok := <-c.incoming:
			if !ok {
				return nil, fmt.Errorf("jazz connector closed")
			}

			var payload jazzRTCMethodPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				continue
			}
			if payload.Method == method {
				return &msg, nil
			}
			c.storePending(msg)
		}
	}
}

func (c *jazzRTCConnector) waitForConfig(ctx context.Context) (*jazzRTCConfig, error) {
	msg, err := c.waitForMethod(ctx, "rtc:config")
	if err != nil {
		return nil, err
	}
	var payload jazzRTCConfig
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (c *jazzRTCConnector) waitForJoin(ctx context.Context) (*jazzRTCJoin, error) {
	msg, err := c.waitForMethod(ctx, "rtc:join")
	if err != nil {
		return nil, err
	}
	var payload jazzRTCJoin
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return nil, err
	}
	if payload.Join.Participant.Identity != "" {
		c.participantID = payload.Join.Participant.Identity
	}
	if msg.GroupID != "" {
		c.groupID = msg.GroupID
	}
	if payload.Join.PingInterval > 0 {
		c.pingInterval = time.Duration(payload.Join.PingInterval) * time.Second
	}
	return &payload, nil
}

func (c *jazzRTCConnector) waitForOffer(ctx context.Context) (*jazzRTCOffer, error) {
	msg, err := c.waitForMethod(ctx, "rtc:offer")
	if err != nil {
		return nil, err
	}
	var payload jazzRTCOffer
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (c *jazzRTCConnector) waitForAnswer(ctx context.Context) (*jazzRTCAnswer, error) {
	msg, err := c.waitForMethod(ctx, "rtc:answer")
	if err != nil {
		return nil, err
	}
	var payload jazzRTCAnswer
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (c *jazzRTCConnector) waitForICE(ctx context.Context) (*jazzRTCIce, error) {
	msg, err := c.waitForMethod(ctx, "rtc:ice")
	if err != nil {
		return nil, err
	}
	var payload jazzRTCIce
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (c *jazzRTCConnector) ping() error {
	now := time.Now().UnixMilli()
	var rtt int64
	if c.lastPingTime > 0 {
		rtt = now - c.lastPingTime
	}
	c.lastPingTime = now
	return c.writeJSON(map[string]any{
		"event":     "media-in",
		"roomId":    c.roomID,
		"groupId":   c.groupID,
		"requestId": uuid.New().String(),
		"payload": map[string]any{
			"method": "rtc:ping",
			"ping_req": map[string]any{
				"timestamp": now,
				"rtt":       rtt,
			},
		},
	})
}

func (c *jazzRTCConnector) startPingLoop(ctx context.Context) {
	interval := c.pingInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := c.ping(); err != nil {
					return
				}
			}
		}
	}()
}

func (c *jazzRTCConnector) sendAnswer(description jazzRTCDescription) error {
	return c.writeJSON(map[string]any{
		"event":     "media-in",
		"roomId":    c.roomID,
		"groupId":   c.groupID,
		"requestId": uuid.New().String(),
		"payload": map[string]any{
			"method":      "rtc:answer",
			"description": description,
		},
	})
}

func (c *jazzRTCConnector) sendOffer(description jazzRTCDescription) error {
	return c.writeJSON(map[string]any{
		"event":     "media-in",
		"roomId":    c.roomID,
		"groupId":   c.groupID,
		"requestId": uuid.New().String(),
		"payload": map[string]any{
			"method":      "rtc:offer",
			"description": description,
		},
	})
}

func (c *jazzRTCConnector) sendICE(candidates []jazzRTCIceCandidate) error {
	return c.writeJSON(map[string]any{
		"event":     "media-in",
		"roomId":    c.roomID,
		"groupId":   c.groupID,
		"requestId": uuid.New().String(),
		"payload": map[string]any{
			"method":           "rtc:ice",
			"rtcIceCandidates": candidates,
		},
	})
}

func (s *jazzClientCandidateSender) send(candidate jazzRTCIceCandidate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.target == "" {
		s.pending = append(s.pending, candidate)
		return nil
	}

	candidate.Target = s.target
	return s.sendBatch([]jazzRTCIceCandidate{candidate})
}

func (s *jazzClientCandidateSender) setTarget(target string) error {
	if target == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.target == target {
		return nil
	}
	s.target = target
	if len(s.pending) == 0 {
		return nil
	}

	batch := make([]jazzRTCIceCandidate, len(s.pending))
	copy(batch, s.pending)
	for i := range batch {
		batch[i].Target = target
	}
	s.pending = nil
	return s.sendBatch(batch)
}

func (q *jazzClientRemoteCandidateQueue) addOrQueue(pc *webrtc.PeerConnection, candidate webrtc.ICECandidateInit) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if pc.RemoteDescription() == nil {
		q.pending = append(q.pending, candidate)
		return nil
	}
	return pc.AddICECandidate(candidate)
}

func (q *jazzClientRemoteCandidateQueue) flush(pc *webrtc.PeerConnection) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, candidate := range q.pending {
		if err := pc.AddICECandidate(candidate); err != nil {
			return err
		}
	}
	q.pending = nil
	return nil
}

func newJazzClientCandidateSender(connector *jazzRTCConnector, target string) *jazzClientCandidateSender {
	sender := &jazzClientCandidateSender{
		sendBatch: func(candidates []jazzRTCIceCandidate) error {
			log.Printf("Jazz sending %d local ICE candidates target=%q", len(candidates), func() string {
				if len(candidates) == 0 {
					return ""
				}
				return candidates[0].Target
			}())
			return connector.sendICE(candidates)
		},
	}
	_ = sender.setTarget(target)
	return sender
}

func attachJazzClientStateLogging(name string, pc *webrtc.PeerConnection, errCh chan<- error) {
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Jazz %s WebRTC state: %s", name, state.String())
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			select {
			case errCh <- fmt.Errorf("%s peer connection state %s", name, state.String()):
			default:
			}
		}
	})
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("Jazz %s ICE state: %s", name, state.String())
	})
	pc.OnICEGatheringStateChange(func(state webrtc.ICEGatheringState) {
		log.Printf("Jazz %s ICE gathering state: %s", name, state.String())
	})
	pc.OnSignalingStateChange(func(state webrtc.SignalingState) {
		log.Printf("Jazz %s signaling state: %s", name, state.String())
	})
}

func startJazzWebRTCProxy(ctx context.Context, link string, listenConn net.PacketConn, inCh <-chan []byte, wgAddr *atomic.Value) error {
	room, err := fetchJazzRoom(ctx, link)
	if err != nil {
		return fmt.Errorf("fetch Jazz room: %w", err)
	}
	log.Printf("Jazz room acquired: %s", room.RoomID)

	connector, err := openJazzRTCConnector(ctx, room)
	if err != nil {
		return err
	}
	defer connector.close()

	setupCtx, cancelSetup := context.WithTimeout(ctx, 30*time.Second)
	defer cancelSetup()

	rtcConfig, err := connector.waitForConfig(setupCtx)
	if err != nil {
		return fmt.Errorf("wait rtc:config: %w", err)
	}
	if _, err := connector.waitForJoin(setupCtx); err != nil {
		return fmt.Errorf("wait rtc:join: %w", err)
	}
	connector.startPingLoop(ctx)

	subscriberPC, err := newJazzClientPeerConnection(rtcConfig)
	if err != nil {
		return err
	}
	defer subscriberPC.Close()

	publisherPC, err := newJazzClientPeerConnection(rtcConfig)
	if err != nil {
		return err
	}
	defer publisherPC.Close()
	audioTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		"audio",
		"jazz-publisher",
	)
	if err != nil {
		return fmt.Errorf("create publisher audio track: %w", err)
	}
	if _, err := publisherPC.AddTransceiverFromTrack(
		audioTrack,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly},
	); err != nil {
		return fmt.Errorf("add publisher audio transceiver: %w", err)
	}

	// Create _reliable DC on publisher PC before offer — LiveKit relays data
	// sent on publisher's DC to other participants' subscriber DCs.
	publisherDC, err := publisherPC.CreateDataChannel("_reliable", &webrtc.DataChannelInit{
		Ordered: func() *bool { b := true; return &b }(),
	})
	if err != nil {
		return fmt.Errorf("create publisher data channel: %w", err)
	}

	errCh := make(chan error, 8)
	subscriberAnswerSent := make(chan struct{})
	attachJazzClientStateLogging("subscriber", subscriberPC, errCh)
	attachJazzClientStateLogging("publisher", publisherPC, errCh)

	subscriberSender := newJazzClientCandidateSender(connector, "SUBSCRIBER")
	publisherSender := newJazzClientCandidateSender(connector, "PUBLISHER")
	subscriberRemoteICE := &jazzClientRemoteCandidateQueue{}
	publisherRemoteICE := &jazzClientRemoteCandidateQueue{}

	// Publisher DC sends WG packets to VPS; subscriber DC receives VPS responses.
	publisherDC.OnOpen(func() {
		log.Printf("Established Jazz WebRTC data channel")
		select {
		case proxyReady <- struct{}{}:
		default:
		}

		go func() {
			for pkt := range inCh {
				if err := publisherDC.Send(encodeDataPacket(pkt)); err != nil {
					log.Printf("Jazz local->DataChannel send failed: %v", err)
					return
				}
			}
		}()
	})

	subscriberPC.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Jazz DataChannel discovered: label=%s id=%v", dc.Label(), dc.ID())
		if dc.Label() != "_reliable" {
			return
		}
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			payload, ok := decodeDataPacket(msg.Data)
			if !ok || len(payload) == 0 {
				return
			}
			addr1, ok := wgAddr.Load().(net.Addr)
			if !ok {
				return
			}
			if _, err := listenConn.WriteTo(payload, addr1); err != nil {
				log.Printf("Jazz DataChannel->local write failed: %v", err)
			}
		})
	})

	subscriberPC.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		if err := subscriberSender.send(jazzRTCIceCandidate{
			Candidate:     candidate.ToJSON().Candidate,
			SDPMLineIndex: 0,
			Target:        "SUBSCRIBER",
		}); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Jazz subscriber send local ICE failed: %v", err)
		}
	})
	publisherPC.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		if err := publisherSender.send(jazzRTCIceCandidate{
			Candidate:     candidate.ToJSON().Candidate,
			SDPMLineIndex: 0,
			Target:        "PUBLISHER",
		}); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Jazz publisher send local ICE failed: %v", err)
		}
	})

	iceCtx, cancelICE := context.WithCancel(ctx)
	defer cancelICE()
	go func() {
		for {
			icePayload, err := connector.waitForICE(iceCtx)
			if err != nil {
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					select {
					case errCh <- fmt.Errorf("wait rtc:ice: %w", err):
					default:
					}
				}
				return
			}

			for _, candidate := range icePayload.RTCIceCandidates {
				log.Printf("Jazz received remote ICE target=%q candidate=%q", candidate.Target, candidate.Candidate)
				init := webrtc.ICECandidateInit{Candidate: candidate.Candidate}
				switch candidate.Target {
				case "PUBLISHER":
					if err := publisherRemoteICE.addOrQueue(publisherPC, init); err != nil {
						log.Printf("Jazz publisher AddICECandidate failed: %v", err)
					}
				case "", "SUBSCRIBER":
					if err := subscriberRemoteICE.addOrQueue(subscriberPC, init); err != nil {
						log.Printf("Jazz subscriber AddICECandidate failed: %v", err)
					}
				default:
					log.Printf("Jazz ignoring remote ICE for unexpected target=%q", candidate.Target)
				}
			}
		}
	}()

	log.Printf("Proxy started on %s", listenConn.LocalAddr().String())

	go func() {
		offer, err := waitForJazzDataOffer(setupCtx, connector)
		if err != nil {
			select {
			case errCh <- fmt.Errorf("wait subscriber rtc:offer: %w", err):
			default:
			}
			return
		}

		if err := subscriberPC.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  offer.Description.SDP,
		}); err != nil {
			select {
			case errCh <- fmt.Errorf("set subscriber remote description: %w", err):
			default:
			}
			return
		}
		log.Printf("Jazz subscriber remote offer set (len=%d)", len(offer.Description.SDP))
		if err := subscriberRemoteICE.flush(subscriberPC); err != nil {
			log.Printf("Jazz subscriber flush ICE failed: %v", err)
		}

		answer, err := subscriberPC.CreateAnswer(nil)
		if err != nil {
			select {
			case errCh <- fmt.Errorf("create subscriber answer: %w", err):
			default:
			}
			return
		}
		gatherComplete := webrtc.GatheringCompletePromise(subscriberPC)
		if err := subscriberPC.SetLocalDescription(answer); err != nil {
			select {
			case errCh <- fmt.Errorf("set subscriber local description: %w", err):
			default:
			}
			return
		}

		localDesc := subscriberPC.LocalDescription()
		if localDesc == nil {
			select {
			case errCh <- fmt.Errorf("subscriber local description missing after set local"):
			default:
			}
			return
		}
		log.Printf("Jazz subscriber local answer ready (len=%d)", len(localDesc.SDP))

		if err := connector.sendAnswer(jazzRTCDescription{
			Type: localDesc.Type.String(),
			SDP:  localDesc.SDP,
		}); err != nil {
			select {
			case errCh <- fmt.Errorf("send subscriber rtc:answer: %w", err):
			default:
			}
			return
		}
		log.Printf("Jazz subscriber WebRTC answer sent")
		close(subscriberAnswerSent)

		go func() {
			select {
			case <-ctx.Done():
			case <-time.After(20 * time.Second):
			case <-gatherComplete:
				log.Printf("Jazz subscriber local ICE gathering completed")
			}
		}()
	}()

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-subscriberAnswerSent:
		}

		offer, err := publisherPC.CreateOffer(nil)
		if err != nil {
			select {
			case errCh <- fmt.Errorf("create publisher offer: %w", err):
			default:
			}
			return
		}
		gatherComplete := webrtc.GatheringCompletePromise(publisherPC)
		if err := publisherPC.SetLocalDescription(offer); err != nil {
			select {
			case errCh <- fmt.Errorf("set publisher local description: %w", err):
			default:
			}
			return
		}

		localDesc := publisherPC.LocalDescription()
		if localDesc == nil {
			select {
			case errCh <- fmt.Errorf("publisher local description missing after set local"):
			default:
			}
			return
		}
		log.Printf("Jazz publisher local offer ready (len=%d)", len(localDesc.SDP))

		if err := connector.sendOffer(jazzRTCDescription{
			Type: localDesc.Type.String(),
			SDP:  localDesc.SDP,
		}); err != nil {
			select {
			case errCh <- fmt.Errorf("send publisher rtc:offer: %w", err):
			default:
			}
			return
		}
		log.Printf("Jazz publisher WebRTC offer sent")

		answer, err := waitForJazzPublisherAnswer(setupCtx, connector)
		if err != nil {
			select {
			case errCh <- fmt.Errorf("wait publisher rtc:answer: %w", err):
			default:
			}
			return
		}
		if err := publisherPC.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  answer.Description.SDP,
		}); err != nil {
			select {
			case errCh <- fmt.Errorf("set publisher remote description: %w", err):
			default:
			}
			return
		}
		log.Printf("Jazz publisher remote answer set (len=%d)", len(answer.Description.SDP))
		if err := publisherRemoteICE.flush(publisherPC); err != nil {
			log.Printf("Jazz publisher flush ICE failed: %v", err)
		}

		go func() {
			select {
			case <-ctx.Done():
			case <-time.After(20 * time.Second):
			case <-gatherComplete:
				log.Printf("Jazz publisher local ICE gathering completed")
			}
		}()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func newJazzClientPeerConnection(rtcConfig *jazzRTCConfig) (*webrtc.PeerConnection, error) {
	iceServers := make([]webrtc.ICEServer, 0, len(rtcConfig.Configuration.ICEServers))
	for _, server := range rtcConfig.Configuration.ICEServers {
		iceServers = append(iceServers, webrtc.ICEServer{
			URLs:       append([]string(nil), server.URLs...),
			Username:   server.Username,
			Credential: server.Credential,
		})
	}

	return webrtc.NewPeerConnection(webrtc.Configuration{
		// Match the browser's default ICE policy instead of forcing relay-only.
		// Real Jazz sessions still choose relay candidates, but they also expose
		// host/srflx candidates during gathering.
		ICEServers: iceServers,
	})
}

func waitForJazzDataOffer(ctx context.Context, connector *jazzRTCConnector) (*jazzRTCOffer, error) {
	for {
		offer, err := connector.waitForOffer(ctx)
		if err != nil {
			return nil, err
		}
		if strings.Contains(offer.Description.SDP, "m=application") {
			return offer, nil
		}
		log.Printf("Jazz ignoring non-data subscriber offer")
	}
}

func waitForJazzPublisherAnswer(ctx context.Context, connector *jazzRTCConnector) (*jazzRTCAnswer, error) {
	for {
		answer, err := connector.waitForAnswer(ctx)
		if err != nil {
			return nil, err
		}
		if strings.Contains(answer.Description.SDP, "m=audio") {
			return answer, nil
		}
		log.Printf("Jazz ignoring non-audio publisher answer")
	}
}

func attachJazzLocalDataChannel(ctx context.Context, dc *webrtc.DataChannel, listenConn net.PacketConn) {
	var addr atomic.Value

	dc.OnOpen(func() {
		log.Printf("Established Jazz WebRTC data channel")
		select {
		case proxyReady <- struct{}{}:
		default:
		}

		go func() {
			buf := make([]byte, 2048)
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

				addr.Store(addr1)
				if err := dc.Send(encodeDataPacket(buf[:n])); err != nil {
					log.Printf("Jazz local->DataChannel send failed: %v", err)
					return
				}
			}
		}()
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		addr1, ok := addr.Load().(net.Addr)
		if !ok || len(msg.Data) == 0 {
			return
		}
		payload, ok := decodeDataPacket(msg.Data)
		if !ok || len(payload) == 0 {
			return
		}
		if _, err := listenConn.WriteTo(payload, addr1); err != nil {
			log.Printf("Jazz DataChannel->local write failed: %v", err)
		}
	})
}
