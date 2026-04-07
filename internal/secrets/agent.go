package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
)

const (
	agentAddIdentity    = byte(17)
	agentRemoveIdentity = byte(18)
	agentRequestList    = byte(11)
	agentIdentities     = byte(12)
	agentSignRequest    = byte(13)
	agentSignResponse   = byte(14)
	agentSuccess        = byte(6)

	identityComment = "paasctl-config-unlock-v1"
	encryptedPrefix = "enc:v1:"
)

var signChallenge = []byte("paasctl-config-encryption-key-v1")

type Identity struct {
	Blob    []byte
	Comment string
}

func IsEncrypted(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), encryptedPrefix)
}

func EncryptString(value string) (string, error) {
	key, err := CurrentKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(value), nil)
	return encryptedPrefix + base64.RawStdEncoding.EncodeToString(nonce) + ":" + base64.RawStdEncoding.EncodeToString(ciphertext), nil
}

func DecryptString(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if !IsEncrypted(raw) {
		return value, nil
	}
	key, err := CurrentKey()
	if err != nil {
		return "", err
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 4 {
		return "", fmt.Errorf("invalid encrypted value")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("invalid encrypted nonce: %w", err)
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return "", fmt.Errorf("invalid encrypted ciphertext: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt config value; run `paasctl config unlock`: %w", err)
	}
	return string(plaintext), nil
}

func Unlock(password string) error {
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("password is empty")
	}
	privateKey := privateKeyFromPassword(password)
	pub := privateKey.Public().(ed25519.PublicKey)
	if err := RemoveIdentity(publicKeyBlob(pub)); err != nil {
		// Ignore missing identity; AddIdentity below is authoritative.
		_ = err
	}
	return AddIdentity(privateKey)
}

func Relock() error {
	ids, err := ListIdentities()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if id.Comment == identityComment {
			if err := RemoveIdentity(id.Blob); err != nil {
				return err
			}
		}
	}
	return nil
}

func CurrentKey() ([]byte, error) {
	ids, err := ListIdentities()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if id.Comment != identityComment {
			continue
		}
		sig, err := Sign(id.Blob, signChallenge)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(sig)
		return sum[:], nil
	}
	return nil, fmt.Errorf("config encryption token is locked; run `paasctl config unlock`")
}

func ReadPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	if err := off.Run(); err != nil {
		return "", err
	}
	defer func() {
		on := exec.Command("stty", "echo")
		on.Stdin = os.Stdin
		_ = on.Run()
		fmt.Fprintln(os.Stderr)
	}()
	var b bytes.Buffer
	for {
		var one [1]byte
		n, err := os.Stdin.Read(one[:])
		if n > 0 {
			if one[0] == '\n' || one[0] == '\r' {
				break
			}
			b.WriteByte(one[0])
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
	}
	return b.String(), nil
}

func AddIdentity(privateKey ed25519.PrivateKey) error {
	pub := privateKey.Public().(ed25519.PublicKey)
	var payload bytes.Buffer
	payload.WriteByte(agentAddIdentity)
	writeString(&payload, []byte("ssh-ed25519"))
	writeString(&payload, pub)
	writeString(&payload, privateKey)
	writeString(&payload, []byte(identityComment))
	resp, err := agentRoundTrip(payload.Bytes())
	if err != nil {
		return err
	}
	if len(resp) == 0 || resp[0] != agentSuccess {
		return fmt.Errorf("ssh-agent refused config unlock identity")
	}
	return nil
}

func RemoveIdentity(blob []byte) error {
	var payload bytes.Buffer
	payload.WriteByte(agentRemoveIdentity)
	writeString(&payload, blob)
	resp, err := agentRoundTrip(payload.Bytes())
	if err != nil {
		return err
	}
	if len(resp) == 0 || resp[0] != agentSuccess {
		return fmt.Errorf("ssh-agent refused config unlock identity removal")
	}
	return nil
}

func ListIdentities() ([]Identity, error) {
	resp, err := agentRoundTrip([]byte{agentRequestList})
	if err != nil {
		return nil, err
	}
	if len(resp) < 5 || resp[0] != agentIdentities {
		return nil, fmt.Errorf("unexpected ssh-agent identities response")
	}
	count := int(binary.BigEndian.Uint32(resp[1:5]))
	rest := resp[5:]
	out := make([]Identity, 0, count)
	for i := 0; i < count; i++ {
		blob, rem, err := readString(rest)
		if err != nil {
			return nil, err
		}
		comment, rem, err := readString(rem)
		if err != nil {
			return nil, err
		}
		out = append(out, Identity{Blob: blob, Comment: string(comment)})
		rest = rem
	}
	return out, nil
}

func Sign(blob, data []byte) ([]byte, error) {
	var payload bytes.Buffer
	payload.WriteByte(agentSignRequest)
	writeString(&payload, blob)
	writeString(&payload, data)
	writeUint32(&payload, 0)
	resp, err := agentRoundTrip(payload.Bytes())
	if err != nil {
		return nil, err
	}
	if len(resp) < 5 || resp[0] != agentSignResponse {
		return nil, fmt.Errorf("unexpected ssh-agent sign response")
	}
	sigBlob, _, err := readString(resp[1:])
	if err != nil {
		return nil, err
	}
	_, rem, err := readString(sigBlob)
	if err != nil {
		return nil, err
	}
	sig, _, err := readString(rem)
	if err != nil {
		return nil, err
	}
	return sig, nil
}

func privateKeyFromPassword(password string) ed25519.PrivateKey {
	sum := sha256.Sum256([]byte("paasctl-config-unlock-seed-v1\x00" + password))
	for i := 0; i < 200000; i++ {
		next := sha256.Sum256(sum[:])
		sum = next
	}
	return ed25519.NewKeyFromSeed(sum[:])
}

func publicKeyBlob(pub ed25519.PublicKey) []byte {
	var b bytes.Buffer
	writeString(&b, []byte("ssh-ed25519"))
	writeString(&b, pub)
	return b.Bytes()
}

func agentRoundTrip(payload []byte) ([]byte, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if strings.TrimSpace(sock) == "" {
		return nil, fmt.Errorf("SSH_AUTH_SOCK is not set; start ssh-agent first")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to ssh-agent: %w", err)
	}
	defer conn.Close()

	var frame bytes.Buffer
	writeUint32(&frame, uint32(len(payload)))
	frame.Write(payload)
	if _, err := conn.Write(frame.Bytes()); err != nil {
		return nil, err
	}

	var lenBuf [4]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	resp := make([]byte, n)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func writeString(b *bytes.Buffer, value []byte) {
	writeUint32(b, uint32(len(value)))
	b.Write(value)
}

func writeUint32(b *bytes.Buffer, value uint32) {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], value)
	b.Write(raw[:])
}

func readString(raw []byte) ([]byte, []byte, error) {
	if len(raw) < 4 {
		return nil, nil, fmt.Errorf("short ssh-agent string")
	}
	n := int(binary.BigEndian.Uint32(raw[:4]))
	if len(raw) < 4+n {
		return nil, nil, fmt.Errorf("truncated ssh-agent string")
	}
	return raw[4 : 4+n], raw[4+n:], nil
}
