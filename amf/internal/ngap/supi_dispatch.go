package ngap

import (
	"strings"
	"sync"

	amf_context "github.com/free5gc/amf/internal/context"
	"github.com/free5gc/amf/internal/logger"
	"github.com/free5gc/nas"
	"github.com/free5gc/nas/nasMessage"
	nasConvert "github.com/free5gc/nas/nasConvert"
	"github.com/free5gc/ngap"
	"github.com/free5gc/ngap/ngapType"
)

// The paper's dispatch policy (Nha & Nakao, GC Wkshps 2025), with its
// IMSI-parity prioritisation removed so that only the mechanism differs from
// upstream:
//
//	upstream: key = NGAP UE ID, known as soon as the NGAP PDU is decoded.
//	          The key changes from RAN-UE-NGAP-ID to AMF-UE-NGAP-ID partway
//	          through registration, so a UE can move between workers.
//	paper:    key = the subscriber identity carried in the NAS Registration
//	          Request, which costs an extra NAS decode on the reader goroutine
//	          but never changes for the lifetime of the UE.
//
// Everything downstream - the worker pool, the queues, the shutdown drain - is
// shared, so a measured difference between the two arms is attributable to the
// dispatch decision and nothing else.

// supiKeyCache remembers the subscriber-derived key for both NGAP identifiers,
// so later messages (which carry only AMF-UE-NGAP-ID) reach the same worker
// without re-decoding NAS.
var (
	supiKeyByRanUeID sync.Map // int64 -> uint64
	supiKeyByAmfUeID sync.Map // int64 -> uint64
)

// ResetSupiKeyCache drops all remembered keys. Test helper.
func ResetSupiKeyCache() {
	supiKeyByRanUeID = sync.Map{}
	supiKeyByAmfUeID = sync.Map{}
}

// SupiDispatchKey computes the dispatch key for one raw NGAP message under the
// supi policy. fallback reports that the subscriber identity could not be
// established and the NGAP UE ID was used instead; the benchmark must report
// the fallback rate, because a high one would mean the two arms were not
// actually running different policies.
func SupiDispatchKey(msg []byte) (key uint64, procedureCode int64, found bool, fallback bool) {
	pdu, err := ngap.Decoder(msg)
	if err != nil || pdu == nil {
		return 0, -1, false, false
	}

	switch pdu.Present {
	case ngapType.NGAPPDUPresentInitiatingMessage:
		if pdu.InitiatingMessage == nil {
			return 0, -1, false, false
		}
		procedureCode = pdu.InitiatingMessage.ProcedureCode.Value
		if procedureCode == ngapType.ProcedureCodeInitialUEMessage {
			return keyFromInitialUEMessage(pdu.InitiatingMessage)
		}
	case ngapType.NGAPPDUPresentSuccessfulOutcome:
		if pdu.SuccessfulOutcome != nil {
			procedureCode = pdu.SuccessfulOutcome.ProcedureCode.Value
		}
	case ngapType.NGAPPDUPresentUnsuccessfulOutcome:
		if pdu.UnsuccessfulOutcome != nil {
			procedureCode = pdu.UnsuccessfulOutcome.ProcedureCode.Value
		}
	default:
		return 0, -1, false, false
	}

	// Not an InitialUEMessage: the subscriber identity is not in this message,
	// so recover it from what the first one taught us. Reuse the PDU decoded
	// above - decoding again would charge this policy for work it does not do.
	ueID, _, ok := ExtractUEIDFromPDU(pdu)
	if !ok {
		return 0, procedureCode, false, false
	}
	if k, hit := lookupSupiKey(int64(ueID)); hit {
		return k, procedureCode, true, false
	}

	logger.NgapLog.Tracef("supi dispatch: no subscriber key for UE ID %d, falling back to hash", ueID)
	return ueID, procedureCode, true, true
}

