package common

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

type Socks5Upstream struct {
	Addr string
	User string
	Pass string
}

func NewSocks5Upstream(addr, user, pass string) *Socks5Upstream {
	if addr == "" {
		return nil
	}
	return &Socks5Upstream{Addr: addr, User: user, Pass: pass}
}

func (u *Socks5Upstream) greet(conn net.Conn) error {
	method := byte(0x00)
	if u.User != "" {
		method = 0x02
	}
	if _, err := conn.Write([]byte{0x05, 0x01, method}); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}
	if resp[0] != 0x05 {
		return errors.New("socks5: bad version in method reply")
	}
	switch resp[1] {
	case 0x00:
		return nil
	case 0x02:
		return u.authUserPass(conn)
	default:
		return fmt.Errorf("socks5: server rejected offered auth methods (0x%02x)", resp[1])
	}
}

func (u *Socks5Upstream) authUserPass(conn net.Conn) error {
	if len(u.User) > 255 || len(u.Pass) > 255 {
		return errors.New("socks5: username or password too long")
	}
	req := []byte{0x01, byte(len(u.User))}
	req = append(req, u.User...)
	req = append(req, byte(len(u.Pass)))
	req = append(req, u.Pass...)
	if _, err := conn.Write(req); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}
	if resp[1] != 0x00 {
		return errors.New("socks5: username/password auth rejected")
	}
	return nil
}

func encodeSocksAddr(hostPort string) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(hostPort)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("socks5: bad port %q: %w", portStr, err)
	}
	var out []byte
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			out = append(out, 0x01)
			out = append(out, v4...)
		} else {
			out = append(out, 0x04)
			out = append(out, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, errors.New("socks5: hostname too long")
		}
		out = append(out, 0x03, byte(len(host)))
		out = append(out, host...)
	}
	out = append(out, byte(port>>8), byte(port))
	return out, nil
}

func readSocksReply(conn net.Conn) (string, int, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return "", 0, err
	}
	if head[0] != 0x05 {
		return "", 0, errors.New("socks5: bad version in reply")
	}
	if head[1] != 0x00 {
		return "", 0, fmt.Errorf("socks5: request rejected (0x%02x)", head[1])
	}
	var host string
	switch head[3] {
	case 0x01:
		b := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()
	case 0x04:
		b := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return "", 0, err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = string(b)
	default:
		return "", 0, errors.New("socks5: bad address type in reply")
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return "", 0, err
	}
	return host, int(binary.BigEndian.Uint16(portBytes)), nil
}

func (u *Socks5Upstream) DialTCP(dst string, timeout time.Duration) (net.Conn, error) {
	addr, err := encodeSocksAddr(dst)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", u.Addr, timeout)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(timeout))
	if err := u.greet(conn); err != nil {
		conn.Close()
		return nil, err
	}
	req := append([]byte{0x05, 0x01, 0x00}, addr...)
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}
	if _, _, err := readSocksReply(conn); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	return conn, nil
}

type Socks5UDPSession struct {
	ctrl  net.Conn
	relay *net.UDPConn
}

func (u *Socks5Upstream) UDPAssociate(timeout time.Duration) (*Socks5UDPSession, error) {
	ctrl, err := net.DialTimeout("tcp", u.Addr, timeout)
	if err != nil {
		return nil, err
	}
	ctrl.SetDeadline(time.Now().Add(timeout))
	if err := u.greet(ctrl); err != nil {
		ctrl.Close()
		return nil, err
	}
	if _, err := ctrl.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		ctrl.Close()
		return nil, err
	}
	bndHost, bndPort, err := readSocksReply(ctrl)
	if err != nil {
		ctrl.Close()
		return nil, err
	}
	ctrl.SetDeadline(time.Time{})
	target, err := u.relayTarget(bndHost, bndPort)
	if err != nil {
		ctrl.Close()
		return nil, err
	}
	relay, err := net.DialUDP("udp", nil, target)
	if err != nil {
		ctrl.Close()
		return nil, err
	}
	return &Socks5UDPSession{ctrl: ctrl, relay: relay}, nil
}

func (u *Socks5Upstream) relayTarget(bndHost string, bndPort int) (*net.UDPAddr, error) {
	host := bndHost
	if host == "" || host == "0.0.0.0" || host == "::" {
		h, _, err := net.SplitHostPort(u.Addr)
		if err != nil {
			return nil, err
		}
		host = h
	}
	return net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(bndPort)))
}

func (s *Socks5UDPSession) WriteTo(data []byte, dst string) error {
	addr, err := encodeSocksAddr(dst)
	if err != nil {
		return err
	}
	pkt := make([]byte, 0, 3+len(addr)+len(data))
	pkt = append(pkt, 0x00, 0x00, 0x00)
	pkt = append(pkt, addr...)
	pkt = append(pkt, data...)
	_, err = s.relay.Write(pkt)
	return err
}

func (s *Socks5UDPSession) Read(buf []byte) (int, error) {
	n, err := s.relay.Read(buf)
	if err != nil {
		return 0, err
	}
	if n < 4 {
		return 0, errors.New("socks5: short udp reply")
	}
	var header int
	switch buf[3] {
	case 0x01:
		header = 4 + net.IPv4len + 2
	case 0x04:
		header = 4 + net.IPv6len + 2
	case 0x03:
		if n < 5 {
			return 0, errors.New("socks5: short udp reply")
		}
		header = 4 + 1 + int(buf[4]) + 2
	default:
		return 0, errors.New("socks5: bad address type in udp reply")
	}
	if n < header {
		return 0, errors.New("socks5: short udp reply")
	}
	payload := n - header
	copy(buf, buf[header:n])
	return payload, nil
}

func (s *Socks5UDPSession) SetReadDeadline(t time.Time) error {
	return s.relay.SetReadDeadline(t)
}

func (s *Socks5UDPSession) Close() error {
	s.relay.Close()
	return s.ctrl.Close()
}
