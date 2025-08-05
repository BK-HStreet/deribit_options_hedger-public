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
	"sort"
	"strconv"
	"time"

	"github.com/quickfixgo/enum"
	"github.com/quickfixgo/field"
	"github.com/quickfixgo/fix44/marketdatarequest"
	"github.com/quickfixgo/quickfix"
	"github.com/quickfixgo/quickfix/store/file"
)

// App implements quickfix.Application
type App struct{}

var optionSymbols []string
var engine *strategy.BoxSpreadEngine

func InitBoxEngine(e *strategy.BoxSpreadEngine) {
	engine = e
}

func SetOptionSymbols(symbols []string) {
	optionSymbols = symbols
}

func (App) OnCreate(id quickfix.SessionID) {}

func (App) OnLogon(id quickfix.SessionID) {
	log.Println("[FIX] >>>> OnLogon received from server!")

	// ✅ MarketDataRequest 생성
	mdReq := marketdatarequest.New(
		field.NewMDReqID("BTC_OPTIONS"),
		field.NewSubscriptionRequestType(enum.SubscriptionRequestType_SNAPSHOT_PLUS_UPDATES),
		field.NewMarketDepth(1),
	)
	mdReq.Set(field.NewMDUpdateType(enum.MDUpdateType_INCREMENTAL_REFRESH))
	mdReq.Set(field.NewAggregatedBook(true))

	// ✅ MDEntryTypes (Bid + Offer)
	mdEntryGroup := marketdatarequest.NewNoMDEntryTypesRepeatingGroup()
	bidEntry := mdEntryGroup.Add()
	bidEntry.Set(field.NewMDEntryType(enum.MDEntryType_BID))
	askEntry := mdEntryGroup.Add()
	askEntry.Set(field.NewMDEntryType(enum.MDEntryType_OFFER))
	mdReq.SetGroup(mdEntryGroup)

	// ✅ 옵션 심볼 + BTC Index 추가
	symGroup := marketdatarequest.NewNoRelatedSymRepeatingGroup()
	for _, sym := range optionSymbols {
		entry := symGroup.Add()
		entry.Set(field.NewSymbol(sym))
	}

	// ✅ BTC-USD Index 심볼 추가 (IndexPrice 수신)
	idxEntry := symGroup.Add()
	idxEntry.Set(field.NewSymbol("BTC-DERIBIT-INDEX"))
	mdReq.SetGroup(symGroup)

	// ✅ 요청 전송
	if err := quickfix.SendToTarget(mdReq, id); err != nil {
		log.Println("[FIX] MarketDataRequest send error:", err)
	} else {
		log.Println("[FIX] MarketDataRequest sent for options + BTC-USD Index")
	}
}

