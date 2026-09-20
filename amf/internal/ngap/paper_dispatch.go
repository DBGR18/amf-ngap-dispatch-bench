package ngap

import (
	"net"
	"strings"
	"sync"

	amf_context "github.com/free5gc/amf/internal/context"
	"github.com/free5gc/amf/internal/logger"
	"github.com/free5gc/nas"
	nasConvert "github.com/free5gc/nas/nasConvert"
	"github.com/free5gc/nas/nasMessage"
	"github.com/free5gc/ngap"
	"github.com/free5gc/ngap/ngapType"
)

// Subscriber-identity dispatch, shared by the two paper-derived modes.
//
// The paper (Nha & Nakao, GC Wkshps 2025) classifies each UE by an IMSI-derived
// attribute at the earliest point the identity is available, and routes the UE
// to a thread on that basis; its IMSI-parity prioritisation is deliberately not
// implemented here, so that only the dispatch mechanism differs between arms.
//
// The two modes obtain the identity differently, and the difference is forced
// by where each of them decides:
//
//	paper-early  decides on the SCTP reader goroutine, before the handler runs,
//	             while a worker may be inside the previous message of the same
//	             UE. AmfUe.Suci and AmfUe.Supi are written from there
//	             (internal/gmm/handler.go:477, :1563, :2035), so reading them
//	             here is a data race - confirmed with -race against a live
//	             registration load. The key learned at registration is therefore
//	             cached below instead, which is race-free by construction.
//	paper        decides inside the NGAP handler, holding the *RanUe the handler
//	             has already resolved, and reads AmfUe directly. That read is
//	             subject to the same race, but paper mode's hand-off point
//	             already puts a UE's NGAP half and NAS half on two goroutines,
//	             so this adds no exposure the mode does not already have. It is
//	             recorded in the README under "Known consequences".
//
// Everything downstream - the worker pool, the queues, the shutdown drain - is
// shared, so a measured difference between arms is attributable to the dispatch
// decision and nothing else.

// paperRanKey scopes a RAN-UE-NGAP-ID to the gNB that issued it.
//
// A RAN-UE-NGAP-ID is only unique within one NG connection - free5gc keeps
// them in a per-gNB AmfRan.RanUeList, while AMF-UE-NGAP-IDs live in the
// AMF-wide RanUePool. Keying on the bare int64 therefore works with a single
// gNB and silently breaks with two: the later gNB's entry overwrites the
// earlier one, and the first gNB's UE then resolves to the other UE's key,
// scattering its messages across workers - exactly what this policy exists to
// prevent. free5gc itself keys AmfRanPool by net.Conn, so a connection is a
// safe and cheap stand-in for the gNB's identity here.
type paperRanKey struct {
	conn net.Conn
	id   int64
}

// The key learned at registration, remembered under both NGAP identifiers so
// later messages (which carry only the AMF-UE-NGAP-ID) reach the same worker
// without re-decoding NAS.
//
// Caching it also pins it: a UE whose identity could not be read keeps the
// fallback key it was first given, instead of switching to an IMSI key once
// the AMF learns the identity from an Identity Response and moving worker
// mid-registration.
var (
	paperKeyByRanUeID sync.Map // paperRanKey -> uint64
	paperKeyByAmfUeID sync.Map // int64 -> uint64
)

// ResetPaperKeyCache drops all remembered keys. Test helper.
func ResetPaperKeyCache() {
	paperKeyByRanUeID = sync.Map{}
	paperKeyByAmfUeID = sync.Map{}
}

