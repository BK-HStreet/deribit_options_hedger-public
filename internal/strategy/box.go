package strategy

import "OptionsHedger/internal/model"

// Dummy logic – replace with actual box spread opportunity detection
func IsBoxOpportunity(d *model.Depth) bool {
	spread := d.Ask - d.Bid
	return spread < 5 // example condition
}