func (App) OnLogout(id quickfix.SessionID)                           {}
func (App) ToApp(msg *quickfix.Message, id quickfix.SessionID) error { return nil }
func (App) ToAdmin(msg *quickfix.Message, id quickfix.SessionID) {
	msgType, _ := msg.Header.GetString(quickfix.Tag(35))
	if msgType == "A" {
		clientID := os.Getenv("DERIBIT_CLIENT_ID")
		clientSecret := os.Getenv("DERIBIT_CLIENT_SECRET")
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)

		nonce := make([]byte, 32)
		_, _ = rand.Read(nonce)
		encodedNonce := base64.StdEncoding.EncodeToString(nonce)

		rawData := timestamp + "." + encodedNonce
		rawConcat := rawData + clientSecret

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

// PriceLevel - 헬퍼 구조체
type PriceLevel struct {
	Price float64
	Qty   float64
}

func (app *App) FromApp(msg *quickfix.Message, id quickfix.SessionID) quickfix.MessageRejectError {
	msgType, _ := msg.Header.GetString(quickfix.Tag(35))
	// seqNum, _ := msg.Header.GetString(quickfix.Tag(34))

	// raw := msg.String()
	// log.Printf("[FIX-RAW] MsgType=%s Seq=%s Raw=%s", msgType, seqNum, raw)

	var idxPrice float64
	foundIndex := false

	// ✅ Tag 810 (UnderlyingPx) 확인
	var idxField quickfix.FIXFloat
	if err := msg.Body.GetField(810, &idxField); err == nil {
		idxPrice = float64(idxField)
		data.SetIndexPrice(idxPrice)
		// log.Printf("[INDEX-810] IndexPrice=%.2f MsgType=%s Seq=%s", idxPrice, msgType, seqNum)
		foundIndex = true
	}

	// ✅ MsgType W or X 에서 Tag 269=3 처리 (개선된 버전)
	if msgType == "W" || msgType == "X" {
		group := quickfix.NewRepeatingGroup(268,
			quickfix.GroupTemplate{
				quickfix.GroupElement(279), // MDUpdateAction
				quickfix.GroupElement(55),  // Symbol
				quickfix.GroupElement(269), // MDEntryType
				quickfix.GroupElement(270), // Price
				quickfix.GroupElement(271), // Size
			})

		if err := msg.Body.GetGroup(group); err == nil {
			for i := 0; i < group.Len(); i++ {
				entry := group.Get(i)

				var mdType quickfix.FIXString
				var px quickfix.FIXFloat
				var sym quickfix.FIXString

				entry.GetField(269, &mdType)
				entry.GetField(270, &px)
				entry.GetField(55, &sym)

				// Symbol이 그룹 레벨에 없으면 메시지 레벨에서 가져오기
				if sym.String() == "" {
					var msgSym quickfix.FIXString
					if err := msg.Body.GetField(55, &msgSym); err == nil {
						sym = msgSym
					}
				}

				if string(mdType) == "3" && sym.String() == "BTC-DERIBIT-INDEX" {
					idxPrice = float64(px)
					data.SetIndexPrice(idxPrice)
					foundIndex = true
					// log.Printf("[INDEX-269=3] IndexPrice=%.2f Sym=%s Seq=%s", idxPrice, sym.String(), seqNum)
					break
				}
			}
		}
	}

	// ✅ IndexPrice가 여전히 0이면 마지막 값 사용
	if !foundIndex {
		idxPrice = data.GetIndexPrice()
		// log.Printf("[DEBUG-INDEX] No new index, using last=%.2f MsgType=%s Seq=%s", idxPrice, msgType, seqNum)
	}

	// ✅ Bid/Ask 처리 (수정된 버전)
	if msgType == "W" || msgType == "X" {
		sym, bid, ask, bidQty, askQty, delBid, delAsk := fastParseFIX(msg)

		// Index 심볼은 무시
		if sym == "BTC-DERIBIT-INDEX" || sym == "BTC-USD" {
			// log.Printf("[DEBUG-INDEX-ONLY] Seq=%s Index=%.2f", seqNum, idxPrice)
			return nil
		}

		// log.Printf("[DEBUG-APPLY] Seq=%s Sym=%s Bid=%.4f bidQty=%.4f Ask=%.4f askQty=%.4f Index=%.2f", seqNum, sym, bid, bidQty, ask, askQty, idxPrice)
		// log.Printf("[DEBUG-APPLY] Index=%.2f", idxPrice)

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

func fastParseFIX(msg *quickfix.Message) (string, float64, float64, float64, float64, bool, bool) {
	var sym string
	var bestBid, bestAsk, bidQty, askQty float64
	var delBid, delAsk bool

	msgType, _ := msg.Header.GetString(quickfix.Tag(35))

	// Extract Symbol (Tag 55) from message level
	var symField quickfix.FIXString
	if err := msg.Body.GetField(55, &symField); err == nil {
		sym = symField.String()
	}

	// Define a more comprehensive group template
	group := quickfix.NewRepeatingGroup(268,
		quickfix.GroupTemplate{
			quickfix.GroupElement(279), // MDUpdateAction
			quickfix.GroupElement(269), // MDEntryType
			quickfix.GroupElement(270), // Price
			quickfix.GroupElement(271), // Size
			quickfix.GroupElement(272), // MDEntryTime (optional)
			// Add other possible tags if needed
		})

	if err := msg.Body.GetGroup(group); err != nil {
		log.Printf("[ERROR] Failed to parse group 268: %v", err)
		return sym, bestBid, bestAsk, bidQty, askQty, delBid, delAsk
	}

	var bids, asks []PriceLevel
	var bidUpdates, askUpdates []PriceLevel

	for i := 0; i < group.Len(); i++ {
		entry := group.Get(i)

		var mdType quickfix.FIXString
		var price quickfix.FIXFloat
		var size quickfix.FIXFloat
		var action quickfix.FIXString

		// Extract fields with error checking
		if err := entry.GetField(269, &mdType); err != nil {
			// log.Printf("[ERROR] Missing MDEntryType (269) in group %d", i)
			continue
		}
		if err := entry.GetField(270, &price); err != nil {
			// log.Printf("[ERROR] Missing Price (270) in group %d", i)
			continue
		}
		if err := entry.GetField(271, &size); err != nil {
			// log.Printf("[ERROR] Missing Size (271) in group %d", i)
			continue
		}
		entry.GetField(279, &action) // Optional, may not be present in Snapshot

		p := float64(price)
		q := float64(size)

		// Skip INDEX type (3)
		if mdType.String() == "3" {
			continue
		}

		// log.Printf("[DEBUG-ENTRY] Seq=%s Entry=%d Type=%s Price=%.4f Size=%.2f Action=%s",
		// 	"", i, mdType.String(), p, q, action.String())

		switch msgType {
		case "W": // Snapshot
			if p > 0 && q > 0 {
				switch mdType.String() {
				case "0": // BID
					bids = append(bids, PriceLevel{Price: p, Qty: q})
				case "1": // OFFER
					asks = append(asks, PriceLevel{Price: p, Qty: q})
				}
			}

		case "X": // Incremental
			if action.String() == "2" { // DELETE
				q = 0
				switch mdType.String() {
				case "0": // BID
					delBid = true
				case "1": // OFFER
					delAsk = true
				}
			}

			switch mdType.String() {
			case "0": // BID
				bidUpdates = append(bidUpdates, PriceLevel{Price: p, Qty: q})
			case "1": // OFFER
				askUpdates = append(askUpdates, PriceLevel{Price: p, Qty: q})
			}
		}
	}

	// Process Snapshot
	if msgType == "W" {
		if len(bids) > 0 {
			bestBidLevel := findBestBid(bids)
			bestBid = bestBidLevel.Price
			bidQty = bestBidLevel.Qty
			log.Printf("[DEBUG-SNAPSHOT] Sym=%s BestBid=%.4f Qty=%.2f (from %d levels)",
				sym, bestBid, bidQty, len(bids))
		}
		if len(asks) > 0 {
			bestAskLevel := findBestAsk(asks)
			bestAsk = bestAskLevel.Price
			askQty = bestAskLevel.Qty
			log.Printf("[DEBUG-SNAPSHOT] Sym=%s BestAsk=%.4f Qty=%.2f (from %d levels)",
				sym, bestAsk, askQty, len(asks))
		}
	}

	// Process Incremental
	if msgType == "X" {
		if len(bidUpdates) > 0 {
			bestBidLevel := findBestBid(bidUpdates)
			bestBid = bestBidLevel.Price
			bidQty = bestBidLevel.Qty
			// log.Printf("[DEBUG-INCREMENTAL] Sym=%s BidUpdate=%.4f Qty=%.2f",
			// 	sym, bestBid, bidQty)
		}
		if len(askUpdates) > 0 {
			bestAskLevel := findBestAsk(askUpdates)
			bestAsk = bestAskLevel.Price
			askQty = bestAskLevel.Qty
			// log.Printf("[DEBUG-INCREMENTAL] Sym=%s AskUpdate=%.4f Qty=%.2f",
			// 	sym, bestAsk, askQty)
		}
	}

	return sym, bestBid, bestAsk, bidQty, askQty, delBid, delAsk
}

// ✅ Best Bid 찾기 (가장 높은 매수가)
func findBestBid(bids []PriceLevel) PriceLevel {
	if len(bids) == 0 {
		return PriceLevel{}
	}

	sort.Slice(bids, func(i, j int) bool {
		return bids[i].Price > bids[j].Price // 내림차순
	})

	return bids[0]
}

// ✅ Best Ask 찾기 (가장 낮은 매도가)
func findBestAsk(asks []PriceLevel) PriceLevel {
	if len(asks) == 0 {
		return PriceLevel{}
	}

	sort.Slice(asks, func(i, j int) bool {
		return asks[i].Price < asks[j].Price // 오름차순
	})

	return asks[0]
}

var initiator *quickfix.Initiator

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
	logFactory := quickfix.NewNullLogFactory()

	app := &App{}
	initr, err := quickfix.NewInitiator(app, storeFactory, settings, logFactory)
	if err != nil {
		return err
	}
	initiator = initr
	return initiator.Start()
}

func StopFIXEngine() {
	if initiator != nil {
		initiator.Stop()
	}
}
