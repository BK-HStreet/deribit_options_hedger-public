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
	"github.com/quickfixgo/fix44/marketdatarequest"
	"github.com/quickfixgo/quickfix"
	"github.com/quickfixgo/quickfix/store/file"
)

// App implements quickfix.Application
type App struct{}

var optionSymbols []string
var engine *strategy.BoxSpreadHFT

// ✅ HFT 최적화: 심볼 -> 인덱스 매핑 (O(1) 룩업)
var symbolToIndex map[string]int32
var indexToSymbol [data.MaxOptions]string

func InitBoxEngine(e *strategy.BoxSpreadHFT) {
	engine = e
}

func SetOptionSymbols(symbols []string) {
	optionSymbols = symbols

	// ✅ 심볼 인덱스 매핑 초기화 (HFT 최적화)
	symbolToIndex = make(map[string]int32, len(symbols))
	for i, sym := range symbols {
		if i >= data.MaxOptions {
			break
		}
		symbolToIndex[sym] = int32(i)
		indexToSymbol[i] = sym
	}
}

//go:noinline
func getSymbolIndex(symbol string) int32 {
	if idx, ok := symbolToIndex[symbol]; ok {
		return idx
	}
	return -1
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

// ✅ HFT 최적화: 구조체 크기 최소화
type PriceLevel struct {
	Price float64
	Qty   float64
}

//go:noinline
func (app *App) FromApp(msg *quickfix.Message, id quickfix.SessionID) quickfix.MessageRejectError {
	msgType, _ := msg.Header.GetString(quickfix.Tag(35))

	// //benkim..복원필
	// seqNum, _ := msg.Header.GetString(quickfix.Tag(34))
	// raw := msg.String()
	// log.Printf("[FIX-RAW] MsgType=%s Seq=%s Raw=%s", msgType, seqNum, raw)
	// // benkim..end

	var idxPrice float64
	foundIndex := false

	// ✅ Tag 810 (UnderlyingPx) 확인
	var idxField quickfix.FIXFloat
	if err := msg.Body.GetField(810, &idxField); err == nil {
		idxPrice = float64(idxField)
		data.SetIndexPrice(idxPrice)
		foundIndex = true
	}

	// ✅ MsgType W or X 에서 Tag 269=3 처리 (HFT 최적화 버전)
	if msgType == "W" || msgType == "X" {
		if !foundIndex {
			// ✅ 인덱스 가격 빠른 파싱
			idxPrice = parseIndexPriceFast(msg)
			if idxPrice > 0 {
				data.SetIndexPrice(idxPrice)
				foundIndex = true
			}
		}

		// ✅ Bid/Ask 처리 (HFT 최적화)
		sym, bid, ask, bidQty, askQty, delBid, delAsk := fastParseHFT(msg, msgType)

		// Index 심볼은 무시 (빠른 문자열 비교)
		if len(sym) > 10 && (strings.HasPrefix(sym, "BTC-DERIBIT") || strings.HasPrefix(sym, "BTC-USD")) {
			return nil
		}

		if sym != "" {
			symbolIdx := getSymbolIndex(sym)
			if symbolIdx >= 0 {
				// ✅ HFT 최적화된 업데이트 함수 사용
				if !foundIndex {
					idxPrice = data.GetIndexPrice()
				}

				if bid > 0 || delBid {
					data.ApplyUpdateFast(symbolIdx, true, bid, bidQty, idxPrice)
				}
				if ask > 0 || delAsk {
					data.ApplyUpdateFast(symbolIdx, false, ask, askQty, idxPrice)
				}
			}
		}
	}

	return nil
}

// ✅ HFT 최적화: 인덱스 가격만 빠르게 파싱
//
//go:noinline
func parseIndexPriceFast(msg *quickfix.Message) float64 {
	// ✅ 먼저 심볼 확인
	var symField quickfix.FIXString
	if err := msg.Body.GetField(55, &symField); err != nil {
		return 0
	}

	sym := symField.String()
	if sym != "BTC-DERIBIT-INDEX" {
		return 0
	}

	// ✅ 간단한 그룹 템플릿으로 빠른 파싱
	group := quickfix.NewRepeatingGroup(268,
		quickfix.GroupTemplate{
			quickfix.GroupElement(279), // MDUpdateAction (X 메시지에 있음)
			quickfix.GroupElement(269), // MDEntryType
			quickfix.GroupElement(270), // Price
		})

	if err := msg.Body.GetGroup(group); err != nil {
		return 0
	}

	for i := 0; i < group.Len(); i++ {
		entry := group.Get(i)

		var mdType quickfix.FIXString
		var px quickfix.FIXFloat

		if entry.GetField(269, &mdType) != nil || entry.GetField(270, &px) != nil {
			continue
		}

		if mdType.String() == "3" { // INDEX 타입
			return float64(px)
		}
	}
	return 0
}

// ✅ HFT 최적화된 FIX 파싱 (메시지 타입별 최적화)
//
//go:noinline
func fastParseHFT(msg *quickfix.Message, msgType string) (string, float64, float64, float64, float64, bool, bool) {
	var sym string
	var bestBid, bestAsk, bidQty, askQty float64
	var delBid, delAsk bool

	// ✅ 심볼 빠른 추출
	var symField quickfix.FIXString
	if err := msg.Body.GetField(55, &symField); err == nil {
		sym = symField.String()
	}

	// ✅ 메시지 타입별 최적화된 그룹 템플릿
	var group *quickfix.RepeatingGroup

	switch msgType {
	case "W": // Snapshot
		g := quickfix.NewRepeatingGroup(268,
			quickfix.GroupTemplate{
				quickfix.GroupElement(269), // MDEntryType
				quickfix.GroupElement(270), // Price
				quickfix.GroupElement(271), // Size
			})
		group = g
	case "X": // Incremental
		g := quickfix.NewRepeatingGroup(268,
			quickfix.GroupTemplate{
				quickfix.GroupElement(279), // MDUpdateAction
				quickfix.GroupElement(269), // MDEntryType
				quickfix.GroupElement(270), // Price
				quickfix.GroupElement(271), // Size
			})
		group = g
	default:
		return sym, bestBid, bestAsk, bidQty, askQty, delBid, delAsk
	}

	if err := msg.Body.GetGroup(group); err != nil {
		return sym, bestBid, bestAsk, bidQty, askQty, delBid, delAsk
	}

	// ✅ HFT 최적화: 스택 배열 사용 (힙 할당 없음)
	var bids [8]PriceLevel // 최대 8개 레벨
	var asks [8]PriceLevel
	var bidCount, askCount int

	var bidUpdates [2]PriceLevel // Incremental은 보통 1-2개
	var askUpdates [2]PriceLevel
	var bidUpdateCount, askUpdateCount int

	for i := 0; i < group.Len(); i++ {
		entry := group.Get(i)

		var mdType quickfix.FIXString
		var price quickfix.FIXFloat
		var size quickfix.FIXFloat
		var action quickfix.FIXString

		// ✅ 필수 필드만 체크
		if entry.GetField(269, &mdType) != nil ||
			entry.GetField(270, &price) != nil ||
			entry.GetField(271, &size) != nil {
			continue
		}

		// ✅ INDEX 타입 스킵 (빠른 체크)
		if mdType.String() == "3" {
			continue
		}

		p := float64(price)
		q := float64(size)

		switch msgType {
		case "W": // Snapshot
			if p > 0 && q > 0 {
				switch mdType.String() {
				case "0": // BID
					if bidCount < 8 {
						bids[bidCount] = PriceLevel{Price: p, Qty: q}
						bidCount++
					}
				case "1": // OFFER
					if askCount < 8 {
						asks[askCount] = PriceLevel{Price: p, Qty: q}
						askCount++
					}
				}
			}

		case "X": // Incremental
			entry.GetField(279, &action) // 선택적

			if action.String() == "2" { // DELETE
				q = 0
				switch mdType.String() {
				case "0":
					delBid = true
				case "1":
					delAsk = true
				}
			}

			switch mdType.String() {
			case "0": // BID
				if bidUpdateCount < 2 {
					bidUpdates[bidUpdateCount] = PriceLevel{Price: p, Qty: q}
					bidUpdateCount++
				}
			case "1": // OFFER
				if askUpdateCount < 2 {
					askUpdates[askUpdateCount] = PriceLevel{Price: p, Qty: q}
					askUpdateCount++
				}
			}
		}
	}

	// ✅ 결과 처리 (인라인 최적화)
	switch msgType {
	case "W": // Snapshot
		if bidCount > 0 {
			bestBidLevel := findBestBidFast(bids[:bidCount])
			bestBid = bestBidLevel.Price
			bidQty = bestBidLevel.Qty
		}
		if askCount > 0 {
			bestAskLevel := findBestAskFast(asks[:askCount])
			bestAsk = bestAskLevel.Price
			askQty = bestAskLevel.Qty
		}

	case "X": // Incremental
		if bidUpdateCount > 0 {
			bestBidLevel := bidUpdates[0] // 첫 번째 업데이트 사용
			bestBid = bestBidLevel.Price
			bidQty = bestBidLevel.Qty
		}
		if askUpdateCount > 0 {
			bestAskLevel := askUpdates[0] // 첫 번째 업데이트 사용
			bestAsk = bestAskLevel.Price
			askQty = bestAskLevel.Qty
		}
	}

	return sym, bestBid, bestAsk, bidQty, askQty, delBid, delAsk
}

// ✅ HFT 최적화: 인라인 정렬 (작은 배열용)
//
//go:noinline
func findBestBidFast(bids []PriceLevel) PriceLevel {
	if len(bids) == 0 {
		return PriceLevel{}
	}

	best := bids[0]
	for i := 1; i < len(bids); i++ {
		if bids[i].Price > best.Price {
			best = bids[i]
		}
	}
	return best
}

//go:noinline
func findBestAskFast(asks []PriceLevel) PriceLevel {
	if len(asks) == 0 {
		return PriceLevel{}
	}

	best := asks[0]
	for i := 1; i < len(asks); i++ {
		if asks[i].Price < best.Price {
			best = asks[i]
		}
	}
	return best
}

// ✅ 기존 함수들 (호환성 유지 - 사용하지 않음)
func findBestBid(bids []PriceLevel) PriceLevel {
	if len(bids) == 0 {
		return PriceLevel{}
	}

	sort.Slice(bids, func(i, j int) bool {
		return bids[i].Price > bids[j].Price
	})

	return bids[0]
}

func findBestAsk(asks []PriceLevel) PriceLevel {
	if len(asks) == 0 {
		return PriceLevel{}
	}

	sort.Slice(asks, func(i, j int) bool {
		return asks[i].Price < asks[j].Price
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