// PaperEarlyDispatchKey computes the dispatch key for one raw NGAP message
// under the paper-early policy.
//
// fallback reports that the subscriber identity could not be established and
// the NGAP UE ID was used instead. The benchmark must report the fallback rate:
// a high one would mean the arms were not actually running different policies.
func PaperEarlyDispatchKey(conn net.Conn, msg []byte) (key uint64, procedureCode int64, found bool, fallback bool) {
	pdu, err := ngap.Decoder(msg)
	if err != nil || pdu == nil {
		return 0, -1, false, false
	}

	// A UE's first message is the only one that carries its identity. It is
	// also the only one for which no UE context exists yet, so the identity
	// has to come out of the NAS payload - which is the one NAS decode the
	// paper's classification step needs, and the only one this mode does.
	if pdu.Present == ngapType.NGAPPDUPresentInitiatingMessage &&
		pdu.InitiatingMessage != nil &&
		pdu.InitiatingMessage.ProcedureCode.Value == ngapType.ProcedureCodeInitialUEMessage {
		return keyFromInitialUEMessage(conn, pdu.InitiatingMessage)
	}

	// Not an InitialUEMessage: the subscriber identity is not in this message,
	// so recover it from what the first one taught us. Reuse the PDU decoded
	// above - decoding again would charge this policy for work it does not do.
	ueID, procedureCode, ok := ExtractUEIDFromPDU(pdu)
	if !ok {
		return 0, procedureCode, false, false
	}
	if k, hit := lookupPaperKey(int64(ueID)); hit {
		return k, procedureCode, true, false
	}

	logger.NgapLog.Tracef("paper-early dispatch: no subscriber key for UE ID %d, falling back to the blog key", ueID)
	return ueID, procedureCode, true, true
}

// lookupPaperKey resolves an AMF-UE-NGAP-ID to the key learned at registration,
// linking the two identifiers through the AMF context when needed. Only fields
// fixed when the RanUe was created are read, never AmfUe, so this does not race
// with the worker that owns the UE.
func lookupPaperKey(amfUeNgapID int64) (uint64, bool) {
	if k, ok := paperKeyByAmfUeID.Load(amfUeNgapID); ok {
		return k.(uint64), true
	}

	// First message under the new identifier: bridge from RAN-UE-NGAP-ID.
	ranUe := amf_context.GetSelf().RanUeFindByAmfUeNgapID(amfUeNgapID)
	if ranUe == nil || ranUe.Ran == nil {
		return 0, false
	}
	// ranUe.Ran.Conn is the connection the registration arrived on. A handover
	// (RanUe.SwitchToRan) replaces both of these, so a UE handed over before
	// its first post-registration message misses here and falls back; from
	// then on the AMF-UE-NGAP-ID cache answers and handovers no longer matter.
	k, ok := paperKeyByRanUeID.Load(paperRanKey{ranUe.Ran.Conn, ranUe.RanUeNgapId})
	if !ok {
		return 0, false
	}

	paperKeyByAmfUeID.Store(amfUeNgapID, k)
	return k.(uint64), true
}

// SubscriberKeyFromRanUe reads the UE's IMSI key out of the AMF context hanging
// off a RanUe. HoldingAmfUe covers the gap between finding an existing AmfUe in
// handleInitialUEMessage and attaching it in HandleNAS.
//
// The RanUe itself comes from RanUePool, which AmfRan.NewRanUe populates the
// moment the RanUe is created; the AmfUe is attached slightly later, inside
// HandleNAS. The gNB cannot use the AMF-UE-NGAP-ID before the AMF's first
// downlink message, which is sent from within that same call, so that window is
// closed from the RAN's side.
func SubscriberKeyFromRanUe(ranUe *amf_context.RanUe) (uint64, bool) {
	if ranUe == nil {
		return 0, false
	}
	if k, ok := subscriberKeyFromAmfUe(ranUe.AmfUe); ok {
		return k, true
	}
	return subscriberKeyFromAmfUe(ranUe.HoldingAmfUe)
}

