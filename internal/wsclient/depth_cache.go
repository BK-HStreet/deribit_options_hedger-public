package wsclient

import "OptionsHedger/internal/model"

var depthCache = make(map[string]model.Depth)

func GetDepthCache(instr string) *model.Depth {
	if d, ok := depthCache[instr]; ok {
		return &d
	}
	return nil
}

func UpdateDepthCache(d model.Depth) {
	depthCache[d.Instrument] = d
}
