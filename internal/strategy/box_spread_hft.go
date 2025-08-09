package strategy

import (
	"Options_Hedger/internal/data"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

// ✅ 메모리 효율적인 옵션 정보 (32바이트)
type OptionInfo struct {
	Strike float64  // 8
	Expiry uint16   // 2 (인덱스화)
	Index  int16    // 2
	IsCall bool     // 1
	_      [19]byte // 패딩
}

// ✅ 시그널 구조체 최적화
type BoxSignal struct {
	LowCallIdx   int16
	LowPutIdx    int16
	HighCallIdx  int16
	HighPutIdx   int16
	LowStrike    float64
	HighStrike   float64
	Profit       float64
	UpdateTimeNs int64
}

type BoxSpreadHFT struct {
	updates chan data.Update
	signals chan BoxSignal

	// ✅ 캐시 최적화된 데이터 구조
	options     [data.MaxOptions]OptionInfo
	optionCount int32

	// ✅ 빠른 룩업 테이블 (컴파일 타임 최적화)
	expiryMap  [16]uint16                             // 만료일 -> 인덱스
	strikeMap  [data.MaxOptions]float64               // 인덱스 -> 행사가
	pairLookup [data.MaxOptions][data.MaxOptions]bool // 매치 가능한 페어

	// ✅ 중복 제거 (비트마스크)
	recentSignals uint64 // 최근 64개 신호 비트마스크
	lastCheck     int64  // 마지막 체크 시간
}

func NewBoxSpreadHFT(ch chan data.Update) *BoxSpreadHFT {
	return &BoxSpreadHFT{
		updates: ch,
		signals: make(chan BoxSignal, 128), // 버퍼 크기 줄임
	}
}

//go:noinline
func (e *BoxSpreadHFT) InitializeHFT(symbols []string) {
	count := len(symbols)
	if count > data.MaxOptions {
		count = data.MaxOptions
	}

	expiryIndex := make(map[string]uint16)
	var expiryCounter uint16

	for i := 0; i < count; i++ {
		parts := strings.Split(symbols[i], "-")
		if len(parts) != 4 {
			continue
		}

		strike, err := strconv.ParseFloat(parts[2], 64)
		if err != nil {
			continue
		}

		expiry := parts[1]
		if _, exists := expiryIndex[expiry]; !exists {
			expiryIndex[expiry] = expiryCounter
			e.expiryMap[expiryCounter] = expiryCounter
			expiryCounter++
		}

		e.options[i] = OptionInfo{
			Strike: strike,
			Expiry: expiryIndex[expiry],
			Index:  int16(i),
			IsCall: parts[3] == "C",
		}
		e.strikeMap[i] = strike
	}

	e.optionCount = int32(count)
	e.buildPairLookup()
}

//go:noinline
func (e *BoxSpreadHFT) buildPairLookup() {
	count := int(e.optionCount)
	for i := 0; i < count; i++ {
		for j := i + 1; j < count; j++ {
			opt1 := &e.options[i]
			opt2 := &e.options[j]

			// 같은 만료일이고 다른 행사가인 경우만 매치
			if opt1.Expiry == opt2.Expiry && opt1.Strike != opt2.Strike {
				e.pairLookup[i][j] = true
				e.pairLookup[j][i] = true
			}
		}
	}
}

//go:noinline
func (e *BoxSpreadHFT) Run() {
	for update := range e.updates {
		e.processUpdateHFT(update)
	}
	// var update data.Update

	// for {
	// 	select {
	// 	case update = <-e.updates:
	// 		e.processUpdateHFT(update)
	// 	default:
	// 		// 논블로킹으로 계속 처리
	// 	}
	// }
}

//go:noinline
func (e *BoxSpreadHFT) processUpdateHFT(update data.Update) {
	idx := int(update.SymbolIdx)
	if idx >= int(e.optionCount) {
		return
	}

	// updatedOpt := &e.options[idx]
	currentTime := update.UpdateTime

	// ✅ 중복 체크 최적화 (10μs 이내 무시)
	if currentTime-e.lastCheck < 10000 { // 10μs
		return
	}
	e.lastCheck = currentTime

	// ✅ 페어 룩업 테이블 사용으로 O(1) 검색
	count := int(e.optionCount)
	for i := range count {
		if !e.pairLookup[idx][i] {
			continue
		}

		e.checkBoxFast(idx, i, update.IndexPrice)
	}
}

//go:noinline
func (e *BoxSpreadHFT) checkBoxFast(idx1, idx2 int, indexPrice float64) {
	opt1 := &e.options[idx1]
	opt2 := &e.options[idx2]

	lowStrike := math.Min(opt1.Strike, opt2.Strike)
	highStrike := math.Max(opt1.Strike, opt2.Strike)

	if highStrike-lowStrike < 1000 { // 최소 $1000 차이
		return
	}

	// ✅ 해시 기반 중복 제거 (비트 연산)
	hash := uint64(lowStrike)*1000 + uint64(highStrike)
	bit := hash % 64
	if (e.recentSignals>>bit)&1 == 1 {
		return
	}
	atomic.OrUint64(&e.recentSignals, 1<<bit)

	// ✅ 4개 옵션 빠른 검색
	var lowCallIdx, lowPutIdx, highCallIdx, highPutIdx int16 = -1, -1, -1, -1

	count := int(e.optionCount)
	for i := 0; i < count; i++ {
		opt := &e.options[i]
		if opt.Expiry != opt1.Expiry {
			continue
		}

		if opt.Strike == lowStrike {
			if opt.IsCall && lowCallIdx == -1 {
				lowCallIdx = int16(i)
			} else if !opt.IsCall && lowPutIdx == -1 {
				lowPutIdx = int16(i)
			}
		} else if opt.Strike == highStrike {
			if opt.IsCall && highCallIdx == -1 {
				highCallIdx = int16(i)
			} else if !opt.IsCall && highPutIdx == -1 {
				highPutIdx = int16(i)
			}
		}

		// 조기 종료
		if lowCallIdx != -1 && lowPutIdx != -1 && highCallIdx != -1 && highPutIdx != -1 {
			break
		}
	}

	if lowCallIdx == -1 || lowPutIdx == -1 || highCallIdx == -1 || highPutIdx == -1 {
		return
	}

	// ✅ 인라인 수익성 계산
	lowCall := data.ReadDepthFast(int(lowCallIdx))
	lowPut := data.ReadDepthFast(int(lowPutIdx))
	highCall := data.ReadDepthFast(int(highCallIdx))
	highPut := data.ReadDepthFast(int(highPutIdx))

	// 가격 유효성 체크
	if lowCall.AskPrice <= 0 || lowPut.BidPrice <= 0 ||
		highCall.BidPrice <= 0 || highPut.AskPrice <= 0 || indexPrice <= 0 {
		return
	}

	netCost := (lowCall.AskPrice + highPut.AskPrice - lowPut.BidPrice - highCall.BidPrice) * indexPrice
	expectedValue := highStrike - lowStrike
	profit := expectedValue - netCost

	if profit > 1.0 { // $5 최소 수익

		// benkim..복원필
		log.Printf(
			"[BOX-SPREAD] strikes=%.0f→%.0f index=%.2f  "+
				"buyCallLo=ask@%.4f(qty=%.2f)  sellCallHi=bid@%.4f(qty=%.2f)  "+
				"sellPutLo=bid@%.4f(qty=%.2f)  buyPutHi=ask@%.4f(qty=%.2f)  profit=%.2f",
			lowStrike, highStrike, indexPrice,
			lowCall.AskPrice, lowCall.AskQty,
			highCall.BidPrice, highCall.BidQty,
			lowPut.BidPrice, lowPut.BidQty,
			highPut.AskPrice, highPut.AskQty,
			profit,
		)
		// benkim..end

		fmt.Print("\a") // 소리
		// 안전하게 종료
		os.Exit(0)

		signal := BoxSignal{
			LowCallIdx:   lowCallIdx,
			LowPutIdx:    lowPutIdx,
			HighCallIdx:  highCallIdx,
			HighPutIdx:   highPutIdx,
			LowStrike:    lowStrike,
			HighStrike:   highStrike,
			Profit:       profit,
			UpdateTimeNs: data.Nanotime(),
		}

		// 논블로킹 전송
		select {
		case e.signals <- signal:
		default:
		}
	}
}

func (e *BoxSpreadHFT) Signals() <-chan BoxSignal {
	return e.signals
}

// ✅ 주기적으로 비트마스크 클리어 (별도 고루틴에서)
func (e *BoxSpreadHFT) ResetSignalMask() {
	atomic.StoreUint64(&e.recentSignals, 0)
}
