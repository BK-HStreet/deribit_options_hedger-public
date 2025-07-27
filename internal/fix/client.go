package fix

import (
	"OptionsHedger/internal/data"
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
	"github.com/quickfixgo/fix44/marketdataincrementalrefresh"
	"github.com/quickfixgo/fix44/marketdatarequest"
	"github.com/quickfixgo/fix44/marketdatasnapshotfullrefresh"
	"github.com/quickfixgo/quickfix"
	"github.com/quickfixgo/quickfix/store/file"
)

// App implements quickfix.Application
type App struct{}

// ✅ 전역 선언: 옵션 심볼 리스트와 QuoteStore
var optionSymbols []string
var store *data.QuoteStore

// ✅ 외부에서 QuoteStore를 주입
func InitQuoteStore(s *data.QuoteStore) {
	store = s
}

// ✅ 옵션 심볼 리스트 설정
func SetOptionSymbols(symbols []string) {
	optionSymbols = symbols
}

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

	// MDEntryTypes (Bid + Offer)
	mdEntryGroup := marketdatarequest.NewNoMDEntryTypesRepeatingGroup()
	e1 := mdEntryGroup.Add()
	e1.Set(field.NewMDEntryType(enum.MDEntryType_BID))
	e2 := mdEntryGroup.Add()
	e2.Set(field.NewMDEntryType(enum.MDEntryType_OFFER))
	mdReq.SetGroup(mdEntryGroup)

	// ✅ 모든 심볼 추가
	symGroup := marketdatarequest.NewNoRelatedSymRepeatingGroup()
	for _, sym := range optionSymbols {
		entry := symGroup.Add()
		entry.Set(field.NewSymbol(sym))
	}
	mdReq.SetGroup(symGroup)

	if err := quickfix.SendToTarget(mdReq, id); err != nil {
		log.Println("[FIX] MarketDataRequest send error:", err)
	} else {
		log.Println("[FIX] MarketDataRequest sent for options")
	}
}

func (App) OnLogout(id quickfix.SessionID)                           {}
func (App) ToApp(msg *quickfix.Message, id quickfix.SessionID) error { return nil }

func (App) FromApp(msg *quickfix.Message, id quickfix.SessionID) quickfix.MessageRejectError {
	msgType, _ := msg.Header.GetString(quickfix.Tag(35))
	if msgType == "W" || msgType == "X" {
		sym, bid, ask, bidQty, askQty := fastParseFIX(msg)

		// ✅ FIX 시세를 채널 기반 QuoteStore로 전달
		if store != nil && bid > 0 && ask > 0 {
			store.Set(sym, bid, ask)
		}

		log.Printf("[FIX-DEPTH] %s | Bid=%.4f (Qty=%.4f) | Ask=%.4f (Qty=%.4f)",
			sym, bid, bidQty, ask, askQty)
	}
	return nil
}

func (App) ToAdmin(msg *quickfix.Message, id quickfix.SessionID) {
	msgType, _ := msg.Header.GetString(quickfix.Tag(35))
	if msgType == "A" {
		clientID := os.Getenv("DERIBIT_CLIENT_ID")
		clientSecret := os.Getenv("DERIBIT_CLIENT_SECRET")

		// ✅ Timestamp (strictly increasing, ms)
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)

		// ✅ 32-byte cryptographically secure random nonce
		nonce := make([]byte, 32)
		_, _ = rand.Read(nonce)

		// ✅ Standard Base64 encoding
		encodedNonce := base64.StdEncoding.EncodeToString(nonce)

		// ✅ RawData = timestamp.nonce
		rawData := timestamp + "." + encodedNonce
		rawConcat := rawData + clientSecret

		// ✅ SHA256(rawData + secret) -> Base64
		h := sha256.New()
		h.Write([]byte(rawConcat))
		passwordHash := h.Sum(nil)
		password := base64.StdEncoding.EncodeToString(passwordHash)

		msg.Body.SetField(quickfix.Tag(108), quickfix.FIXInt(30))
		msg.Body.SetField(quickfix.Tag(141), quickfix.FIXString("Y"))
		msg.Body.SetField(quickfix.Tag(95), quickfix.FIXInt(len(rawData)))
		msg.Body.SetField(quickfix.Tag(96), quickfix.FIXString(rawData))
		msg.Body.SetField(quickfix.Tag(553), quickfix.FIXString(clientID))
		msg.Body.SetField(quickfix.Tag(554), quickfix.FIXString(password))
	}
}

func (App) FromAdmin(msg *quickfix.Message, id quickfix.SessionID) quickfix.MessageRejectError {
	return nil
}

func fastParseFIX(msg *quickfix.Message) (string, float64, float64, float64, float64) {
	var sym string
	var bid, ask, bidQty, askQty float64

	msgType, _ := msg.Header.GetString(quickfix.Tag(35))
	switch msgType {
	case "W": // Snapshot
		snap := marketdatasnapshotfullrefresh.FromMessage(msg)
		symField := new(field.SymbolField)
		if err := snap.Get(symField); err == nil {
			sym = symField.Value()
		}
		group, _ := snap.GetNoMDEntries()
		for i := 0; i < group.Len(); i++ {
			entry := group.Get(i)

			etypeField := new(field.MDEntryTypeField)
			priceField := new(field.MDEntryPxField)
			qtyField := new(field.MDEntrySizeField)

			_ = entry.Get(etypeField)
			_ = entry.Get(priceField)
			_ = entry.Get(qtyField)

			price, _ := priceField.Value().Float64()
			size, _ := qtyField.Value().Float64()

			switch etypeField.Value() {
			case enum.MDEntryType_BID:
				bid = price
				bidQty = size
			case enum.MDEntryType_OFFER:
				ask = price
				askQty = size
			}
		}

	case "X": // Incremental
		incr := marketdataincrementalrefresh.FromMessage(msg)
		group, _ := incr.GetNoMDEntries()
		for i := 0; i < group.Len(); i++ {
			entry := group.Get(i)

			symField := new(field.SymbolField)
			etypeField := new(field.MDEntryTypeField)
			priceField := new(field.MDEntryPxField)
			qtyField := new(field.MDEntrySizeField)

			_ = entry.Get(symField)
			_ = entry.Get(etypeField)
			_ = entry.Get(priceField)
			_ = entry.Get(qtyField)

			sym = symField.Value()
			price, _ := priceField.Value().Float64()
			size, _ := qtyField.Value().Float64()

			switch etypeField.Value() {
			case enum.MDEntryType_BID:
				bid = price
				bidQty = size
			case enum.MDEntryType_OFFER:
				ask = price
				askQty = size
			}
		}
	}

	return sym, bid, ask, bidQty, askQty
}

var initiator *quickfix.Initiator

// InitFIXEngine initializes the FIX engine with the correct config path.
func InitFIXEngine(cfgPath string) error {
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

	storeFactory := file.NewStoreFactory(settings)
	logFactory := quickfix.NewNullLogFactory()

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
