package fix

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/quickfixgo/enum"
	"github.com/quickfixgo/field"
	"github.com/quickfixgo/fix44/marketdatarequest"
	"github.com/quickfixgo/quickfix"
	"github.com/quickfixgo/quickfix/log/screen"
	"github.com/quickfixgo/quickfix/store/file"
)

// App implements quickfix.Application
type App struct{}

func (App) OnCreate(id quickfix.SessionID) {}
func (App) OnLogon(id quickfix.SessionID) {
	log.Println("[FIX] >>>> OnLogon received from server!")

	// MarketDataRequest 생성
	mdReq := marketdatarequest.New(
		field.NewMDReqID("BTC_OPTIONS"),
		field.NewSubscriptionRequestType(enum.SubscriptionRequestType_SNAPSHOT_PLUS_UPDATES),
		field.NewMarketDepth(1),
	)
	mdReq.Set(field.NewMDUpdateType(enum.MDUpdateType_INCREMENTAL_REFRESH))

	// ✅ MDEntryTypes 추가 (Bid + Offer)
	mdEntryGroup := marketdatarequest.NewNoMDEntryTypesRepeatingGroup()
	e1 := mdEntryGroup.Add()
	e1.Set(field.NewMDEntryType(enum.MDEntryType_BID))
	e2 := mdEntryGroup.Add()
	e2.Set(field.NewMDEntryType(enum.MDEntryType_OFFER))
	mdReq.SetGroup(mdEntryGroup)

	// ✅ Symbol 추가
	symGroup := marketdatarequest.NewNoRelatedSymRepeatingGroup()
	entry := symGroup.Add()
	entry.Set(field.NewSymbol("BTC-27JUL25-118000-C"))
	mdReq.SetGroup(symGroup)

	if err := quickfix.SendToTarget(mdReq, id); err != nil {
		log.Println("[FIX] MarketDataRequest send error:", err)
	} else {
		log.Println("[FIX] MarketDataRequest sent for BTC-27JUL25-118000-C")
	}
}

func (App) OnLogout(id quickfix.SessionID)                           {}
func (App) ToApp(msg *quickfix.Message, id quickfix.SessionID) error { return nil }
func (App) FromApp(msg *quickfix.Message, id quickfix.SessionID) quickfix.MessageRejectError {
	msgType, _ := msg.Header.GetString(quickfix.Tag(35))
	switch msgType {
	case "W":
		log.Println("[FIX] Snapshot:", msg.String())
	case "X":
		log.Println("[FIX] Incremental:", msg.String())
	default:
		log.Println("[FIX] FromApp msgType:", msgType)
	}
	// ✅ nil 리턴 → Validation 통과
	return nil
}

func (App) ToAdmin(msg *quickfix.Message, id quickfix.SessionID) {
	msgType, _ := msg.Header.GetString(quickfix.Tag(35))
	if msgType == "A" {
		clientID := os.Getenv("DERIBIT_CLIENT_ID")
		clientSecret := os.Getenv("DERIBIT_CLIENT_SECRET")

		log.Printf("[FIX-DEBUG] Using ClientID=%s (len=%d)", clientID, len(clientID))
		log.Printf("[FIX-DEBUG] Using ClientSecret prefix=%s... (len=%d)", clientSecret[:4], len(clientSecret))

		// ✅ Timestamp (strictly increasing, ms)
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)

		// ✅ 32-byte cryptographically secure random nonce
		nonce := make([]byte, 32)
		_, _ = rand.Read(nonce)

		// ✅ Standard Base64 encoding (with +, /, = padding)
		encodedNonce := base64.StdEncoding.EncodeToString(nonce)

		// ✅ RawData = timestamp.nonce
		rawData := timestamp + "." + encodedNonce
		rawConcat := rawData + clientSecret

		log.Printf("[FIX-DEBUG] RawData(96): %s", rawData)
		log.Printf("[FIX-DEBUG] RawData++Secret: %s", rawConcat)

		// ✅ SHA256(rawData + secret) -> Base64
		h := sha256.New()
		h.Write([]byte(rawConcat))
		passwordHash := h.Sum(nil)
		password := base64.StdEncoding.EncodeToString(passwordHash)

		log.Printf("[FIX-DEBUG] SHA256(rawData+secret) HEX: %x", passwordHash)
		log.Printf("[FIX-DEBUG] Password(554): %s", password)

		// ✅ Set fields with proper order and RawDataLength
		msg.Body.SetField(quickfix.Tag(108), quickfix.FIXInt(30))          // HeartBtInt
		msg.Body.SetField(quickfix.Tag(141), quickfix.FIXString("Y"))      // ResetOnLogon
		msg.Body.SetField(quickfix.Tag(95), quickfix.FIXInt(len(rawData))) // RawDataLength
		msg.Body.SetField(quickfix.Tag(96), quickfix.FIXString(rawData))   // RawData
		msg.Body.SetField(quickfix.Tag(553), quickfix.FIXString(clientID)) // Username
		msg.Body.SetField(quickfix.Tag(554), quickfix.FIXString(password)) // Password

		log.Printf("[FIX-OUT] Final Logon message: %s", msg.String())
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
	settings.GlobalSettings().Set("ValidateFieldsHaveValues", "N")
	settings.GlobalSettings().Set("ValidateUserDefinedFields", "N")
	settings.GlobalSettings().Set("ValidateIncomingMessage", "N")
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
