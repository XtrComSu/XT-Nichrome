package fixtures

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
)

// wsGUID is the RFC 6455 magic value used to derive Sec-WebSocket-Accept.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WebSocket opcodes (RFC 6455 §5.2).
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xa
)

// wsPage opens the echo socket, sends "hello" and logs the outcome.
//
// The scheme is chosen from location.protocol so the same page works on the
// TLS fixture (wss) as well as the plain one (ws); on http it is exactly
// ws://<host>/ws/echo.
const wsPage = `<h1>WebSocket echo</h1>
<pre id=log>connecting</pre>
<script>
(function () {
  var log = document.getElementById('log');
  function add(line) {
    if (log.textContent === 'connecting') { log.textContent = ''; }
    log.textContent += line + '\n';
  }
  var url = location.protocol === 'https:'
    ? 'wss://' + location.host + '/ws/echo'   // TLS fixture needs wss
    : 'ws://' + location.host + '/ws/echo';
  var ws;
  try {
    ws = new WebSocket(url);
  } catch (e) {
    add('<error:' + e.message + '>');
    return;
  }
  ws.addEventListener('open', function () { ws.send('hello'); });
  ws.addEventListener('message', function (ev) { add('<received:' + ev.data + '>'); });
  ws.addEventListener('error', function () { add('<error:websocket>'); });
  ws.addEventListener('close', function (ev) {
    add('<closed:' + ev.code + '>');
  });
})();
</script>`

// handleWSPage serves the echo client page.
func (s *Server) handleWSPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ws" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	writeHTML(w, http.StatusOK, htmlDoc("WebSocket echo", wsPage))
}

// handleWSEcho performs the HTTP/1.1 Upgrade handshake by hand and echoes text
// frames. Each successfully echoed text frame is one hit under "ws/echo".
func (s *Server) handleWSEcho(w http.ResponseWriter, r *http.Request) {
	if !headerHasToken(r.Header, "Connection", "upgrade") ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "expected a websocket upgrade", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()

	// Handshake response. Sent through the hijacked buffered writer so no
	// net/http machinery runs after the connection is taken over.
	accept := wsAccept(key)
	var resp strings.Builder
	resp.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	resp.WriteString("Upgrade: websocket\r\n")
	resp.WriteString("Connection: Upgrade\r\n")
	resp.WriteString("Sec-WebSocket-Accept: " + accept + "\r\n\r\n")
	if _, err := brw.WriteString(resp.String()); err != nil {
		return
	}
	if err := brw.Flush(); err != nil {
		return
	}

	// Keep any bytes the server already buffered: the client may pipeline its
	// first frame right behind the handshake.
	rd := brw.Reader
	if n := rd.Buffered(); n == 0 {
		rd = bufio.NewReader(conn)
	}
	s.wsLoop(conn, rd)
}

// wsLoop reads frames until close or error, echoing text to the client.
func (s *Server) wsLoop(conn net.Conn, rd *bufio.Reader) {
	var frag []byte
	var fragOp byte
	for {
		fin, op, payload, err := readWSFrame(rd)
		if err != nil {
			return
		}
		switch op {
		case opPing:
			if err := writeWSFrame(conn, opPong, payload); err != nil {
				return
			}
		case opPong:
			// Ignored.
		case opClose:
			_ = writeWSFrame(conn, opClose, payload)
			return
		case opText, opBinary:
			if !fin {
				fragOp, frag = op, append(frag[:0], payload...)
				continue
			}
			if err := s.echoWS(conn, op, payload); err != nil {
				return
			}
		case opContinuation:
			frag = append(frag, payload...)
			if !fin {
				continue
			}
			if err := s.echoWS(conn, fragOp, frag); err != nil {
				return
			}
			frag, fragOp = frag[:0], 0
		default:
			return
		}
	}
}

// echoWS writes one frame back and counts the round trip.
func (s *Server) echoWS(conn net.Conn, op byte, payload []byte) error {
	if err := writeWSFrame(conn, op, payload); err != nil {
		return err
	}
	if op == opText {
		s.Hit.Add("ws/echo")
	}
	return nil
}

// wsAccept derives Sec-WebSocket-Accept from the client key.
func wsAccept(key string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(key) + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// headerHasToken reports whether header h contains token as a comma-separated
// value, case-insensitively.
func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// errWSProtocol reports a malformed frame.
var errWSProtocol = errors.New("fixtures: malformed websocket frame")

// readWSFrame reads one frame, unmasking the payload if the client masked it.
func readWSFrame(r *bufio.Reader) (fin bool, opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return
	}
	fin = hdr[0]&0x80 != 0
	opcode = hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	length := uint64(hdr[1] & 0x7f)

	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	// A fixture never needs multi-megabyte frames; refusing them keeps a buggy
	// client from allocating the host out of memory.
	const maxPayload = 1 << 20
	if length > maxPayload {
		err = errWSProtocol
		return
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(r, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(r, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

// writeWSFrame writes one unmasked server-to-client frame.
func writeWSFrame(w io.Writer, opcode byte, payload []byte) error {
	var hdr [10]byte
	hdr[0] = 0x80 | opcode
	n := 2
	switch l := len(payload); {
	case l < 126:
		hdr[1] = byte(l)
	case l <= 0xffff:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(l))
		n = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(l))
		n = 10
	}
	if _, err := w.Write(hdr[:n]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