// keyFromInitialUEMessage decodes the NAS Registration Request far enough to
// read the subscriber identity, and remembers it under the RAN-UE-NGAP-ID.
func keyFromInitialUEMessage(msg *ngapType.InitiatingMessage) (uint64, int64, bool, bool) {
	const pc = ngapType.ProcedureCodeInitialUEMessage

	if msg.Value.InitialUEMessage == nil {
		return 0, pc, false, false
	}

	var ranUeNgapID int64 = -1
	var nasPDU []byte
	for _, ie := range msg.Value.InitialUEMessage.ProtocolIEs.List {
		switch ie.Id.Value {
		case ngapType.ProtocolIEIDRANUENGAPID:
			if ie.Value.RANUENGAPID != nil {
				ranUeNgapID = ie.Value.RANUENGAPID.Value
			}
		case ngapType.ProtocolIEIDNASPDU:
			if ie.Value.NASPDU != nil {
				nasPDU = ie.Value.NASPDU.Value
			}
		}
	}

	if ranUeNgapID < 0 {
		return 0, pc, false, false
	}

	key, ok := subscriberKeyFromNAS(nasPDU)
	if !ok {
		// A re-registration by 5G-GUTI carries no SUCI. Upstream's key is all
		// we have, so use it and count the fallback.
		supiKeyByRanUeID.Store(ranUeNgapID, uint64(ranUeNgapID))
		return uint64(ranUeNgapID), pc, true, true
	}

	supiKeyByRanUeID.Store(ranUeNgapID, key)
	return key, pc, true, false
}

// subscriberKeyFromNAS pulls the MSIN out of the SUCI in a Registration
// Request. The initial Registration Request is not integrity protected, so it
// decodes as plain NAS.
func subscriberKeyFromNAS(payload []byte) (uint64, bool) {
	if len(payload) == 0 {
		return 0, false
	}

	m := nas.NewMessage()
	buf := make([]byte, len(payload))
	copy(buf, payload)
	if err := m.PlainNasDecode(&buf); err != nil {
		return 0, false
	}
	if m.GmmMessage == nil || m.GmmMessage.RegistrationRequest == nil {
		return 0, false
	}

	contents := m.GmmMessage.RegistrationRequest.MobileIdentity5GS.GetMobileIdentity5GSContents()
	if len(contents) == 0 {
		return 0, false
	}
	if contents[0]&0x07 != nasMessage.MobileIdentity5GSTypeSuci {
		// 5G-GUTI or IMEI: no subscriber identity available this early.
		return 0, false
	}

	suci, _, err := nasConvert.SuciToStringWithError(contents)
	if err != nil {
		return 0, false
	}
	return msinKey(suci)
}

// msinKey turns "suci-0-208-93-0000-0-0-0000000001" into 1.
//
// Only a null-scheme SUCI (protection scheme 0) exposes the MSIN; with a
// profile A/B SUCI the scheme output is ciphertext and the paper's
// identity-based dispatch is not possible before authentication completes.
func msinKey(suci string) (uint64, bool) {
	parts := strings.Split(suci, "-")
	if len(parts) < 8 || parts[0] != "suci" {
		return 0, false
	}
	if parts[5] != "0" { // protection scheme id
		return 0, false
	}

	msin := parts[len(parts)-1]
	var key uint64
	for _, c := range msin {
		if c < '0' || c > '9' {
			return 0, false
		}
		key = key*10 + uint64(c-'0')
	}
	return key, true
}

// lookupSupiKey resolves an AMF-UE-NGAP-ID to the key learned at registration,
// linking the two identifiers through the AMF context when needed.
func lookupSupiKey(amfUeNgapID int64) (uint64, bool) {
	if k, ok := supiKeyByAmfUeID.Load(amfUeNgapID); ok {
		return k.(uint64), true
	}

	// First message under the new identifier: bridge from RAN-UE-NGAP-ID.
	ranUe := amf_context.GetSelf().RanUeFindByAmfUeNgapID(amfUeNgapID)
	if ranUe == nil {
		return 0, false
	}
	k, ok := supiKeyByRanUeID.Load(ranUe.RanUeNgapId)
	if !ok {
		return 0, false
	}

	supiKeyByAmfUeID.Store(amfUeNgapID, k)
	return k.(uint64), true
}
