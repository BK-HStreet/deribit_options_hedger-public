// File: internal/app/universe.go
package app

import (
	"Options_Hedger/internal/data"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Instrument struct {
	Name     string `json:"instrument_name"`
	IsActive bool   `json:"is_active"`
	ExpireMs int64  `json:"expiration_timestamp"`
}

type Universe struct {
	Symbols []string
}

func BuildUniverse() (Universe, string) {
	S := fetchBTCPrice()
	data.SetIndexPrice(S)

	instruments := fetchInstruments()
	label, expiryUTC := findNearestExpiryUTC(instruments)
	symbols := filterOptionsByTS(instruments, expiryUTC, S)
	if len(symbols) > data.MaxOptions {
		symbols = symbols[:data.MaxOptions]
	}
	return Universe{Symbols: symbols}, label
}

func fetchBTCPrice() float64 {
	res, err := http.Get("https://www.deribit.com/api/v2/public/get_index_price?index_name=btc_usd")
	if err != nil {
		log.Printf("[PRICE] fetch failed, using default: %v", err)
		return 65000.0
	}
	defer res.Body.Close()
	var r struct {
		Result struct {
			IndexPrice float64 `json:"index_price"`
		} `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		log.Printf("[PRICE] decode failed, using default: %v", err)
		return 65000.0
	}
	return r.Result.IndexPrice
}

func fetchInstruments() []Instrument {
	res, err := http.Get("https://www.deribit.com/api/v2/public/get_instruments?currency=BTC&kind=option")
	if err != nil {
		log.Fatal("[INSTR] fetch failed:", err)
	}
	defer res.Body.Close()

	var r struct {
		Result []Instrument `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		log.Fatal("[INSTR] decode failed:", err)
	}
	out := make([]Instrument, 0, len(r.Result))
	for _, inst := range r.Result {
		if inst.IsActive {
			out = append(out, inst)
		}
	}
	log.Printf("[INFO] Fetched %d active instruments", len(out))
	return out
}

func findNearestExpiryUTC(instruments []Instrument) (label string, expiryUTC time.Time) {
	nowUTC := time.Now().UTC()
	var best time.Time
	seen := make(map[string]bool)

	for _, inst := range instruments {
		t := time.UnixMilli(inst.ExpireMs).UTC()
		if !t.After(nowUTC) {
			continue
		}
		parts := strings.Split(inst.Name, "-")
		if len(parts) < 3 {
			continue
		}
		lbl := parts[1]
		if seen[lbl] {
			continue
		}
		seen[lbl] = true
		if best.IsZero() || t.Before(best) {
			best, label = t, lbl
		}
	}
	if best.IsZero() {
		log.Fatal("[EXPIRY] no future expiries found (by timestamp)")
	}
	kst := time.FixedZone("KST", 9*3600)
	log.Printf("[INFO] Nearest expiry: %s (UTC %s / KST %s)",
		label, best.Format(time.RFC3339), best.In(kst).Format("2006-01-02 15:04:05"))
	return label, best
}

func filterOptionsByTS(instruments []Instrument, expiryUTC time.Time, atmPrice float64) []string {
	type opt struct {
		name     string
		strike   float64
		distance float64
		isCall   bool
	}
	var list []opt
	for _, inst := range instruments {
		if !inst.IsActive {
			continue
		}
		t := time.UnixMilli(inst.ExpireMs).UTC()
		if !t.Equal(expiryUTC) {
			continue
		}
		parts := strings.Split(inst.Name, "-")
		if len(parts) < 4 {
			continue
		}
		var s float64
		if _, err := fmtSscanf(parts[2], &s); err != nil {
			continue
		}
		distance := math.Abs(s - atmPrice)
		if distance > atmPrice*0.2 {
			continue
		}
		isCall := parts[3] == "C"
		list = append(list, opt{inst.Name, s, distance, isCall})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].distance < list[j].distance })

	callCap, putCap := data.MaxOptions/2, data.MaxOptions/2
	callCount, putCount := 0, 0
	balanced := make([]opt, 0, len(list))
	for _, o := range list {
		if o.isCall && callCount < callCap {
			balanced = append(balanced, o)
			callCount++
		} else if !o.isCall && putCount < putCap {
			balanced = append(balanced, o)
			putCount++
		}
		if len(balanced) >= data.MaxOptions {
			break
		}
	}
	out := make([]string, len(balanced))
	for i, o := range balanced {
		out[i] = o.name
	}
	log.Printf("[INFO] Filtered to %d options (%d calls, %d puts) within 20%% of ATM",
		len(out), callCount, putCount)
	return out
}

// tiny scanf without fmt import in this file (alloc-free)
func fmtSscanf(s string, out *float64) (int, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	*out = v
	return 1, nil
}