// subscriberKeyFromAmfUe reads the UE's key from the SUCI, and only consults
// the SUPI when there is no SUCI at all - even though the SUPI is the
// authenticated identity and the SUCI is not.
//
// The reason is key stability, not trust. AmfUe.Supi stays empty until AUSF
// confirms authentication (gmm/handler.go), so during registration - the phase
// this whole benchmark is about - only the SUCI is set.
//
//   - Null scheme: the SUCI carries the MSIN in the clear, so both fields yield
//     the same digits and the key is identical before and after authentication.
//   - Protection scheme A/B: the SUCI conceals the MSIN and nobody in the AMF
//     holds the IMSI until AUSF/UDM de-conceals it. Falling through to the SUPI
//     here would give such a UE no key before authentication and an IMSI key
//     after it - moving it to a different worker halfway through its own
//     registration, with messages possibly still in flight on the old one.
//     Refusing the SUPI keeps one UE on one worker for the whole run. The arm
//     is then not measuring identity-based dispatch at all, which is what the
//     fallback column in the trace is there to make obvious.
//
// A UE with no SUCI but a known SUPI - a context transferred from another AMF -
// is keyed on the SUPI, since there is no concealment question to answer.
func subscriberKeyFromAmfUe(amfUe *amf_context.AmfUe) (uint64, bool) {
	if amfUe == nil {
		return 0, false
	}
	if amfUe.Suci != "" {
		return imsiKey(amfUe.Suci)
	}
	if amfUe.Supi != "" {
		return supiKey(amfUe.Supi)
	}
	return 0, false
}

// keyFromInitialUEMessage decodes the NAS Registration Request far enough to
// read the subscriber identity.
func keyFromInitialUEMessage(conn net.Conn, msg *ngapType.InitiatingMessage) (uint64, int64, bool, bool) {
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
		// we have, so use it, remember it so the UE keeps it, and count the
		// fallback.
		paperKeyByRanUeID.Store(paperRanKey{conn, ranUeNgapID}, uint64(ranUeNgapID))
		return uint64(ranUeNgapID), pc, true, true
	}

	paperKeyByRanUeID.Store(paperRanKey{conn, ranUeNgapID}, key)
	return key, pc, true, false
}

// subscriberKeyFromNAS pulls the IMSI out of the SUCI in a Registration
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
	return imsiKey(suci)
}

// imsiKey turns "suci-0-208-93-0000-0-0-0000000001" into 208930000000001, the
// full IMSI: MCC + MNC + MSIN, as the paper keys on.
//
// nasConvert.SuciToStringWithError builds the string as
//
//	suci-0-<mcc>-<mnc>-<routing indicator>-<protection scheme>-<key id>-<scheme output>
//
// so the three IMSI parts are fields 2, 3 and 7. The MNC is two or three
// digits, which is why they are concatenated as text rather than combined
// arithmetically. A 15-digit IMSI is at most ~1e15 and fits a uint64 easily.
//
// Only a null-scheme SUCI (protection scheme 0) exposes the MSIN; with a
// profile A/B SUCI the scheme output is ciphertext and the paper's
// identity-based dispatch is not possible before authentication completes.
func imsiKey(suci string) (uint64, bool) {
	parts := strings.Split(suci, "-")
	if len(parts) != 8 || parts[0] != "suci" {
		return 0, false
	}
	if parts[1] != "0" { // SUPI format: 0 = IMSI. A NAI carries no IMSI.
		return 0, false
	}
	if parts[5] != "0" { // protection scheme id
		return 0, false
	}

	return digitsKey(parts[2], parts[3], parts[7]) // MCC, MNC, MSIN
}

// supiKey turns "imsi-208930000000001" into 208930000000001. A NAI SUPI
// ("nai-...") carries no IMSI and is rejected.
func supiKey(supi string) (uint64, bool) {
	digits, ok := strings.CutPrefix(supi, "imsi-")
	if !ok {
		return 0, false
	}
	return digitsKey(digits)
}

// digitsKey concatenates decimal fields into one integer. Concatenation is
// textual because the MNC may be two or three digits, so a leading zero is
// significant and arithmetic combination would lose it.
func digitsKey(fields ...string) (uint64, bool) {
	var key uint64
	for _, field := range fields {
		if field == "" {
			return 0, false
		}
		for _, c := range field {
			if c < '0' || c > '9' {
				return 0, false
			}
			key = key*10 + uint64(c-'0')
		}
	}
	return key, true
}
