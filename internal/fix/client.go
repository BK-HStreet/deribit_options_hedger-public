package fix

import (
	"Options_Hedger/internal/data"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"log"
	"os"
	"path/filepath"
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

// Optimized for HFT: symbol -> index mapping (O(1) lookup)
var symbolToIndex map[string]int32
var indexToSymbol [data.MaxOptions]string

// SetOptionSymbols initializes symbol-index mappings for fast lookup.
func SetOptionSymbols(symbols []string) {
	optionSymbols = symbols

	symbolToIndex = make(map[string]int32, len(symbols))
	for i, sym := range symbols {
		if i >= data.MaxOptions {
			break
		}
		symbolToIndex[sym] = int32(i)
		indexToSymbol[i] = sym
	}
}

func getSymbolIndex(symbol string) int32 {
	if idx, ok := symbolToIndex[symbol]; ok {
		return idx
	}
	return -1
}

func (App) OnCreate(id quickfix.SessionID) {}

// OnLogon: sends a MarketDataRequest for options + BTC index once logged in.
func (App) OnLogon(id quickfix.SessionID) {
	log.Println("[FIX] >>>> OnLogon received from server!")

	// Create MarketDataRequest
	mdReq := marketdatarequest.New(
		field.NewMDReqID("BTC_OPTIONS"),
		field.NewSubscriptionRequestType(enum.SubscriptionRequestType_SNAPSHOT_PLUS_UPDATES),
		field.NewMarketDepth(1),
	)
	mdReq.Set(field.NewMDUpdateType(enum.MDUpdateType_INCREMENTAL_REFRESH))
	mdReq.Set(field.NewAggregatedBook(true))

	// Add MDEntryTypes (Bid + Offer)
	mdEntryGroup := marketdatarequest.NewNoMDEntryTypesRepeatingGroup()
	bidEntry := mdEntryGroup.Add()
	bidEntry.Set(field.NewMDEntryType(enum.MDEntryType_BID))
	askEntry := mdEntryGroup.Add()
	askEntry.Set(field.NewMDEntryType(enum.MDEntryType_OFFER))
	mdReq.SetGroup(mdEntryGroup)

	// Add option symbols + BTC-USD Index symbol
	symGroup := marketdatarequest.NewNoRelatedSymRepeatingGroup()
	for _, sym := range optionSymbols {
		entry := symGroup.Add()
		entry.Set(field.NewSymbol(sym))
	}

	// Include BTC index
	idxEntry := symGroup.Add()
	idxEntry.Set(field.NewSymbol("BTC-DERIBIT-INDEX"))
	mdReq.SetGroup(symGroup)

	// Send request
	if err := quickfix.SendToTarget(mdReq, id); err != nil {
		log.Println("[FIX] MarketDataRequest send error:", err)
	} else {
		log.Println("[FIX] MarketDataRequest sent for options + BTC-USD Index")
	}
}

func (App) OnLogout(id quickfix.SessionID)                           {}
func (App) ToApp(msg *quickfix.Message, id quickfix.SessionID) error { return nil }

// ToAdmin: custom login authentication handling.
func (App) ToAdmin(msg *quickfix.Message, id quickfix.SessionID) {
	msgType, _ := msg.Header.GetString(quickfix.Tag(35))
	if msgType == "A" { // Logon
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
		// log.Println("[FIX-SEND]", msg.String())
	}
}

func (App) FromAdmin(msg *quickfix.Message, id quickfix.SessionID) quickfix.MessageRejectError {
	return nil
}

// PriceLevel: lightweight struct for bid/ask price levels (optimized for HFT).
type PriceLevel struct {
	Price float64
	Qty   float64
}

// FromApp: handles incoming market data messages.
func (app *App) FromApp(msg *quickfix.Message, id quickfix.SessionID) quickfix.MessageRejectError {
	msgType, _ := msg.Header.GetString(quickfix.Tag(35))

	var idxPrice float64
	foundIndex := false

	// Check Tag 810 (UnderlyingPx) for index price
	var idxField quickfix.FIXFloat
	if err := msg.Body.GetField(810, &idxField); err == nil {
		idxPrice = float64(idxField)
		data.SetIndexPrice(idxPrice)
		foundIndex = true
	}

	// Process Snapshot (W) or Incremental (X)
	if msgType == "W" || msgType == "X" {
		if !foundIndex {
			// Fallback: fast parse index price
			idxPrice = parseIndexPriceFast(msg)
			if idxPrice > 0 {
				data.SetIndexPrice(idxPrice)
				foundIndex = true
			}
		}

		// Parse bid/ask updates
		sym, bid, ask, bidQty, askQty, delBid, delAsk := fastParseHFT(msg, msgType)

		// Ignore index symbol
		if len(sym) > 10 && (strings.HasPrefix(sym, "BTC-DERIBIT") || strings.HasPrefix(sym, "BTC-USD")) {
			return nil
		}

		if sym != "" {
			symbolIdx := getSymbolIndex(sym)
			if symbolIdx >= 0 {
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

// parseIndexPriceFast: quickly parses index price from FIX message.
func parseIndexPriceFast(msg *quickfix.Message) float64 {
	// First check symbol
	var symField quickfix.FIXString
	if err := msg.Body.GetField(55, &symField); err != nil {
		return 0
	}

	sym := symField.String()
	if sym != "BTC-DERIBIT-INDEX" {
		return 0
	}

	// Use a lightweight group template
	group := quickfix.NewRepeatingGroup(268,
		quickfix.GroupTemplate{
			quickfix.GroupElement(279), // MDUpdateAction
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

		if mdType.String() == "3" { // Index type
			return float64(px)
		}
	}
	return 0
}

// fastParseHFT: optimized parser for FIX messages (snapshot/incremental).
func fastParseHFT(msg *quickfix.Message, msgType string) (string, float64, float64, float64, float64, bool, bool) {
	var sym string
	var bestBid, bestAsk, bidQty, askQty float64
	var delBid, delAsk bool

	// Fast extract symbol
	var symField quickfix.FIXString
	if err := msg.Body.GetField(55, &symField); err == nil {
		sym = symField.String()
	}

	// Group template selection
	var group *quickfix.RepeatingGroup
	switch msgType {
	case "W": // Snapshot
		group = quickfix.NewRepeatingGroup(268,
			quickfix.GroupTemplate{
				quickfix.GroupElement(269),
				quickfix.GroupElement(270),
				quickfix.GroupElement(271),
			})
	case "X": // Incremental
		group = quickfix.NewRepeatingGroup(268,
			quickfix.GroupTemplate{
				quickfix.GroupElement(279),
				quickfix.GroupElement(269),
				quickfix.GroupElement(270),
				quickfix.GroupElement(271),
			})
	default:
		return sym, bestBid, bestAsk, bidQty, askQty, delBid, delAsk
	}

	if err := msg.Body.GetGroup(group); err != nil {
		return sym, bestBid, bestAsk, bidQty, askQty, delBid, delAsk
	}

	switch msgType {
	case "W": // Snapshot: collect levels, find best bid/ask
		var bids [16]PriceLevel
		var asks [16]PriceLevel
		var bidCount, askCount int

		for i := 0; i < group.Len() && i < 32; i++ {
			entry := group.Get(i)

			var mdType quickfix.FIXString
			var price quickfix.FIXFloat
			var size quickfix.FIXFloat

			if entry.GetField(269, &mdType) != nil ||
				entry.GetField(270, &price) != nil ||
				entry.GetField(271, &size) != nil {
				continue
			}

			if mdType.String() == "3" {
				continue
			}

			p := float64(price)
			q := float64(size)

			if p > 0 && q > 0 {
				switch mdType.String() {
				case "0": // Bid
					if bidCount < 16 {
						bids[bidCount] = PriceLevel{Price: p, Qty: q}
						bidCount++
					}
				case "1": // Ask
					if askCount < 16 {
						asks[askCount] = PriceLevel{Price: p, Qty: q}
						askCount++
					}
				}
			}
		}

		if bidCount > 0 {
			bestIdx := 0
			for i := 1; i < bidCount; i++ {
				if bids[i].Price > bids[bestIdx].Price {
					bestIdx = i
				}
			}
			bestBid = bids[bestIdx].Price
			bidQty = bids[bestIdx].Qty
		}

		if askCount > 0 {
			bestIdx := 0
			for i := 1; i < askCount; i++ {
				if asks[i].Price < asks[bestIdx].Price {
					bestIdx = i
				}
			}
			bestAsk = asks[bestIdx].Price
			askQty = asks[bestIdx].Qty
		}

	case "X": // Incremental: process updates directly
		for i := 0; i < group.Len() && i < 8; i++ {
			entry := group.Get(i)

			var mdType quickfix.FIXString
			var price quickfix.FIXFloat
			var size quickfix.FIXFloat
			var action quickfix.FIXString

			if entry.GetField(269, &mdType) != nil ||
				entry.GetField(270, &price) != nil ||
				entry.GetField(271, &size) != nil {
				continue
			}

			if mdType.String() == "3" {
				continue
			}

			entry.GetField(279, &action)

			p := float64(price)
			q := float64(size)

			// Handle delete action
			if action.String() == "2" {
				q = 0
				switch mdType.String() {
				case "0":
					delBid = true
				case "1":
					delAsk = true
				}
			}

			// Only keep first update (usually top of book)
			switch mdType.String() {
			case "0": // Bid
				if bestBid == 0 {
					bestBid = p
					bidQty = q
				}
			case "1": // Ask
				if bestAsk == 0 {
					bestAsk = p
					askQty = q
				}
			}
		}
	}

	return sym, bestBid, bestAsk, bidQty, askQty, delBid, delAsk
}

var initiator *quickfix.Initiator

// InitFIXEngine starts the FIX initiator with the given configuration file.
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

// StopFIXEngine stops the FIX initiator.
func StopFIXEngine() {
	if initiator != nil {
		initiator.Stop()
	}
}
