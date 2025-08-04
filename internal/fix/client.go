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
	"strings"
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

func (App) FromApp(msg *quickfix.Message, id quickfix.SessionID) quickfix.MessageRejectError {
	msgType, _ := msg.Header.GetString(quickfix.Tag(35))
	seqNum, _ := msg.Header.GetString(quickfix.Tag(34))

	raw := msg.String()
	log.Printf("[FIX-RAW] MsgType=%s Seq=%s Raw=%s", msgType, seqNum, raw)

	var idxPrice float64
	foundIndex := false

	// ✅ Tag 810 (UnderlyingPx) 확인
	var idxField quickfix.FIXFloat
	if err := msg.Body.GetField(810, &idxField); err == nil {
		idxPrice = float64(idxField)
		data.SetIndexPrice(idxPrice)
		log.Printf("[INDEX-810] IndexPrice=%.2f MsgType=%s Seq=%s", idxPrice, msgType, seqNum)
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
					log.Printf("[INDEX-269=3] IndexPrice=%.2f Sym=%s Seq=%s", idxPrice, sym.String(), seqNum)
					break
				}
			}
		}
	}

	// ✅ IndexPrice가 여전히 0이면 마지막 값 사용
	if !foundIndex {
		idxPrice = data.GetIndexPrice()
		log.Printf("[DEBUG-INDEX] No new index, using last=%.2f MsgType=%s Seq=%s", idxPrice, msgType, seqNum)
	}

	// ✅ Bid/Ask 처리 (개선된 버전)
	if msgType == "W" || msgType == "X" {
		sym, bid, ask, bidQty, askQty, delBid, delAsk := fastParseFIX(msg)

		// Index 심볼은 무시
		if sym == "BTC-DERIBIT-INDEX" || sym == "BTC-USD" {
			log.Printf("[DEBUG-INDEX-ONLY] Seq=%s Index=%.2f", seqNum, idxPrice)
			return nil
		}

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

// 개선된 FIX 메시지 파싱 함수
func fastParseFIX(msg *quickfix.Message) (string, float64, float64, float64, float64, bool, bool) {
	var sym string
	var bestBid, bestAsk, bidQty, askQty float64
	var delBid, delAsk bool

	msgType, _ := msg.Header.GetString(quickfix.Tag(35))

	switch msgType {
	case "W": // Market Data Snapshot
		snap := marketdatasnapshotfullrefresh.FromMessage(msg)
		symField := new(field.SymbolField)
		if err := snap.Get(symField); err == nil {
			sym = symField.Value()
		}

		group, err := snap.GetNoMDEntries()
		if err != nil {
			return sym, 0, 0, 0, 0, false, false
		}

		// ✅ 모든 bid/ask 레벨을 수집
		var bids, asks []PriceLevel

		for i := 0; i < group.Len(); i++ {
			entry := group.Get(i)
			etype := new(field.MDEntryTypeField)
			price := new(field.MDEntryPxField)
			qty := new(field.MDEntrySizeField)

			if err := entry.Get(etype); err != nil {
				continue
			}
			if err := entry.Get(price); err != nil {
				continue
			}
			if err := entry.Get(qty); err != nil {
				continue
			}

			p, ok := price.Value().Float64()
			if !ok || p <= 0 {
				continue
			}
			q, _ := qty.Value().Float64()

			// ✅ INDEX 타입(3)은 스킵
			if etype.Value() == enum.MDEntryType_INDEX_VALUE {
				continue
			}

			switch etype.Value() {
			case enum.MDEntryType_BID:
				if q > 0 { // 수량이 0인 레벨은 제외
					bids = append(bids, PriceLevel{Price: p, Qty: q})
				}
			case enum.MDEntryType_OFFER:
				if q > 0 { // 수량이 0인 레벨은 제외
					asks = append(asks, PriceLevel{Price: p, Qty: q})
				}
			}
		}

		// ✅ Best Bid = 가장 높은 매수가
		if len(bids) > 0 {
			bestBidLevel := findBestBid(bids)
			bestBid = bestBidLevel.Price
			bidQty = bestBidLevel.Qty
			log.Printf("[DEBUG-SNAPSHOT] Sym=%s BestBid=%.4f (from %d levels)", sym, bestBid, len(bids))
		}

		// ✅ Best Ask = 가장 낮은 매도가
		if len(asks) > 0 {
			bestAskLevel := findBestAsk(asks)
			bestAsk = bestAskLevel.Price
			askQty = bestAskLevel.Qty
			log.Printf("[DEBUG-SNAPSHOT] Sym=%s BestAsk=%.4f (from %d levels)", sym, bestAsk, len(asks))
		}

	case "X": // Market Data Incremental Refresh
		incr := marketdataincrementalrefresh.FromMessage(msg)

		// ✅ Symbol 추출 개선
		symField := new(field.SymbolField)
		if err := incr.Get(symField); err == nil {
			sym = symField.Value()
		}

		group, err := incr.GetNoMDEntries()
		if err != nil {
			return sym, 0, 0, 0, 0, false, false
		}

		// ✅ Incremental에서는 업데이트된 레벨만 처리
		for i := 0; i < group.Len(); i++ {
			entry := group.Get(i)
			etype := new(field.MDEntryTypeField)
			price := new(field.MDEntryPxField)
			qty := new(field.MDEntrySizeField)
			action := new(field.MDUpdateActionField)

			if err := entry.Get(etype); err != nil {
				continue
			}

			// ✅ INDEX 타입(3)은 스킵
			if etype.Value() == enum.MDEntryType_INDEX_VALUE {
				continue
			}

			if err := entry.Get(price); err != nil {
				continue
			}
			if err := entry.Get(qty); err != nil {
				continue
			}
			if err := entry.Get(action); err != nil {
				continue
			}

			// ✅ Symbol이 그룹에 없으면 메시지 레벨에서 가져오기
			if sym == "" {
				var entrySym quickfix.FIXString
				if err := entry.GetField(55, &entrySym); err == nil {
					sym = entrySym.String()
				}
			}

			p, ok := price.Value().Float64()
			if !ok {
				continue
			}
			q, _ := qty.Value().Float64()

			// ✅ DELETE 액션 처리
			if action.Value() == enum.MDUpdateAction_DELETE {
				q = 0
				if etype.Value() == enum.MDEntryType_BID {
					delBid = true
				} else if etype.Value() == enum.MDEntryType_OFFER {
					delAsk = true
				}
			}

			// ✅ 업데이트된 레벨 반환
			switch etype.Value() {
			case enum.MDEntryType_BID:
				bestBid = p
				bidQty = q
				log.Printf("[DEBUG-INCREMENTAL] Sym=%s BidUpdate Price=%.4f Qty=%.2f Action=%v", sym, p, q, action.Value())
			case enum.MDEntryType_OFFER:
				bestAsk = p
				askQty = q
				log.Printf("[DEBUG-INCREMENTAL] Sym=%s AskUpdate Price=%.4f Qty=%.2f Action=%v", sym, p, q, action.Value())
			}
		}
	}

	return sym, bestBid, bestAsk, bidQty, askQty, delBid, delAsk
}

// 가장 높은 매수가 찾기 (개선된 버전)
func findBestBid(bids []PriceLevel) PriceLevel {
	if len(bids) == 0 {
		return PriceLevel{}
	}

	// 가격 기준 내림차순 정렬 (높은 가격이 먼저)
	sort.Slice(bids, func(i, j int) bool {
		return bids[i].Price > bids[j].Price
	})

	return bids[0] // 가장 높은 가격
}

// 가장 낮은 매도가 찾기 (개선된 버전)
func findBestAsk(asks []PriceLevel) PriceLevel {
	if len(asks) == 0 {
		return PriceLevel{}
	}

	// 가격 기준 오름차순 정렬 (낮은 가격이 먼저)
	sort.Slice(asks, func(i, j int) bool {
		return asks[i].Price < asks[j].Price
	})

	return asks[0] // 가장 낮은 가격
}

// 인덱스 전용 심볼인지 확인
func isIndexSymbol(symbol string) bool {
	indexSymbols := []string{
		"BTC-DERIBIT-INDEX",
		"BTC-USD",
		"ETH-DERIBIT-INDEX",
		"ETH-USD",
		"BTC_USD", // 언더스코어 버전도 체크
		"ETH_USD",
	}

	for _, indexSym := range indexSymbols {
		if symbol == indexSym {
			return true
		}
	}
	return false
}

// 유효한 옵션 심볼인지 확인
func isValidOptionSymbol(symbol string) bool {
	// 옵션 심볼 패턴: BTC-DDMMMYY-STRIKE-C/P (예: BTC-29MAR24-50000-C)
	if len(symbol) < 10 {
		return false
	}

	// BTC 또는 ETH로 시작하고 -C 또는 -P로 끝나는지 확인
	if (strings.HasPrefix(symbol, "BTC-") || strings.HasPrefix(symbol, "ETH-")) &&
		(strings.HasSuffix(symbol, "-C") || strings.HasSuffix(symbol, "-P")) {
		return true
	}

	return false
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
