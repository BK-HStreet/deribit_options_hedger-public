package fix

import (
	"Options_Hedger/internal/data"
	"Options_Hedger/internal/strategy"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"log"
	"os"
	"path/filepath"
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

var engine *strategy.BoxSpreadEngine

// var idxVal float64

// // ✅ 외부에서 QuoteStore를 주입
// func InitQuoteStore(s *data.QuoteStore) {
// 	store = s
// }

func InitBoxEngine(e *strategy.BoxSpreadEngine) {
	engine = e
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

	// Incremental Refresh + AggregatedBook
	mdReq.Set(field.NewMDUpdateType(enum.MDUpdateType_INCREMENTAL_REFRESH))
	mdReq.Set(field.NewAggregatedBook(true))

	// MDEntryTypes (Bid + Offer만 요청)
	mdEntryGroup := marketdatarequest.NewNoMDEntryTypesRepeatingGroup()
	bidEntry := mdEntryGroup.Add()
	bidEntry.Set(field.NewMDEntryType(enum.MDEntryType_BID))
	askEntry := mdEntryGroup.Add()
	askEntry.Set(field.NewMDEntryType(enum.MDEntryType_OFFER))
	mdReq.SetGroup(mdEntryGroup)

	// 옵션 심볼들 추가
	symGroup := marketdatarequest.NewNoRelatedSymRepeatingGroup()
	for _, sym := range optionSymbols {
		entry := symGroup.Add()
		entry.Set(field.NewSymbol(sym))
	}
	mdReq.SetGroup(symGroup)

	// 요청 전송
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
	seqNum, _ := msg.Header.GetString(quickfix.Tag(34))

	raw := msg.String()
	log.Printf("[FIX-RAW] MsgType=%s Seq=%s Raw=%s", msgType, seqNum, raw)

	var idxPrice float64

	// ✅ Tag 810 (UnderlyingPx) 우선 확인
	var idxPriceField quickfix.FIXFloat
	if err := msg.Body.GetField(810, &idxPriceField); err == nil {
		idxPrice = float64(idxPriceField)
		data.SetIndexPrice(idxPrice)
		log.Printf("[DEBUG-810] IndexPrice=%.2f MsgType=%s Seq=%s", idxPrice, msgType, seqNum)
	}

	// ✅ Market Data Snapshot(W)/Incremental(X)에서 Tag 44 (IndexPrice) 탐색
	if msgType == "W" || msgType == "X" {
		group := quickfix.NewRepeatingGroup(268,
			quickfix.GroupTemplate{
				quickfix.GroupElement(269), // MDEntryType
				quickfix.GroupElement(44),  // Price
			})

		if err := msg.Body.GetGroup(group); err == nil {
			for i := 0; i < group.Len(); i++ {
				entry := group.Get(i)

				var mdType quickfix.FIXString
				if err := entry.GetField(269, &mdType); err == nil && string(mdType) == "2" {
					var px quickfix.FIXFloat
					if err := entry.GetField(44, &px); err == nil && float64(px) > 0 {
						idxPrice = float64(px)
						data.SetIndexPrice(idxPrice)
						log.Printf("[DEBUG-44] IndexPrice=%.2f Seq=%s", idxPrice, seqNum)
					}
				}
			}
		}
	}

	// ✅ 새로운 IndexPrice가 없으면 마지막 값 사용
	if idxPrice == 0 {
		idxPrice = data.GetIndexPrice()
		log.Printf("[DEBUG-INDEX] No new index, using last=%.2f MsgType=%s Seq=%s", idxPrice, msgType, seqNum)
	}

	// ✅ Bid/Ask 업데이트
	if msgType == "W" || msgType == "X" {
		sym, bid, ask, bidQty, askQty, delBid, delAsk := fastParseFIX(msg)
		log.Printf("[DEBUG-APPLY] Seq=%s Sym=%s Bid=%.4f Ask=%.4f Index=%.2f", seqNum, sym, bid, ask, idxPrice)

		if sym != "" {
			if bid > 0 || delBid {
				data.ApplyUpdate(sym, true, bid, bidQty, idxPrice)
			}
			if ask > 0 || delAsk {
				data.ApplyUpdate(sym, false, ask, askQty, idxPrice)
			}
		}
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
		log.Println("[FIX-SEND]", msg.String())
	}
}

func (App) FromAdmin(msg *quickfix.Message, id quickfix.SessionID) quickfix.MessageRejectError {
	return nil
}

func fastParseFIX(msg *quickfix.Message) (string, float64, float64, float64, float64, bool, bool) {
	var sym string
	var bestBid, bestAsk, bidQty, askQty float64
	var delBid, delAsk bool

	msgType, _ := msg.Header.GetString(quickfix.Tag(35))
	switch msgType {
	case "W":
		snap := marketdatasnapshotfullrefresh.FromMessage(msg)
		symField := new(field.SymbolField)
		if err := snap.Get(symField); err == nil {
			sym = symField.Value()
		}

		group, _ := snap.GetNoMDEntries()
		for i := 0; i < group.Len(); i++ {
			entry := group.Get(i)
			etype := new(field.MDEntryTypeField)
			price := new(field.MDEntryPxField)
			qty := new(field.MDEntrySizeField)

			_ = entry.Get(etype)
			_ = entry.Get(price)
			_ = entry.Get(qty)

			p, _ := price.Value().Float64()
			q, _ := qty.Value().Float64()

			switch etype.Value() {
			case enum.MDEntryType_BID:
				if p > bestBid {
					bestBid = p
					bidQty = q
				}
			case enum.MDEntryType_OFFER:
				if bestAsk == 0 || p < bestAsk {
					bestAsk = p
					askQty = q
				}
			}
		}

	case "X":
		incr := marketdataincrementalrefresh.FromMessage(msg)
		symField := new(field.SymbolField)
		if err := incr.Get(symField); err == nil {
			sym = symField.Value()
		}

		group, _ := incr.GetNoMDEntries()
		for i := 0; i < group.Len(); i++ {
			entry := group.Get(i)
			etype := new(field.MDEntryTypeField)
			price := new(field.MDEntryPxField)
			qty := new(field.MDEntrySizeField)
			action := new(field.MDUpdateActionField)

			_ = entry.Get(etype)
			_ = entry.Get(price)
			_ = entry.Get(qty)
			_ = entry.Get(action)

			p, _ := price.Value().Float64()
			q, _ := qty.Value().Float64()

			if action.Value() == enum.MDUpdateAction_DELETE {
				q = 0
				if etype.Value() == enum.MDEntryType_BID {
					delBid = true
				} else if etype.Value() == enum.MDEntryType_OFFER {
					delAsk = true
				}
			}

			switch etype.Value() {
			case enum.MDEntryType_BID:
				bestBid = p
				bidQty = q
			case enum.MDEntryType_OFFER:
				bestAsk = p
				askQty = q
			}
		}
	}

	return sym, bestBid, bestAsk, bidQty, askQty, delBid, delAsk
}

var initiator *quickfix.Initiator

// InitFIXEngine initializes the FIX engine with the correct config path.
func InitFIXEngine(cfgPath string) error {
	absPath, err := filepath.Abs(cfgPath)
	if err != nil {
		return err
	}

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
	logFactory := quickfix.NewNullLogFactory() // inactivation of FIX default meg
	// logFactory := screen.NewLogFactory()

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
