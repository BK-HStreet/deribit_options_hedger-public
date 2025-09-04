// File: internal/app/universe.go
package app

import (
	"Options_Hedger/internal/data"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
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

// BuildUniverse: 오늘~HEDGE_EM_MAX_DAYS(기본 7일) 이내의 만기 중
// 가장 가까운 만기(near)와 가장 먼 만기(far)를 동시에 포함해서
// 심볼 리스트를 구성한다.
// 반환: Universe, nearLabel, farLabel
func BuildUniverse() (Universe, string, string) {
	S := fetchBTCPrice()
	data.SetIndexPrice(S)

	instruments := fetchInstruments()
	// 기간: 기본 7일, ENV로 조정(HEDGE_EM_MAX_DAYS)
	maxDays := 7
	if v := strings.TrimSpace(os.Getenv("HEDGE_EM_MAX_DAYS")); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			maxDays = x
		}
	}

	nearLabel, nearUTC, farLabel, farUTC := findNearAndFarWithinDays(instruments, maxDays)

	// per-expiry cap: 총 40을 near/far 1:1로 배분(20/20).
	perCap := data.MaxOptions / 2
	nearSyms := filterOptionsByTSCap(instruments, nearUTC, S, perCap)
	farSyms := filterOptionsByTSCap(instruments, farUTC, S, perCap)

	// near==far 인 경우 중복 제거
	merged := make([]string, 0, data.MaxOptions)
	seen := make(map[string]struct{}, data.MaxOptions)
	for _, s := range append(nearSyms, farSyms...) {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		merged = append(merged, s)
		if len(merged) >= data.MaxOptions {
			break
		}
	}

	log.Printf("[INFO] Nearest expiry: %s (UTC %s)", nearLabel, nearUTC.Format(time.RFC3339))
	log.Printf("[INFO] Farthest expiry within %dd: %s (UTC %s)", maxDays, farLabel, farUTC.Format(time.RFC3339))
	log.Printf("[INFO] Filtered %d options (near %d, far %d) within 20%% of ATM", len(merged), len(nearSyms), len(farSyms))

	return Universe{Symbols: merged}, nearLabel, farLabel
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

// findNearAndFarWithinDays: 오늘 이후 now+maxDays 이내의 만기 중
// 가장 가까운(near) & 가장 먼(far) 만기를 찾는다.
func findNearAndFarWithinDays(instruments []Instrument, maxDays int) (nearLabel string, nearUTC time.Time, farLabel string, farUTC time.Time) {
	nowUTC := time.Now().UTC()
	limit := nowUTC.Add(time.Duration(maxDays) * 24 * time.Hour)

	type exp struct {
		label string
		t     time.Time
	}
	seen := make(map[string]bool)
	var exps []exp

	for _, inst := range instruments {
		t := time.UnixMilli(inst.ExpireMs).UTC()
		if !t.After(nowUTC) || t.After(limit) {
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
		exps = append(exps, exp{label: lbl, t: t})
	}
	if len(exps) == 0 {
		log.Fatal("[EXPIRY] no future expiries found within window")
	}
	sort.Slice(exps, func(i, j int) bool { return exps[i].t.Before(exps[j].t) })
	nearLabel, nearUTC = exps[0].label, exps[0].t
	farLabel, farUTC = exps[len(exps)-1].label, exps[len(exps)-1].t
	return
}

// 특정 만기의 옵션을 ATM±20% 내에서 cap 개수(콜/풋 반반)만 선별
func filterOptionsByTSCap(instruments []Instrument, expiryUTC time.Time, atmPrice float64, cap int) []string {
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

	if cap <= 0 {
		cap = data.MaxOptions
	}
	callCap, putCap := cap/2, cap/2
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
		if len(balanced) >= cap {
			break
		}
	}
	out := make([]string, len(balanced))
	for i, o := range balanced {
		out[i] = o.name
	}
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
