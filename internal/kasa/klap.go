package kasa

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Credentials are the TP-Link cloud account used to derive KLAP keys.
type Credentials struct {
	Username string
	Password string
}

// ErrAuth means the device rejected the credentials during the handshake.
var ErrAuth = errors.New("klap: device rejected credentials")

// KLAP is a session with one device. It is safe for concurrent use but
// serialises requests, as the device does.
type KLAP struct {
	host  string
	creds Credentials

	mu      sync.Mutex
	client  *http.Client
	sess    *klapSession
	expires time.Time
}

// NewKLAP returns a client for the device at host (port 80).
func NewKLAP(host string, creds Credentials) *KLAP {
	return &KLAP{host: host, creds: creds}
}

func sha256sum(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func sha1sum(b []byte) []byte { s := sha1.Sum(b); return s[:] }
func md5sum(b []byte) []byte  { s := md5.Sum(b); return s[:] }

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// authHashes returns the two credential hashes in the order to try them:
// KLAP v2 (sha256 of sha1s) then v1 (md5 of md5s).
func (c Credentials) authHashes() (v2, v1 []byte) {
	u, p := []byte(c.Username), []byte(c.Password)
	v2 = sha256sum(sha1sum(u), sha1sum(p))
	v1 = md5sum(cat(md5sum(u), md5sum(p)))
	return v2, v1
}

func (k *KLAP) post(ctx context.Context, path string, query url.Values, body []byte) (int, http.Header, []byte, error) {
	u := url.URL{Scheme: "http", Host: k.host, Path: path}
	if query != nil {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, resp.Header, data, err
}

// newTransport returns a transport private to one device session: no proxy
// (the devices are on the LAN) and one connection, so closing it never
// disturbs another device or package.
func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		MaxIdleConns:          1,
		MaxIdleConnsPerHost:   1,
		MaxConnsPerHost:       1,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
}

func (k *KLAP) reset() {
	k.sess = nil
	if k.client != nil {
		k.client.CloseIdleConnections() // its own transport only
	}
	k.client = nil
}

// handshake performs handshake1 and handshake2 and installs a session.
func (k *KLAP) handshake(ctx context.Context) error {
	k.reset()
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	k.client = &http.Client{Jar: jar, Transport: newTransport()}

	local := make([]byte, 16)
	if _, err := rand.Read(local); err != nil {
		return err
	}
	status, hdr, body, err := k.post(ctx, "/app/handshake1", nil, local)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("klap handshake1: HTTP %d", status)
	}
	if len(body) != 48 {
		return fmt.Errorf("klap handshake1: reply is %d bytes, want 48", len(body))
	}
	remote, server := body[:16], body[16:]

	v2, v1 := k.creds.authHashes()
	var auth, proof []byte
	switch {
	case subtle.ConstantTimeCompare(sha256sum(local, remote, v2), server) == 1:
		auth, proof = v2, sha256sum(remote, local, v2)
	case subtle.ConstantTimeCompare(sha256sum(local, v1), server) == 1:
		auth, proof = v1, sha256sum(remote, v1)
	default:
		return ErrAuth
	}

	status, _, _, err = k.post(ctx, "/app/handshake2", nil, proof)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("klap handshake2: HTTP %d", status)
	}

	k.sess = newKlapSession(local, remote, auth)
	k.expires = time.Now().Add(sessionLifetime(hdr))
	return nil
}

// sessionLifetime honours the TIMEOUT cookie attribute the device sets, minus a margin.
func sessionLifetime(hdr http.Header) time.Duration {
	life := 24 * time.Hour
	for _, sc := range hdr.Values("Set-Cookie") {
		for _, part := range strings.Split(sc, ";") {
			name, val, ok := strings.Cut(part, "=")
			if ok && strings.EqualFold(strings.TrimSpace(name), "TIMEOUT") {
				if s, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && s > 0 {
					life = time.Duration(s) * time.Second
				}
			}
		}
	}
	if life > 20*time.Second {
		life -= 20 * time.Second
	}
	return life
}

// Request sends one encrypted request and returns the decrypted reply. It
// re-handshakes when the session is missing, expired or rejected.
func (k *KLAP) Request(ctx context.Context, payload []byte) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.sess == nil || time.Now().After(k.expires) {
		if err := k.handshake(ctx); err != nil {
			return nil, err
		}
	}
	out, err := k.requestOnce(ctx, payload)
	if err == errSessionRejected {
		if err := k.handshake(ctx); err != nil {
			return nil, err
		}
		out, err = k.requestOnce(ctx, payload)
	}
	if err != nil {
		k.reset() // next poll starts clean
	}
	return out, err
}

var errSessionRejected = errors.New("klap: session rejected")

func (k *KLAP) requestOnce(ctx context.Context, payload []byte) ([]byte, error) {
	enc, seq := k.sess.encrypt(payload)
	status, _, body, err := k.post(ctx, "/app/request", url.Values{"seq": {strconv.Itoa(int(seq))}}, enc)
	if err != nil {
		return nil, err
	}
	if status == http.StatusForbidden {
		return nil, errSessionRejected
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("klap request: HTTP %d", status)
	}
	return k.sess.decrypt(body)
}

// klapSession holds the derived keys and the request sequence number.
type klapSession struct {
	key []byte // AES-128
	iv  []byte // 12 bytes; the 4-byte sequence number completes it
	sig []byte // 28 bytes
	seq int32

	// block cipher state of the last encrypt, reused to decrypt the reply
	lastIV []byte
}

func newKlapSession(local, remote, auth []byte) *klapSession {
	fullIV := sha256sum([]byte("iv"), local, remote, auth)
	return &klapSession{
		key: sha256sum([]byte("lsk"), local, remote, auth)[:16],
		iv:  fullIV[:12],
		sig: sha256sum([]byte("ldk"), local, remote, auth)[:28],
		seq: int32(binary.BigEndian.Uint32(fullIV[len(fullIV)-4:])),
	}
}

func seqBytes(seq int32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(seq))
	return b
}

func (s *klapSession) encrypt(msg []byte) ([]byte, int32) {
	s.seq++
	iv := cat(s.iv, seqBytes(s.seq))
	s.lastIV = iv
	block, _ := aes.NewCipher(s.key) // key length is fixed, cannot fail
	pad := aes.BlockSize - len(msg)%aes.BlockSize
	padded := cat(msg, bytes.Repeat([]byte{byte(pad)}, pad))
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	return cat(sha256sum(s.sig, seqBytes(s.seq), ct), ct), s.seq
}

func (s *klapSession) decrypt(msg []byte) ([]byte, error) {
	if len(msg) < 32+aes.BlockSize || (len(msg)-32)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("klap: reply length %d invalid", len(msg))
	}
	// The reply is signed like the request, with the request's sequence
	// number. Check it before decrypting anything.
	want := sha256sum(s.sig, seqBytes(s.seq), msg[32:])
	if subtle.ConstantTimeCompare(want, msg[:32]) != 1 {
		return nil, errors.New("klap: reply signature mismatch")
	}
	block, _ := aes.NewCipher(s.key)
	pt := make([]byte, len(msg)-32)
	cipher.NewCBCDecrypter(block, s.lastIV).CryptBlocks(pt, msg[32:])
	pad := int(pt[len(pt)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(pt) {
		return nil, errors.New("klap: bad padding in reply")
	}
	for _, b := range pt[len(pt)-pad:] {
		if int(b) != pad {
			return nil, errors.New("klap: bad padding in reply")
		}
	}
	return pt[:len(pt)-pad], nil
}
