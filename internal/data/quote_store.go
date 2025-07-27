package data

import "time"

// OptionQuote represents bid/ask for a symbol
type OptionQuote struct {
	Bid float64
	Ask float64
	T   time.Time
}

type quoteRequest struct {
	symbol  string
	quote   OptionQuote
	replyCh chan map[string]OptionQuote
	action  string // "set" or "snapshot"
}

type QuoteStore struct {
	requests chan quoteRequest
}

func NewQuoteStore() *QuoteStore {
	s := &QuoteStore{requests: make(chan quoteRequest, 1000)}
	go s.run()
	return s
}

func (s *QuoteStore) run() {
	quotes := make(map[string]OptionQuote)
	for req := range s.requests {
		switch req.action {
		case "set":
			quotes[req.symbol] = req.quote
		case "snapshot":
			snapshot := make(map[string]OptionQuote, len(quotes))
			for k, v := range quotes {
				snapshot[k] = v
			}
			req.replyCh <- snapshot
		}
	}
}

func (s *QuoteStore) Set(symbol string, bid, ask float64) {
	s.requests <- quoteRequest{
		symbol: symbol,
		quote:  OptionQuote{Bid: bid, Ask: ask, T: time.Now()},
		action: "set",
	}
}

func (s *QuoteStore) Snapshot() map[string]OptionQuote {
	ch := make(chan map[string]OptionQuote, 1)
	s.requests <- quoteRequest{replyCh: ch, action: "snapshot"}
	return <-ch
}
