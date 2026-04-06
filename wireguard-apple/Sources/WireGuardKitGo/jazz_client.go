package main

import (
	"context"
	"fmt"
	neturl "net/url"
	"strings"
)

const jazzCallLinkExample = "https://salutejazz.ru/call/ROOM_ID/PASSWORD"

type jazzRoomInfo struct {
	RoomID   string
	Password string
}

// isJazzSignalingLink detects Jazz call links of the form:
//
//	https://salutejazz.ru/call/ROOM_ID/PASSWORD
//	https://jazz.sber.ru/call/ROOM_ID/PASSWORD
func isJazzSignalingLink(link string) bool {
	parsed, err := neturl.Parse(strings.TrimSpace(link))
	if err != nil {
		return false
	}

	if !isJazzCallHost(parsed.Hostname()) {
		return false
	}

	return strings.HasPrefix(strings.ToLower(parsed.EscapedPath()), "/call/")
}

// fetchJazzRoom parses room ID and password directly from the Jazz call link.
// No HTTP request to VPS - all parsing is local.
// Link format: https://salutejazz.ru/call/{ROOM_ID}/{PASSWORD}
func fetchJazzRoom(_ context.Context, link string) (*jazzRoomInfo, error) {
	parsed, err := neturl.Parse(strings.TrimSpace(link))
	if err != nil {
		return nil, fmt.Errorf("invalid Jazz link: %w", err)
	}

	if !isJazzCallHost(parsed.Hostname()) {
		return nil, fmt.Errorf("invalid Jazz call link, expected %s", jazzCallLinkExample)
	}

	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 3 || !strings.EqualFold(parts[0], "call") {
		return nil, fmt.Errorf("invalid Jazz call link, expected %s", jazzCallLinkExample)
	}

	roomID, err := neturl.PathUnescape(parts[1])
	if err != nil || roomID == "" {
		return nil, fmt.Errorf("invalid Jazz room ID in %s", jazzCallLinkExample)
	}

	password, err := neturl.PathUnescape(parts[2])
	if err != nil || password == "" {
		return nil, fmt.Errorf("invalid Jazz password in %s", jazzCallLinkExample)
	}

	return &jazzRoomInfo{
		RoomID:   roomID,
		Password: password,
	}, nil
}

func isJazzCallHost(host string) bool {
	switch strings.ToLower(host) {
	case "salutejazz.ru", "jazz.sber.ru":
		return true
	default:
		return false
	}
}
