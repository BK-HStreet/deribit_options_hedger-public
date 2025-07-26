package fix

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/quickfixgo/quickfix"
	"github.com/quickfixgo/quickfix/log/screen"
	"github.com/quickfixgo/quickfix/store/file"
)

// App implements quickfix.Application
type App struct{}

func (App) OnCreate(id quickfix.SessionID)                           {}
func (App) OnLogon(id quickfix.SessionID)                            {}
func (App) OnLogout(id quickfix.SessionID)                           {}
func (App) ToApp(msg *quickfix.Message, id quickfix.SessionID) error { return nil }
func (App) FromApp(msg *quickfix.Message, id quickfix.SessionID) quickfix.MessageRejectError {
	return nil
}

// ✅ timestamp strictly increasing 보장
var lastTS int64
var tsMu sync.Mutex

func nextTimestampMS() string {
	tsMu.Lock()
	defer tsMu.Unlock()
	now := time.Now().UnixMilli()
	if now <= lastTS {
		now = lastTS + 1
	}
	lastTS = now
	return strconv.FormatInt(now, 10)
}

// ✅ 32-byte nonce Base64
func generateNonce() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func (App) ToAdmin(msg *quickfix.Message, id quickfix.SessionID) {
	msgType, _ := msg.Header.GetString(quickfix.Tag(35))
	if msgType == "A" {
		clientID := os.Getenv("DERIBIT_CLIENT_ID")
		clientSecret := os.Getenv("DERIBIT_CLIENT_SECRET")

		log.Printf("[FIX-DEBUG] Using ClientID=%s (len=%d)", clientID, len(clientID))
		if len(clientSecret) >= 4 {
			log.Printf("[FIX-DEBUG] Using ClientSecret prefix=%s... (len=%d)", clientSecret[:4], len(clientSecret))
		} else {
			log.Printf("[FIX-DEBUG] Using ClientSecret (len=%d)", len(clientSecret))
		}

		// ✅ RawData = timestamp.nonce
		timestamp := nextTimestampMS()
		nonce := generateNonce()
		rawData := fmt.Sprintf("%s.%s", timestamp, nonce)

		// ✅ Password = base64(sha256(RawData ++ client_secret))
		rawConcat := rawData + clientSecret
		h := sha256.New()
		h.Write([]byte(rawConcat))
		passwordHash := h.Sum(nil)
		password := base64.StdEncoding.EncodeToString(passwordHash)

		// ✅ 로깅
		log.Printf("[FIX-DEBUG] RawData(96): %s", rawData)
		log.Printf("[FIX-DEBUG] RawData++Secret: %s", rawConcat)
		log.Printf("[FIX-DEBUG] SHA256(rawData+secret) HEX: %x", passwordHash)
		log.Printf("[FIX-DEBUG] Password(554): %s", password)

		// ✅ 태그 세팅
		msg.Body.SetField(quickfix.Tag(96), quickfix.FIXString(rawData))
		msg.Body.SetField(quickfix.Tag(553), quickfix.FIXString(clientID))
		msg.Body.SetField(quickfix.Tag(554), quickfix.FIXString(password))
		msg.Body.SetField(quickfix.Tag(108), quickfix.FIXInt(30))

		// ✅ 최종 메시지 로깅
		log.Printf("[FIX-DEBUG] Final Logon Message:\n%s", msg.String())
	}
}

func (App) FromAdmin(msg *quickfix.Message, id quickfix.SessionID) quickfix.MessageRejectError {
	return nil
}

var initiator *quickfix.Initiator

// InitFIXEngine initializes the FIX engine with the correct config path.
func InitFIXEngine(cfgPath string) error {
	// 실행 파일 기준 프로젝트 루트 경로 계산
	_, b, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(b), "../../")
	absPath := filepath.Join(root, cfgPath)

	f, err := os.Open(absPath)
	if err != nil {
		return err
	}
	defer f.Close()

	settings, err := quickfix.ParseSettings(f)
	if err != nil {
		return err
	}

	// FileStoreFactory 사용 (세션 지속)
	storeFactory := NewFileStoreFactoryWithDefault(settings)
	logFactory := screen.NewLogFactory()

	app := App{}
	initr, err := quickfix.NewInitiator(app, storeFactory, settings, logFactory)
	if err != nil {
		return err
	}
	initiator = initr
	return initiator.Start()
}

// StopFIXEngine stops the FIX initiator cleanly.
func StopFIXEngine() {
	if initiator != nil {
		initiator.Stop()
	}
}

// FileStoreFactory 헬퍼
func NewFileStoreFactoryWithDefault(settings *quickfix.Settings) quickfix.MessageStoreFactory {
	return file.NewStoreFactory(settings)
}
