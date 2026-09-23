package benchtrace

import (
	amf_context "github.com/free5gc/amf/internal/context"
	"strconv"
)

// RecordForRanUE binds a protocol action to its NGAP and privacy-safe UE identifiers.
func RecordForRanUE(event string, ue *amf_context.RanUe) {
	if !Enabled() || ue == nil {
		return
	}
	gnbID, connID, supi := "", "", ""
	if ue.Ran != nil {
		connID = ConnectionID(ue.Ran.Conn)
		if ue.Ran.RanId != nil && ue.Ran.RanId.GNbId != nil {
			gnbID = ue.Ran.RanId.GNbId.GNBValue
		}
	}
	if ue.AmfUe != nil {
		supi = ue.AmfUe.Supi
	}
	Record(event, connID, gnbID, strconv.FormatInt(ue.RanUeNgapId, 10), strconv.FormatInt(ue.AmfUeNgapId, 10), HashSUPI(supi), "", 0)
}
