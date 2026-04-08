package main

import (
	"context"
	"fmt"
	neturl "net/url"
	"strings"
)

const jazzCallLinkExample = "https://salutejazz.ru/calls/ROOM_ID?psw=PASSWORD or https://salutejazz.ru/call/ROOM_ID/PASSWORD"

type jazzRoomInfo struct {
	RoomID   string
	Password string
}

// isJazzSignalingLink detects Jazz call links of the form:
//
//	https://salutejazz.ru/calls/ROOM_ID?psw=PASSWORD  (new format)
//	https://salutejazz.ru/call/ROOM_ID/PASSWORD        (old format)
func isJazzSignalingLink(link string) bool {
	parsed, err := neturl.Parse(strings.TrimSpace(link))
	if err != nil {
		return false
	}

	if !isJazzCallHost(parsed.Hostname()) {
		return false
	}

	path := strings.ToLower(parsed.EscapedPath())
	return strings.HasPrefix(path, "/call/") || strings.HasPrefix(path, "/calls/")
}

// fetchJazzRoom parses room ID and password directly from the Jazz call link.
// No HTTP request to VPS - all parsing is local.
// Supported formats:
//
//	https://salutejazz.ru/calls/ROOM_ID?psw=PASSWORD  (new)
//	https://salutejazz.ru/call/ROOM_ID/PASSWORD        (old)
func fetchJazzRoom(_ context.Context, link string) (*jazzRoomInfo, error) {
	parsed, err := neturl.Parse(strings.TrimSpace(link))
	if err != nil {
		return nil, fmt.Errorf("invalid Jazz link: %w", err)
	}

	if !isJazzCallHost(parsed.Hostname()) {
		return nil, fmt.Errorf("invalid Jazz call link, expected %s", jazzCallLinkExample)
	}

	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")

	// New format: /calls/ROOM_ID?psw=PASSWORD
	if len(parts) == 2 && strings.EqualFold(parts[0], "calls") {
		roomID, err := neturl.PathUnescape(parts[1])
		if err != nil || roomID == "" {
			return nil, fmt.Errorf("invalid Jazz room ID in %s", jazzCallLinkExample)
		}
		password := parsed.Query().Get("psw")
		if password == "" {
			return nil, fmt.Errorf("invalid Jazz link: missing psw parameter")
		}
		return &jazzRoomInfo{
			RoomID:   roomID,
			Password: password,
		}, nil
	}

	// Old format: /call/ROOM_ID/PASSWORD
	if len(parts) == 3 && strings.EqualFold(parts[0], "call") {
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

	return nil, fmt.Errorf("invalid Jazz call link, expected %s", jazzCallLinkExample)
}

func isJazzCallHost(host string) bool {
	switch strings.ToLower(host) {
	case "salutejazz.ru", "jazz.sber.ru":
		return true
	default:
		return false
	}
}
